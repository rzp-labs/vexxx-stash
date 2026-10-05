package generate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stashapp/stash/pkg/ffmpeg"
	"github.com/stashapp/stash/pkg/fsutil"
	"github.com/stashapp/stash/pkg/generationbudget"
)

type intelOnlyBudgetConfig struct {
	testBudgetConfig
	intel *generationbudget.Budget
}

func (c intelOnlyBudgetConfig) GetIntelGenerationBudget() *generationbudget.Budget {
	return c.intel
}

func TestIntelScopePreservesOrdinaryCPUAdmission(t *testing.T) {
	budget, _ := generationbudget.New(generationbudget.Settings{})
	g := Generator{FFMpegConfig: intelOnlyBudgetConfig{intel: budget}}
	intel := g.WithIntelGenerationBudget()
	if g.generationBudget() != nil || intel.generationBudget() != budget {
		t.Fatal("Intel scope mutated ordinary CPU settings")
	}
	release, err := budget.Acquire(context.Background(), generationbudget.GPU)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	args := []string{"-i", "in.mp4", "out.mp4"}
	got, cpuRelease, err := g.acquireGeneration(ctx, args)
	if err != nil {
		t.Fatal("ordinary CPU work waited for Intel admission", err)
	}
	cpuRelease()
	if !reflect.DeepEqual(got, args) {
		t.Fatal("ordinary CPU threads changed")
	}
	if _, _, err := intel.acquireGeneration(ctx, []string{"-hwaccel", "vaapi", "-i", "in.mp4", "out.mp4"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Intel stage bypassed mandatory admission: %v", err)
	}
	// Explicit shared mode must use a single scheduler across both scopes.
	shared := Generator{FFMpegConfig: intelOnlyBudgetConfig{testBudgetConfig: testBudgetConfig{budget}, intel: budget}}
	if shared.WithIntelGenerationBudget().generationBudget() != shared.generationBudget() {
		t.Fatal("Intel scope replaced the explicit shared scheduler")
	}
}

func TestIntelStagesAndUnsupportedMarkerAdmission(t *testing.T) {
	for _, workload := range []string{"sprite", "marker-quality-rejection"} {
		t.Run(workload, func(t *testing.T) {
			budget, _ := generationbudget.New(generationbudget.Settings{})
			release, _ := budget.Acquire(context.Background(), generationbudget.GPU)
			defer release()
			g := Generator{FFMpegConfig: intelOnlyBudgetConfig{intel: budget}, LockManager: fsutil.NewReadLockManager()}
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
			defer cancel()
			var err error
			if workload == "sprite" {
				g.IntelSprites = &ffmpeg.IntelGenerationConfig{Backend: "vaapi"}
				_, err = g.IntelSpriteSheet(ctx, "synthetic.mp4", []float64{0, 1}, 9, 9, filepath.Join(t.TempDir(), "sprite.jpg"))
			} else {
				g.IntelMarker = &ffmpeg.IntelGenerationConfig{Backend: "qsv"}
				lock := g.LockManager.ReadLock(ctx, "synthetic.mp4")
				defer lock.Cancel()
				err = g.generateIntelMarker(lock, "synthetic.mp4", "out.mp4", sceneMarkerOptions{}, []string{"-i", "synthetic.mp4", "out.mp4"})
			}
			if workload == "sprite" {
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("%s bypassed Intel budget: %v", workload, err)
				}
			} else if err == nil || errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "QSV") {
				t.Fatalf("unsupported marker must reject before admission, without CPU fallback: %v", err)
			}
			release()
			fresh, stop := context.WithTimeout(context.Background(), time.Second)
			defer stop()
			permit, err := budget.Acquire(fresh, generationbudget.GPU)
			if err != nil {
				t.Fatal("cancelled stage leaked admission", err)
			}
			permit()
		})
	}
}

func TestIntelSpriteMetadataFailureDoesNotStartCPUFallback(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix command fixture")
	}
	dir := t.TempDir()
	argsPath := filepath.Join(dir, "args")
	binary := filepath.Join(dir, "ffmpeg")
	script := "#!/bin/sh\nif [ \"$1\" = '-version' ]; then echo 'ffmpeg version 7.0'; exit 0; fi\nprintf '%s\\n' \"$@\" >> '" + argsPath + "'\nexit 0\n"
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	budget, _ := generationbudget.New(generationbudget.Settings{})
	g := Generator{FFMpegConfig: intelOnlyBudgetConfig{intel: budget}, Encoder: ffmpeg.NewEncoder(binary), LockManager: fsutil.NewReadLockManager(), IntelSprites: &ffmpeg.IntelGenerationConfig{Backend: "vaapi"}}
	output := filepath.Join(dir, "sprite.jpg")
	if err := os.WriteFile(output, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	d, err := g.IntelSpriteSheet(context.Background(), "synthetic.mp4", []float64{0, 1}, 9, 9, output)
	if err == nil || d.Actual != "none" || d.Stage != "metadata" {
		t.Fatalf("diagnostic=%+v error=%v", d, err)
	}
	if _, err := os.Stat(argsPath); !os.IsNotExist(err) {
		t.Fatal("CPU fallback renderer ran", err)
	}
	data, err := os.ReadFile(output)
	if err != nil || string(data) != "original" {
		t.Fatal("metadata failure replaced existing sheet")
	}
	files, _ := filepath.Glob(filepath.Join(dir, ".sprite-*"))
	if len(files) != 0 {
		t.Fatal("temporary output leaked", files)
	}
	if g.generationBudget() != nil {
		t.Fatal("GPU operation mutated ordinary CPU scope")
	}
	release, err := budget.Acquire(context.Background(), generationbudget.GPU)
	if err != nil {
		t.Fatal("metadata failure leaked permit", err)
	}
	release()
}
