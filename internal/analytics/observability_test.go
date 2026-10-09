package analytics

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/posthog/posthog-go"
	"github.com/stashapp/stash/pkg/diagnostics"
	"github.com/stashapp/stash/pkg/ffmpeg"
	"github.com/stashapp/stash/pkg/job"
	"github.com/stashapp/stash/pkg/pkg"
	"github.com/stashapp/stash/pkg/python"
)

type offlineTransport struct {
	mu      sync.Mutex
	batches [][]byte
	status  int
}

func (r *offlineTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.URL.Host != "offline.invalid" {
		return nil, errors.New("unexpected destination")
	}
	data, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	r.batches = append(r.batches, data)
	r.mu.Unlock()
	status := r.status
	if status == 0 {
		status = 200
	}
	return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"status":1}`)), Request: request}, nil
}
func offlineSDK(t *testing.T, status int) (*offlineTransport, func() []map[string]any) {
	t.Helper()
	transport := &offlineTransport{status: status}
	sdk, err := posthog.NewWithConfig("synthetic-project", posthog.Config{Endpoint: "https://offline.invalid", Transport: transport, BeforeSend: beforeSend, Callback: deliveryCallback{}, Logger: deliveryLogger{}, BatchSize: 100, Interval: time.Hour, ShutdownTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	previous := client
	client = observedClient{sdk}
	t.Cleanup(func() { _ = sdk.Close(); client = previous })
	return transport, func() []map[string]any {
		t.Helper()
		if err := sdk.Flush(); err != nil {
			t.Fatal(err)
		}
		transport.mu.Lock()
		defer transport.mu.Unlock()
		var events []map[string]any
		for _, data := range transport.batches {
			var payload struct {
				Batch []map[string]any `json:"batch"`
			}
			if err := json.Unmarshal(data, &payload); err != nil {
				t.Fatal(err)
			}
			events = append(events, payload.Batch...)
		}
		if dir := os.Getenv("VEX80_EVIDENCE_DIR"); dir != "" {
			encoded, _ := json.MarshalIndent(transport.batches, "", "  ")
			_ = os.WriteFile(dir+"/"+t.Name()+"-transport.json", encoded, 0600)
			decoded, _ := json.MarshalIndent(events, "", "  ")
			_ = os.WriteFile(dir+"/"+t.Name()+"-events.json", decoded, 0600)
		}
		return events
	}
}
func TestComprehensiveOfflineSDKPrivacyAndMetadata(t *testing.T) {
	_, events := offlineSDK(t, 0)
	value := "unfamiliar panic decoder-sentinel cookie='cookie-secret'\nAuthorization: Bearer header-secret\n-----BEGIN PRIVATE KEY-----\nkey-secret\n-----END PRIVATE KEY-----\nopen '/mnt/Private Alice/movie.mkv': permission denied"
	event := PanicException(value)
	event.Properties.Set("mystery_diagnostic", "new-driver-sentinel NV12/P010 /dev/dri/renderD128").Set("nested", map[string]any{"authorization": "arbitrary-secret", "cause": "new-wheel-sentinel"})
	if err := client.Enqueue(event); err != nil {
		t.Fatal(err)
	}
	received := events()
	data, _ := json.Marshal(received)
	for _, secret := range []string{"cookie-secret", "header-secret", "key-secret", "Private Alice", "arbitrary-secret"} {
		if strings.Contains(string(data), secret) {
			t.Fatalf("fixture leaked %s", secret)
		}
	}
	for _, diagnostic := range []string{"unfamiliar panic decoder-sentinel", "new-driver-sentinel", "new-wheel-sentinel", "NV12/P010", "renderD128", "$app_namespace", "lineno", "function", "$lib_version"} {
		if !strings.Contains(string(data), diagnostic) {
			t.Errorf("lost diagnostic %s", diagnostic)
		}
	}
	// Native image support is platform dependent. When SDK gives an image, its
	// debug ID and instruction addresses must survive actual wire serialization.
	if len(event.DebugImages) > 0 {
		for _, field := range []string{"debug_id", "instruction_addr", "image_addr"} {
			if !strings.Contains(string(data), field) {
				t.Errorf("lost SDK field %s", field)
			}
		}
	}
	if strings.Contains(string(data), "/Users/") {
		t.Fatal("build root leaked")
	}
}
func TestCauseOutputAndAllModuleIdentities(t *testing.T) {
	var failures []error
	for i := 0; i < 20; i++ {
		failures = append(failures, fmt.Errorf("installing Python dependencies: installing requirements: %w", &python.CommandError{Stage: "module_install", Module: fmt.Sprintf("widget%d", i), Err: errors.New("wheel-cause"), Output: strings.Repeat("x", 10000) + "\nfinal-pip-sentinel password=secret"}))
	}
	event := PackageInstallException(&pkg.InstallError{Stage: "python_dependencies", Err: fmt.Errorf("operation-prefix: %w: operation-suffix", errors.Join(failures...))}, PackageFailureContext{PackageID: "PythonTools"})
	details := event.Properties["package_failures"].([]map[string]any)
	if len(details) != 20 {
		t.Fatal("module details lost")
	}
	var outputBytes int
	for i, detail := range details {
		cause := detail["error_cause"].(string)
		for _, part := range []string{"operation-prefix", "operation-suffix", "installing Python dependencies", "installing requirements", fmt.Sprintf("widget%d", i), "wheel-cause"} {
			if !strings.Contains(cause, part) {
				t.Errorf("missing %s", part)
			}
		}
		if output, ok := detail["python_output"].(string); ok {
			outputBytes += len(output)
		}
	}
	if outputBytes > 64*1024 || event.Properties["diagnostic_omitted_bytes"].(int) == 0 {
		t.Fatal("output not bounded/observable")
	}
	if strings.Contains(event.ExceptionList[0].Value, "[truncated]") || !strings.Contains(event.ExceptionList[0].Value, "widget0") {
		t.Fatal("output displaced headline")
	}
}
func TestConcurrentBoundaryDedupAndCancellationWithRealFailure(t *testing.T) {
	_, events := offlineSDK(t, 0)
	ctx, cancel := context.WithCancel(diagnostics.WithState(context.Background()))
	cancel()
	real := errors.New("genuine-driver-sentinel")
	aggregate := fmt.Errorf("joined-operation: %w", errors.Join(context.Canceled, real))
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			CaptureGenerationFailure(ctx, aggregate, GenerationFailureContext{JobCorrelation: uuid.NewString()})
		}()
	}
	wg.Wait()
	CaptureJobFailure(ctx, aggregate, uuid.NewString(), "syntheticJob")
	received := events()
	if len(received) != 1 {
		t.Fatalf("expected one genuine failure, got %d", len(received))
	}
	encoded, _ := json.Marshal(received)
	if !strings.Contains(string(encoded), "joined-operation") || !strings.Contains(string(encoded), "genuine-driver-sentinel") {
		t.Fatal("join context lost")
	}
	// A different boundary context must capture an independent occurrence.
	CaptureGenerationFailure(diagnostics.WithState(context.Background()), real, GenerationFailureContext{})
	if len(events()) != 2 {
		t.Fatal("independent occurrence suppressed")
	}
}
func TestOrdinaryJobFailurePanicAndObserverIsolation(t *testing.T) {
	_, events := offlineSDK(t, 0)
	m := job.NewManager()
	defer m.Stop()
	m.OnError = func(ctx context.Context, err error, kind string) {
		CaptureJobFailure(ctx, err, job.Correlation(ctx), kind)
	}
	m.OnPanic = func(ctx context.Context, value any) { CaptureWorkerPanic(ctx, value, job.Correlation(ctx)) }
	ids := []int{m.Add(context.Background(), "private job description", job.MakeJobExec(func(context.Context, *job.Progress) error { return errors.New("ordinary failure sentinel") })), m.Add(context.Background(), "private panic job", job.MakeJobExec(func(context.Context, *job.Progress) error { panic("worker panic sentinel") })), m.Add(context.Background(), "success", job.MakeJobExec(func(context.Context, *job.Progress) error { return nil }))}
	for i, id := range ids {
		deadline := time.Now().Add(3 * time.Second)
		for {
			result := m.GetJob(id)
			if result != nil && result.EndTime != nil {
				want := job.StatusFailed
				if i == 2 {
					want = job.StatusFinished
				}
				if result.Status != want {
					t.Fatalf("status=%s want %s", result.Status, want)
				}
				if i < 2 && (result.Error == nil || *result.Error == "") {
					t.Fatal("failure status lost cause")
				}
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("job did not finish")
			}
			time.Sleep(time.Millisecond)
		}
	}
	received := events()
	if len(received) != 2 {
		t.Fatalf("ordinary/panic events=%d", len(received))
	}
	data, _ := json.Marshal(received)
	if strings.Contains(string(data), "private job") || strings.Contains(string(data), "private panic job") {
		t.Fatal("description leaked")
	}
	m.OnError = func(context.Context, error, string) { panic("observer fault") }
	id := m.Add(context.Background(), "observer isolation", job.MakeJobExec(func(context.Context, *job.Progress) error { return errors.New("real job failure") }))
	deadline := time.Now().Add(time.Second)
	for {
		result := m.GetJob(id)
		if result.EndTime != nil {
			if result.Status != job.StatusFailed {
				t.Fatal("observer changed job result")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("observer blocked job")
		}
		time.Sleep(time.Millisecond)
	}

}
func TestDeliveryDiscardIsObservableWithoutRecursion(t *testing.T) {
	_, events := offlineSDK(t, http.StatusBadRequest)
	before := deliveryFailed.Load()
	CaptureGenerationFailure(context.Background(), errors.New("synthetic failure"), GenerationFailureContext{})
	received := events()
	if len(received) != 1 || deliveryFailed.Load() != before+1 {
		t.Fatal("delivery loss not counted or recursive")
	}
}
func TestSafeGenerationSourceContextAndUnknownDriverText(t *testing.T) {
	err := ffmpeg.WithIntelGenerationDiagnostic(errors.New("unfamiliar VAAPI driver cause password='secret'"), ffmpeg.IntelGenerationDiagnostic{Selected: "vaapi", Actual: "vaapi", Stage: "decode", Device: "/dev/dri/renderD129", Reason: "unknown driver diagnostic", Filter: "scale_vaapi=format=nv12", ProbeTimeout: 3 * time.Second, ProbeStages: []string{"decode", "encode"}}, ffmpeg.IntelSource{Codec: "hevc", Profile: "Main 10", PixelFormat: "p010le", BitDepth: 10, ColorTransfer: "smpte2084", ColorPrimaries: "bt2020", ColorSpace: "bt2020nc", Rotation: 90, SampleAspectRatio: "1:1", RuntimeFingerprint: "driver-version-sentinel"})
	event := GenerationException(err, GenerationFailureContext{})
	data, _ := json.Marshal(beforeSend(event))
	for _, part := range []string{"unknown driver diagnostic", "Main 10", "p010le", "smpte2084", "bt2020", "renderD129", "scale_vaapi", "driver-version-sentinel"} {
		if !strings.Contains(string(data), part) {
			t.Errorf("missing context %s", part)
		}
	}
	if strings.Contains(string(data), "'secret'") {
		t.Fatal("source context leaked secret")
	}
}
func TestPropertyBudgetMalformedMetadataAndOmissions(t *testing.T) {
	p := posthog.NewProperties().Set("retained_diagnostic", "unknown diagnostic cause").Set("bad_float", math.NaN())
	huge := make([]any, 100000)
	for i := range huge {
		huge[i] = strings.Repeat("x", 100)
	}
	p.Set("verbose_output", huge)
	safe := safeProperties(p)
	data, err := json.Marshal(safe)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > 200*1024 || safe["diagnostics_omitted_units"].(int) == 0 || safe["retained_diagnostic"] != "unknown diagnostic cause" {
		t.Fatal("property cost/loss unbounded or cause displaced")
	}
}
func TestMetadataTagsOmittedWithoutDiscardingDriverRecords(t *testing.T) {
	text, omitted := diagnosticStderrDetails([]byte("ffmpeg version version-sentinel\n  Metadata:\n    title: private-title\n    arbitrary_tag: private-tag\n  Stream #0:0: Video: hevc\n[hevc] unknown driver diagnostic: missing reference frame\n"))
	if omitted != 3 || strings.Contains(text, "private-title") || strings.Contains(text, "private-tag") || !strings.Contains(text, "version-sentinel") || !strings.Contains(text, "unknown driver diagnostic") {
		t.Fatal("metadata removal lost driver context or leaked tags")
	}
}
func TestStructuredPanicContextAndFutureStageKeepDiagnostics(t *testing.T) {
	event := PanicException(map[string]any{"cause": "unfamiliar callback failure", "request": map[string]any{"body": "private-body"}, "title": "private-title", "driver_version": "unfamiliar-driver-version"})
	encoded, _ := json.Marshal(beforeSend(event))
	if strings.Contains(string(encoded), "private-body") || strings.Contains(string(encoded), "private-title") || !strings.Contains(string(encoded), "unfamiliar callback failure") || !strings.Contains(string(encoded), "unfamiliar-driver-version") {
		t.Fatal("structured panic policy lost diagnostic or private content escaped")
	}
	future := GenerationException(ffmpeg.WithIntelGenerationDiagnostic(errors.New("new transformation failed"), ffmpeg.IntelGenerationDiagnostic{Selected: "future_gpu", Stage: "hdr_transform"}, ffmpeg.IntelSource{}), GenerationFailureContext{Workload: "future_workload"})
	if future.Properties["generation_stage"] != "hdr_transform" || future.Properties["generation_selected_backend"] != "future_gpu" || future.Properties["generation_workload"] != "future_workload" {
		t.Fatal("unknown safe stage/backend/workload discarded")
	}
}
func TestConcurrentSavedNativeStackOwnsEachReport(t *testing.T) {
	native := diagnostics.CaptureStack()
	command := &python.CommandError{Stage: "module_install", Module: "widget", Err: errors.New("fixture failure"), NativeStack: native}
	generation := &ffmpeg.GenerationCommandError{Err: errors.New("fixture ffmpeg failure"), NativeStack: native}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			event := PackageInstallException(command, PackageFailureContext{})
			_ = beforeSend(event)
			other := GenerationException(generation, GenerationFailureContext{})
			_ = beforeSend(other)
		}()
	}
	wg.Wait()
}
func TestLargeCausesKeepEveryModuleSummaryAndControlContextOnWire(t *testing.T) {
	_, events := offlineSDK(t, 0)
	var failures []error
	for i := 0; i < 64; i++ {
		failures = append(failures, &python.CommandError{Stage: "module_install", Module: fmt.Sprintf("module_%02d", i), Err: errors.New("cause-prefix " + strings.Repeat("x", 20000) + " cause-suffix"), Output: strings.Repeat("y", 10000)})
	}
	CapturePackageInstallFailure(diagnostics.WithState(context.Background()), &pkg.InstallError{Stage: "python_dependencies", Err: errors.Join(failures...)}, PackageFailureContext{PackageID: "PythonTools", Operation: "update", JobCorrelation: uuid.NewString()})
	received := events()
	if len(received) != 1 {
		t.Fatal("aggregate capture changed")
	}
	encoded, _ := json.Marshal(received)
	for i := 0; i < 64; i++ {
		if !strings.Contains(string(encoded), fmt.Sprintf("module_%02d", i)) {
			t.Fatalf("module %d summary lost", i)
		}
	}
	for _, context := range []string{"PythonTools", "package_operation", "package_failure_count", "diagnostics_omitted_units", "cause-prefix", "cause-suffix"} {
		if !strings.Contains(string(encoded), context) {
			t.Errorf("context lost %s", context)
		}
	}
	if len(encoded) > 250*1024 {
		t.Fatal("event exceeded generous budget")
	}
}

func TestNoOutputGenerationKeepsWrapperAndDropsArguments(t *testing.T) {
	command := &ffmpeg.GenerationCommandError{Err: errors.New("ffmpeg command produced no output: <-i /private/media.mp4 -vf PRIVATE_FILTER>")}
	err := fmt.Errorf("extracting preview: %w: preview incomplete", command)
	event := GenerationException(err, GenerationFailureContext{})
	encoded, _ := json.Marshal(beforeSend(event))
	for _, retained := range []string{"extracting preview", "ffmpeg command produced no output", "preview incomplete"} {
		if !strings.Contains(string(encoded), retained) {
			t.Errorf("wrapper/cause lost %s", retained)
		}
	}
	for _, private := range []string{"/private/", "media.mp4", "PRIVATE_FILTER"} {
		if strings.Contains(string(encoded), private) {
			t.Errorf("argument leaked %s", private)
		}
	}
}

func TestJoinedCancellationKilledChildIsNotARealFailure(t *testing.T) {
	command := exec.Command("sh", "-c", "kill -TERM $$")
	child := command.Run()
	if child == nil {
		t.Fatal("fixture child unexpectedly succeeded")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	joined := errors.Join(context.Canceled, child)
	if shouldCapture(ctx, joined) {
		t.Fatal("cancellation plus killed child treated as genuine failure")
	}
	if !shouldCapture(ctx, errors.Join(joined, errors.New("real independent failure"))) {
		t.Fatal("real joined failure omitted")
	}
}
