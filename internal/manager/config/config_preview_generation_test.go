package config

import (
	"path/filepath"
	"testing"
)

func TestScenePreviewSettingIndependentPersistence(t *testing.T) {
	c := InitializeEmpty()
	c.filePath = filepath.Join(t.TempDir(), "config.yml")
	if c.GetIntelPreviewGeneration() != nil {
		t.Fatal("scene defaults changed")
	}
	preview := "vaapi"
	total, gpu := 16, 12
	if err := c.WriteGenerationConfigurationPatch(GenerationConfigurationPatch{PreviewBackend: &preview, MaxProcesses: &total, MaxGPUProcesses: &gpu}); err != nil {
		t.Fatal(err)
	}
	requested, _ := c.GetRequestedGenerationConfiguration()
	if requested.PreviewBackend != "vaapi" || requested.MarkerBackend != "software" || requested.SpriteBackend != "software" {
		t.Fatal(requested)
	}
	if c.GetIntelPreviewGeneration() != nil || !c.GenerationRestartRequired() {
		t.Fatal("save changed active scheduler")
	}
	restarted := InitializeEmpty()
	if err := restarted.load(c.filePath); err != nil {
		t.Fatal(err)
	}
	if restarted.GetIntelPreviewGeneration() == nil || restarted.GetIntelGenerationBudget().Settings().MaxGPUProcesses != 12 {
		t.Fatal("preview restart lost configured limits")
	}
	if restarted.GetGenerationBudget() != nil {
		t.Fatal("preview selection enabled unrelated CPU budget")
	}
	qsv := "qsv"
	if err := restarted.WriteGenerationConfigurationPatch(GenerationConfigurationPatch{PreviewBackend: &qsv}); err == nil {
		t.Fatal("unvalidated scene QSV saved")
	}
	saved, _ := restarted.GetRequestedGenerationConfiguration()
	if saved.PreviewBackend != "vaapi" {
		t.Fatal("invalid save mutated request")
	}
}
