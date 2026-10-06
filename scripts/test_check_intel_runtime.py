"""Exercise the GPU-free build gate's failure paths with simulated tools."""

import subprocess
import hashlib
import json
from pathlib import Path
import shutil
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
        self.enterContext(patch.object(runtime, "require_strict_build"))
        self.enterContext(patch.object(runtime, "require_probe_build"))
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


class StrictManifestTest(unittest.TestCase):
    def setUp(self):
        self.directory = Path(self.enterContext(tempfile.TemporaryDirectory()))
        self.program = self.directory / "ffmpeg"
        self.program.write_bytes(b"verified strict executable")
        self.database = self.directory / "installed"
        self.database.write_text("P:ffmpeg\nV:8.1.2-r0\n\n")
        self.manifest = self.directory / "strict-build.json"
        self.data = {
            "package_version": "8.1.2-r0", "program": "ffmpeg",
            "replacement_sha256": hashlib.sha256(self.program.read_bytes()).hexdigest(),
            "configuration_unchanged": True, "version_unchanged": True,
            "features_unchanged": True, "dependencies_unchanged": True,
            "shared_libraries_unchanged": True, "strict_hwaccel": True,
            "strict_context_retention": True, "hwaccel_metadata": True, "no_pixel_probe": True,
        }
        self.output = self.enterContext(patch.object(runtime.subprocess, "check_output",
                                                     return_value="-hwaccel_strict <boolean> prohibit software decoding\n-hwaccel_metadata <boolean> GPU metadata\n"))

    def check(self):
        self.manifest.write_text(json.dumps(self.data))
        runtime.require_strict_build(self.manifest, self.program, self.database)

    def test_exact_verified_program(self):
        self.check()

    def test_per_stream_option_help(self):
        self.output.return_value = "-hwaccel_strict[:<stream_spec>] <boolean> prohibit software decoding\n-hwaccel_metadata[:<stream_spec>] <boolean> GPU metadata\n"
        self.check()

    def test_replacement_tampering(self):
        self.program.write_bytes(b"stock ffmpeg")
        with self.assertRaisesRegex(RuntimeError, "does not match"):
            self.check()

    def test_new_package_with_old_manifest(self):
        self.database.write_text("P:ffmpeg\nV:8.1.3-r0\n\n")
        with self.assertRaisesRegex(RuntimeError, "Installed FFmpeg package"):
            self.check()

    def test_unverified_contract(self):
        for field in ("configuration_unchanged", "version_unchanged", "features_unchanged",
                      "dependencies_unchanged", "shared_libraries_unchanged", "strict_hwaccel",
                      "strict_context_retention", "hwaccel_metadata", "no_pixel_probe"):
            with self.subTest(field=field):
                self.data[field] = False
                with self.assertRaisesRegex(RuntimeError, field):
                    self.check()
                self.data[field] = True

    def test_option_missing(self):
        self.output.return_value = "-hwaccel <string> select hardware acceleration\n"
        with self.assertRaisesRegex(RuntimeError, "lacks strict"):
            self.check()

    def test_similarly_named_option_is_not_strict(self):
        self.output.return_value = "-hwaccel_strict_other <boolean> another option\n"
        with self.assertRaisesRegex(RuntimeError, "lacks strict"):
            self.check()

    def test_hardware_metadata_option_missing(self):
        self.output.return_value = "-hwaccel_strict <boolean> prohibit software decoding\n"
        with self.assertRaisesRegex(RuntimeError, "lacks hardware frame metadata"):
            self.check()

    def test_wrong_program(self):
        self.data["program"] = "ffprobe"
        with self.assertRaisesRegex(RuntimeError, "unexpected program"):
            self.check()


