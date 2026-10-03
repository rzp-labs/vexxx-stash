package main

import (
	"context"
	"errors"
	"fmt"
	"image"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/stashapp/stash/pkg/ffmpeg"
	"github.com/stashapp/stash/pkg/generationbudget"
	"github.com/stashapp/stash/pkg/hash/videophash"
	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/scene/generate"
	"github.com/stashapp/stash/pkg/utils"
)

type mixedJob struct {
	Name            string                             `json:"name"`
	Status          string                             `json:"status"`
	StartedMS       float64                            `json:"started_ms"`
	CompletedMS     float64                            `json:"completed_ms"`
	Error           string                             `json:"error,omitempty"`
	Diagnostics     []ffmpeg.IntelGenerationDiagnostic `json:"diagnostics"`
	SelectedBackend string                             `json:"selected_backend"`
	ActualBackend   string                             `json:"actual_backend"`
	Validation      map[string]any                     `json:"validation,omitempty"`
}

// runMixed coordinates four jobs using the exact same generator dependencies
// and budget. Only callback attribution is copied per job: no parent scene job
// holds a permit while waiting for its own generator's subprocess stages.
func runMixed(ctx context.Context, g *generate.Generator, paths labPaths, input, hashFixture, outDir string) (map[string]any, error) {
	if g.Budget == nil || g.Budget.Settings().MaxProcesses != 1 || g.Budget.Settings().MaxGPUProcesses != 1 || g.Budget.Settings().Threads != 1 {
		return map[string]any{"status": "failed"}, errors.New("mixed requires one shared process/GPU slot and one thread")
	}
	for _, fixture := range []struct {
		path string
		hash bool
	}{{input, false}, {hashFixture, true}} {
		release, err := g.Budget.Acquire(ctx, generationbudget.CPU)
		if err != nil {
			return map[string]any{"status": "failed"}, err
		}
		source, err := g.Probe.IntelSource(ctx, fixture.path)
		release()
		if err != nil {
			return map[string]any{"status": "failed"}, err
		}
		if err := mixedFixtureContract(source, fixture.hash); err != nil {
			return map[string]any{"status": "failed"}, err
		}
	}
	start := time.Now()
	metrics := startMixedSampler(ctx)
	jobs := make([]mixedJob, 4)
	names := []string{"marker_mp4", "animated_webp", "sprites_81", "canonical_cpu_phash"}
	ready := make(chan struct{})
	var wg sync.WaitGroup
	var completionMu sync.Mutex
	var completion []string
	for i, name := range names {
		wg.Add(1)
		go func(i int, name string) {
			defer wg.Done()
			<-ready
			job := &jobs[i]
			job.Name, job.Status, job.StartedMS = name, "running", float64(time.Since(start).Microseconds())/1000
			// Generator methods are value receivers; all coordination dependencies
			// still point to the single configured generator's shared objects.
			local := *g
			local.IntelDiagnostic = func(d ffmpeg.IntelGenerationDiagnostic) {
				job.Diagnostics = append(job.Diagnostics, d)
				if g.IntelDiagnostic != nil {
					g.IntelDiagnostic(d)
				}
			}
			var err error
			switch name {
			case "marker_mp4":
				end := 3.0
				err = local.MarkerPreviewVideo(ctx, input, "synthetic", 1, &end, false, "")
				if err == nil {
					// Validation uses the same admission limit as generation.
					err = g.Budget.Run(ctx, generationbudget.CPU, func(ctx context.Context) error {
						var e error
						job.Validation, e = validateMarker(ctx, paths.GetVideoPreviewPath("", 0), 2)
						return e
					})
				}
			case "animated_webp":
				err = local.SceneMarkerWebp(ctx, input, "synthetic", 1, "")
				local.IntelDiagnostic(ffmpeg.IntelGenerationDiagnostic{Selected: "software", Actual: "software", Reason: "lossless1/compression6 CPU WebP; presetnone corrects legacy presetdefault lossy-output bug"})
				if err == nil {
					var data []byte
					data, err = os.ReadFile(paths.GetWebpPreviewPath("", 0))
					if err == nil {
						job.Validation, err = validateAnimatedWebP(data)
					}
				}
			case "sprites_81":
				times := make([]float64, 81)
				for i := range times {
					times[i] = 1 + float64(i)*2/81
				}
				var images []image.Image
				images, _, err = local.IntelSpriteTiles(ctx, input, times)
				if err == nil {
					if len(images) != 81 {
						err = fmt.Errorf("got%d sprite tiles, expected81", len(images))
					}
					for _, img := range images {
						if img == nil || img.Bounds().Dx() != 160 || img.Bounds().Dy() != 90 {
							err = errors.New("unexpected sprite tile geometry")
							break
						}
					}
				}
				if err == nil {
					err = local.SaveSprite(ctx, images, filepath.Join(outDir, "sprite.jpg"))
				}
				if err == nil {
					err = g.Budget.Run(ctx, generationbudget.CPU, func(ctx context.Context) error {
						vtt := filepath.Join(outDir, "sprite.vtt")
						if e := local.SpriteVTT(ctx, vtt, filepath.Join(outDir, "sprite.jpg"), 2.0/81, 1); e != nil {
							return e
						}
						return validateMixedVTT(vtt)
					})
				}
				job.Validation = map[string]any{"tile_count": len(images), "timestamps": times, "tile_geometry": "160x90", "montage_geometry": "1440x810", "vtt": "canonical81 intervals/coordinates checked on success", "visual": "untested"}
			case "canonical_cpu_phash":
				var hash *uint64
				// The hash coordinator owns the whole-hash CPU permit. Canonical
				// extraction only uses Budget for thread arguments, never reacquires.
				err = g.Budget.Run(ctx, generationbudget.CPU, func(ctx context.Context) error {
					var e error
					file := &models.VideoFile{BaseFile: &models.BaseFile{Path: hashFixture}, Duration: 10, Width: 640, Height: 360}
					hash, e = videophash.Generate(g.Encoder, file, videophash.PhashOptions{Context: ctx, Budget: g.Budget, Native: false})
					return e
				})
				local.IntelDiagnostic(ffmpeg.IntelGenerationDiagnostic{Selected: "software", Actual: "software", Reason: "canonical CPU pHash; GPU pHash gate remains closed"})
				if err == nil && hash != nil {
					job.Validation = map[string]any{"hash": fmt.Sprintf("%016x", *hash), "frame_count": 25, "exact_compatibility": "prior640 fixture parity test required; this run is mixed-queue execution only"}
				}
			}
			job.Status = "passed"
			job.SelectedBackend, job.ActualBackend = "unreported", "unreported"
			if len(job.Diagnostics) > 0 {
				d := job.Diagnostics[len(job.Diagnostics)-1]
				job.SelectedBackend, job.ActualBackend = d.Selected, d.Actual
				if job.SelectedBackend == "" {
					job.SelectedBackend = "software"
				}
			}
			if err != nil {
				job.Error, job.Status = err.Error(), "failed"
				if ctx.Err() != nil {
					job.Status = "cancelled"
				}
			}
			job.CompletedMS = float64(time.Since(start).Microseconds()) / 1000
			completionMu.Lock()
			completion = append(completion, name)
			completionMu.Unlock()
		}(i, name)
	}
	close(ready)
	wg.Wait()
	metrics.Stop()
	// A fresh bounded acquisition proves the shared permit drains after success,
	// failure or caller cancellation without reusing the cancelled context.
	drainCtx, drainCancel := context.WithTimeout(context.Background(), time.Second)
	defer drainCancel()
	release, drainErr := g.Budget.Acquire(drainCtx, generationbudget.CPU)
	if drainErr == nil {
		release()
	}
	report := map[string]any{"status": "passed", "jobs": jobs, "completion_order": completion, "shared_budget": g.Budget.Settings(), "observations": metrics.Report(), "budget_reusable": drainErr == nil, "all_coordinators_drained": true, "ui_playback_responsiveness": "untested; scheduler delay measures this CLI only", "fairness": "finite four-job completion order; per-leaf FIFO/starvation proven by budget regression tests, not this finite run"}
	var errs []error
	for _, job := range jobs {
		if job.Error != "" {
			errs = append(errs, fmt.Errorf("%s: %s", job.Name, job.Error))
		}
	}
	if drainErr != nil {
		errs = append(errs, fmt.Errorf("budget drain: %w", drainErr))
	}
	if metrics.Available && metrics.PeakChildren > 1 {
		errs = append(errs, fmt.Errorf("observed%d concurrent own FFmpeg/FFprobe children", metrics.PeakChildren))
	}
	if metrics.Available && metrics.FinalChildren != 0 {
		errs = append(errs, fmt.Errorf("%d own media children remain after coordinator drain", metrics.FinalChildren))
	}
	if len(errs) > 0 {
		report["status"] = "failed"
		return report, errors.Join(errs...)
	}
	return report, nil
}

