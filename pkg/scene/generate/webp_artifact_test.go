package generate

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stashapp/stash/pkg/ffmpeg"
	"github.com/stashapp/stash/pkg/fsutil"
	"github.com/stashapp/stash/pkg/generationbudget"
)

// Run on the offline Linux lab using a cross-compiled test binary. Ordinary
// environments without FFmpeg skip; no downloads or hardware are required.
func TestAnimatedWebPArtifactLossless(t *testing.T) {
	path, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	encoder := ffmpeg.NewEncoder(path)
	dir := t.TempDir()
	input := filepath.Join(dir, "synthetic.mp4")
	if err := encoder.Generate(ctx, ffmpeg.Args{"-v", "error", "-nostdin", "-y", "-filter_threads", "1", "-f", "lavfi", "-i", "testsrc2=size=128x64:rate=12:duration=6", "-c:v", "libx264", "-threads", "1", "-pix_fmt", "yuv420p", input}); err != nil {
		t.Fatalf("synthetic fixture: %v", err)
	}
	budget, err := generationbudget.New(generationbudget.Settings{MaxProcesses: 1, MaxGPUProcesses: 1, Threads: 1})
	if err != nil {
		t.Fatal(err)
	}
	for _, vr := range []string{"", "MONO360"} {
		t.Run("marker_"+vr, func(t *testing.T) {
			p := markerTestPaths{t.TempDir()}
			g := Generator{Encoder: encoder, LockManager: fsutil.NewReadLockManager(), MarkerPaths: p, Budget: budget}
			if err := g.SceneMarkerWebp(ctx, input, "synthetic", 0, vr); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(p.GetWebpPreviewPath("synthetic", 0))
			if err != nil {
				t.Fatal(err)
			}
			frames, lossless, lossy, duration, loop, err := inspectAnimatedWebP(data)
			if err != nil {
				t.Fatal(err)
			}
			if frames != 60 || lossless != frames || lossy != 0 || duration < 4990 || duration > 5010 || loop != 0 {
				t.Fatalf("frames=%d VP8L=%d VP8=%d duration_ms=%d loop=%d", frames, lossless, lossy, duration, loop)
			}
		})
	}
}

func inspectAnimatedWebP(data []byte) (frames, lossless, lossy, duration, loop int, err error) {
	loop = -1
	if len(data) < 12 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WEBP" {
		return 0, 0, 0, 0, loop, fmt.Errorf("invalid WebP RIFF header")
	}
	err = walkWebPChunks(data[12:], func(kind string, payload []byte) error {
		switch kind {
		case "ANIM":
			if len(payload) < 6 {
				return fmt.Errorf("short ANIM")
			}
			loop = int(binary.LittleEndian.Uint16(payload[4:6]))
		case "ANMF":
			if len(payload) < 16 {
				return fmt.Errorf("short ANMF")
			}
			frames++
			duration += int(payload[12]) | int(payload[13])<<8 | int(payload[14])<<16
			return walkWebPChunks(payload[16:], func(kind string, _ []byte) error {
				if kind == "VP8L" {
					lossless++
				}
				if kind == "VP8 " {
					lossy++
				}
				return nil
			})
		}
		return nil
	})
	return
}
func walkWebPChunks(data []byte, visit func(string, []byte) error) error {
	for len(data) > 0 {
		if len(data) < 8 {
			return fmt.Errorf("truncated WebP chunk")
		}
		size := int(binary.LittleEndian.Uint32(data[4:8]))
		padded := size + (size & 1)
		if size < 0 || padded > len(data)-8 {
			return fmt.Errorf("WebP chunk exceeds payload")
		}
		if err := visit(string(data[:4]), data[8:8+size]); err != nil {
			return err
		}
		data = data[8+padded:]
	}
	return nil
}
