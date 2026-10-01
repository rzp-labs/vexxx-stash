"""Check Intel runtime packaging without requiring a GPU or render device."""

import ctypes
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


def main():
    for package in ("intel-media-driver", "libvpl", "onevpl-intel-gpu"):
        subprocess.run(["apk", "info", "-e", package], check=True)

    # CDLL resolves dependencies immediately; missing shared libraries fail here.
    # Loading libraries alone does not create a VAAPI or QSV device/session.
    for library in (
        "/usr/lib/dri/iHD_drv_video.so", "libvpl.so.2", "libmfx-gen.so.1.2"
    ):
        ctypes.CDLL(library)

    require_ffmpeg_features(
        "-encoders", {"h264_vaapi", "vp9_vaapi", "h264_qsv", "vp9_qsv"}
    )
    require_ffmpeg_features("-filters", {"hwupload", "scale_vaapi", "scale_qsv"})
    print("Intel runtime packaging OK; GPU encoding has not been tested.")


if __name__ == "__main__":
    main()