func mixedFixtureContract(s ffmpeg.IntelSource, hash bool) error {
	duration, err := strconv.ParseFloat(s.Duration, 64)
	if err != nil || math.IsNaN(duration) || math.IsInf(duration, 0) || duration < 9.99 || duration > 10.01 || s.Rotation != 0 || s.Width <= 0 || s.Height <= 0 || s.Width*9 != s.Height*16 || s.FrameRate != "30/1" || s.AverageFrameRate != "30/1" || s.PixelFormat != "yuv420p" {
		return errors.New("mixed fixtures must be10s30fps16:9 unrotated8-bit yuv420p synthetic inputs")
	}
	if hash && (s.Width != 640 || s.Height != 360) {
		return errors.New("mixed canonical pHash fixture must be640x360; larger inputs are outside this bounded lab contract")
	}
	return nil
}

func validateMixedVTT(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := []string{"WEBVTT", ""}
	for i := 0; i < 81; i++ {
		lines = append(lines, utils.GetVTTTime(1+float64(i)*2/81)+" --> "+utils.GetVTTTime(1+float64(i+1)*2/81), fmt.Sprintf("sprite.jpg#xywh=%d,%d,160,90", 160*(i%9), 90*(i/9)), "")
	}
	if string(data) != strings.Join(lines, "\n") {
		return errors.New("sprite VTT does not match canonical intervals/order/coordinates")
	}
	return nil
}

