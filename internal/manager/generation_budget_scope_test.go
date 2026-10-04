package manager

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stashapp/stash/internal/manager/config"
	"github.com/stashapp/stash/pkg/ffmpeg"
	"github.com/stashapp/stash/pkg/fsutil"
	"github.com/stashapp/stash/pkg/generationbudget"
	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/scene/generate"
)

func TestIntelOnlyBudgetPreservesCPUPhashCommands(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix command fixture")
	}
	for _, size := range [][2]int{{640, 358}, {8192, 4096}} {
		for _, shared := range []bool{false, true} {
			t.Run(fmt.Sprintf("%dx%d/shared=%t", size[0], size[1], shared), func(t *testing.T) {
				dir := t.TempDir()
				argsPath := filepath.Join(dir, "first-args")
				binary := filepath.Join(dir, "ffmpeg")
				// Fail extraction after recording its command so the task never writes
				// a fingerprint. This checks actual task routing without real media.
				script := "#!/bin/sh\nif [ \"$1\" = '-version' ]; then echo 'ffmpeg version 7.0'; exit 0; fi\nif [ ! -f '" + argsPath + "' ]; then printf '%s\\n' \"$@\" > '" + argsPath + "'; fi\nexit 1\n"
				if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
					t.Fatal(err)
				}
				c := config.InitializeEmpty()
				c.SetString(config.MarkerGenerationBackend, "vaapi")
				c.SetString(config.SpriteGenerationBackend, "vaapi")
				c.SetBool(config.GenerationBudgetEnabled, shared)
				c.SetBool(config.NativeGeneration, false)
				c.SetBool(config.NativePhashGeneration, true)
				previous := instance
				instance = &Manager{Config: c, FFMpeg: ffmpeg.NewEncoder(binary)}
				defer func() { instance = previous }()
				task := GeneratePhashTask{Overwrite: true, File: &models.VideoFile{
					BaseFile: &models.BaseFile{Path: "synthetic.mp4"}, Duration: 100,
					Width: size[0], Height: size[1],
				}}
				if err := task.Start(context.Background()); err != nil {
					t.Fatal(err)
				}
				data, err := os.ReadFile(argsPath)
				if err != nil {
					t.Fatal(err)
				}
				args := strings.Split(strings.TrimSpace(string(data)), "\n")
				inputs, bounded := 0, false
				for i, arg := range args {
					if arg == "-i" {
						inputs++
					}
					if arg == "-threads" && i+1 < len(args) && args[i+1] == "1" {
						bounded = true
					}
				}
				wantInputs := 1
				if !shared && size[0] == 640 {
					wantInputs = 25
				}
				if inputs != wantInputs || bounded != shared {
					t.Fatalf("CPU extraction changed scope: inputs=%d want=%d bounded=%t shared=%t args=%q", inputs, wantInputs, bounded, shared, args)
				}
			})
		}
	}
}

func TestIntelOnlyBudgetRetainsNativePoolExclusion(t *testing.T) {
	previous := instance
	defer func() { instance = previous }()
	for _, mode := range []string{"legacy", "intel", "shared"} {
		c := config.InitializeEmpty()
		c.SetBool(config.NativeGeneration, true)
		c.SetBool(config.NativePhashGeneration, true)
		if mode == "intel" {
			c.SetString(config.MarkerGenerationBackend, "vaapi")
		}
		if mode == "shared" {
			c.SetBool(config.GenerationBudgetEnabled, true)
		}
		instance = &Manager{Config: c}
		if nativeGenerationAllowed() != (mode == "legacy") {
			t.Fatalf("separate native pools admitted under %s budget", mode)
		}
		if mode == "intel" && c.GetGenerationBudget() != nil {
			t.Fatal("native exclusion must not implicitly constrain CPU pHash")
		}
	}
}

func TestIntelSpriteManagerEligibilityFallbackKeepsAdmission(t *testing.T) {
	for _, vr := range []bool{false, true} {
		c := config.InitializeEmpty()
		c.SetString(config.SpriteGenerationBackend, "vaapi")
		budget := c.GetIntelGenerationBudget()
		release, _ := budget.Acquire(context.Background(), generationbudget.GPU)
		g := &SpriteGenerator{g: &generate.Generator{FFMpegConfig: c, IntelSprites: c.GetIntelSpriteGeneration(), LockManager: fsutil.NewReadLockManager()}}
		req := spriteRequest{path: "synthetic.mp4", count: 1, frameCount: 1, streamDuration: 100, slowSeek: !vr}
		if vr {
			req.vrMode = "LR180"
		}
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
		_, handled, err := g.intelSpriteTiles(ctx, req)
		cancel()
		release()
		if !handled || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("manager eligibility fallback escaped Intel admission (VR=%t): %v", vr, err)
		}
	}
}