class ProbeManifestTest(unittest.TestCase):
    def setUp(self):
        self.directory = Path(self.enterContext(tempfile.TemporaryDirectory()))
        self.library = self.directory / "libavformat.so.62.12.102"
        self.library.write_bytes(b"verified probe library")
        self.active = self.directory / "libavformat.so.62"
        self.active.symlink_to(self.library.name)
        self.database = self.directory / "installed"
        self.database.write_text("P:ffmpeg-libavformat\nV:8.1.2-r0\n\n")
        self.manifest = self.directory / "probe-build.json"
        self.data = {"package_version": "8.1.2-r0", "library": self.library.name,
                     "replacement_sha256": hashlib.sha256(self.library.read_bytes()).hexdigest(),
                     "exported_symbols_unchanged": True, "version_unchanged": True,
                     "configuration_unchanged": True, "dependencies_unchanged": True,
                     "no_pixel_probe": True}
        self.output = self.enterContext(patch.object(runtime.subprocess, "check_output",
                                                     return_value="     no_pixel_probe .D......... probe video headers without software pixel decoding\n"))

    def check(self):
        self.manifest.write_text(json.dumps(self.data))
        runtime.require_probe_build(self.manifest, self.directory, self.database)

    def test_exact_verified_library(self):
        self.check()

    def test_library_tampering(self):
        self.library.write_bytes(b"stock library")
        with self.assertRaisesRegex(RuntimeError, "does not match"):
            self.check()

    def test_package_drift(self):
        self.database.write_text("P:ffmpeg-libavformat\nV:8.1.3-r0\n\n")
        with self.assertRaisesRegex(RuntimeError, "Installed libavformat package"):
            self.check()

    def test_active_soname_mismatch(self):
        other = self.directory / "libavformat.so.62.13.100"
        other.write_bytes(b"stock library")
        self.active.unlink()
        self.active.symlink_to(other.name)
        with self.assertRaisesRegex(RuntimeError, "Active libavformat SONAME"):
            self.check()

    def test_unverified_contract(self):
        for field in ("exported_symbols_unchanged", "version_unchanged", "configuration_unchanged",
                      "dependencies_unchanged", "no_pixel_probe"):
            with self.subTest(field=field):
                self.data[field] = False
                with self.assertRaisesRegex(RuntimeError, field):
                    self.check()
                self.data[field] = True

    def test_probe_policy_not_advertised(self):
        self.output.return_value = "     other_probe .D......... another option\n"
        with self.assertRaisesRegex(RuntimeError, "lacks header-only"):
            self.check()


