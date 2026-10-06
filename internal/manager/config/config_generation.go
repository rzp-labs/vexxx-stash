package config

import (
	"time"

	"github.com/stashapp/stash/pkg/ffmpeg"
	"github.com/stashapp/stash/pkg/generationbudget"
	"github.com/stashapp/stash/pkg/logger"
)

const (
	PreviewGenerationBackend  = "generation.previews.backend"
	MarkerGenerationBackend   = "generation.markers.backend"
	SpriteGenerationBackend   = "generation.sprites.backend"
	GenerationDevice          = "generation.device"
	GenerationBudgetEnabled   = "generation.budget.enabled"
	GenerationMaxProcesses    = "generation.budget.processes"
	GenerationMaxGPUProcesses = "generation.budget.gpu_processes"
	GenerationThreads         = "generation.budget.threads"
)

type generationSettings struct {
	marker, sprite, preview, device string
	budget                          *generationbudget.Budget
	requested                       GenerationConfiguration
	fallback                        bool
}

// readGenerationSettings assumes the configuration read lock is held. These
// controls are intentionally separate from playback and Windows native generation.
func (i *Config) readGenerationSettings() (generationSettings, error) {
	requested, err := i.readRequestedGeneration(nil)
	if err != nil {
		return generationSettings{requested: requested}, err
	}
	s := generationSettings{marker: requested.MarkerBackend, sprite: requested.SpriteBackend, preview: requested.PreviewBackend, device: requested.Device, requested: requested}
	// Intel stages always have bounded admission. Ordinary CPU generation shares
	// this scheduler only when the caller explicitly enables the shared budget.
	if requested.BudgetEnabled || s.marker != "software" || s.sprite != "software" || s.preview != "software" {
		s.budget, err = generationbudget.NewForDevice(requested.Limits(), s.device)
		if err != nil {
			return s, err
		}
	}
	return s, nil
}

// Snapshot once per process/config lifetime: replacing a live scheduler could
// admit jobs against old and new limits simultaneously. YAML edits require restart.
func (i *Config) generation() generationSettings {
	i.generationOnce.Do(func() {
		i.RLock()
		defer i.RUnlock()
		s, err := i.readGenerationSettings()
		if err != nil {
			logger.Warnf("[generation] invalid settings, Intel disabled and conservative CPU budget used: %v", err)
			s = generationSettings{marker: "software", sprite: "software", preview: "software", device: "/dev/dri/renderD128", requested: s.requested, fallback: true}
			s.budget, _ = generationbudget.New(generationbudget.Settings{MaxProcesses: 1, MaxGPUProcesses: 1, Threads: 1})
		}
		i.generationSnapshot = s
	})
	return i.generationSnapshot
}

// GetGenerationBudget controls ordinary CPU generation, including pHash. An
// Intel backend request must not implicitly change those workloads' threading,
// batching or admission. Invalid in-memory settings retain conservative fallback.
func (i *Config) GetGenerationBudget() *generationbudget.Budget {
	s := i.generation()
	if !s.requested.BudgetEnabled && !s.fallback {
		return nil
	}
	return s.budget
}

// GetIntelGenerationBudget bounds Intel probes, execution and software fallback.
// Explicit shared mode uses this same scheduler, never an independent budget.
func (i *Config) GetIntelGenerationBudget() *generationbudget.Budget {
	return i.generation().budget
}

func (i *Config) GetMarkerGenerationBackend() string { return i.generation().marker }
func (i *Config) GetSpriteGenerationBackend() string { return i.generation().sprite }
func (i *Config) GetGenerationDevice() string        { return i.generation().device }
func (i *Config) GetIntelMarkerGeneration() *ffmpeg.IntelGenerationConfig {
	s := i.generation()
	if s.marker == "software" {
		return nil
	}
	return &ffmpeg.IntelGenerationConfig{Backend: s.marker, Device: s.device, ProbeTimeout: 10 * time.Second}
}
func (i *Config) GetIntelSpriteGeneration() *ffmpeg.IntelGenerationConfig {
	s := i.generation()
	if s.sprite == "software" {
		return nil
	}
	return &ffmpeg.IntelGenerationConfig{Backend: s.sprite, Device: s.device, ProbeTimeout: 10 * time.Second}
}

func (i *Config) GetPreviewGenerationBackend() string { return i.generation().preview }
func (i *Config) GetIntelPreviewGeneration() *ffmpeg.IntelGenerationConfig {
	s := i.generation()
	if s.preview == "software" {
		return nil
	}
	return &ffmpeg.IntelGenerationConfig{Backend: s.preview, Device: s.device, ProbeTimeout: 10 * time.Second}
}
