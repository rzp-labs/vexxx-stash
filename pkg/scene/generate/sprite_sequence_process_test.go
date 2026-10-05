package generate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/disintegration/imaging"
	"github.com/stashapp/stash/pkg/ffmpeg"
	"github.com/stashapp/stash/pkg/fsutil"
	"github.com/stashapp/stash/pkg/generationbudget"
	"golang.org/x/image/bmp"
)

func TestSoftwareSpriteUsesTotalWorkerBudget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix subprocess fixture")
	}
	dir := t.TempDir()
	tilePath := filepath.Join(dir, "tile.bmp")
	tile, err := os.Create(tilePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := bmp.Encode(tile, image.NewNRGBA(image.Rect(0, 0, 160, 90))); err != nil {
		t.Fatal(err)
	}
	tile.Close()
	binary := filepath.Join(dir, "ffmpeg")
	gate := filepath.Join(dir, "release")
	script := "#!/bin/sh\nif [ \"$1\" = '-version' ]; then echo 'ffmpeg version 7.0'; exit 0; fi\n: > '" + dir + "/started-'$$\nwhile [ ! -f '" + gate + "' ]; do sleep 0.01; done\ncat '" + tilePath + "'\n"
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	defer os.WriteFile(gate, nil, 0600)
	budget, _ := generationbudget.New(generationbudget.Settings{MaxProcesses: 4, MaxGPUProcesses: 2})
	g := Generator{Budget: budget, Encoder: ffmpeg.NewEncoder(binary), LockManager: fsutil.NewReadLockManager()}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		images, d, err := g.IntelSpriteTiles(ctx, "input.mp4", []float64{0, 1, 2, 3, 4, 5})
		if err == nil && (len(images) != 6 || d.Stage != "" || d.Actual != "software") {
			err = fmt.Errorf("tiles=%d diagnostic=%+v", len(images), d)
		}
		done <- err
	}()
	for {
		files, err := filepath.Glob(filepath.Join(dir, "started-*"))
		if err != nil {
			t.Fatal(err)
		}
		if len(files) == 4 {
			break
		}
		select {
		case err := <-done:
			t.Fatal("fallback returned before configured concurrency", err)
		case <-ctx.Done():
			t.Fatal("fallback used fewer than total-process workers", ctx.Err())
		case <-time.After(time.Millisecond):
		}
	}
	if err := os.WriteFile(gate, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if g.generationBudget() != budget {
		t.Fatal("software rendering changed configured budget")
	}
}

func TestSpriteSubprocessCancellationDrainsAdmission(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix subprocess fixture")
	}
	for _, mode := range []string{"caller", "deletion", "failure"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			binary := filepath.Join(dir, "ffmpeg")
			gate := filepath.Join(dir, "fail")
			script := "#!/bin/sh\nif [ \"$1\" = '-version' ]; then echo 'ffmpeg version 7.0'; exit 0; fi\n: > '" + dir + "/started-'$$\n"
			if mode == "failure" {
				script += "is_first=0\nwhile [ $# -gt 0 ]; do if [ \"$1\" = '-ss' ] && [ \"$2\" = '0' ]; then is_first=1; fi; shift; done\nif [ $is_first = 1 ]; then while [ ! -f '" + gate + "' ]; do sleep 0.01; done; exit 1; fi\n"
			}
			script += "exec sleep 30\n"
			if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			budget, _ := generationbudget.New(generationbudget.Settings{MaxProcesses: 4, MaxGPUProcesses: 2})
			locks := fsutil.NewReadLockManager()
			g := Generator{Budget: budget, Encoder: ffmpeg.NewEncoder(binary), LockManager: locks}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				images, err := g.SpriteScreenshots(ctx, "input.mp4", []float64{0, 1, 2, 3, 4, 5}, "")
				if images != nil {
					err = errors.New("failed subprocess returned partial images")
				}
				done <- err
			}()
			for {
				files, _ := filepath.Glob(filepath.Join(dir, "started-*"))
				if len(files) == 4 {
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal("subprocesses did not reach configured concurrency", ctx.Err())
				case <-time.After(time.Millisecond):
				}
			}
			switch mode {
			case "caller":
				cancel()
			case "deletion":
				locks.Cancel("input.mp4")
			case "failure":
				if err := os.WriteFile(gate, nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("failed/cancelled extraction succeeded")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("worker subprocess cleanup did not finish")
			}
			fresh, stop := context.WithTimeout(context.Background(), time.Second)
			defer stop()
			// Reacquire every slot, proving failed/active/queued workers released it.
			for range 4 {
				release, err := budget.Acquire(fresh, generationbudget.CPU)
				if err != nil {
					t.Fatal("worker leaked a permit", err)
				}
				defer release()
			}
		})
	}
}