class StrictDecoderSelectionTest(unittest.TestCase):
    def test_actual_patched_selector_with_installed_decoder_registry(self):
        """Compile the patch's real selector against a small fake codec registry.

        This exercises CLI selection without compiling FFmpeg or opening a GPU.
        It does not establish that a driver can decode a particular source.
        """
        compiler = shutil.which("cc")
        if not compiler:
            self.skipTest("a C compiler is required for the isolated selector test")
        patch_text = (Path(__file__).parent / "ffmpeg/strict-hardware-output.patch").read_text()
        section = patch_text.split("+++ b/fftools/ffmpeg_demux.c\n", 1)[1].split("--- a/", 1)[0]
        # The demux hunk includes the complete existing selector; compile its
        # new side rather than maintaining a Python copy of the selection logic.
        postimage = "".join(line[1:] for line in section.splitlines(keepends=True)
                            if line.startswith(("+", " ")))
        selector = postimage.split("static int decoder_supports_hw_output", 1)[1]
        selector = "static int decoder_supports_hw_output" + selector.split(
            "static int guess_input_channel_layout", 1)[0]
        decoder_section = patch_text.split("+++ b/fftools/ffmpeg_dec.c\n", 1)[1].split("--- a/", 1)[0]
        decoder_postimage = "".join(line[1:] for line in decoder_section.splitlines(keepends=True)
                                    if line.startswith(("+", " ")))
        validation = "static int hwaccel_output_required" + decoder_postimage.split(
            "static int hwaccel_output_required", 1)[1].split("static enum AVPixelFormat get_format", 1)[0]
        metadata = "static int emit_hwaccel_metadata" + decoder_postimage.split(
            "static int emit_hwaccel_metadata", 1)[1].split("static int video_frame_process", 1)[0]
        fixture = r'''
#define _POSIX_C_SOURCE 200809L
#include <assert.h>
#include <errno.h>
#include <stdint.h>
#include <stdio.h>
#include <string.h>
#define AVERROR(x) (-(x))
#define AV_PIX_FMT_FLAG_HWACCEL 1
#define AV_PIX_FMT_FLAG_RGB 2
#define AV_CODEC_HW_CONFIG_METHOD_HW_DEVICE_CTX 1
#define AV_CODEC_HW_CONFIG_METHOD_HW_FRAMES_CTX 2
#define AV_LOG_ERROR 1
#define AV_LOG_VERBOSE 2
#define FFMAX(a, b) ((a) > (b) ? (a) : (b))
enum HWAccelID { HWACCEL_NONE, HWACCEL_GENERIC, HWACCEL_AUTO };
enum AVHWDeviceType { AV_HWDEVICE_TYPE_NONE, AV_HWDEVICE_TYPE_VAAPI, AV_HWDEVICE_TYPE_CUDA };
enum AVPixelFormat { AV_PIX_FMT_NONE = -1, AV_PIX_FMT_VAAPI, AV_PIX_FMT_CUDA,
                     AV_PIX_FMT_YUV420P, AV_PIX_FMT_DEPTH10, AV_PIX_FMT_DEPTH12,
                     AV_PIX_FMT_BAD_DEPTH, AV_PIX_FMT_RGB10 };
enum { AVMEDIA_TYPE_VIDEO, AVMEDIA_TYPE_AUDIO, AV_CODEC_ID_AV1 = 10, AV_CODEC_ID_HEVC = 11 };
typedef struct AVCodecHWConfig {
    int methods;
    enum AVHWDeviceType device_type;
    enum AVPixelFormat pix_fmt;
} AVCodecHWConfig;
typedef struct AVCodec {
    int id, type, decoder;
    const char *name;
    const AVCodecHWConfig *configs[4];
} AVCodec;
typedef struct AVCodecParameters { int codec_type, codec_id; } AVCodecParameters;
typedef struct AVStream { AVCodecParameters *codecpar; } AVStream;
typedef struct AVFormatContext { int unused; } AVFormatContext;
typedef struct OptionsContext { const char *codec_names; } OptionsContext;
typedef struct AVPixFmtDescriptor {
    int flags, nb_components;
    struct { int depth; } comp[4];
} AVPixFmtDescriptor;
typedef struct AVRational { int num, den; } AVRational;
typedef struct AVBufferRef { unsigned char *data; } AVBufferRef;
typedef struct AVHWFramesContext { enum AVPixelFormat format, sw_format; } AVHWFramesContext;
typedef struct AVCodecContext { AVRational framerate; } AVCodecContext;
typedef struct AVFrame {
    enum AVPixelFormat format;
    AVBufferRef *hw_frames_ctx;
    int width, height, color_range, colorspace, color_primaries, color_trc;
    AVRational sample_aspect_ratio;
} AVFrame;
typedef struct DecoderPriv {
    struct { int type; } dec;
    int hwaccel_strict;
    enum HWAccelID hwaccel_id;
    enum AVHWDeviceType hwaccel_device_type;
    enum AVPixelFormat hwaccel_output_format;
    int input_stream_index, hwaccel_metadata_emitted;
    AVCodecContext *dec_ctx;
} DecoderPriv;
static const AVCodec *installed[8];
static int recast_media, default_lookups;
static const AVCodecHWConfig *avcodec_get_hw_config(const AVCodec *codec, int index) {
    return index < 4 ? codec->configs[index] : NULL;
}
static const AVCodec *av_codec_iterate(void **opaque) {
    uintptr_t index = (uintptr_t)*opaque;
    *opaque = (void *)(index + 1);
    return installed[index];
}
static int av_codec_is_decoder(const AVCodec *codec) { return codec->decoder; }
static const AVPixFmtDescriptor *av_pix_fmt_desc_get(enum AVPixelFormat format) {
    static const AVPixFmtDescriptor hardware = { AV_PIX_FMT_FLAG_HWACCEL, 0 },
        software8 = { 0, 3, { {8}, {8}, {8} } },
        software10 = { 0, 3, { {8}, {10}, {8} } },
        software12 = { 0, 3, { {12}, {10}, {10} } },
        bad_depth = { 0, 3, { {0}, {0}, {0} } },
        rgb10 = { AV_PIX_FMT_FLAG_RGB, 3, { {10}, {10}, {10} } };
    switch (format) {
    case AV_PIX_FMT_NONE: return NULL;
    case AV_PIX_FMT_YUV420P: return &software8;
    case AV_PIX_FMT_DEPTH10: return &software10;
    case AV_PIX_FMT_DEPTH12: return &software12;
    case AV_PIX_FMT_BAD_DEPTH: return &bad_depth;
    case AV_PIX_FMT_RGB10: return &rgb10;
    default: return &hardware;
    }
}
static void av_log(void *context, int level, const char *format, ...) { }
static const char *av_hwdevice_get_type_name(enum AVHWDeviceType type) { return "device"; }
static const char *av_get_pix_fmt_name(enum AVPixelFormat format) { return "format"; }
static const char *av_color_range_name(int value) { return "tv"; }
static const char *av_color_space_name(int value) { return "bt709"; }
static const char *av_color_primaries_name(int value) { return "bt709"; }
static const char *av_color_transfer_name(int value) { return "bt709"; }
static const char *avcodec_get_name(int codec) { return "codec"; }
static void opt_match_per_stream_str(void *context, const char *const *option,
                                    AVFormatContext *format, AVStream *stream, const char **value) {
    *value = *option;
}
static int find_codec(void *context, const char *name, int type, int encoder, const AVCodec **codec) {
    for (int i = 0; installed[i]; i++) {
        if (!strcmp(name, installed[i]->name) && installed[i]->decoder) {
            *codec = installed[i];
            return 0;
        }
    }
    return AVERROR(ENOENT);
}
static const AVCodec *avcodec_find_decoder(int id) {
    default_lookups++;
    for (int i = 0; installed[i]; i++)
        if (installed[i]->id == id && installed[i]->decoder)
            return installed[i];
    return NULL;
}
'''
        cases = r'''
int main(void) {
    const AVCodecHWConfig vaapi = { 1, AV_HWDEVICE_TYPE_VAAPI, AV_PIX_FMT_VAAPI };
    const AVCodecHWConfig frames_only = { 2, AV_HWDEVICE_TYPE_VAAPI, AV_PIX_FMT_VAAPI };
    const AVCodecHWConfig wrong_format = { 1, AV_HWDEVICE_TYPE_VAAPI, AV_PIX_FMT_CUDA };
    const AVCodecHWConfig wrong_device = { 1, AV_HWDEVICE_TYPE_CUDA, AV_PIX_FMT_VAAPI };
    const AVCodec software = { AV_CODEC_ID_AV1, AVMEDIA_TYPE_VIDEO, 1, "libdav1d", { NULL } };
    const AVCodec native = { AV_CODEC_ID_AV1, AVMEDIA_TYPE_VIDEO, 1, "av1", { &wrong_format, &vaapi, NULL } };
    const AVCodec encoder = { AV_CODEC_ID_AV1, AVMEDIA_TYPE_VIDEO, 0, "av1_encoder", { &vaapi, NULL } };
    const AVCodec other_codec = { AV_CODEC_ID_HEVC, AVMEDIA_TYPE_VIDEO, 1, "hevc", { &vaapi, NULL } };
    AVCodec incompatible = { AV_CODEC_ID_AV1, AVMEDIA_TYPE_VIDEO, 1, "other_av1", { &frames_only, NULL } };
    AVCodecParameters par = { AVMEDIA_TYPE_VIDEO, AV_CODEC_ID_AV1 };
    AVStream stream = { &par };
    AVFormatContext format = { 0 };
    OptionsContext options = { NULL };
    const AVCodec *selected = NULL;
    DecoderPriv decoder = { { AVMEDIA_TYPE_VIDEO }, 1, HWACCEL_GENERIC,
                            AV_HWDEVICE_TYPE_VAAPI, AV_PIX_FMT_VAAPI };
    installed[0] = &software; installed[1] = &encoder; installed[2] = &other_codec;
    installed[3] = &incompatible; installed[4] = &native;
    assert(choose_decoder(&options, NULL, &format, &stream, HWACCEL_GENERIC,
                          AV_HWDEVICE_TYPE_VAAPI, 1, AV_PIX_FMT_VAAPI, &selected) == 0);
    assert(selected == &native && default_lookups == 0);
    options.codec_names = "libdav1d";
    assert(choose_decoder(&options, NULL, &format, &stream, HWACCEL_GENERIC,
                          AV_HWDEVICE_TYPE_VAAPI, 1, AV_PIX_FMT_VAAPI, &selected) == 0);
    assert(selected == &software && hwaccel_decoder_check(&decoder, selected) == AVERROR(ENOSYS));
    options.codec_names = "av1";
    assert(choose_decoder(&options, NULL, &format, &stream, HWACCEL_GENERIC,
                          AV_HWDEVICE_TYPE_VAAPI, 1, AV_PIX_FMT_VAAPI, &selected) == 0 && selected == &native);
    assert(hwaccel_decoder_check(&decoder, selected) == 0);
    options.codec_names = NULL;
    installed[4] = NULL;
    assert(choose_decoder(&options, NULL, &format, &stream, HWACCEL_GENERIC,
                          AV_HWDEVICE_TYPE_VAAPI, 1, AV_PIX_FMT_VAAPI, &selected) == 0);
    assert(selected == &software && hwaccel_decoder_check(&decoder, selected) == AVERROR(ENOSYS));
    assert(hwaccel_decoder_check(&decoder, &incompatible) == AVERROR(ENOSYS));
    incompatible.configs[0] = &wrong_format;
    assert(choose_decoder(&options, NULL, &format, &stream, HWACCEL_GENERIC,
                          AV_HWDEVICE_TYPE_VAAPI, 1, AV_PIX_FMT_VAAPI, &selected) == 0);
    assert(hwaccel_decoder_check(&decoder, &incompatible) == AVERROR(ENOSYS));
    incompatible.configs[0] = &wrong_device;
    assert(choose_decoder(&options, NULL, &format, &stream, HWACCEL_GENERIC,
                          AV_HWDEVICE_TYPE_VAAPI, 1, AV_PIX_FMT_VAAPI, &selected) == 0);
    assert(hwaccel_decoder_check(&decoder, &incompatible) == AVERROR(ENOSYS));
    assert(default_lookups == 3);
    assert(choose_decoder(&options, NULL, &format, &stream, HWACCEL_NONE,
                          AV_HWDEVICE_TYPE_NONE, 0, AV_PIX_FMT_NONE, &selected) == 0 && selected == &software);
    assert(default_lookups == 4);
    decoder.hwaccel_strict = 0;
    assert(hwaccel_decoder_check(&decoder, selected) == 0);
    assert(choose_decoder(&options, NULL, &format, &stream, HWACCEL_GENERIC,
                          AV_HWDEVICE_TYPE_VAAPI, 1, AV_PIX_FMT_YUV420P, &selected) == AVERROR(EINVAL));
    assert(choose_decoder(&options, NULL, &format, &stream, HWACCEL_GENERIC,
                          AV_HWDEVICE_TYPE_VAAPI, 1, AV_PIX_FMT_NONE, &selected) == AVERROR(EINVAL));
    assert(choose_decoder(&options, NULL, &format, &stream, HWACCEL_AUTO,
                          AV_HWDEVICE_TYPE_VAAPI, 1, AV_PIX_FMT_VAAPI, &selected) == AVERROR(EINVAL));
    assert(choose_decoder(&options, NULL, &format, &stream, HWACCEL_GENERIC,
                          AV_HWDEVICE_TYPE_NONE, 1, AV_PIX_FMT_VAAPI, &selected) == AVERROR(EINVAL));
    assert(choose_decoder(&options, NULL, &format, &stream, HWACCEL_GENERIC,
                          AV_HWDEVICE_TYPE_VAAPI, 2, AV_PIX_FMT_VAAPI, &selected) == AVERROR(EINVAL));
    AVHWFramesContext pool = { AV_PIX_FMT_VAAPI, AV_PIX_FMT_YUV420P };
    AVBufferRef reference = { (unsigned char *)&pool };
    AVCodecContext context = { { 60000, 1001 } };
    AVFrame frame = { AV_PIX_FMT_VAAPI, &reference, 1920, 1080, 0, 0, 0, 0, { 1, 1 } };
    decoder.input_stream_index = 5;
    decoder.dec_ctx = &context;
    assert(emit_hwaccel_metadata(&decoder, &frame) == 0);
    pool.sw_format = AV_PIX_FMT_DEPTH10;
    assert(emit_hwaccel_metadata(&decoder, &frame) == 0);
    pool.sw_format = AV_PIX_FMT_DEPTH12;
    assert(emit_hwaccel_metadata(&decoder, &frame) == 0);
    pool.sw_format = AV_PIX_FMT_RGB10;
    assert(emit_hwaccel_metadata(&decoder, &frame) == 0);
    pool.sw_format = AV_PIX_FMT_BAD_DEPTH;
    assert(emit_hwaccel_metadata(&decoder, &frame) == AVERROR(EINVAL));
    pool.sw_format = AV_PIX_FMT_NONE;
    assert(emit_hwaccel_metadata(&decoder, &frame) == AVERROR(EINVAL));
    pool.sw_format = AV_PIX_FMT_VAAPI;
    assert(emit_hwaccel_metadata(&decoder, &frame) == AVERROR(EINVAL));
    frame.hw_frames_ctx = NULL;
    assert(emit_hwaccel_metadata(&decoder, &frame) == AVERROR(EINVAL));
    return 0;
}
'''
        with tempfile.TemporaryDirectory(prefix="vexxx-decoder-selection-") as directory:
            directory = Path(directory)
            source, executable = directory / "selector.c", directory / "selector"
            source.write_text(fixture + selector + validation + metadata + cases)
            subprocess.run([compiler, "-std=c11", "-Werror=implicit-function-declaration",
                            str(source), "-o", str(executable)], check=True, timeout=30,
                           capture_output=True, text=True)
            result = subprocess.run([str(executable)], check=True, timeout=5,
                                    capture_output=True, text=True)
            records = [json.loads(line.removeprefix("VEXXX_GPU_METADATA="))
                       for line in result.stdout.splitlines()]
            self.assertEqual([record["bit_depth"] for record in records], [8, 10, 12, 10])
            self.assertEqual([record["is_rgb"] for record in records], [False, False, False, True])
            for record in records:
                self.assertEqual(record["stream_index"], 5)
                self.assertEqual((record["width"], record["height"]), (1920, 1080))
                self.assertEqual(record["frame_rate"], "60000/1001")


if __name__ == "__main__":
    unittest.main()
