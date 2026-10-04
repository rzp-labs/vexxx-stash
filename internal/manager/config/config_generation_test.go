package config

import (
	"fmt"
	"github.com/stashapp/stash/pkg/generationbudget"
	"path/filepath"
	"testing"
)

func TestGenerationDefaultsAndSnapshot(t *testing.T) {
	c := InitializeEmpty()
	if c.GetIntelMarkerGeneration() != nil || c.GetIntelSpriteGeneration() != nil || c.GetGenerationBudget() != nil {
		t.Fatal("legacy defaults must remain software and unbudgeted")
	}
	c.SetInterface(MarkerGenerationBackend, "vaapi")
	if c.GetIntelMarkerGeneration() != nil {
		t.Fatal("running snapshot changed; restart required")
	}
	newConfig := InitializeEmpty()
	newConfig.SetInterface(MarkerGenerationBackend, "vaapi")
	if got := newConfig.GetIntelMarkerGeneration(); got == nil || got.Device != "/dev/dri/renderD128" {
		t.Fatalf("hardware config: %#v", got)
	}
	if newConfig.GetIntelGenerationBudget() == nil || newConfig.GetGenerationBudget() != nil {
		t.Fatal("hardware opt-in must bound Intel work without enabling CPU limits")
	}
}

func TestIntelBudgetDoesNotImplicitlyEnableCPUGeneration(t *testing.T) {
	for _, backend := range []string{MarkerGenerationBackend, SpriteGenerationBackend} {
		for _, shared := range []bool{false, true} {
			for _, limits := range []generationbudget.Settings{{}, {MaxProcesses: 3, MaxGPUProcesses: 2, Threads: 4}} {
				t.Run(fmt.Sprintf("%s/shared=%t/limits=%v", backend, shared, limits), func(t *testing.T) {
					c := InitializeEmpty()
					c.SetString(backend, "vaapi")
					c.SetBool(GenerationBudgetEnabled, shared)
					c.SetInt(GenerationMaxProcesses, limits.MaxProcesses)
					c.SetInt(GenerationMaxGPUProcesses, limits.MaxGPUProcesses)
					c.SetInt(GenerationThreads, limits.Threads)
					intel := c.GetIntelGenerationBudget()
					want, _ := limits.Normalize()
					if intel == nil || intel.Settings() != want || intel != c.GetIntelGenerationBudget() {
						t.Fatal("Intel limits or frozen scheduler lost")
					}
					cpu := c.GetGenerationBudget()
					if shared && cpu != intel || !shared && cpu != nil {
						t.Fatal("CPU limits do not respect the explicit shared switch")
					}
					active := c.GetActiveGenerationConfiguration()
					if active.BudgetEnabled != shared || active.Limits() != want || c.GenerationRestartRequired() {
						t.Fatalf("active configuration misreports scope: %+v", active)
					}
					c.SetBool(GenerationBudgetEnabled, !shared)
					if c.GetGenerationBudget() != cpu || c.GetIntelGenerationBudget() != intel || !c.GenerationRestartRequired() {
						t.Fatal("saving replaced the running budget scope")
					}
				})
			}
		}
	}
}

func TestGenerationSettingsValidation(t *testing.T) {
	for _, tt := range []struct {
		key   string
		value any
	}{
		{MarkerGenerationBackend, "native"}, {SpriteGenerationBackend, "auto"}, {GenerationDevice, "relative"},
		{GenerationThreads, -1}, {GenerationThreads, 65}, {GenerationThreads, "unlimited"}, {GenerationThreads, 1.5}, {GenerationMaxGPUProcesses, 2},
		{GenerationDevice, "/dev/dri/renderDwrong"}, {GenerationBudgetEnabled, "auto"},
		{GenerationBudgetEnabled, 1}, {GenerationBudgetEnabled, "1"}, {GenerationBudgetEnabled, "t"}, {GenerationThreads, 1.0},
	} {
		t.Run(tt.key+"_"+fmtValue(tt.value), func(t *testing.T) {
			c := InitializeEmpty()
			c.SetInterface(tt.key, tt.value)
			c.RLock()
			_, err := c.readGenerationSettings()
			c.RUnlock()
			if err == nil {
				t.Fatal("invalid configuration accepted")
			}
			if c.GetIntelMarkerGeneration() != nil {
				t.Fatal("invalid configuration enabled hardware")
			}
		})
	}
	for _, v := range []any{0, "auto", 1} {
		c := InitializeEmpty()
		c.SetInterface(GenerationBudgetEnabled, true)
		c.SetInterface(GenerationThreads, v)
		got := c.GetGenerationBudget()
		if got == nil || got.Settings() != (generationbudget.Settings{MaxProcesses: 1, MaxGPUProcesses: 1, Threads: 1}) {
			t.Fatal("conservative automatic settings not applied")
		}
		if got != c.GetGenerationBudget() {
			t.Fatal("scheduler not shared")
		}
	}
}

func fmtValue(v any) string { return fmt.Sprint(v) }

func TestGenerationPersistenceAndSoftwareRollback(t *testing.T) {
	c := InitializeEmpty()
	c.filePath = filepath.Join(t.TempDir(), "config.yml")
	c.SetInterface(MarkerGenerationBackend, "qsv")
	c.SetInterface(SpriteGenerationBackend, "vaapi")
	c.SetInterface(GenerationDevice, "/dev/dri/renderD129")
	c.SetInterface(GenerationThreads, 2)
	if err := c.Write(); err != nil {
		t.Fatal(err)
	}
	restarted := InitializeEmpty()
	if err := restarted.load(c.filePath); err != nil {
		t.Fatal(err)
	}
	if got := restarted.GetIntelMarkerGeneration(); got == nil || got.Backend != "qsv" || got.Device != "/dev/dri/renderD129" {
		t.Fatalf("lost marker settings: %#v", got)
	}
	if got := restarted.GetIntelSpriteGeneration(); got == nil || got.Backend != "vaapi" {
		t.Fatalf("lost sprite settings: %#v", got)
	}
	if restarted.GetIntelGenerationBudget().Settings().Threads != 2 {
		t.Fatal("lost thread limit")
	}
	restarted.SetInterface(MarkerGenerationBackend, "software")
	restarted.SetInterface(SpriteGenerationBackend, "software")
	if err := restarted.Write(); err != nil {
		t.Fatal(err)
	}
	rollback := InitializeEmpty()
	if err := rollback.load(c.filePath); err != nil {
		t.Fatal(err)
	}
	if rollback.GetIntelMarkerGeneration() != nil || rollback.GetIntelSpriteGeneration() != nil || rollback.GetGenerationBudget() != nil {
		t.Fatal("software rollback failed after restart")
	}
}
