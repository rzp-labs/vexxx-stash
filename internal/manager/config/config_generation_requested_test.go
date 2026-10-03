package config

import (
	"bytes"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func ptr[T any](v T) *T { return &v }

func TestGenerationPatchRejectsWholeInvalidProposal(t *testing.T) {
	for _, invalid := range []GenerationConfigurationPatch{
		{MarkerBackend: ptr("native")}, {SpriteBackend: ptr("auto")},
		{Device: ptr("relative")}, {Device: ptr("/dev/dri/renderDwrong")},
		{MaxProcesses: ptr(-1)}, {Threads: ptr(65)},
		{MaxProcesses: ptr(1), MaxGPUProcesses: ptr(2)},
		{MaxProcesses: ptr(0), MaxGPUProcesses: ptr(2)},
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
	if got := restarted.GetActiveGenerationConfiguration(); got.MarkerBackend != "qsv" || got.SpriteBackend != "vaapi" || !got.BudgetEnabled || got.Threads != 2 {
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
	if restarted.GenerationRestartRequired() || restarted.GetIntelMarkerGeneration() == nil || restarted.GetGenerationBudget().Settings().Threads != 2 {
		t.Fatal("saved settings did not activate after restart")
	}
}

func TestGenerationAtomicPersistenceFailurePreservesDiskAndRuntime(t *testing.T) {
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
			patch := GenerationConfigurationPatch{MarkerBackend: ptr("vaapi"), BudgetEnabled: ptr(true), Threads: ptr(2)}
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
