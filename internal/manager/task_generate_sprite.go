package manager

import (
	"context"
	"errors"
	"fmt"

	"github.com/stashapp/stash/pkg/fsutil"
	"github.com/stashapp/stash/pkg/logger"
	"github.com/stashapp/stash/pkg/models"
)

type GenerateSpriteTask struct {
	Scene               models.Scene
	Overwrite           bool
	fileNamingAlgorithm models.HashAlgorithm
}

func (t *GenerateSpriteTask) GetDescription() string {
	return fmt.Sprintf("Generating sprites for %s", t.Scene.Path)
}

func (t *GenerateSpriteTask) Start(ctx context.Context) error {
	if !t.required() {
		return nil
	}

	videoFile, err := instance.generationVideoFile(ctx, t.Scene.Path)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		logger.Errorf("error reading video file: %s", err.Error())
		return fmt.Errorf("reading sprite source: %w", err)
	}

	sceneHash := t.Scene.GetHash(t.fileNamingAlgorithm)
	imagePath := instance.Paths.Scene.GetSpriteImageFilePath(sceneHash)
	vttPath := instance.Paths.Scene.GetSpriteVttFilePath(sceneHash)
	generator, err := NewSpriteGenerator(ctx, *videoFile, sceneHash, imagePath, vttPath, 9, 9)

	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		logger.Errorf("error creating sprite generator: %s", err.Error())
		return fmt.Errorf("creating sprite generator: %w", err)
	}
	generator.Overwrite = t.Overwrite

	if t.Scene.VRMode != nil {
		generator.VRMode = string(*t.Scene.VRMode)
	}

	if t.Scene.StartPoint != nil && t.Scene.EndPoint != nil && *t.Scene.EndPoint > *t.Scene.StartPoint {
		generator.StartOffset = *t.Scene.StartPoint
		generator.Duration = *t.Scene.EndPoint - *t.Scene.StartPoint
		logger.Infof("Setting sprite generator offset: %f, duration: %f", generator.StartOffset, generator.Duration)
	}

	if err := generator.Generate(ctx); err != nil {
		logger.Errorf("error generating sprite: %s", err.Error())
		logErrorOutput(err)
		return fmt.Errorf("generating sprite: %w", err)
	}
	return nil
}

// required returns true if the sprite needs to be generated
func (t GenerateSpriteTask) required() bool {
	if t.Scene.Path == "" {
		return false
	}

	if t.Overwrite {
		return true
	}

	sceneHash := t.Scene.GetHash(t.fileNamingAlgorithm)
	return !t.doesSpriteExist(sceneHash)
}

func (t *GenerateSpriteTask) doesSpriteExist(sceneChecksum string) bool {
	if sceneChecksum == "" {
		return false
	}

	imagePath := instance.Paths.Scene.GetSpriteImageFilePath(sceneChecksum)
	vttPath := instance.Paths.Scene.GetSpriteVttFilePath(sceneChecksum)

	imageExists, _ := fsutil.FileExists(imagePath)
	vttExists, _ := fsutil.FileExists(vttPath)

	return imageExists && vttExists
}
