package manager

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/big"
	"strconv"

	"github.com/stashapp/stash/pkg/ffmpeg"
	"github.com/stashapp/stash/pkg/fsutil"
	"github.com/stashapp/stash/pkg/logger"
	"github.com/stashapp/stash/pkg/scene/generate"
)

// SpriteGenerator generates sprite images and VTT files for scenes.

type SpriteGenerator struct {
	Info *generatorInfo

	VideoChecksum   string
	ImageOutputPath string
	VTTOutputPath   string
	Rows            int
	Columns         int
	SlowSeek        bool // use alternate seek function, very slow!

	Overwrite bool

	StartOffset float64
	Duration    float64

	VRMode string

	g *generate.Generator
}

func NewSpriteGenerator(ctx context.Context, videoFile ffmpeg.VideoFile, videoChecksum string, imageOutputPath string, vttOutputPath string, rows int, cols int) (*SpriteGenerator, error) {
	exists, err := fsutil.FileExists(videoFile.Path)
	if !exists {
		return nil, err
	}
	intelConfig := instance.Config.GetIntelSpriteGeneration()
	gpuSelected := intelConfig != nil && intelConfig.Enabled()
	gpuGenerator := generate.Generator{Encoder: instance.FFMpeg, Probe: instance.FFProbe, IntelSprites: intelConfig, FFMpegConfig: instance.Config, LockManager: instance.ReadLockManager}
	slowSeek := false
	chunkCount := rows * cols

	// For files with small duration / low frame count  try to seek using frame number intead of seconds
	if videoFile.VideoStreamDuration < 5 || (0 < videoFile.FrameCount && videoFile.FrameCount <= int64(chunkCount)) { // some files can have FrameCount == 0, only use SlowSeek  if duration < 5
		if videoFile.VideoStreamDuration <= 0 {
			s := fmt.Sprintf("video %s: duration(%.3f)/frame count(%d) invalid, skipping sprite creation", videoFile.Path, videoFile.VideoStreamDuration, videoFile.FrameCount)
			return nil, errors.New(s)
		}
		logger.Warnf("[generator] video %s too short (%.3fs, %d frames), using frame seeking", videoFile.Path, videoFile.VideoStreamDuration, videoFile.FrameCount)
		slowSeek = true
		// do an actual frame count of the file ( number of frames = read frames)
		var fc int64
		var countErr error
		if gpuSelected {
			frameInfo, err := gpuGenerator.IntelSpriteFrameInfo(ctx, videoFile.Path)
			countErr = err
			if err == nil {
				fc = int64(frameInfo.NumberOfFrames)
				if videoFile.FrameRate <= 0 {
					videoFile.FrameRate = frameInfo.FrameRate
				}
			}
		} else {
			fc, countErr = GetInstance().generationReadFrameCount(ctx, videoFile.Path)
		}
		err := countErr
		if gpuSelected && err != nil {
			return nil, fmt.Errorf("GPU sprite frame count failed without software decoding: %w", err)
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		if err == nil {
			if fc != videoFile.FrameCount {
				logger.Warnf("[generator] updating framecount (%d) for %s with read frames count (%d)", videoFile.FrameCount, videoFile.Path, fc)
				videoFile.FrameCount = fc
			}
		}
	}

	generator, err := newGeneratorInfo(videoFile)
	if err != nil {
		return nil, err
	}
	generator.ChunkCount = chunkCount
	if gpuSelected {
		if err := configureGPUSpriteInfo(generator, slowSeek); err != nil {
			return nil, err
		}
	} else if err := generator.configure(ctx); err != nil {
		return nil, err
	}

	return &SpriteGenerator{
		Info:            generator,
		VideoChecksum:   videoChecksum,
		ImageOutputPath: imageOutputPath,
		VTTOutputPath:   vttOutputPath,
		Rows:            rows,
		SlowSeek:        slowSeek,
		Columns:         cols,
		g: &generate.Generator{
			Encoder:      instance.FFMpeg,
			Probe:        instance.FFProbe,
			IntelMarker:  instance.Config.GetIntelMarkerGeneration(),
			IntelSprites: instance.Config.GetIntelSpriteGeneration(),
			FFMpegConfig: instance.Config,
			LockManager:  instance.ReadLockManager,
			ScenePaths:   instance.Paths.Scene,
		},
	}, nil
}

func (g *SpriteGenerator) Generate(ctx context.Context) error {
	if err := g.generateSpriteImage(ctx); err != nil {
		return err
	}
	if err := g.generateSpriteVTT(ctx); err != nil {
		return err
	}
	return nil
}

func (g *SpriteGenerator) generateSpriteImage(ctx context.Context) error {
	if !g.Overwrite && g.imageExists() {
		return nil
	}

	if handled, err := g.intelSpriteSheet(ctx, g.spriteRequest()); handled {
		return err
	}

	images, err := g.spriteTiles(ctx)
	if err != nil {
		return err
	}

	if len(images) == 0 {
		return fmt.Errorf("images slice is empty, failed to generate sprite images for %s", g.Info.VideoFile.Path)
	}

	return g.g.SaveSprite(ctx, images, g.ImageOutputPath)
}

func (g *SpriteGenerator) generateSpriteVTT(ctx context.Context) error {
	if !g.Overwrite && g.vttExists() {
		return nil
	}
	logger.Infof("[generator] generating sprite vtt for %s", g.Info.VideoFile.Path)

	var stepSize float64
	if g.Duration > 0 {
		stepSize = g.Duration / float64(g.Info.ChunkCount)
	} else if !g.SlowSeek && g.g.IntelSprites != nil && g.g.IntelSprites.Enabled() {
		stepSize = g.Info.VideoFile.VideoStreamDuration / float64(g.Info.ChunkCount)
	} else if !g.SlowSeek {
		stepSize = float64(g.Info.NthFrame) / g.Info.FrameRate
	} else {
		// for files with a low framecount (<ChunkCount) g.Info.NthFrame can be zero
		// so recalculate from scratch
		stepSize = float64(g.Info.VideoFile.FrameCount-1) / float64(g.Info.ChunkCount)
		stepSize /= g.Info.FrameRate
	}

	return g.g.SpriteVTT(ctx, g.VTTOutputPath, g.ImageOutputPath, stepSize, g.StartOffset)
}

func (g *SpriteGenerator) imageExists() bool {
	exists, _ := fsutil.FileExists(g.ImageOutputPath)
	return exists
}

func (g *SpriteGenerator) vttExists() bool {
	exists, _ := fsutil.FileExists(g.VTTOutputPath)
	return exists
}

// configureGPUSpriteInfo derives VTT metadata without entering legacy full-file
// frame inspection. Time-based GPU sheets index the actual requested interval;
// short clips already have an exact GPU-decoded frame count.
func configureGPUSpriteInfo(info *generatorInfo, frameSampling bool) error {
	if info.VideoFile.VideoStream == nil {
		return fmt.Errorf("missing video stream")
	}
	if info.ChunkCount <= 0 {
		return fmt.Errorf("sprite tile count must be positive")
	}
	rate := info.VideoFile.FrameRate
	if rate <= 0 || math.IsNaN(rate) || math.IsInf(rate, 0) {
		rational, ok := new(big.Rat).SetString(info.VideoFile.VideoStream.RFrameRate)
		if ok && rational.Sign() > 0 {
			rate, _ = rational.Float64()
		}
	}
	if math.IsNaN(rate) || math.IsInf(rate, 0) || rate < 0 {
		rate = 0
	}
	if frameSampling && rate <= 0 {
		return fmt.Errorf("GPU sprite frame-sampled VTT requires a finite positive frame rate")
	}
	frames := info.VideoFile.FrameCount
	if frames <= 0 {
		frames, _ = strconv.ParseInt(info.VideoFile.VideoStream.NbFrames, 10, 64)
	}
	if frames <= 0 && rate > 0 && info.VideoFile.VideoStreamDuration > 0 {
		frames = int64(rate * info.VideoFile.VideoStreamDuration)
	}
	if rate == 0 && info.VideoFile.VideoStreamDuration <= 0 {
		return fmt.Errorf("GPU sprite VTT duration and frame rate are unavailable")
	}
	if frames < 0 || frames >= math.MaxInt {
		return fmt.Errorf("GPU sprite frame count is invalid")
	}
	info.FrameRate = rate
	info.NumberOfFrames = int(frames)
	info.NthFrame = info.NumberOfFrames / info.ChunkCount
	return nil
}
