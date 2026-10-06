package transcoder

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stashapp/stash/pkg/ffmpeg"
)

func gpuSpritePlan(t *testing.T) ffmpeg.IntelGenerationPlan {
	t.Helper()
	p, err := ffmpeg.NewIntelSpritePlan(ffmpeg.IntelGenerationConfig{Backend: "vaapi", Device: "/dev/dri/renderD128"}, ffmpeg.IntelSource{Codec: "h264", PixelFormat: "yuv420p", Width: 320, Height: 180}, "source.mp4", 1.9876543209876543, 160)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func TestIntelSpriteCommandsNeverTransferOrEncodeCPUPixels(t *testing.T) {
	p := gpuSpritePlan(t)
	still := IntelSpriteScreenshot("source.mp4", 1.9876543209876543, p)
	if still[len(still)-1] != "-" {
		t.Fatal(still)
	}
	if !strings.Contains(strings.Join(still, " "), "-ss 1.9876543209876543 -i source.mp4") {
		t.Fatal(still)
	}
	for _, count := range []int{1, 3, 81} {
		sheet, err := IntelSpriteSheet("seeks.ffconcat", p, count, 9, 9, "sprite.jpg")
		if err != nil {
			t.Fatal(err)
		}
		s := strings.Join(sheet, " ")
		for _, want := range []string{"-hwaccel_output_format vaapi", "-segment_time_metadata 1 -i seeks.ffconcat", "concatdec_select", "trim=end_frame=1", "scale_vaapi=", "mjpeg_vaapi -global_quality 95"} {
			if !strings.Contains(s, want) {
				t.Fatalf("missing %q in %s", want, s)
			}
		}
		if count > 1 && !strings.Contains(s, "xstack_vaapi=") {
			t.Fatal(s)
		}
		if count < 81 && !strings.Contains(s, "[content][blank]xstack_vaapi=inputs=2:layout=0_0|1280_720:fill=black") {
			t.Fatal(s)
		}
		for _, bad := range []string{"hwdownload", "hwupload", "format=bgr", "-c:v bmp", "scale="} {
			if strings.Contains(s, bad) {
				t.Fatalf("CPU pixel operation %q: %s", bad, s)
			}
		}
	}
	if _, err := IntelSpriteSheet("seeks.ffconcat", p, 82, 9, 9, "out.jpg"); err == nil {
		t.Fatal("accepted oversized grid")
	}
}

func TestIntelSpriteSheetInputsIndependentDecodersAndCanonicalTileOrder(t *testing.T) {
	plan := gpuSpritePlan(t)
	plan.Source.StreamIndex = 2
	plan.InputArgs = ffmpeg.IntelVulkanInputArgs(plan.Config, plan.Source)
	plan.InputArgs = append(plan.InputArgs, "-noautorotate", "-display_rotation", "0", "-fflags", "+no_pixel_probe")
	for _, counts := range [][]int{{7, 7, 7, 7, 7, 7, 7, 7, 7, 6, 6, 6}, {1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1}, {3}} {
		lists := make([]string, len(counts))
		for i := range lists {
			lists[i] = fmt.Sprintf("seek-%d.ffconcat", i)
		}
		args, err := IntelSpriteSheetInputs(lists, plan, counts, 9, 9, "sprite.jpg")
		if err != nil {
			t.Fatal(err)
		}
		command := strings.Join(args, " ")
		for _, global := range []string{"-init_hw_device vaapi=vex:/dev/dri/renderD128", "-init_hw_device vulkan=vexvk@vex", "-filter_hw_device vexvk"} {
			if strings.Count(command, global) != 1 {
				t.Fatalf("global option repeated or missing %q: %s", global, command)
			}
		}
		for _, perInput := range []string{"-hwaccel vaapi", "-hwaccel_device vex", "-hwaccel_output_format vaapi", "-hwaccel_strict 1", "-noautorotate -display_rotation 0", "-fflags +no_pixel_probe", "-segment_time_metadata 1"} {
			if strings.Count(command, perInput) != len(counts) {
				t.Fatalf("per-input option missing %q: %s", perInput, command)
			}
		}
		graph := ""
		inputs := 0
		for i, arg := range args {
			if arg == "-i" {
				if args[i+1] != lists[inputs] {
					t.Fatal(args)
				}
				inputs++
			}
			if arg == "-filter_complex" {
				graph = args[i+1]
			}
		}
		if inputs != len(counts) {
			t.Fatal(args)
		}
		offset := 0
		for input, count := range counts {
			if !strings.Contains(graph, fmt.Sprintf("[%d:2]select='concatdec_select", input)) {
				t.Fatal(graph)
			}
			for local := 0; local < count; local++ {
				want := fmt.Sprintf(";[s%d]trim=start=%d:end=%d,trim=end_frame=1,setpts=PTS-STARTPTS", offset+local, local, local+1)
				if !strings.Contains(graph, want) {
					t.Fatalf("missing canonical tile %q: %s", want, graph)
				}
			}
			offset += count
		}
		for _, bad := range []string{"hwdownload", "hwupload", "scale=", "-c:v bmp", "xstack_vaapi=inputs=81", "xstack_vaapi=inputs=12"} {
			if strings.Contains(command, bad) {
				t.Fatalf("invalid GPU operation %q: %s", bad, command)
			}
		}
		if strings.Count(command, "mjpeg_vaapi -global_quality 95") != 1 {
			t.Fatal(command)
		}
	}
}

func TestIntelSpriteSheetInputsRejectInvalidPartitions(t *testing.T) {
	plan := gpuSpritePlan(t)
	for _, tc := range []struct {
		lists  []string
		counts []int
	}{
		{nil, nil}, {[]string{"a"}, nil}, {[]string{"a", "b"}, []int{1}},
		{[]string{""}, []int{1}}, {[]string{"a"}, []int{0}}, {[]string{"a"}, []int{-1}},
		{[]string{"a", "b"}, []int{41, 41}}, {[]string{"a", "b"}, []int{int(^uint(0) >> 1), 1}},
	} {
		if _, err := IntelSpriteSheetInputs(tc.lists, plan, tc.counts, 9, 9, "sprite.jpg"); err == nil {
			t.Fatalf("accepted invalid partition %+v", tc)
		}
	}
	plan.InputArgs = ffmpeg.Args{"-init_hw_device"}
	if _, err := IntelSpriteSheetInputs([]string{"a"}, plan, []int{1}, 9, 9, "sprite.jpg"); err == nil {
		t.Fatal("accepted incomplete device definition")
	}
}

// This real CPU control projects ONLY unavailable hardware pixel operations.
// Exact source pixels prove concat segment seeks/metadata selection, including
// duplicate timestamps and nonzero source origins; they do not prove GPU VPP.
func TestIntelSpriteConcatAccurateSourceFrameSelection(t *testing.T) {
	bin, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	run := func(args ...string) []byte {
		t.Helper()
		cmd := exec.CommandContext(ctx, bin, args...)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("ffmpeg: %v: %s", err, stderr.String())
		}
		return out
	}
	dir := t.TempDir()
	input := filepath.Join(dir, "source's clip.mkv")
	run("-v", "error", "-f", "lavfi", "-i", "testsrc2=size=320x180:rate=10:duration=6", "-c:v", "libx264", "-g", "50", "-bf", "3", "-output_ts_offset", "5", input)
	times := []float64{0.19, 0.19, 1.9876543209876543, 3.49}
	plan := gpuSpritePlan(t)
	plan.InputArgs = nil
	references := make([][]byte, len(times))
	for i, at := range times {
		references[i] = run("-v", "error", "-ss", fmt.Sprint(at), "-i", input, "-vf", "scale=160:-2", "-frames:v", "1", "-c:v", "rawvideo", "-pix_fmt", "rgb24", "-f", "rawvideo", "-")
	}
	for _, counts := range [][]int{{4}, {2, 2}, {1, 2, 1}, {1, 1, 1, 1}} {
		var seeks []string
		offset := 0
		for part, count := range counts {
			list, err := ffmpeg.IntelSpriteSeekList(input, ffmpeg.IntelSource{StartTime: "5"}, times[offset:offset+count])
			if err != nil {
				t.Fatal(err)
			}
			seek := filepath.Join(dir, fmt.Sprintf("seeks-%d.ffconcat", part))
			if err := os.WriteFile(seek, []byte(list), 0600); err != nil {
				t.Fatal(err)
			}
			seeks = append(seeks, seek)
			offset += count
		}
		args, err := IntelSpriteSheetInputs(seeks, plan, counts, 2, 2, "-")
		if err != nil {
			t.Fatal(err)
		}
		var projected []string
		for i := 0; i < len(args); i++ {
			if args[i] == "-filter_complex" {
				i++
				graph := strings.ReplaceAll(args[i], plan.Filter, "scale=160:90")
				graph = strings.ReplaceAll(graph, "xstack_vaapi=", "xstack=")
				graph = strings.ReplaceAll(graph, ffmpeg.IntelJPEGRangeFilter(), "null")
				projected = append(projected, "-filter_complex", graph)
			} else if args[i] == "-c:v" {
				break
			} else {
				projected = append(projected, args[i])
			}
		}
		projected = append(projected, "-c:v", "rawvideo", "-pix_fmt", "rgb24", "-f", "rawvideo", "-")
		sheet := run(projected...)
		if len(sheet) != 320*180*3 {
			t.Fatalf("partition %v sheet bytes=%d", counts, len(sheet))
		}
		for i, at := range times {
			var tile []byte
			for y := 0; y < 90; y++ {
				start := ((i/2*90+y)*320 + i%2*160) * 3
				tile = append(tile, sheet[start:start+160*3]...)
			}
			if !bytes.Equal(tile, references[i]) {
				t.Fatalf("partition %v tile %d timestamp %g changed source frame", counts, i, at)
			}
		}
	}
}

