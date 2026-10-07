package config

import (
	"bytes"
	"github.com/stashapp/stash/pkg/generationbudget"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func ptr[T any](v T) *T { return &v }

func TestGenerationPatchRejectsWholeInvalidProposal(t *testing.T) {
	for _, invalid := range []GenerationConfigurationPatch{
		{MarkerBackend: ptr("native")}, {SpriteBackend: ptr("auto")},
		{Device: ptr("relative")}, {Device: ptr("/dev/dri/renderDwrong")},
		{MaxProcesses: ptr(-1)}, {Threads: ptr(-1)},
		{MaxProcesses: ptr(1), MaxGPUProcesses: ptr(2)},
	} {
		c := InitializeEmpty()
		invalid.BudgetEnabled = ptr(true)
		before, _ := c.GetRequestedGenerationConfiguration()
		if err := c.ApplyGenerationConfigurationPatch(invalid); err == nil {
			t.Fatalf("accepted invalid patch: %#v", invalid)
		}
		after, _ := c.GetRequestedGenerationConfiguration()
		if before != after {
			t.Fatalf("partially applied rejected patch: %#v", after)
		}
		if c.GenerationRestartRequired() {
			t.Fatal("rejected request changed active/requested comparison")
		}
	}
}

func TestGenerationRequestedActivePersistenceAndRollback(t *testing.T) {
	c := InitializeEmpty()
	c.filePath = filepath.Join(t.TempDir(), "config.yml")
	requested, err := c.GetRequestedGenerationConfiguration()
	if err != nil || requested.MarkerBackend != "software" || requested.SpriteBackend != "software" || requested.BudgetEnabled || c.GenerationRestartRequired() {
		t.Fatalf("legacy/default migration: %#v %v", requested, err)
	}
	patch := GenerationConfigurationPatch{MarkerBackend: ptr("qsv"), SpriteBackend: ptr("vaapi"), Device: ptr("/dev/dri/renderD129"), MaxProcesses: ptr(2), MaxGPUProcesses: ptr(1), Threads: ptr(2)}
	if err := c.ApplyGenerationConfigurationPatch(patch); err != nil {
		t.Fatal(err)
	}
	if !c.GenerationRestartRequired() {
		t.Fatal("save must require restart")
	}
	if got := c.GetActiveGenerationConfiguration(); got.MarkerBackend != "software" || got.BudgetEnabled {
		t.Fatalf("save replaced active scheduler: %#v", got)
	}
	if got, _ := c.GetRequestedGenerationConfiguration(); got.MarkerBackend != "qsv" || got.Device != "/dev/dri/renderD129" {
		t.Fatalf("read returned active rather than requested: %#v", got)
	}
	if err := c.Write(); err != nil {
		t.Fatal(err)
	}
	restarted := InitializeEmpty()
	if err := restarted.load(c.filePath); err != nil {
		t.Fatal(err)
	}
	if restarted.GenerationRestartRequired() {
		t.Fatal("persisted request still pending after restart")
	}
	if got := restarted.GetActiveGenerationConfiguration(); got.MarkerBackend != "qsv" || got.SpriteBackend != "vaapi" || got.BudgetEnabled || got.Threads != 2 {
		t.Fatalf("active persisted settings: %#v", got)
	}
	if err := restarted.ApplyGenerationConfigurationPatch(GenerationConfigurationPatch{MarkerBackend: ptr("software"), SpriteBackend: ptr("software"), BudgetEnabled: ptr(false)}); err != nil {
		t.Fatal(err)
	}
	if !restarted.GenerationRestartRequired() || restarted.GetIntelMarkerGeneration() == nil {
		t.Fatal("rollback must preserve active backend until restart")
	}
	if err := restarted.Write(); err != nil {
		t.Fatal(err)
	}
	rollback := InitializeEmpty()
	if err := rollback.load(c.filePath); err != nil {
		t.Fatal(err)
	}
	if rollback.GenerationRestartRequired() || rollback.GetGenerationBudget() != nil || rollback.GetIntelMarkerGeneration() != nil || rollback.GetIntelSpriteGeneration() != nil {
		t.Fatal("rollback failed after restart")
	}
}

func TestGenerationPatchValidatesAgainstRequestedCoupledLimits(t *testing.T) {
	c := InitializeEmpty()
	if err := c.ApplyGenerationConfigurationPatch(GenerationConfigurationPatch{MaxProcesses: ptr(3), MaxGPUProcesses: ptr(2)}); err != nil {
		t.Fatal(err)
	}
	if err := c.ApplyGenerationConfigurationPatch(GenerationConfigurationPatch{MaxProcesses: ptr(1)}); err == nil {
		t.Fatal("partial update ignored persisted GPU limit")
	}
	if got, _ := c.GetRequestedGenerationConfiguration(); got.MaxProcesses != 3 {
		t.Fatal("failed partial request modified configuration")
	}
	if err := c.ApplyGenerationConfigurationPatch(GenerationConfigurationPatch{MaxProcesses: ptr(1), MaxGPUProcesses: ptr(1)}); err != nil {
		t.Fatal(err)
	}
	if !c.GenerationRestartRequired() {
		t.Fatal("inactive budget changes must still show pending restart")
	}
}

func TestGenerationRequestOverrideAndInvalidYAML(t *testing.T) {
	c := InitializeEmpty()
	if err := c.overrides.Set(GenerationDevice, "/dev/dri/renderD128"); err != nil {
		t.Fatal(err)
	}
	if err := c.ApplyGenerationConfigurationPatch(GenerationConfigurationPatch{Device: ptr("/dev/dri/renderD129"), MarkerBackend: ptr("vaapi")}); err == nil {
		t.Fatal("accepted overridden setting")
	}
	if got, _ := c.GetRequestedGenerationConfiguration(); got.MarkerBackend != "software" {
		t.Fatal("partially changed rejected override")
	}
	c.SetInterface(GenerationBudgetEnabled, "invalid")
	if _, err := c.GetRequestedGenerationConfiguration(); err == nil {
		t.Fatal("invalid boolean not reported")
	}
	if c.GetIntelMarkerGeneration() != nil {
		t.Fatal("invalid YAML enabled hardware")
	}
	if !c.GenerationRestartRequired() {
		t.Fatal("invalid saved settings not reported as pending")
	}
	if err := c.ApplyGenerationConfigurationPatch(GenerationConfigurationPatch{BudgetEnabled: ptr(false)}); err != nil {
		t.Fatal(err)
	}
}

func TestGenerationEmptyYAMLMigrationAndExplicitEmptyRejection(t *testing.T) {
	c := InitializeEmpty()
	c.SetString(MarkerGenerationBackend, "")
	c.SetString(SpriteGenerationBackend, "")
	c.SetString(GenerationDevice, "")
	requested, err := c.GetRequestedGenerationConfiguration()
	if err != nil || requested.MarkerBackend != "software" || requested.SpriteBackend != "software" || requested.Device != "/dev/dri/renderD128" {
		t.Fatalf("empty optional YAML migration: %#v %v", requested, err)
	}
	if err := c.ApplyGenerationConfigurationPatch(GenerationConfigurationPatch{Device: ptr("")}); err == nil {
		t.Fatal("explicit empty API device accepted")
	}
	if err := c.ApplyGenerationConfigurationPatch(GenerationConfigurationPatch{MarkerBackend: ptr("")}); err == nil {
		t.Fatal("explicit empty API backend accepted")
	}
}

func TestGenerationFailedPersistenceRestoresRequestedState(t *testing.T) {
	c := InitializeEmpty()
	c.filePath = t.TempDir() // writing to a directory must fail
	before, _ := c.GetRequestedGenerationConfiguration()
	patch := GenerationConfigurationPatch{MarkerBackend: ptr("vaapi"), BudgetEnabled: ptr(true), Threads: ptr(2)}
	if err := c.WriteGenerationConfigurationPatch(patch); err == nil {
		t.Fatal("write unexpectedly succeeded")
	}
	after, _ := c.GetRequestedGenerationConfiguration()
	if before != after || c.GenerationRestartRequired() || c.GetIntelMarkerGeneration() != nil {
		t.Fatalf("failed write left an unpersisted request: %#v", after)
	}
	// Preserve an existing raw auto setting on rollback, including its YAML type.
	c.SetInterface(GenerationThreads, "auto")
	if err := c.WriteGenerationConfigurationPatch(patch); err == nil {
		t.Fatal("write unexpectedly succeeded")
	}
	c.RLock()
	raw := c.main.Get(GenerationThreads)
	c.RUnlock()
	if raw != "auto" {
		t.Fatalf("failed write changed existing raw value: %#v", raw)
	}
}

func TestGenerationSuccessfulPersistenceWritesRequestedOnly(t *testing.T) {
	c := InitializeEmpty()
	c.filePath = filepath.Join(t.TempDir(), "config.yml")
	if err := c.WriteGenerationConfigurationPatch(GenerationConfigurationPatch{MarkerBackend: ptr("vaapi"), Threads: ptr(2)}); err != nil {
		t.Fatal(err)
	}
	if !c.GenerationRestartRequired() || c.GetIntelMarkerGeneration() != nil {
		t.Fatal("persistence replaced running settings")
	}
	restarted := InitializeEmpty()
	if err := restarted.load(c.filePath); err != nil {
		t.Fatal(err)
	}
	if restarted.GenerationRestartRequired() || restarted.GetIntelMarkerGeneration() == nil || restarted.GetIntelGenerationBudget().Settings().Threads != 2 {
		t.Fatal("saved settings did not activate after restart")
	}
}

func TestGenerationAtomicPersistenceFailurePreservesDiskAndRuntime(t *testing.T) {
	for name, patch := range map[string]GenerationConfigurationPatch{
		"empty":      {},
		"generation": {MarkerBackend: ptr("vaapi"), BudgetEnabled: ptr(true), Threads: ptr(2)},
	} {
		t.Run(name, func(t *testing.T) {
			for _, stage := range []string{"write", "short-write", "publish"} {
				t.Run(stage, func(t *testing.T) {
					c := InitializeEmpty()
					c.filePath = filepath.Join(t.TempDir(), "config.yml")
					c.SetInterface(GenerationThreads, "auto")
					if err := c.Write(); err != nil {
						t.Fatal(err)
					}
					original, err := os.ReadFile(c.filePath)
					if err != nil {
						t.Fatal(err)
					}
					before, err := c.GetRequestedGenerationConfiguration()
					if err != nil {
						t.Fatal(err)
					}
					active := c.GetActiveGenerationConfiguration()
					// ConfigureGeneral can have pending general settings with no
					// generation fields: an empty patch must still flush atomically.
					c.SetInt(ParallelTasks, 8)
					ops := generationConfigFileOps{}
					switch stage {
					case "write", "short-write":
						ops.write = func(file *os.File, data []byte) (int, error) {
							written, err := file.Write(data[:len(data)/2])
							if err != nil {
								return written, err
							}
							if stage == "write" {
								return written, syscall.ENOSPC
							}
							return written, nil
						}
					case "publish":
						ops.publish = func(string, string) error { return syscall.EACCES }
					}
					err = c.writeGenerationConfigurationPatch(patch, func(path string, data []byte) error {
						return writeGenerationConfigAtomically(path, data, ops)
					})
					if err == nil {
						t.Fatal("injected save failure succeeded")
					}
					current, err := os.ReadFile(c.filePath)
					if err != nil || !bytes.Equal(current, original) {
						t.Fatalf("failed save damaged existing disk config: %v", err)
					}
					after, err := c.GetRequestedGenerationConfiguration()
					if err != nil || before != after || active != c.GetActiveGenerationConfiguration() || c.GenerationRestartRequired() {
						t.Fatal("failed save changed requested/active/restart values")
					}
					c.RLock()
					raw := c.main.Get(GenerationThreads)
					c.RUnlock()
					if raw != "auto" {
						t.Fatal("failed save changed raw YAML value")
					}
					temporary, err := filepath.Glob(filepath.Join(filepath.Dir(c.filePath), ".config.yml.generation-*"))
					if err != nil || len(temporary) != 0 {
						t.Fatalf("failed save left temporary files: %v %v", temporary, err)
					}
				})
			}
		})
	}
}

func TestGenerationEmptyPatchPersistsGeneralSettings(t *testing.T) {
	c := InitializeEmpty()
	c.filePath = filepath.Join(t.TempDir(), "config.yml")
	c.SetInt(ParallelTasks, 8)
	if err := c.WriteGenerationConfigurationPatch(GenerationConfigurationPatch{}); err != nil {
		t.Fatal(err)
	}
	restarted := InitializeEmpty()
	if err := restarted.load(c.filePath); err != nil || restarted.GetParallelTasks() != 8 {
		t.Fatalf("empty patch lost pending general settings: %v", err)
	}
	// As before, saving only general settings must not take a generation
	// snapshot before its first use.
	c.SetString(MarkerGenerationBackend, "vaapi")
	if c.GetIntelMarkerGeneration() == nil {
		t.Fatal("empty patch prematurely froze generation settings")
	}
}

func TestGenerationCorrectedFallbackRequiresRestart(t *testing.T) {
	c := InitializeEmpty()
	c.filePath = filepath.Join(t.TempDir(), "config.yml")
	c.SetInterface(GenerationBudgetEnabled, "invalid")
	active := c.GetGenerationBudget()
	if active == nil {
		t.Fatal("invalid request did not activate conservative fallback")
	}
	if err := c.WriteGenerationConfigurationPatch(GenerationConfigurationPatch{BudgetEnabled: ptr(false)}); err != nil {
		t.Fatal(err)
	}
	requested, err := c.GetRequestedGenerationConfiguration()
	if err != nil || requested.BudgetEnabled || c.GetGenerationBudget() != active || !c.GenerationRestartRequired() {
		t.Fatalf("corrected request hid active fallback restart: %+v, %v", requested, err)
	}
	restarted := InitializeEmpty()
	if err := restarted.load(c.filePath); err != nil {
		t.Fatal(err)
	}
	if restarted.GenerationRestartRequired() || restarted.GetGenerationBudget() != nil {
		t.Fatal("corrected settings did not activate after restart")
	}
}

func TestGenerationAtomicPersistencePreservesFileModeAndSymlink(t *testing.T) {
	c := InitializeEmpty()
	directory := t.TempDir()
	target := filepath.Join(directory, "config.yml")
	c.filePath = filepath.Join(directory, "linked-config.yml")
	if err := os.WriteFile(target, []byte("generation:\n  threads: 0\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, c.filePath); err != nil {
		t.Fatal(err)
	}
	if err := c.WriteGenerationConfigurationPatch(GenerationConfigurationPatch{MarkerBackend: ptr("vaapi")}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(target)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("lost existing private file mode: %v %v", info, err)
	}
	link, err := os.Lstat(c.filePath)
	if err != nil || link.Mode()&os.ModeSymlink == 0 {
		t.Fatal("atomic save replaced symlink instead of target")
	}
	data, err := os.ReadFile(target)
	if err != nil || !bytes.Contains(data, []byte("vaapi")) {
		t.Fatal("saved request did not reach target")
	}
}

func TestGenerationInvalidSavedFieldPreservesReadableValues(t *testing.T) {
	valid := GenerationConfiguration{PreviewBackend: "software", MarkerBackend: "qsv", SpriteBackend: "vaapi", Device: "/dev/dri/renderD129", BudgetEnabled: true, MaxProcesses: 4, MaxGPUProcesses: 2, Threads: 3}
	for _, key := range []string{MarkerGenerationBackend, GenerationDevice, GenerationBudgetEnabled, GenerationMaxProcesses, GenerationMaxGPUProcesses, GenerationThreads} {
		t.Run(key, func(t *testing.T) {
			c := InitializeEmpty()
			c.filePath = filepath.Join(t.TempDir(), "config.yml")
			c.SetString(MarkerGenerationBackend, valid.MarkerBackend)
			c.SetString(SpriteGenerationBackend, valid.SpriteBackend)
			c.SetString(GenerationDevice, valid.Device)
			c.SetBool(GenerationBudgetEnabled, true)
			c.SetInt(GenerationMaxProcesses, 4)
			c.SetInt(GenerationMaxGPUProcesses, 2)
			c.SetInt(GenerationThreads, 3)
			c.SetInterface(key, "bad")
			requested, err := c.GetRequestedGenerationConfiguration()
			if err == nil {
				t.Fatal("invalid saved value not reported")
			}
			want := valid
			switch key {
			case MarkerGenerationBackend:
				want.MarkerBackend = "bad"
			case GenerationDevice:
				want.Device = "bad"
			case GenerationBudgetEnabled:
				want.BudgetEnabled = false
			case GenerationMaxProcesses:
				want.MaxProcesses = 0
			case GenerationMaxGPUProcesses:
				want.MaxGPUProcesses = 0
			case GenerationThreads:
				want.Threads = 0
			}
			if requested != want {
				t.Fatalf("invalid %s hid readable saved values: got %+v, want %+v", key, requested, want)
			}
			if c.GetIntelMarkerGeneration() != nil || c.GetIntelSpriteGeneration() != nil || c.GetGenerationBudget().Settings().Threads != 1 {
				t.Fatal("invalid saved config escaped conservative CPU fallback")
			}
			// Simulate the complete form correcting only its invalid value.
			switch key {
			case MarkerGenerationBackend:
				requested.MarkerBackend = valid.MarkerBackend
			case GenerationDevice:
				requested.Device = valid.Device
			case GenerationBudgetEnabled:
				requested.BudgetEnabled = valid.BudgetEnabled
			case GenerationMaxProcesses:
				requested.MaxProcesses = valid.MaxProcesses
			case GenerationMaxGPUProcesses:
				requested.MaxGPUProcesses = valid.MaxGPUProcesses
			case GenerationThreads:
				requested.Threads = valid.Threads
			}
			patch := GenerationConfigurationPatch{MarkerBackend: &requested.MarkerBackend, SpriteBackend: &requested.SpriteBackend, Device: &requested.Device, BudgetEnabled: &requested.BudgetEnabled, MaxProcesses: &requested.MaxProcesses, MaxGPUProcesses: &requested.MaxGPUProcesses, Threads: &requested.Threads}
			if err := c.WriteGenerationConfigurationPatch(patch); err != nil {
				t.Fatal(err)
			}
			reloaded := InitializeEmpty()
			if err := reloaded.load(c.filePath); err != nil {
				t.Fatal(err)
			}
			if saved, err := reloaded.GetRequestedGenerationConfiguration(); err != nil || saved != valid {
				t.Fatalf("correction reset valid saved values: %+v %v", saved, err)
			}
		})
	}
}

func TestAutoEffectiveLimitsDoNotRewriteSavedRequestOrRequireRestart(t *testing.T) {
	c := InitializeEmpty()
	c.SetString(SpriteGenerationBackend, "vaapi")
	c.filePath = filepath.Join(t.TempDir(), "config.yml")
	b := c.GetIntelGenerationBudget()
	request, err := c.GetRequestedGenerationConfiguration()
	if err != nil {
		t.Fatal(err)
	}
	w := generationbudget.Workload{Key: "validated-plan"}
	before := b.PrepareWorkload(w)
	for range 2 {
		b.Observe(w, before, time.Second, 81, nil)
	}
	effective := c.GetActiveGenerationConfiguration()
	if effective.MaxGPUProcesses != b.Settings().MaxGPUProcesses {
		t.Fatal("active configuration hides learned limits")
	}
	after, err := c.GetRequestedGenerationConfiguration()
	if err != nil || after != request || after.MaxProcesses != 0 || after.MaxGPUProcesses != 0 || after.Threads != 0 {
		t.Fatalf("Auto request rewritten: %+v %v", after, err)
	}
	if c.GenerationRestartRequired() || c.GetGenerationBudget() != nil {
		t.Fatal("learning changed restart status or CPU budget scope")
	}
	if err := c.Write(); err != nil {
		t.Fatal(err)
	}
	restarted := InitializeEmpty()
	if err := restarted.load(c.filePath); err != nil {
		t.Fatal(err)
	}
	saved, err := restarted.GetRequestedGenerationConfiguration()
	if err != nil || saved != request {
		t.Fatalf("persisted resolved values: %+v %v", saved, err)
	}
	if err := restarted.ApplyGenerationConfigurationPatch(GenerationConfigurationPatch{MaxProcesses: ptr(0), MaxGPUProcesses: ptr(2)}); err != nil {
		t.Fatal("Auto total rejected explicit GPU limit", err)
	}
}
