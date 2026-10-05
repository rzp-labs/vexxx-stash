"""Exercise the GPU-free build gate's failure paths with simulated tools."""

import subprocess
import hashlib
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import check_intel_runtime as runtime


ENCODERS = (
    " V....D h264_vaapi H264\n V....D vp9_vaapi VP9\n V....D mjpeg_vaapi MJPEG\n"
    " V..... h264_qsv H264\n V..... vp9_qsv VP9\n"
)
REQUIRED_FILTERS = ("hwupload", "hwmap", "scale_vaapi", "scale_qsv", "libplacebo",
                    "transpose_vaapi", "xstack_vaapi", "procamp_vaapi")
FILTERS = "".join(" ... " + name + " V->V\n" for name in REQUIRED_FILTERS)


class RuntimeCheckTest(unittest.TestCase):
    def setUp(self):
        self.enterContext(patch.object(runtime, "require_mapping_build"))
        self.enterContext(patch.object(runtime.Path, "read_text", return_value=json.dumps({
            "ICD": {"library_path": "/usr/lib/libvulkan_intel.so"}
        })))
        self.run = self.enterContext(patch.object(runtime.subprocess, "run"))
        self.load = self.enterContext(patch.object(runtime.ctypes, "CDLL"))
        self.output = self.enterContext(patch.object(
            runtime.subprocess, "check_output", side_effect=[ENCODERS, FILTERS]
        ))

    def test_complete_runtime(self):
        runtime.main()

    def test_missing_package(self):
        for index in range(5):
            with self.subTest(package=index):
                self.run.side_effect = [None] * index + [
                    subprocess.CalledProcessError(1, "apk info -e")
                ]
                with self.assertRaises(subprocess.CalledProcessError):
                    runtime.main()

    def test_unloadable_driver_or_runtime(self):
        for index in range(5):
            with self.subTest(library=index):
                self.load.side_effect = [None] * index + [OSError("missing dependency")]
                with self.assertRaises(OSError):
                    runtime.main()

    def test_each_required_encoder(self):
        for name in ("h264_vaapi", "mjpeg_vaapi", "vp9_vaapi", "h264_qsv", "vp9_qsv"):
            with self.subTest(encoder=name):
                self.output.side_effect = [ENCODERS.replace(name, "unrelated")]
                with self.assertRaisesRegex(RuntimeError, name):
                    runtime.main()

    def test_each_required_filter(self):
        for name in REQUIRED_FILTERS:
            with self.subTest(filter=name):
                self.output.side_effect = [ENCODERS, FILTERS.replace(name, "unrelated")]
                with self.assertRaisesRegex(RuntimeError, name):
                    runtime.main()

    def test_ffmpeg_failure(self):
        self.output.side_effect = subprocess.CalledProcessError(1, "ffmpeg")
        with self.assertRaises(subprocess.CalledProcessError):
            runtime.main()


class MappingManifestTest(unittest.TestCase):
    def setUp(self):
        self.directory = Path(self.enterContext(tempfile.TemporaryDirectory()))
        self.library = self.directory / "libavutil.so.60.26.102"
        self.library.write_bytes(b"verified library")
        self.active = self.directory / "libavutil.so.60"
        self.active.symlink_to(self.library.name)
        self.database = self.directory / "installed"
        self.database.write_text("P:ffmpeg-libavutil\nV:8.1.2-r0\n\n")
        self.manifest = self.directory / "manifest.json"
        self.data = {
            "package_version": "8.1.2-r0",
            "library": self.library.name,
            "replacement_sha256": hashlib.sha256(self.library.read_bytes()).hexdigest(),
            "exported_symbols_unchanged": True,
            "version_unchanged": True,
        }

    def check(self):
        self.manifest.write_text(json.dumps(self.data))
        runtime.require_mapping_build(self.manifest, self.directory, self.database)

    def test_exact_verified_library(self):
        self.check()

    def test_replacement_tampering(self):
        self.library.write_bytes(b"different library")
        with self.assertRaisesRegex(RuntimeError, "does not match"):
            self.check()

    def test_new_package_with_old_manifest(self):
        self.database.write_text("P:ffmpeg-libavutil\nV:8.1.3-r0\n\n")
        with self.assertRaisesRegex(RuntimeError, "Installed libavutil package"):
            self.check()

    def test_active_soname_points_to_another_library(self):
        another = self.directory / "libavutil.so.60.27.100"
        another.write_bytes(b"unpatched library")
        self.active.unlink()
        self.active.symlink_to(another.name)
        with self.assertRaisesRegex(RuntimeError, "Active libavutil SONAME"):
            self.check()

    def test_unverified_abi(self):
        for field in ("exported_symbols_unchanged", "version_unchanged"):
            with self.subTest(field=field):
                self.data[field] = False
                with self.assertRaisesRegex(RuntimeError, "original ABI"):
                    self.check()
                self.data[field] = True


if __name__ == "__main__":
    unittest.main()
