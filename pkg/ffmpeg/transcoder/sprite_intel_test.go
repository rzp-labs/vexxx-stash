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
	list, err := ffmpeg.IntelSpriteSeekList(input, ffmpeg.IntelSource{StartTime: "5"}, times)
	if err != nil {
		t.Fatal(err)
	}
	seek := filepath.Join(dir, "seeks.ffconcat")
	if err := os.WriteFile(seek, []byte(list), 0600); err != nil {
		t.Fatal(err)
	}
	plan := gpuSpritePlan(t)
	plan.InputArgs = nil
	args, err := IntelSpriteSheet(seek, plan, len(times), 2, 2, "-")
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
	for i, at := range times {
		ref := run("-v", "error", "-ss", fmt.Sprint(at), "-i", input, "-vf", "scale=160:-2", "-frames:v", "1", "-c:v", "rawvideo", "-pix_fmt", "rgb24", "-f", "rawvideo", "-")
		var tile []byte
		for y := 0; y < 90; y++ {
			start := ((i/2*90+y)*320 + i%2*160) * 3
			tile = append(tile, sheet[start:start+160*3]...)
		}
		if !bytes.Equal(tile, ref) {
			t.Fatalf("tile %d timestamp %g changed source frame", i, at)
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
