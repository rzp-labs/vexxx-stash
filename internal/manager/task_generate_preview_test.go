package manager

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stashapp/stash/pkg/ffmpeg"
	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/scene/generate"
)

func TestScenePreviewTaskReportsAssetFailures(t *testing.T) {
	for _, kind := range []string{"metadata", "mp4", "webp"} {
		t.Run(kind, func(t *testing.T) {
			mgr, input, _, _ := metadataFixture(t)
			previous := instance
			instance = mgr
			defer func() { instance = previous }()
			if err := os.MkdirAll(mgr.Paths.Generated.Tmp, 0700); err != nil {
				t.Fatal(err)
			}
			binary := filepath.Join(t.TempDir(), "ffmpeg")
			script := "#!/bin/sh\nif [ \"$1\" = '-version' ];then echo 'ffmpeg version 7.1';exit 0;fi\necho 'controlled preview failure' >&2\nexit 1\n"
			if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			mgr.FFMpeg = ffmpeg.NewEncoder(binary)
			g := &generate.Generator{Encoder: mgr.FFMpeg, Probe: mgr.FFProbe, FFMpegConfig: mgr.Config, LockManager: mgr.ReadLockManager, ScenePaths: mgr.Paths.Scene, Overwrite: true}
			task := GeneratePreviewTask{Scene: models.Scene{Path: input, Checksum: "fixture", HasPreview: true}, Options: generate.PreviewOptions{Segments: 2, SegmentDuration: 1}, Overwrite: true, generator: g, fileNamingAlgorithm: models.HashAlgorithmMd5}
			if kind == "metadata" {
				mgr.FFProbe = nil
			}
			if kind == "webp" {
				task.Overwrite = false
				exists, missing := true, false
				task.videoPreviewExists = &exists
				task.imagePreviewExists = &missing
				task.ImagePreview = true
			}
			err := task.Start(context.Background())
			if err == nil || !strings.Contains(err.Error(), map[string]string{"metadata": "reading scene preview source", "mp4": "generating scene preview", "webp": "generating scene preview WebP"}[kind]) {
				t.Fatalf("%s swallowed failure: %v", kind, err)
			}
			if _, err := os.Stat(mgr.Paths.Scene.GetVideoPreviewPath("fixture")); !os.IsNotExist(err) {
				t.Fatal("failed task published MP4", err)
			}
			entries, err := os.ReadDir(mgr.Paths.Generated.Tmp)
			if err != nil || len(entries) != 0 {
				t.Fatal("failed task leaked temporary files", entries, err)
			}
		})
	}
}
