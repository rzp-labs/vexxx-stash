package api

import (
	"context"
	"testing"

	"github.com/vektah/gqlparser/v2"

	"github.com/stashapp/stash/internal/manager/config"
)

func TestConfigureGeneralRejectsGenerationBeforeAnySettings(t *testing.T) {
	c := config.InitializeEmpty()
	c.SetInt(config.ParallelTasks, 3)
	marker, device, total, gpu, unrelated := "vaapi", "/dev/dri/renderD128", 1, 2, 8
	r := &mutationResolver{}
	_, err := r.ConfigureGeneral(context.Background(), ConfigGeneralInput{
		GenerationMarkerBackend: &marker, GenerationDevice: &device,
		GenerationMaxProcesses: &total, GenerationMaxGPUProcesses: &gpu, ParallelTasks: &unrelated,
	})
	if err == nil {
		t.Fatal("invalid combined configuration accepted")
	}
	requested, _ := c.GetRequestedGenerationConfiguration()
	if requested.MarkerBackend != "software" || c.GetParallelTasks() != 3 {
		t.Fatal("rejected request partially applied")
	}
}

func TestConfigGeneralGenerationResultSeparatesActiveAndRequested(t *testing.T) {
	c := config.InitializeEmpty()
	c.GetGenerationBudget()
	marker, total, gpu := "qsv", 2, 1
	if err := c.ApplyGenerationConfigurationPatch(config.GenerationConfigurationPatch{MarkerBackend: &marker, MaxProcesses: &total, MaxGPUProcesses: &gpu}); err != nil {
		t.Fatal(err)
	}
	got := makeConfigGeneralResult()
	if got.GenerationMarkerBackend != "qsv" || got.ActiveGeneration.MarkerBackend != "software" || !got.GenerationRestartRequired {
		t.Fatalf("requested/active API: %#v", got)
	}
	c.SetString(config.GenerationDevice, "invalid")
	got = makeConfigGeneralResult()
	if got.GenerationConfigurationError == nil || got.ActiveGeneration.MarkerBackend != "software" {
		t.Fatal("invalid saved configuration not reported safely")
	}
}

func TestGenerationGraphQLRejectsNonBooleanAndFractionalLimits(t *testing.T) {
	schema := NewExecutableSchema(Config{}).Schema()
	for _, operation := range []string{
		`mutation { configureGeneral(input: { generationBudgetEnabled: "true" }) { generationBudgetEnabled } }`,
		`mutation { configureGeneral(input: { generationThreads: 1.5 }) { generationThreads } }`,
	} {
		if _, err := gqlparser.LoadQuery(schema, operation); err == nil {
			t.Fatal("invalid GraphQL settings accepted")
		}
	}
}
