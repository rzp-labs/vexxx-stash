package ffmpeg

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

func (s IntelSource) isMain10Sprite() bool {
	return s.Codec == "hevc" && s.Profile == "Main 10" && s.PixelFormat == "yuv420p10le" && s.ColorTransfer == "bt709" && s.ColorPrimaries == "bt709" && s.ColorSpace == "bt709" && s.ColorRange == "tv"
}
func (s IntelSource) ValidateSprite(backend string) error {
	if backend == "vaapi" && (s.isMain10Sprite() || s.PixelFormat == "yuvj420p") {
		s.PixelFormat = "yuv420p"
	}
	return s.Validate()
}

// IntelSpriteScaleFilter matches the software screenshot's source range/matrix
// interpretation. An RGB hardware intermediate forces actual matrix/range CSC:
// iHD otherwise treats NV12-to-NV12 colour options as metadata without conversion.
// RGB metadata is normalized to the BT.601 triplet before converting back;
// iHD chooses the RGB-to-YUV matrix from that complete colour standard.
// Composition uses real limited-range BT.601 samples throughout.
func IntelSpriteScaleFilter(config IntelGenerationConfig, source IntelSource, width int) string {
	height := int(math.Round(float64(source.Height)*float64(width)/float64(source.Width)/2)) * 2
	if height < 2 {
		height = 2
	}
	sourceRange := "limited"
	if source.ColorRange == "pc" || source.ColorRange == "jpeg" || source.PixelFormat == "yuvj420p" {
		sourceRange = "full"
	}
	matrix := source.ColorSpace
	switch matrix {
	case "bt709", "bt470bg", "smpte170m", "smpte240m", "fcc":
	default:
		matrix = "bt470bg"
	}
	filter := fmt.Sprintf("setparams=range=%s:colorspace=%s", sourceRange, matrix)
	// A single 4K/8K-to-thumbnail VPP reduction aliases fine source detail.
	// Reduce by at most two in each hardware stage, preserving the source
	// surface format and colour standard until the final RGB conversion.
	stageWidth := source.Width
	for stageWidth > width && stageWidth-width > width {
		stageWidth = int(math.Ceil(float64(stageWidth)/4)) * 2
		stageHeight := int(math.Round(float64(source.Height)*float64(stageWidth)/float64(source.Width)/2)) * 2
		if stageHeight < 2 {
			stageHeight = 2
		}
		filter += fmt.Sprintf(",scale_vaapi=w=%d:h=%d:mode=hq", stageWidth, stageHeight)
	}
	return filter + fmt.Sprintf(",scale_vaapi=w=%d:h=%d:format=rgba:mode=hq:out_range=full,setparams=colorspace=bt470bg:color_primaries=bt470bg:color_trc=smpte170m,scale_vaapi=w=iw:h=ih:format=nv12:mode=hq:out_color_matrix=bt470bg:out_range=limited,setparams=range=limited:colorspace=bt470bg", width, height)
}

// IntelJPEGRangeFilter performs the exact studio-to-JPEG range transform on GPU
// samples. iHD ProcAmp uses Y'=c*Y+16-16*c+b and UV'=c*s*(UV-128)+128.
// c=255/219, b=-16, s=219/224 therefore expands Y16..235 and UV16..240.
// mjpeg_vaapi advertises MPEG metadata despite encoding baseline JPEG samples;
// metadata stays TV directly into the encoder, with no subsequent VPP stage.
func IntelJPEGRangeFilter() string {
	return "procamp_vaapi=c=1.1643835616438356:b=-16:s=0.9776785714285714,setparams=range=limited:colorspace=bt470bg"
}

// NewIntelSpritePlan probes actual source pixels through the GPU JPEG pipeline.
// No raw pixels transfer to the CPU for scaling, composition or encoding.
func NewIntelSpritePlan(config IntelGenerationConfig, source IntelSource, input string, start float64, width int) (IntelGenerationPlan, error) {
	p := IntelGenerationPlan{Config: config, Source: source}
	if config.Backend != "vaapi" {
		return p, fmt.Errorf("GPU JPEG requires VAAPI composition and mjpeg_vaapi; backend %q has no validated GPU JPEG path", config.Backend)
	}
	if err := source.ValidateSprite(config.Backend); err != nil {
		return p, err
	}
	if !source.HasSquareOrUnspecifiedSampleAspectRatio() {
		return p, fmt.Errorf("GPU JPEG does not support sample aspect ratio %q (display aspect ratio %q)", source.SampleAspectRatio, source.DisplayAspectRatio)
	}
	if width <= 0 || width%2 != 0 {
		return p, fmt.Errorf("Intel output width must be positive and even")
	}
	if math.IsNaN(start) || math.IsInf(start, 0) || start < 0 {
		return p, fmt.Errorf("invalid GPU JPEG timestamp")
	}
	p.InputArgs = IntelInputArgs(config, source)
	p.Filter = IntelSpriteScaleFilter(config, source, width)
	base := Args{"-v", "error", "-nostdin", "-abort_on", "empty_output"}
	base = append(base, p.InputArgs...)
	base = base.Seek(start).Input(input)
	base = append(base, "-map", fmt.Sprintf("0:%d", source.StreamIndex), "-an", "-frames:v", "1")
	p.Probes = append(p.Probes, IntelProbeStep{"decode", append(append(Args{}, base...), "-f", "null", "-")})
	p.Probes = append(p.Probes, IntelProbeStep{"scale", append(append(Args{}, base...), "-vf", p.Filter, "-f", "null", "-")})
	encode := append(append(Args{}, base...), "-vf", p.Filter+","+IntelJPEGRangeFilter(), "-c:v", "mjpeg_vaapi", "-global_quality", "95", "-color_range", "tv", "-f", "null", "-")
	p.Probes = append(p.Probes, IntelProbeStep{"encode", encode})
	return p, nil
}

// IntelSpriteSeekList feeds one hardware decoder independent packet seeks.
// concatdec_select discards preroll using the segment's actual inpoint, just as
// accurate -ss does. A segment covers one second beyond the requested time;
// absence of a frame in that interval is an explicit unsupported frame gap.
// Explicit segment durations give each tile a distinct interval, including
// duplicate requested timestamps. Paths use concat's own token quoting.
func IntelSpriteSeekList(input string, source IntelSource, times []float64) (string, error) {
	if strings.ContainsAny(input, "\r\n\x00") || input == "" {
		return "", fmt.Errorf("GPU sprite source path cannot be represented in ffconcat")
	}
	origin := 0.0
	if source.StartTime != "" {
		var err error
		origin, err = strconv.ParseFloat(source.StartTime, 64)
		if err != nil || math.IsNaN(origin) || math.IsInf(origin, 0) {
			return "", fmt.Errorf("GPU sprite demuxer timestamp origin %q is unavailable", source.StartTime)
		}
	}
	if len(times) == 0 {
		return "", fmt.Errorf("sprite timestamps are empty")
	}
	quoted := "'" + strings.ReplaceAll(input, "'", "'\\''") + "'"
	var b strings.Builder
	b.WriteString("ffconcat version 1.0\n")
	for i, at := range times {
		if math.IsNaN(at) || math.IsInf(at, 0) || at < 0 || (i > 0 && at < times[i-1]) {
			return "", fmt.Errorf("invalid sprite timestamp at index %d", i)
		}
		fmt.Fprintf(&b, "file %s\ninpoint %s\noutpoint %s\nduration 1\n", quoted, strconv.FormatFloat(origin+at, 'f', -1, 64), strconv.FormatFloat(origin+at+1, 'f', -1, 64))
	}
	return b.String(), nil
}
