"""Check Intel runtime packaging without requiring a GPU or render device."""

import ctypes
import hashlib
import json
from pathlib import Path
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
