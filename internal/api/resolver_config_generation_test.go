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

func TestConfigGeneralIntelOnlyBudgetScope(t *testing.T) {
	c := config.InitializeEmpty()
	c.SetString(config.SpriteGenerationBackend, "vaapi")
	got := makeConfigGeneralResult()
	if got.GenerationBudgetEnabled || got.ActiveGeneration.BudgetEnabled || got.ActiveGeneration.Threads != 1 || got.GenerationRestartRequired {
		t.Fatalf("Intel-only active CPU budget misreported: %+v", got.ActiveGeneration)
	}
	if c.GetGenerationBudget() != nil || c.GetIntelGenerationBudget() == nil {
		t.Fatal("active API state does not match actual CPU/Intel admission")
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

func TestConfigGeneralScenePreviewRequestAndSchema(t *testing.T) {
	c := config.InitializeEmpty()
	c.GetGenerationBudget()
	preview := "vaapi"
	if err := c.ApplyGenerationConfigurationPatch(config.GenerationConfigurationPatch{PreviewBackend: &preview}); err != nil {
		t.Fatal(err)
	}
	got := makeConfigGeneralResult()
	if got.GenerationPreviewBackend != "vaapi" || got.ActiveGeneration.PreviewBackend != "software" || !got.GenerationRestartRequired {
		t.Fatalf("scene request/active API: %#v", got)
	}
	if got.GenerationMarkerBackend != "software" || got.GenerationSpriteBackend != "software" {
		t.Fatal("scene request coupled other backends")
	}
	schema := NewExecutableSchema(Config{}).Schema()
	operation := `mutation { configureGeneral(input: { generationPreviewBackend: "software" }) { generationPreviewBackend activeGeneration { previewBackend } } }`
	if _, err := gqlparser.LoadQuery(schema, operation); err != nil {
		t.Fatal("scene schema fields unavailable", err)
	}
}

func TestGenerationTuningIsReadOnlyAndInitiallyUnmeasured(t *testing.T) {
	c := config.InitializeEmpty()
	c.SetString(config.SpriteGenerationBackend, "vaapi")
	c.SetString(config.PreviewGenerationBackend, "vaapi")
	c.SetInt(config.GenerationMaxProcesses, 6)
	c.SetInt(config.GenerationMaxGPUProcesses, 6)
	before, err := c.GetRequestedGenerationConfiguration()
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		got := makeConfigGeneralResult()
		if got.GenerationTuning == nil {
			t.Fatal("tuning absent")
		}
		for _, state := range []*GenerationWorkloadTuning{got.GenerationTuning.Sprite, got.GenerationTuning.Preview} {
			if state.AutomaticConcurrency != nil || state.MeasurementState != GenerationMeasurementStateNotMeasured {
				t.Fatal("query promoted seed or started rendering", state)
			}
		}
	}
	after, err := c.GetRequestedGenerationConfiguration()
	if err != nil || after != before {
		t.Fatal("query rewrote requests")
	}
	schema := NewExecutableSchema(Config{}).Schema()
	query := `query { configuration { general { generationTuning { sprite { automaticConcurrency measurementState } preview { automaticConcurrency measurementState } } } } }`
	if _, err := gqlparser.LoadQuery(schema, query); err != nil {
		t.Fatal(err)
	}
	for _, rejected := range []string{
		`mutation { configureGeneral(input: { generationMaxSpriteProcesses: 2 }) { generationThreads } }`,
		`query { configuration { general { generationTuning { sprite { activeOverride savedOverride } } } } }`,
	} {
		if _, err := gqlparser.LoadQuery(schema, rejected); err == nil {
			t.Fatal("unused override surface exposed")
		}
	}
}