func TestSpriteConcurrentCanonicalPixelsSheetAndVTT(t *testing.T) {
	binary, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	dir := t.TempDir()
	input := filepath.Join(dir, "source.mp4")
	if output, err := exec.CommandContext(ctx, binary, "-v", "error", "-f", "lavfi", "-i", "testsrc2=size=320x180:rate=12:duration=3", "-c:v", "libx264", "-threads", "1", "-g", "24", "-pix_fmt", "yuv420p", input).CombinedOutput(); err != nil {
		t.Fatalf("creating source fixture: %v %s", err, output)
	}
	canonical := Generator{Encoder: ffmpeg.NewEncoder(binary), LockManager: fsutil.NewReadLockManager()}
	budget, _ := generationbudget.New(generationbudget.Settings{MaxProcesses: 8, MaxGPUProcesses: 2, Threads: 1})
	parallel := canonical
	parallel.Budget = budget
	times := make([]float64, 81)
	frames := make([]int, 81)
	for i := range times {
		times[i] = 0.125 + float64(i)*2.5/81
		frames[i] = i * 35 / 81 // Preserve duplicate frame indices in short sheets.
	}
	for _, mode := range []string{"time", "frame"} {
		t.Run(mode, func(t *testing.T) {
			control := make([]image.Image, 81)
			for i := range control {
				if mode == "time" {
					control[i], err = canonical.SpriteScreenshot(ctx, input, times[i], "")
				} else {
					control[i], err = canonical.SpriteScreenshotSlow(ctx, input, frames[i], "")
				}
				if err != nil {
					var exitErr *exec.ExitError
					if mode == "frame" && errors.As(err, &exitErr) && strings.Contains(string(exitErr.Stderr), "Unrecognized option 'vsync'") {
						t.Skip("installed FFmpeg removed -vsync, used by the existing canonical frame-seek control")
					}
					t.Fatal(err)
				}
			}
			var images []image.Image
			if mode == "time" {
				images, err = parallel.SpriteScreenshots(ctx, input, times, "")
			} else {
				images, err = parallel.SpriteScreenshotsSlow(ctx, input, frames, "")
			}
			if err != nil || len(images) != 81 {
				t.Fatalf("tiles=%d error=%v", len(images), err)
			}
			for i := range images {
				if images[i].Bounds() != control[i].Bounds() || !bytes.Equal(imaging.Clone(images[i]).Pix, imaging.Clone(control[i]).Pix) {
					t.Fatalf("canonical source pixels differ at tile %d", i)
				}
			}
			var sheets, vtts [][]byte
			for i, tiles := range [][]image.Image{control, images} {
				outputDir := filepath.Join(dir, fmt.Sprintf("%s-%d", mode, i))
				if err := os.Mkdir(outputDir, 0700); err != nil {
					t.Fatal(err)
				}
				sheet := filepath.Join(outputDir, "sprite.jpg")
				vtt := filepath.Join(outputDir, "sprite.vtt")
				if err := parallel.SaveSprite(ctx, tiles, sheet); err != nil {
					t.Fatal(err)
				}
				if err := parallel.spriteVTT(sheet, 2.5/81, 0.125)(nil, vtt); err != nil {
					t.Fatal(err)
				}
				data, err := os.ReadFile(sheet)
				if err != nil {
					t.Fatal(err)
				}
				sheets = append(sheets, data)
				data, err = os.ReadFile(vtt)
				if err != nil {
					t.Fatal(err)
				}
				vtts = append(vtts, data)
			}
			if !bytes.Equal(sheets[0], sheets[1]) || !bytes.Equal(vtts[0], vtts[1]) {
				t.Fatal("concurrent extraction changed complete sheet/VTT bytes")
			}
		})
	}
}