func TestIntelSpriteShortFrameSelectionPreservesDuplicates(t *testing.T) {
	bin, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	run := func(args ...string) []byte {
		t.Helper()
		cmd := exec.CommandContext(ctx, bin, args...)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("ffmpeg: %v %s", err, stderr.String())
		}
		return out
	}
	input := filepath.Join(t.TempDir(), "short.mp4")
	run("-v", "error", "-f", "lavfi", "-i", "testsrc2=size=320x180:rate=10:duration=0.6", "-c:v", "libx264", input)
	frames := []int{0, 0, 2, 4}
	plan := gpuSpritePlan(t)
	plan.InputArgs = nil
	args, err := IntelSpriteSheetFrames(input, plan, frames, 2, 2, "-")
	if err != nil {
		t.Fatal(err)
	}
	var projected []string
	for i := 0; i < len(args); i++ {
		if args[i] == "-filter_complex" {
			i++
			graph := strings.ReplaceAll(args[i], plan.Filter, "scale=160:90")
			graph = strings.ReplaceAll(graph, "xstack_vaapi=", "xstack=")
			graph = strings.ReplaceAll(graph, ffmpeg.IntelJPEGRangeFilter(), "null")
			projected = append(projected, "-filter_complex", graph)
		} else if args[i] == "-c:v" {
			break
		} else {
			projected = append(projected, args[i])
		}
	}
	projected = append(projected, "-c:v", "rawvideo", "-pix_fmt", "rgb24", "-f", "rawvideo", "-")
	sheet := run(projected...)
	if len(sheet) != 320*180*3 {
		t.Fatalf("sheet bytes=%d", len(sheet))
	}
	for i, frame := range frames {
		ref := run("-v", "error", "-i", input, "-vf", fmt.Sprintf("select='eq(n,%d)',scale=160:-2", frame), "-frames:v", "1", "-c:v", "rawvideo", "-pix_fmt", "rgb24", "-f", "rawvideo", "-")
		var tile []byte
		for y := 0; y < 90; y++ {
			start := ((i/2*90+y)*320 + i%2*160) * 3
			tile = append(tile, sheet[start:start+160*3]...)
		}
		if !bytes.Equal(tile, ref) {
			t.Fatalf("tile %d changed canonical source frame %d", i, frame)
		}
	}
	for _, invalid := range [][]int{nil, {-1}, {1, 0}} {
		if _, err := IntelSpriteSheetFrames(input, plan, invalid, 2, 2, "-"); err == nil {
			t.Fatalf("accepted frames %v", invalid)
		}
	}
}
