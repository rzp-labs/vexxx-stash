#!/usr/bin/env python3
"""Rebuild only Alpine's pinned libavutil with its unchanged feature configuration."""
import ctypes
import hashlib
import json
from pathlib import Path
import shlex
import shutil
import subprocess
import sys

VERSION = "8.1.2"
PACKAGE_VERSION = "8.1.2-r0"
SONAME = "libavutil.so.60.26.102"
SOURCE_SHA512 = (
    "b3adc16fe426217bb607da01c4137cee0a9788fc08e077874336db185d2b7287746a7"
    "dc94cf0181ea92cd8afcdb06094ee9456e2986112354f84538cb9a5ed0b"
)


def digest(path, algorithm="sha256"):
    with path.open("rb") as stream:
        return hashlib.file_digest(stream, algorithm).hexdigest()


def symbols(path):
    output = subprocess.check_output(["nm", "-D", "--defined-only", str(path)], text=True)
    return {line.split()[-1] for line in output.splitlines() if line.split()}


def main():
    source, patch, output, *additional_patches = map(Path, sys.argv[1:])
    packages = [dict(line.split(":", 1) for line in block.splitlines() if ":" in line)
                for block in Path("/lib/apk/db/installed").read_text().split("\n\n")]
    package = next(entry["V"] for entry in packages if entry.get("P") == "ffmpeg-libavutil")
    if package != PACKAGE_VERSION:
        raise RuntimeError("FFmpeg package changed; review patch and ABI before rebuilding: " + package)
    if digest(source, "sha512") != SOURCE_SHA512:
        raise RuntimeError("FFmpeg source digest mismatch")
    original = Path("/usr/lib") / SONAME
    library = ctypes.CDLL(str(original))
    library.avutil_configuration.restype = ctypes.c_char_p
    configuration = library.avutil_configuration().decode()
    output.mkdir(parents=True, exist_ok=True)
    subprocess.run(["tar", "-xJf", str(source), "-C", str(output)], check=True)
    tree = output / ("ffmpeg-" + VERSION)
    patches = [patch, *additional_patches]
    for selected_patch in patches:
        subprocess.run(["patch", "--batch", "--fuzz=0", "-p1", "-i", str(selected_patch.resolve())], cwd=tree, check=True)
    subprocess.run(["./configure", *shlex.split(configuration)], cwd=tree, check=True)
    subprocess.run(["make", "-j2", "libavutil/libavutil.so"], cwd=tree, check=True)
    rebuilt = tree / "libavutil" / "libavutil.so.60"
    if symbols(original) != symbols(rebuilt):
        raise RuntimeError("libavutil exported ABI differs from packaged library")
    replacement = output / SONAME
    temporary = output / (SONAME + ".tmp")
    shutil.copy2(rebuilt, temporary)
    temporary.replace(replacement)
    patched = ctypes.CDLL(str(replacement))
    patched.avutil_version.restype = ctypes.c_uint
    patched.avutil_configuration.restype = ctypes.c_char_p
    library.avutil_version.restype = ctypes.c_uint
    if patched.avutil_version() != library.avutil_version():
        raise RuntimeError("libavutil version differs")
    if patched.avutil_configuration().decode() != configuration:
        raise RuntimeError("libavutil feature configuration differs")
    manifest = {
        "package_version": PACKAGE_VERSION,
        "library": SONAME,
        "source_sha512": SOURCE_SHA512,
        "patch_sha256": digest(patch),
        "additional_patches": [{"name": selected.name, "sha256": digest(selected)}
                               for selected in additional_patches],
        "original_sha256": digest(original),
        "replacement_sha256": digest(replacement),
        "configuration": configuration,
        "exported_symbols_unchanged": True,
        "version_unchanged": True,
    }
    manifest_temporary = output / "mapping-build.json.tmp"
    manifest_temporary.write_text(json.dumps(manifest, indent=2) + "\n")
    manifest_temporary.replace(output / "mapping-build.json")
    print(json.dumps(manifest, indent=2))


if __name__ == "__main__":
    main()
