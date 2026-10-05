#!/usr/bin/env python3
"""Rebuild only Alpine's pinned libavformat with its unchanged feature configuration."""
import ctypes
import hashlib
import json
import re
from pathlib import Path
import shlex
import shutil
import subprocess
import sys

VERSION = "8.1.2"
PACKAGE_VERSION = "8.1.2-r0"
SONAME = "libavformat.so.62.12.102"
LIBRARIES = ("avdevice", "avfilter", "avcodec", "swresample", "swscale", "avutil")
ALPINE_PATCH = "add-av_stream_get_first_dts-for-chromium.patch"
ALPINE_PATCH_SHA512 = (
    "0c2284bc053c92478ce6e3665e03273cb93926534c6f927eb60e060c7770c68dca127"
    "24853adbcac03c4a58df81f2de938d54e82f43c7e789d345bed872ffc69"
)
SOURCE_SHA512 = (
    "b3adc16fe426217bb607da01c4137cee0a9788fc08e077874336db185d2b7287746a7"
    "dc94cf0181ea92cd8afcdb06094ee9456e2986112354f84538cb9a5ed0b"
)


def digest(path, algorithm="sha256"):
    with path.open("rb") as stream:
        return hashlib.file_digest(stream, algorithm).hexdigest()


def symbols(path):
    output = subprocess.check_output(["nm", "-D", "--defined-only", str(path)], text=True)
    return {(line.split()[-1], line.split()[-2])
            for line in output.splitlines() if line.split()}


def main():
    source, patch, output, *additional_patches = map(Path, sys.argv[1:])
    packages = [dict(line.split(":", 1) for line in block.splitlines() if ":" in line)
                for block in Path("/lib/apk/db/installed").read_text().split("\n\n")]
    package = next(entry["V"] for entry in packages if entry.get("P") == "ffmpeg-libavformat")
    for name in ("ffmpeg-lib" + library for library in LIBRARIES):
        version = next(entry["V"] for entry in packages if entry.get("P") == name)
        if version != PACKAGE_VERSION:
            raise RuntimeError("FFmpeg dependency package changed: " + name + " " + version)
    if package != PACKAGE_VERSION:
        raise RuntimeError("FFmpeg package changed; review patch and ABI before rebuilding: " + package)
    if digest(source, "sha512") != SOURCE_SHA512:
        raise RuntimeError("FFmpeg source digest mismatch")
    alpine_patch = Path(__file__).resolve().with_name(ALPINE_PATCH)
    if digest(alpine_patch, "sha512") != ALPINE_PATCH_SHA512:
        raise RuntimeError("Pinned Alpine package patch digest mismatch")
    original = Path("/usr/lib") / SONAME
    library = ctypes.CDLL(str(original))
    library.avformat_configuration.restype = ctypes.c_char_p
    configuration = library.avformat_configuration().decode()
    output.mkdir(parents=True, exist_ok=True)
    tree = output / ("ffmpeg-" + VERSION)
    if tree.exists():
        raise RuntimeError("Build output already contains a source tree; use a fresh directory")
    subprocess.run(["tar", "-xJf", str(source), "-C", str(output)], check=True)
    # Preserve Alpine's downstream ABI addition before applying our policy.
    patches = [alpine_patch, patch, *additional_patches]
    for selected_patch in patches:
        subprocess.run(["patch", "--batch", "--fuzz=0", "-p1", "-i", str(selected_patch.resolve())], cwd=tree, check=True)
    subprocess.run(["./configure", *shlex.split(configuration)], cwd=tree, check=True)
    library_hashes = {}
    make_arguments = ["make", "-j2"]
    for name in LIBRARIES:
        active = (Path("/usr/lib") / ("lib" + name + ".so")).resolve(strict=True)
        target = Path("lib" + name) / ("lib" + name + ".so")
        (tree / target).symlink_to(active)
        make_arguments += ["-o", str(target)]
        library_hashes[name] = {"library": active.name, "sha256": digest(active)}
    subprocess.run([*make_arguments, "libavformat/libavformat.so"], cwd=tree, check=True)
    for name, record in library_hashes.items():
        if next((tree / ("lib" + name)).rglob("*.o"), None) is not None:
            raise RuntimeError("Unexpected dependency library object rebuilt: " + name)
        if digest(Path("/usr/lib") / record["library"]) != record["sha256"]:
            raise RuntimeError("Packaged dependency changed during build: " + name)
    rebuilt = tree / "libavformat" / "libavformat.so.62"
    if symbols(original) != symbols(rebuilt):
        raise RuntimeError("libavformat exported ABI differs from packaged library")
    replacement = output / SONAME
    temporary = output / (SONAME + ".tmp")
    shutil.copy2(rebuilt, temporary)
    temporary.replace(replacement)
    patched = ctypes.CDLL(str(replacement))
    patched.avformat_version.restype = ctypes.c_uint
    patched.avformat_configuration.restype = ctypes.c_char_p
    library.avformat_version.restype = ctypes.c_uint
    if patched.avformat_version() != library.avformat_version():
        raise RuntimeError("libavformat version differs")
    if patched.avformat_configuration().decode() != configuration:
        raise RuntimeError("libavformat feature configuration differs")
    def dependencies(path):
        data = subprocess.check_output(["readelf", "-d", str(path)], text=True)
        return sorted(re.findall(r"\(NEEDED\).*\[([^]]+)\]", data))
    original_dependencies = dependencies(original)
    if dependencies(replacement) != original_dependencies:
        raise RuntimeError("libavformat ELF dependencies changed")
    manifest = {
        "package_version": PACKAGE_VERSION,
        "library": SONAME,
        "source_sha512": SOURCE_SHA512,
        "patch_sha256": digest(patch),
        "package_patches": [{"name": ALPINE_PATCH, "sha512": ALPINE_PATCH_SHA512,
                             "sha256": digest(alpine_patch)}],
        "dependency_libraries_unchanged": True,
        "additional_patches": [{"name": selected.name, "sha256": digest(selected)}
                               for selected in additional_patches],
        "original_sha256": digest(original),
        "replacement_sha256": digest(replacement),
        "configuration": configuration,
        "exported_symbols_unchanged": True,
        "version_unchanged": True,
        "configuration_unchanged": True,
        "dependencies_unchanged": True,
        "no_pixel_probe": True,
        "dependencies": original_dependencies,
        "dependency_libraries": library_hashes,
    }
    manifest_temporary = output / "probe-build.json.tmp"
    manifest_temporary.write_text(json.dumps(manifest, indent=2) + "\n")
    manifest_temporary.replace(output / "probe-build.json")
    print(json.dumps(manifest, indent=2))


if __name__ == "__main__":
    main()
