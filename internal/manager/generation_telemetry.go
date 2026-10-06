package manager

import (
	"context"

	"github.com/stashapp/stash/pkg/file/video"
	"github.com/stashapp/stash/pkg/models"
)

func scenePrivateValues(scene *models.Scene) []string {
	if scene == nil {
		return nil
	}
	values := []string{scene.Path}
	if scene.Path != "" {
		values = append(values, video.GetFunscriptPath(scene.Path))
	}
	if scene.Files.Loaded() {
		for _, file := range scene.Files.List() {
			values = append(values, file.Path)
			values = append(values, video.GetFunscriptPath(file.Path))
		}
	} else if scene.Files.PrimaryLoaded() && scene.Files.Primary() != nil {
		values = append(values, scene.Files.Primary().Path)
		values = append(values, video.GetFunscriptPath(scene.Files.Primary().Path))
	}
	if scene.FunscriptPath != nil {
		values = append(values, *scene.FunscriptPath)
	}
	return values
}

func imagePrivateValues(image *models.Image) []string {
	values := []string{image.Path}
	if image.Files.Loaded() {
		for _, file := range image.Files.List() {
			values = append(values, file.Base().Path)
		}
	} else if image.Files.PrimaryLoaded() && image.Files.Primary() != nil {
		values = append(values, image.Files.Primary().Base().Path)
	}
	return values
}

func generationTaskPrivateValues(task Task) []string {
	switch t := task.(type) {
	case *GenerateSpriteTask:
		return scenePrivateValues(&t.Scene)
	case *GeneratePreviewTask:
		return scenePrivateValues(&t.Scene)
	case *GenerateMarkersTask:
		return scenePrivateValues(t.Scene)
	case *GenerateCoverTask:
		return scenePrivateValues(&t.Scene)
	case *GeneratePhashTask:
		values := scenePrivateValues(t.Scene)
		if t.File != nil {
			values = append(values, t.File.Path)
		}
		return values
	case *GenerateImagePhashTask:
		if t.File != nil {
			return []string{t.File.Path}
		}
	case *GenerateClipPreviewTask:
		return imagePrivateValues(&t.Image)
	case *GenerateImageThumbnailTask:
		return imagePrivateValues(&t.Image)
	case *GenerateInteractiveHeatmapSpeedTask:
		return scenePrivateValues(&t.Scene)
	case *GenerateGalleryTask:
		return scenePrivateValues(&t.Scene)
	}
	return nil
}

// Use stable workload names rather than task descriptions, which contain media
// paths and library identifiers.
func generationTaskWorkload(task Task) string {
	switch task.(type) {
	case *GenerateSpriteTask:
		return "sprite"
	case *GeneratePreviewTask:
		return "preview"
	case *GenerateMarkersTask:
		return "marker"
	case *GenerateCoverTask:
		return "cover"
	case *GeneratePhashTask:
		return "phash"
	case *GenerateImagePhashTask:
		return "image_phash"
	case *GenerateClipPreviewTask:
		return "clip_preview"
	case *GenerateImageThumbnailTask:
		return "image_thumbnail"
	case *GenerateInteractiveHeatmapSpeedTask:
		return "interactive_heatmap"
	case *GenerateGalleryTask:
		return "gallery"
	default:
		return "generation"
	}
}

// Legacy tasks sometimes log a failed output and return nil. Observe those
// existing failure sites without changing their job-status or retry semantics.
type generationFailureReporterKey struct{}

func withGenerationFailureReporter(ctx context.Context, report func(error)) context.Context {
	return context.WithValue(ctx, generationFailureReporterKey{}, report)
}
func reportGenerationFailure(ctx context.Context, err error) {
	if report, ok := ctx.Value(generationFailureReporterKey{}).(func(error)); ok {
		report(err)
	}
}
