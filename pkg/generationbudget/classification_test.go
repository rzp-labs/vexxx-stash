package generationbudget

import "testing"

func TestFFMpegHardwareStreamSpecifiersAndDecode(t *testing.T) {
	for _, args := range [][]string{
		{"-c:v:0", "h264_v4l2m2m"}, {"-codec:v:1", "h264_rkmpp"},
		{"-vcodec", "h264_omx"}, {"-c:0", "h264_nvenc"},
		{"-hwaccel:v:0", "d3d11va"}, {"-hwaccel", "dxva2"},
		{"-hwaccel", "auto"}, {"-init_hw_device", "rkmpp=rk"},
		{"-vf", "scale_rkrga=w=640:h=360:format=nv12"},
		{"-filter:v:0", "scale_vt=640:360"},
		{"-filter_complex", "[0:v]hwupload,scale_vaapi=w=640:h=360[out]"},
		{"-filter_complex", "[a][b]overlay[out]; [out]hwupload@transfer=extra_hw_frames=16[gpu]"},
		{"-vf", `drawtext=text='example,hwmap',scale_vaapi=w=640:h=360`},
		{"-vf", `[a\]b]'hwupload'`},
	} {
		if got := ClassifyFFMpeg(args); got != GPU {
			t.Errorf("%v: got %v, want GPU", args, got)
		}
	}
	for _, args := range [][]string{
		nil, {"-hwaccel"}, {"-hwaccel", "none", "-c:v:0", "libwebp"},
		{"-c:v", "copy"}, {"-c:v:0", "libx264", "-filter:v:0", "scale=640:360,format=nv12"},
		{"-i", "qsv_vaapi_rkmpp.mp4", "-c:v", "bmp", "cuda.webp"},
		{"-vf", "drawtext=text=hwupload"},
		{"-vf", `drawtext=text='a,hwupload;hwmap'`},
		{"-vf", `drawtext=text=a\,hwupload\;scale_rkrga`},
		{"-vf", `subtitles=filename='/tmp/scale_qsv.srt'`},
		{"-filter_complex", `[hwupload]null@scale_vaapi[out];[out]scale=640:360`},
	} {
		if got := ClassifyFFMpeg(args); got != CPU {
			t.Errorf("%v: got %v, want CPU", args, got)
		}
	}
}
