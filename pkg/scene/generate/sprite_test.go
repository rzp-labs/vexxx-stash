package generate

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSpriteMontageTileOrder(t *testing.T) {
	images := make([]image.Image, spriteChunks)
	for i := range images {
		tile := image.NewNRGBA(image.Rect(0, 0, 160, 90))
		for y := 0; y < 90; y++ {
			for x := 0; x < 160; x++ {
				tile.SetNRGBA(x, y, color.NRGBA{R: uint8(i), A: 255})
			}
		}
		images[i] = tile
	}
	montage := (Generator{}).CombineSpriteImages(images)
	if got := montage.Bounds().Size(); got != image.Pt(1440, 810) {
		t.Fatalf("montage size = %v", got)
	}
	for i := range images {
		for _, offset := range []image.Point{{0, 0}, {159, 89}, {80, 45}} {
			x, y := (i%9)*160+offset.X, (i/9)*90+offset.Y
			if got := color.NRGBAModel.Convert(montage.At(x, y)).(color.NRGBA); got != (color.NRGBA{R: uint8(i), A: 255}) {
				t.Errorf("tile %d at %d,%d = %v", i, x, y, got)
			}
		}
	}
}

func TestSpriteVTTCanonicalIntervalsAndCoordinates(t *testing.T) {
	dir := t.TempDir()
	sheet := filepath.Join(dir, "sprite.png")
	f, err := os.Create(sheet)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, image.NewNRGBA(image.Rect(0, 0, 1440, 810))); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	vtt := filepath.Join(dir, "sprite.vtt")
	if err := (Generator{}).spriteVTT(sheet, 1.25, 2.5)(nil, vtt); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(vtt)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) != 2+3*spriteChunks {
		t.Fatalf("VTT line count = %d", len(lines))
	}
	if lines[0] != "WEBVTT" || lines[1] != "" {
		t.Fatalf("invalid VTT header")
	}
	for i := 0; i < spriteChunks; i++ {
		// Use integer milliseconds here, independently of the production timestamp formatter.
		timestamp := func(ms int) string {
			return fmt.Sprintf("%02d:%02d:%02d.%03d", ms/3600000, ms/60000%60, ms/1000%60, ms%1000)
		}
		wantInterval := timestamp(2500+i*1250) + " --> " + timestamp(2500+(i+1)*1250)
		wantCoords := fmt.Sprintf("sprite.png#xywh=%d,%d,160,90", (i%9)*160, (i/9)*90)
		if lines[2+i*3] != wantInterval || lines[3+i*3] != wantCoords || lines[4+i*3] != "" {
			t.Errorf("cue %d = %q / %q", i, lines[2+i*3], lines[3+i*3])
		}
	}
}

func TestSpriteWidths(t *testing.T) {
	if got := SpriteTileWidth(""); got != 160 {
		t.Errorf("flat width = %d", got)
	}
	for _, vr := range []string{"LR180", "TB360", "MONO360", "FISHEYE190"} {
		if got := SpriteTileWidth(vr); got != 320 {
			t.Errorf("%s width = %d", vr, got)
		}
	}
}
