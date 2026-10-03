package generate

import (
	"context"
	"fmt"
	"image"
	"os"
	"path/filepath"

	"github.com/disintegration/imaging"
	"github.com/stashapp/stash/pkg/generationbudget"
)

// SaveSprite bounds CPU composition/encoding and publishes only a complete
// image. A failed or cancelled encode removes its temporary output.
func (g Generator) SaveSprite(ctx context.Context, images []image.Image, output string) error {
	if len(images) == 0 {
		return fmt.Errorf("sprite images are empty")
	}
	release, err := g.generationBudget().Acquire(ctx, generationbudget.CPU)
	if err != nil {
		return err
	}
	defer release()
	// A destination-adjacent file guarantees a same-filesystem rename. SafeMove
	// can copy into the destination on EXDEV, which would expose partial output.
	tmp, err := os.CreateTemp(filepath.Dir(output), "."+filepath.Base(output)+"-*"+filepath.Ext(output))
	if err != nil {
		return fmt.Errorf("creating sprite temporary file: %w", err)
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing sprite temporary file: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	montage := g.CombineSpriteImages(images)
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := imaging.Save(montage, tmp.Name()); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), output); err != nil {
		return fmt.Errorf("publishing sprite image: %w", err)
	}
	return nil
}
