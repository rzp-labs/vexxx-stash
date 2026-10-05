"""Check Intel runtime packaging without requiring a GPU or render device."""

import ctypes
import hashlib
import json
from pathlib import Path
import re
import subprocess


def require_ffmpeg_features(option, required):
    output = subprocess.check_output(
        ["ffmpeg", "-hide_banner", option], text=True
    )
    names = {
        fields[1] for line in output.splitlines()
        if len(fields := line.split()) >= 2
    }
    missing = required - names
    if missing:
        raise RuntimeError(f"FFmpeg {option} missing: {', '.join(sorted(missing))}")


def require_mapping_build(manifest_path="/usr/share/vexxx/ffmpeg-mapping-build.json",
                          library_directory="/usr/lib", package_database="/lib/apk/db/installed"):
    manifest = json.loads(Path(manifest_path).read_text())
    if not manifest.get("exported_symbols_unchanged") or not manifest.get("version_unchanged"):
        raise RuntimeError("FFmpeg mapping build did not verify the original ABI")
    packages = [dict(line.split(":", 1) for line in block.splitlines() if ":" in line)
                for block in Path(package_database).read_text().split("\n\n")]
    version = next(entry["V"] for entry in packages if entry.get("P") == "ffmpeg-libavutil")
    if version != manifest["package_version"]:
        raise RuntimeError("Installed libavutil package differs from verified mapping build")
    library = Path(library_directory) / manifest["library"]
    active_library = Path(library_directory) / "libavutil.so.60"
    if active_library.resolve(strict=True) != library.resolve(strict=True):
        raise RuntimeError("Active libavutil SONAME does not resolve to the verified mapping build")
    with active_library.open("rb") as stream:
        actual = hashlib.file_digest(stream, "sha256").hexdigest()
    if actual != manifest["replacement_sha256"]:
        raise RuntimeError("Packaged libavutil does not match verified mapping build")


def require_strict_build(manifest_path="/usr/share/vexxx/ffmpeg-strict-build.json",
                         program="/usr/bin/ffmpeg", package_database="/lib/apk/db/installed"):
    manifest = json.loads(Path(manifest_path).read_text())
    for field in ("configuration_unchanged", "version_unchanged", "features_unchanged",
                  "dependencies_unchanged", "shared_libraries_unchanged", "strict_hwaccel",
                  "strict_context_retention", "hwaccel_metadata", "no_pixel_probe"):
        if manifest.get(field) is not True:
            raise RuntimeError("FFmpeg strict decoder build did not verify " + field)
    packages = [dict(line.split(":", 1) for line in block.splitlines() if ":" in line)
                for block in Path(package_database).read_text().split("\n\n")]
    version = next(entry["V"] for entry in packages if entry.get("P") == "ffmpeg")
    if version != manifest["package_version"]:
        raise RuntimeError("Installed FFmpeg package differs from verified strict decoder build")
    if manifest.get("program") != "ffmpeg":
        raise RuntimeError("Strict decoder manifest names an unexpected program")
    with Path(program).open("rb") as stream:
        actual = hashlib.file_digest(stream, "sha256").hexdigest()
    if actual != manifest["replacement_sha256"]:
        raise RuntimeError("Active FFmpeg does not match verified strict decoder build")
    help_text = subprocess.check_output([str(program), "-hide_banner", "-h", "full"], text=True)
    if not re.search(r"(?m)^\s*-hwaccel_strict(?:\[:<stream_spec>\])?\s", help_text):
        raise RuntimeError("Active FFmpeg lacks strict hardware decoder selection")
    if not re.search(r"(?m)^\s*-hwaccel_metadata(?:\[:<stream_spec>\])?\s", help_text):
        raise RuntimeError("Active FFmpeg lacks hardware frame metadata reporting")


def require_probe_build(manifest_path="/usr/share/vexxx/ffmpeg-probe-build.json",
                        library_directory="/usr/lib", package_database="/lib/apk/db/installed"):
    manifest = json.loads(Path(manifest_path).read_text())
    for field in ("exported_symbols_unchanged", "version_unchanged", "configuration_unchanged",
                  "dependencies_unchanged", "no_pixel_probe"):
        if manifest.get(field) is not True:
            raise RuntimeError("FFmpeg header-only probe build did not verify " + field)
    packages = [dict(line.split(":", 1) for line in block.splitlines() if ":" in line)
                for block in Path(package_database).read_text().split("\n\n")]
    version = next(entry["V"] for entry in packages if entry.get("P") == "ffmpeg-libavformat")
    if version != manifest["package_version"]:
        raise RuntimeError("Installed libavformat package differs from verified probe build")
    library = Path(library_directory) / manifest["library"]
    active = Path(library_directory) / "libavformat.so.62"
    if active.resolve(strict=True) != library.resolve(strict=True):
        raise RuntimeError("Active libavformat SONAME does not resolve to verified probe build")
    with active.open("rb") as stream:
        actual = hashlib.file_digest(stream, "sha256").hexdigest()
    if actual != manifest["replacement_sha256"]:
        raise RuntimeError("Active libavformat does not match verified probe build")
    help_text = subprocess.check_output(["ffprobe", "-hide_banner", "-h", "full"], text=True)
    if not re.search(r"(?m)^\s*no_pixel_probe\s", help_text):
        raise RuntimeError("Active FFProbe lacks header-only video probing")


def main():
    for package in ("intel-media-driver", "libvpl", "onevpl-intel-gpu", "mesa-vulkan-intel", "vulkan-loader"):
        subprocess.run(["apk", "info", "-e", package], check=True)

    # CDLL resolves dependencies immediately; missing shared libraries fail here.
    # Loading libraries alone does not create a VAAPI or QSV device/session.
    for library in (
        "/usr/lib/dri/iHD_drv_video.so", "libvpl.so.2", "libmfx-gen.so.1.2",
        "libvulkan.so.1", "/usr/lib/libvulkan_intel.so"
    ):
        ctypes.CDLL(library)

    icd = json.loads(Path("/usr/share/vulkan/icd.d/intel_icd.x86_64.json").read_text())
    if Path(icd["ICD"]["library_path"]).name != "libvulkan_intel.so":
        raise RuntimeError("Intel Vulkan ICD points to an unexpected library")
    require_mapping_build()
    require_probe_build()
    require_strict_build()
    require_ffmpeg_features(
        "-encoders", {"h264_vaapi", "mjpeg_vaapi", "vp9_vaapi", "h264_qsv", "vp9_qsv"}
    )
    require_ffmpeg_features("-filters", {
        "hwupload", "hwmap", "scale_vaapi", "scale_qsv", "libplacebo",
        "transpose_vaapi", "xstack_vaapi", "procamp_vaapi",
    })
    print("Intel runtime packaging OK; GPU encoding has not been tested.")


if __name__ == "__main__":
    main()
