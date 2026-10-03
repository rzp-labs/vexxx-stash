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
	if newConfig.GetGenerationBudget() == nil {
		t.Fatal("hardware opt-in must use a budget")
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
	if restarted.GetGenerationBudget().Settings().Threads != 2 {
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
