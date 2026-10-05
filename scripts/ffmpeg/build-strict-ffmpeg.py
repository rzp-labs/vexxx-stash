#!/usr/bin/env python3
"""Build only the strict FFmpeg CLI against the pinned packaged shared libraries."""
import hashlib
import json
from pathlib import Path
import re
import shlex
import shutil
import subprocess
import sys

VERSION = "8.1.2"
PACKAGE_VERSION = "8.1.2-r0"
SOURCE_SHA512 = (
    "b3adc16fe426217bb607da01c4137cee0a9788fc08e077874336db185d2b7287746a7"
    "dc94cf0181ea92cd8afcdb06094ee9456e2986112354f84538cb9a5ed0b"
)
LIBRARIES = ("avdevice", "avfilter", "avformat", "avcodec", "swresample", "swscale", "avutil")
FEATURE_OPTIONS = ("-decoders", "-encoders", "-filters", "-formats", "-protocols", "-bsfs", "-hwaccels")


def digest(path, algorithm="sha256"):
    with path.open("rb") as stream:
        return hashlib.file_digest(stream, algorithm).hexdigest()


def run_output(argv, cwd=None):
    return subprocess.check_output(argv, cwd=cwd, text=True, stderr=subprocess.PIPE)


def program_metadata(program):
    lines = run_output([str(program), "-version"]).splitlines()
    version = next(line for line in lines if line.startswith("ffmpeg version "))
    configuration = next(line.removeprefix("configuration: ") for line in lines
                         if line.startswith("configuration: "))
    libraries = [line for line in lines if line.startswith("lib")]
    return version, configuration, libraries


def dependencies(program):
    output = run_output(["readelf", "-d", str(program)])
    return sorted(re.findall(r"\(NEEDED\).*\[([^]]+)\]", output))


def main():
    source, patch, output = map(Path, sys.argv[1:])
    packages = [dict(line.split(":", 1) for line in block.splitlines() if ":" in line)
                for block in Path("/lib/apk/db/installed").read_text().split("\n\n")]
    required_packages = {"ffmpeg", *("ffmpeg-lib" + name for name in LIBRARIES)}
    installed = {entry["P"]: entry["V"] for entry in packages if entry.get("P") in required_packages}
    if set(installed) != required_packages or any(v != PACKAGE_VERSION for v in installed.values()):
        raise RuntimeError("FFmpeg package stack changed; review before rebuilding: " + repr(installed))
    if digest(source, "sha512") != SOURCE_SHA512:
        raise RuntimeError("FFmpeg source digest mismatch")
    original = Path("/usr/bin/ffmpeg")
    original_version, configuration, linked_versions = program_metadata(original)
    if original_version.split()[2] != VERSION:
        raise RuntimeError("FFmpeg executable version differs from pinned source")
    output.mkdir(parents=True, exist_ok=True)
    tree = output / ("ffmpeg-" + VERSION)
    if tree.exists():
        raise RuntimeError("Build output already contains a source tree; use a fresh directory")
    subprocess.run(["tar", "-xJf", str(source), "-C", str(output)], check=True)
    subprocess.run(["patch", "--batch", "--fuzz=0", "-p1", "-i", str(patch.resolve())], cwd=tree, check=True)
    decoder_source = (tree / "fftools/ffmpeg_dec.c").read_text()
    if "if (!dp->hwaccel_strict)\n        avcodec_free_context(&dp->dec_ctx);" not in decoder_source:
        raise RuntimeError("Strict decoder context must survive queued hardware frames")
    subprocess.run(["./configure", *shlex.split(configuration)], cwd=tree, check=True)
    # These exact make targets are program prerequisites. Mark them old to avoid
    # compiling their objects; symlinks keep the normal FFmpeg link recipe intact.
    make_arguments = ["make", "-j2"]
    library_hashes = {}
    for name in LIBRARIES:
        packaged = Path("/usr/lib") / ("lib" + name + ".so")
        active = packaged.resolve(strict=True)
        target = Path("lib" + name) / packaged.name
        (tree / target).symlink_to(active)
        make_arguments += ["-o", str(target)]
        library_hashes[name] = {"library": active.name, "sha256": digest(active)}
    subprocess.run([*make_arguments, "ffmpeg"], cwd=tree, check=True)
    # Executable-only means no codec, filter or utility library objects rebuilt.
    for name in LIBRARIES:
        if next((tree / ("lib" + name)).rglob("*.o"), None) is not None:
            raise RuntimeError("Unexpected shared-library object rebuilt: " + name)
    for name, record in library_hashes.items():
        if digest(Path("/usr/lib") / record["library"]) != record["sha256"]:
            raise RuntimeError("Packaged shared library changed during CLI build: " + name)
    replacement = output / "ffmpeg"
    temporary = output / "ffmpeg.tmp"
    shutil.copy2(tree / "ffmpeg", temporary)
    temporary.replace(replacement)
    version, rebuilt_configuration, rebuilt_linked_versions = program_metadata(replacement)
    if (version, rebuilt_configuration, rebuilt_linked_versions) != (original_version, configuration, linked_versions):
        raise RuntimeError("FFmpeg version, configuration or linked library versions changed")
    original_dependencies = dependencies(original)
    if dependencies(replacement) != original_dependencies:
        raise RuntimeError("FFmpeg ELF dependencies changed")
    features = {}
    for option in FEATURE_OPTIONS:
        before = run_output([str(original), "-hide_banner", option])
        after = run_output([str(replacement), "-hide_banner", option])
        if before != after:
            raise RuntimeError("FFmpeg advertised features changed: " + option)
        features[option] = hashlib.sha256(before.encode()).hexdigest()
    help_text = run_output([str(replacement), "-hide_banner", "-h", "full"])
    for marker in ("hwaccel_strict", "hwaccel_metadata", "no_pixel_probe"):
        if marker not in help_text:
            raise RuntimeError("Required hardware/probe option absent: " + marker)
    manifest = {
        "package_version": PACKAGE_VERSION,
        "program": "ffmpeg",
        "source_sha512": SOURCE_SHA512,
        "patch_sha256": digest(patch),
        "original_sha256": digest(original),
        "replacement_sha256": digest(replacement),
        "configuration": configuration,
        "version": version,
        "configuration_unchanged": True,
        "version_unchanged": True,
        "features_unchanged": True,
        "dependencies_unchanged": True,
        "shared_libraries_unchanged": True,
        "strict_hwaccel": True,
        "strict_context_retention": True,
        "hwaccel_metadata": True,
        "no_pixel_probe": True,
        "features": features,
        "dependencies": original_dependencies,
        "shared_libraries": library_hashes,
    }
    temporary_manifest = output / "strict-build.json.tmp"
    temporary_manifest.write_text(json.dumps(manifest, indent=2) + "\n")
    temporary_manifest.replace(output / "strict-build.json")
    print(json.dumps(manifest, indent=2))


if __name__ == "__main__":
    main()
