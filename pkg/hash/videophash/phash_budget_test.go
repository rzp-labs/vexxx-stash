package videophash

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/corona10/goimagehash"
	"github.com/stashapp/stash/pkg/ffmpeg"
	"github.com/stashapp/stash/pkg/generationbudget"
	"github.com/stashapp/stash/pkg/models"
)

func TestBudgetedPhashPreCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	controls := PhashOptions{Context: ctx}
	if _, err := Generate(nil, nil, controls); !errors.Is(err, context.Canceled) {
		t.Fatalf("generate: %v", err)
	}
	if _, err := spriteBatch(nil, "synthetic.mp4", []float64{1, 2}, controls); !errors.Is(err, context.Canceled) {
		t.Fatalf("batch: %v", err)
	}
	if _, err := generateSpriteScreenshot(nil, "synthetic.mp4", 1, controls); !errors.Is(err, context.Canceled) {
		t.Fatalf("frame: %v", err)
	}
}

func TestBudgetedPhashActiveCancellationAvoidsFallback(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix subprocess fixture")
	}
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	fixture := filepath.Join(dir, "ffmpeg")
	script := "#!/bin/sh\nif [ \"$1\" = \"-version\" ]; then echo 'ffmpeg version 7.0'; exit 0; fi\nprintf 'run\\n' >> '" + calls + "'\nexec sleep 30\n"
	if err := os.WriteFile(fixture, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	encoder := ffmpeg.NewEncoder(fixture)
	budget, _ := generationbudget.New(generationbudget.Settings{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := generateSpriteScreenshots(encoder, "synthetic.mp4", []float64{1, 2}, 2, PhashOptions{Context: ctx, Budget: budget})
		done <- err
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		// The shell creates the file before printf writes its content. Wait for
		// the completed signal, otherwise cancellation can interrupt the write.
		if data, err := os.ReadFile(calls); err == nil && string(data) == "run\n" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fixture did not start")
		}
		runtime.Gosched()
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("CPU phash cancellation hung")
	}
	data, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(data), "run\n") != 1 {
		t.Fatalf("cancelled batch retried fallback: %q", data)
	}
}

// TestBudgetedSpriteMatchesCanonicalRealFile compares pixels before comparing
// hashes: thread limits must not alter a frame, accurate seek, scale or montage.
// Use only synthetic or explicitly authorized media in STASH_PHASH_TEST_FILES.
func TestBudgetedSpriteMatchesCanonicalRealFile(t *testing.T) {
	encoder := realFileEncoder(t)
	budget, _ := generationbudget.New(generationbudget.Settings{})
	for _, path := range realFilePaths(t) {
		t.Run(shortName(path), func(t *testing.T) {
			duration, width, height := probeDurationSize(t, path)
			times := spriteTimes(duration)
			batch := batchSizeFor(width, height)
			legacy, err := generateSpriteScreenshots(encoder, path, times, batch)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			bounded, err := generateSpriteScreenshots(encoder, path, times, batch, PhashOptions{Context: ctx, Budget: budget})
			if err != nil {
				t.Fatal(err)
			}
			if len(legacy) != len(bounded) {
				t.Fatal("frame count changed")
			}
			for i := range legacy {
				if diff := maxAbsDiff(legacy[i], bounded[i]); diff != 0 {
					t.Errorf("frame %d differs by %d", i, diff)
				}
			}
			legacyHash, err := goimagehash.PerceptionHash(combineImages(legacy))
			if err != nil {
				t.Fatal(err)
			}
			boundedHash, err := goimagehash.PerceptionHash(combineImages(bounded))
			if err != nil {
				t.Fatal(err)
			}
			if legacyHash.GetHash() != boundedHash.GetHash() {
				t.Fatalf("hash changed: %016x vs %016x", legacyHash.GetHash(), boundedHash.GetHash())
			}
			// Also compare the public API with native requested: an active budget
			// routes through the canonical CPU implementation before storing a hash.
			generated, err := Generate(encoder, &models.VideoFile{BaseFile: &models.BaseFile{Path: path}, Duration: duration, Width: width, Height: height}, PhashOptions{Context: ctx, Budget: budget, Native: true})
			if err != nil {
				t.Fatal(err)
			}
			if *generated != legacyHash.GetHash() {
				t.Fatalf("public hash changed: %016x vs %016x", legacyHash.GetHash(), *generated)
			}
			t.Logf("25 exact frames and hash %016x preserved", *generated)
		})
	}
}
