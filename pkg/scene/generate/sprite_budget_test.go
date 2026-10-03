package generate

import (
	"context"
	"errors"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/stashapp/stash/pkg/generationbudget"
)

type spriteCancelImage struct {
	image.Image
	cancel context.CancelFunc
}

func (i spriteCancelImage) Bounds() image.Rectangle {
	i.cancel()
	return i.Image.Bounds()
}

func TestSaveSpriteAtomicOutputAndCleanup(t *testing.T) {
	for _, mode := range []string{"success", "encode failure", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			output := filepath.Join(dir, "sprite.png")
			if mode == "encode failure" {
				output = filepath.Join(dir, "sprite.invalid")
			}
			prior := []byte("existing output")
			if err := os.WriteFile(output, prior, 0600); err != nil {
				t.Fatal(err)
			}
			budget, err := generationbudget.New(generationbudget.Settings{})
			if err != nil {
				t.Fatal(err)
			}
			g := Generator{Budget: budget}
			var tile image.Image = image.NewNRGBA(image.Rect(0, 0, 160, 90))
			if mode == "cancel" {
				tile = spriteCancelImage{Image: tile, cancel: cancel}
			}
			err = g.SaveSprite(ctx, []image.Image{tile}, output)
			data, readErr := os.ReadFile(output)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if mode == "success" {
				if err != nil {
					t.Fatal(err)
				}
				f, err := os.Open(output)
				if err != nil {
					t.Fatal(err)
				}
				cfg, err := png.DecodeConfig(f)
				f.Close()
				if err != nil || cfg.Width != 1440 || cfg.Height != 810 {
					t.Fatalf("published config=%+v error=%v", cfg, err)
				}
			} else if err == nil || !reflect.DeepEqual(data, prior) {
				t.Fatalf("failure changed prior output: error=%v data=%q", err, data)
			}
			if mode == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatalf("error=%v", err)
			}
			files, err := os.ReadDir(dir)
			if err != nil || len(files) != 1 || files[0].Name() != filepath.Base(output) {
				t.Fatalf("temp files=%v error=%v", files, err)
			}
			release, err := budget.Acquire(context.Background(), generationbudget.CPU)
			if err != nil {
				t.Fatal(err)
			}
			release()
		})
	}
}

func TestSaveSpriteRenameFailurePreservesDestinationAndCleansTemp(t *testing.T) {
	dir := t.TempDir()
	output := filepath.Join(dir, "sprite.png")
	if err := os.Mkdir(output, 0700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(output, "existing")
	if err := os.WriteFile(sentinel, []byte("unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	err := (Generator{}).SaveSprite(context.Background(), []image.Image{image.NewNRGBA(image.Rect(0, 0, 160, 90))}, output)
	if err == nil {
		t.Fatal("rename to directory should fail")
	}
	data, err := os.ReadFile(sentinel)
	if err != nil || string(data) != "unchanged" {
		t.Fatalf("prior destination damaged: %q %v", data, err)
	}
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 1 || files[0].Name() != "sprite.png" {
		t.Fatalf("temporary output leaked: %v %v", files, err)
	}
}