type mixedSampler struct {
	stop            chan struct{}
	done            chan struct{}
	Available       bool
	PeakChildren    int
	FinalChildren   int
	Samples         int
	seen            map[int]bool
	seenAfterCancel int
	lag             []float64
}

func startMixedSampler(ctx context.Context) *mixedSampler {
	s := &mixedSampler{stop: make(chan struct{}), done: make(chan struct{}), seen: map[int]bool{}}
	go func() {
		defer close(s.done)
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		sample := func() {
			pids, err := ownMediaChildren()
			if err != nil {
				return
			}
			s.Available = true
			s.Samples++
			s.FinalChildren = len(pids)
			if len(pids) > s.PeakChildren {
				s.PeakChildren = len(pids)
			}
			for _, pid := range pids {
				if !s.seen[pid] {
					s.seen[pid] = true
					if ctx.Err() != nil {
						s.seenAfterCancel++
					}
				}
			}
		}
		for {
			select {
			case at := <-ticker.C:
				lag := float64(time.Since(at).Microseconds()) / 1000
				if lag < 0 {
					lag = 0
				}
				s.lag = append(s.lag, lag)
				sample()
			case <-s.stop:
				sample()
				return
			}
		}
	}()
	return s
}

func (s *mixedSampler) Stop() { close(s.stop); <-s.done }
func (s *mixedSampler) Report() map[string]any {
	sort.Float64s(s.lag)
	var p95, max float64
	if len(s.lag) > 0 {
		p95 = s.lag[(len(s.lag)-1)*95/100]
		max = s.lag[len(s.lag)-1]
	}
	return map[string]any{"own_child_sampler_available": s.Available, "sample_interval_ms": 20, "sample_count": s.Samples, "peak_own_media_children": s.PeakChildren, "final_own_media_children": s.FinalChildren, "unique_children_observed": len(s.seen), "new_children_first_seen_after_cancel": s.seenAfterCancel, "scheduler_delay_p95_ms": p95, "scheduler_delay_max_ms": max, "limits": "sampled lower bound; short children can be missed, first observation after cancellation does not prove launch time; process count is not GPU activity"}
}

// Inspect only this process's task child lists and the named direct children's
// stat records. Never enumerate host processes, read cmdlines or host configs.
func ownMediaChildren() ([]int, error) {
	if runtime.GOOS != "linux" {
		return nil, errors.New("own child sampling requires Linux procfs")
	}
	base := fmt.Sprintf("/proc/%d/task", os.Getpid())
	tasks, err := os.ReadDir(base)
	if err != nil {
		return nil, err
	}
	seen := map[int]bool{}
	for _, task := range tasks {
		data, e := os.ReadFile(filepath.Join(base, task.Name(), "children"))
		if e != nil {
			continue
		}
		for _, field := range strings.Fields(string(data)) {
			pid, e := strconv.Atoi(field)
			if e != nil {
				continue
			}
			stat, e := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
			if e != nil {
				continue
			}
			if isOwnMediaStat(string(stat), os.Getpid()) {
				seen[pid] = true
			}
		}
	}
	var pids []int
	for pid := range seen {
		pids = append(pids, pid)
	}
	return pids, nil
}

func isOwnMediaStat(stat string, parent int) bool {
	open, close := strings.IndexByte(stat, '('), strings.LastIndexByte(stat, ')')
	if open < 0 || close <= open {
		return false
	}
	comm := stat[open+1 : close]
	if comm != "ffmpeg" && comm != "ffprobe" {
		return false
	}
	fields := strings.Fields(stat[close+1:])
	if len(fields) < 2 {
		return false
	}
	ppid, err := strconv.Atoi(fields[1])
	return err == nil && ppid == parent
}
