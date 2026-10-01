"""Exercise the GPU-free build gate's failure paths with simulated tools."""

import subprocess
import unittest
from unittest.mock import patch

import check_intel_runtime as runtime


ENCODERS = (
    " V....D h264_vaapi H264\n V....D vp9_vaapi VP9\n"
    " V..... h264_qsv H264\n V..... vp9_qsv VP9\n"
)
FILTERS = " ... hwupload V->V\n ... scale_vaapi V->V\n ... scale_qsv V->V\n"


class RuntimeCheckTest(unittest.TestCase):
    def setUp(self):
        self.run = self.enterContext(patch.object(runtime.subprocess, "run"))
        self.load = self.enterContext(patch.object(runtime.ctypes, "CDLL"))
        self.output = self.enterContext(patch.object(
            runtime.subprocess, "check_output", side_effect=[ENCODERS, FILTERS]
        ))

    def test_complete_runtime(self):
        runtime.main()

    def test_missing_package(self):
        for index in range(3):
            with self.subTest(package=index):
                self.run.side_effect = [None] * index + [
                    subprocess.CalledProcessError(1, "apk info -e")
                ]
                with self.assertRaises(subprocess.CalledProcessError):
                    runtime.main()

    def test_unloadable_driver_or_runtime(self):
        for index in range(3):
            with self.subTest(library=index):
                self.load.side_effect = [None] * index + [OSError("missing dependency")]
                with self.assertRaises(OSError):
                    runtime.main()

    def test_each_required_encoder(self):
        for name in ("h264_vaapi", "vp9_vaapi", "h264_qsv", "vp9_qsv"):
            with self.subTest(encoder=name):
                self.output.side_effect = [ENCODERS.replace(name, "unrelated")]
                with self.assertRaisesRegex(RuntimeError, name):
                    runtime.main()

    def test_each_required_filter(self):
        for name in ("hwupload", "scale_vaapi", "scale_qsv"):
            with self.subTest(filter=name):
                self.output.side_effect = [ENCODERS, FILTERS.replace(name, "unrelated")]
                with self.assertRaisesRegex(RuntimeError, name):
                    runtime.main()

    def test_ffmpeg_failure(self):
        self.output.side_effect = subprocess.CalledProcessError(1, "ffmpeg")
        with self.assertRaises(subprocess.CalledProcessError):
            runtime.main()


if __name__ == "__main__":
    unittest.main()
