package manager

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stashapp/stash/internal/manager/config"
	"github.com/stashapp/stash/pkg/ffmpeg"
	"github.com/stashapp/stash/pkg/generationbudget"
	"github.com/stashapp/stash/pkg/job"
	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/models/mocks"
	"github.com/stretchr/testify/mock"
)

func TestGPUSpriteTaskErrorsReachJobAndPreserveAssets(t *testing.T) {
	for _, failure := range []string{"source", "constructor", "device"} {
		t.Run(failure, func(t *testing.T) {
			mgr, input, _, _ := metadataFixture(t)
			previous := instance
			instance = mgr
			t.Cleanup(func() { instance = previous })
			mgr.Config.SetInterface(config.SpriteGenerationBackend, "vaapi")
			mgr.Config.SetInterface(config.GenerationDevice, "/dev/dri/renderD999")
			// Accept only strict hardware metadata. Any software render attempt
			// fails the fixture rather than accidentally satisfying this test.
			binary := filepath.Join(filepath.Dir(input), "ffmpeg")
			frame := `VEXXX_GPU_METADATA={"stream_index":0,"bit_depth":8,"is_rgb":false,"width":64,"height":36,"pix_fmt":"nv12","sample_aspect_ratio":"1/1","color_range":"tv","color_space":"bt709","color_primaries":"bt709","color_transfer":"bt709","frame_rate":"24/1"}`
			script := "#!/bin/sh\nfor arg in \"$@\"; do if [ \"$arg\" = '-hwaccel_metadata' ]; then printf '%s\\n' '" + frame + "';exit 0;fi;done\necho 'unexpected pixel generation' >&2\nexit 77\n"
			if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			mgr.FFMpeg = ffmpeg.NewEncoder(binary)
			var metadata map[string]any
			if err := json.Unmarshal([]byte(generationMetadataJSON), &metadata); err != nil {
				t.Fatal(err)
			}
			video := metadata["streams"].([]any)[0].(map[string]any)
			video["pix_fmt"] = "yuv420p"
			if failure == "device" {
				delete(video, "side_data_list")
				video["duration"], video["nb_frames"] = "10", "300"
				video["nb_read_frames"] = "300"
				metadata["format"].(map[string]any)["duration"] = "10"
			}
			data, err := json.Marshal(metadata)
			if err != nil {
				t.Fatal(err)
			}
			probe := filepath.Join(filepath.Dir(input), "ffprobe")
			if err := os.WriteFile(probe, []byte("#!/bin/sh\nprintf '%s' '"+string(data)+"'\n"), 0700); err != nil {
				t.Fatal(err)
			}
			if failure == "source" {
				if err := os.Remove(input); err != nil {
					t.Fatal(err)
				}
			}
			file := &models.VideoFile{BaseFile: &models.BaseFile{ID: 1, Path: input}, Width: 64, Height: 36, Duration: 10}
			scene := &models.Scene{ID: 1, Path: input, Checksum: "fixture", OSHash: "fixture"}
			oldAssets := []string{mgr.Paths.Scene.GetSpriteImageFilePath("fixture"), mgr.Paths.Scene.GetSpriteVttFilePath("fixture")}
			for _, path := range oldAssets {
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("existing asset"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			db := mocks.NewDatabase()
			db.Scene.On("FindMany", mock.Anything, []int{1}).Return([]*models.Scene{scene}, nil)
			db.Scene.On("GetFiles", mock.Anything, 1).Return([]*models.VideoFile{file}, nil)
			m := job.NewManager()
			t.Cleanup(func() { m.StopAndWait(time.Second) })
			j := &GenerateJob{repository: models.Repository{TxnManager: db, Scene: db.Scene}, input: GenerateMetadataInput{SceneIDs: []string{"1"}, Sprites: true, Overwrite: true}}
			id := m.Add(context.Background(), "GPU sprite failure", j)
			result := waitMarkerJob(t, m, id)
			stage := map[string]string{"source": "reading sprite source", "constructor": "creating sprite generator", "device": "generating sprite"}[failure]
			if result.Error == nil {
				t.Fatal("GPU sprite error missing", result.Status)
			}
			if result.Status != job.StatusFailed || !strings.Contains(*result.Error, stage) {
				t.Fatalf("GPU sprite status=%s error=%s; expected %s", result.Status, *result.Error, stage)
			}
			for _, path := range oldAssets {
				if data, err := os.ReadFile(path); err != nil || string(data) != "existing asset" {
					t.Fatalf("failed GPU request replaced %s: %q %v", path, data, err)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			release, err := mgr.Config.GetGenerationBudget().Acquire(ctx, generationbudget.GPU)
			if err != nil {
				t.Fatal("failed sprite task leaked permits", err)
			}
			release()
		})
	}
}
