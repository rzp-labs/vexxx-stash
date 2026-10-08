package task

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stashapp/stash/internal/analytics"
	"github.com/stashapp/stash/pkg/job"
	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/pkg"
	"github.com/stashapp/stash/pkg/python"
	"gopkg.in/yaml.v2"
)

type packagePaths struct{}

func (packagePaths) GetAllSourcePaths() []string { return []string{""} }
func (packagePaths) GetSourcePath(string) string { return "" }

type packageEvent struct {
	Event      string         `json:"event"`
	Properties map[string]any `json:"properties"`
}

// Real SDK serialization/transport, terminated at a local receiver. There is no
// live mode, external proxy, production token or real Python/pip installation.
func packageReceiver(t *testing.T) func() []packageEvent {
	t.Helper()
	var mu sync.Mutex
	var events []packageEvent
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/batch/" {
			t.Error("unexpected SDK endpoint")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var body io.Reader = r.Body
		if r.Header.Get("Content-Encoding") == "gzip" {
			reader, err := gzip.NewReader(body)
			if err != nil {
				t.Error(err)
				return
			}
			defer reader.Close()
			body = reader
		}
		var payload struct {
			APIKey string         `json:"api_key"`
			Batch  []packageEvent `json:"batch"`
		}
		if err := json.NewDecoder(io.LimitReader(body, 128*1024)).Decode(&payload); err != nil || payload.APIKey != "test-package-project" {
			t.Error("invalid/surprising SDK payload")
			return
		}
		mu.Lock()
		events = append(events, payload.Batch...)
		mu.Unlock()
		_, _ = io.WriteString(w, `{"status":1}`)
	}))
	t.Setenv("POSTHOG_PROJECT_TOKEN", "test-package-project")
	t.Setenv("POSTHOG_HOST", server.URL)
	if err := analytics.Initialize(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = analytics.Close()
		server.Close()
		_ = os.Setenv("POSTHOG_PROJECT_TOKEN", "")
		_ = os.Setenv("POSTHOG_HOST", "")
		_ = analytics.Initialize()
	})
	return func() []packageEvent {
		if err := analytics.Client().Flush(); err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		defer mu.Unlock()
		return append([]packageEvent(nil), events...)
	}
}

func packageFixture(t *testing.T, contents map[string]map[string]string, script string) (*pkg.Manager, []*models.PackageSpecInput, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	root := t.TempDir()
	var index []pkg.RemotePackage
	for _, id := range []string{"PythonTools", "FollowingPackage"} {
		files := contents[id]
		if files == nil {
			continue
		}
		var data bytes.Buffer
		writer := zip.NewWriter(&data)
		for name, value := range files {
			file, err := writer.Create(name)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := io.WriteString(file, value); err != nil {
				t.Fatal(err)
			}
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		name := id + ".zip"
		if err := os.WriteFile(filepath.Join(root, name), data.Bytes(), 0600); err != nil {
			t.Fatal(err)
		}
		index = append(index, pkg.RemotePackage{ID: id, Name: id, PackageLocation: pkg.PackageLocation{Path: name, Sha256: fmt.Sprintf("%x", sha256.Sum256(data.Bytes()))}})
	}
	data, err := yaml.Marshal(index)
	if err != nil {
		t.Fatal(err)
	}
	indexPath := filepath.Join(root, "packages.yaml")
	if err := os.WriteFile(indexPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	source := (&url.URL{Scheme: "file", Path: indexPath}).String()
	binary := filepath.Join(root, "fake-python")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\n"+script), 0700); err != nil {
		t.Fatal(err)
	}
	m := &pkg.Manager{Local: &pkg.Store{BaseDir: filepath.Join(root, "installed"), ManifestFile: pkg.ManifestFile}, PackagePathGetter: packagePaths{}, PythonPath: binary}
	var specs []*models.PackageSpecInput
	for _, entry := range index {
		specs = append(specs, &models.PackageSpecInput{ID: entry.ID, SourceURL: source})
	}
	return m, specs, root
}

func runPackageJob(t *testing.T, executor job.JobExec) job.Job {
	t.Helper()
	m := job.NewManager()
	t.Cleanup(func() { m.StopAndWait(time.Second) })
	id := m.Add(context.Background(), "synthetic package test", executor)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if result := m.GetJob(id); result != nil && result.EndTime != nil {
			return *result
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("package job did not drain")
	return job.Job{}
}

func TestPackagePipFailureReportsOnceAndContinuesBatch(t *testing.T) {
	for _, operation := range []string{"install", "update"} {
		t.Run(operation, func(t *testing.T) {
			received := packageReceiver(t)
			m, specs, root := packageFixture(t, map[string]map[string]string{
				"PythonTools": {"requirements.txt": "mystery-widget==6"}, "FollowingPackage": {"readme.txt": "success"},
			}, `if [ "$1" = '-c' ]; then printf '%s\n' 'INSTALL:mystery-widget==6'; exit 0; fi
printf '%s\n' 'ERROR: wheel-build-sentinel failed for mystery-widget==6' 'token=fixture-secret' 'Authorization: Bearer fixture-auth' 'Cookie: session=fixture-cookie; other=fixture-cookie' 'https://user:fixture-url@example.invalid/simple?token=fixture-query' '/Users/private person/plugin.py: permission denied' >&2
printf '%s\n' 'password=correct horse battery staple' '私密录像.mp4' 'Cookie: theme=dark; sid=opaque-session-value' 'privateKey=-----BEGIN PRIVATE KEY-----' 'fixture-key-body' '-----END PRIVATE KEY-----' >&2
exit 23
`)
			completed := false
			base := PackagesJob{PackageManager: m, OnComplete: func() { completed = true }}
			var executor job.JobExec = &InstallPackagesJob{PackagesJob: base, Packages: specs}
			if operation == "update" {
				executor = &UpdatePackagesJob{PackagesJob: base, Packages: specs}
			}
			result := runPackageJob(t, executor)
			if result.Status != job.StatusFailed || result.Error == nil || !strings.Contains(*result.Error, "PythonTools") || !strings.Contains(*result.Error, "exit status 23") || !completed || result.Progress != 1 {
				t.Fatal("failed dependency was not reflected in completed batch status")
			}
			for _, id := range []string{"PythonTools", "FollowingPackage"} {
				if _, err := os.Stat(filepath.Join(m.Local.BaseDir, id, pkg.ManifestFile)); err != nil {
					t.Fatal("partial install or following successful package was discarded")
				}
			}
			events := received()
			if len(events) != 1 || events[0].Event != "$exception" {
				t.Fatal("expected exactly one package exception (no batch/log duplicate)")
			}
			p := events[0].Properties
			if p["package_operation"] != operation || p["package_id"] != "PythonTools" || p["package_stage"] != "requirements_install" || p["python_exit_code"] != float64(23) || p["package_error_cause"] != "exit status 23" || p["package_files_installed"] != true || p["package_failure_count"] != float64(1) || p["job_correlation"] == "unavailable" {
				t.Fatal("SDK payload lost structured package/exit/partial-install context")
			}
			output, ok := p["python_output"].(string)
			if !ok || !strings.Contains(output, "wheel-build-sentinel failed for mystery-widget==6") || len(output) > python.MaxDiagnosticBytes {
				t.Fatal("SDK payload lost useful stderr or exceeded bound")
			}
			encoded, err := json.Marshal(events[0])
			if err != nil {
				t.Fatal(err)
			}
			for _, private := range []string{"fixture-secret", "fixture-auth", "fixture-cookie", "fixture-url", "fixture-query", "private person", "correct", "horse", "battery", "staple", "私密录像", "opaque-session-value", "fixture-key-body", root, m.PythonPath, specs[0].SourceURL} {
				if strings.Contains(string(encoded), private) || strings.Contains(*result.Error, private) {
					t.Fatal("credential or private location escaped in SDK payload/job status")
				}
			}
			if !strings.Contains(string(encoded), "pkg/python/exec.go") || !strings.Contains(string(encoded), "pkg/pkg/manager.go") || !strings.Contains(string(encoded), "CheckAndInstallRequirements") || strings.Contains(string(encoded), "PackageInstallException") {
				t.Fatal("SDK stack did not retain the failure origin")
			}
		})
	}
}

func TestFallbackFailuresContinueModulesAndShareOnePackageEvent(t *testing.T) {
	received := packageReceiver(t)
	m, specs, root := packageFixture(t, map[string]map[string]string{"PythonTools": {"plugin.py": "import badone\nimport badtwo\nimport goodmod\n"}}, `if [ "$1" = '-c' ]; then printf '%s\n' badone badtwo goodmod; exit 0; fi
if [ "$4" = 'goodmod' ]; then exit 0; fi
printf 'ERROR: fallback-wheel-sentinel for %s\n' "$4" >&2
exit 17
`)
	// Record every invocation without modifying the executable's install behavior.
	data, err := os.ReadFile(m.PythonPath)
	if err != nil {
		t.Fatal(err)
	}
	trace := filepath.Join(root, "attempts")
	data = bytes.Replace(data, []byte("#!/bin/sh\n"), []byte("#!/bin/sh\nprintf '%s\\n' \"$4\" >> '"+trace+"'\n"), 1)
	if err := os.WriteFile(m.PythonPath, data, 0700); err != nil {
		t.Fatal(err)
	}
	result := runPackageJob(t, &InstallPackagesJob{PackagesJob: PackagesJob{PackageManager: m}, Packages: specs})
	if result.Status != job.StatusFailed || result.Error == nil || !strings.Contains(*result.Error, "badone") || !strings.Contains(*result.Error, "badtwo") {
		t.Fatal("fallback failures did not propagate")
	}
	attempts, err := os.ReadFile(trace)
	if err != nil || !strings.Contains(string(attempts), "goodmod") {
		t.Fatal("fallback stopped before subsequent module")
	}
	events := received()
	if len(events) != 1 || events[0].Properties["package_failure_count"] != float64(2) {
		t.Fatal("fallback module failures caused duplicate/missing package events")
	}
	details := events[0].Properties["package_failures"].([]any)
	for i, mod := range []string{"badone", "badtwo"} {
		d := details[i].(map[string]any)
		if d["stage"] != "module_install" || d["exit_code"] != float64(17) || !strings.Contains(d["python_output"].(string), mod) {
			t.Fatal("joined module diagnostic lost cause/stage/exit")
		}
	}
}

func TestSuccessfulPackagesRemainFinishedWithoutFailureTelemetry(t *testing.T) {
	received := packageReceiver(t)
	m, specs, _ := packageFixture(t, map[string]map[string]string{"PythonTools": {"requirements.txt": "mystery-widget==6"}}, "if [ \"$1\" = '-c' ]; then echo 'INSTALL:mystery-widget==6'; fi\nexit 0\n")
	result := runPackageJob(t, &InstallPackagesJob{PackagesJob: PackagesJob{PackageManager: m}, Packages: specs})
	if result.Status != job.StatusFinished || result.Error != nil || len(received()) != 0 {
		t.Fatal("success changed status or emitted failure telemetry")
	}
}

func TestEmptyPipOutputRetainsEachFailedModuleInSDKPayload(t *testing.T) {
	received := packageReceiver(t)
	m, specs, _ := packageFixture(t, map[string]map[string]string{"PythonTools": {"plugin.py": "import badone\nimport badtwo\n"}}, `if [ "$1" = '-c' ]; then printf '%s\n' badone badtwo; exit 0; fi
exit 23
`)
	result := runPackageJob(t, &InstallPackagesJob{PackagesJob: PackagesJob{PackageManager: m}, Packages: specs})
	events := received()
	if result.Status != job.StatusFailed || len(events) != 1 || events[0].Properties["package_failure_count"] != float64(2) || events[0].Properties["python_module"] != "badone" {
		t.Fatal("empty-output failures lost job/module context or duplicated capture")
	}
	details := events[0].Properties["package_failures"].([]any)
	if len(details) != 2 {
		t.Fatal("expected both empty-output module diagnostics")
	}
	for i, module := range []string{"badone", "badtwo"} {
		detail := details[i].(map[string]any)
		if detail["module"] != module || detail["error_cause"] != "installing module "+module+": exit status 23" || detail["stage"] != "module_install" || detail["exit_code"] != float64(23) {
			t.Fatal("equal exit codes and empty output obscured failed dependency identity")
		}
		if _, exists := detail["python_output"]; exists {
			t.Fatal("empty output should not invent a pip diagnostic")
		}
	}
	encoded, err := json.Marshal(events[0])
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("sanitized_sdk_payload=%s", encoded)
}

func TestVerboseFallbackJobStatusPreservesAllModuleCauses(t *testing.T) {
	received := packageReceiver(t)
	m, specs, _ := packageFixture(t, map[string]map[string]string{"PythonTools": {"plugin.py": "import badone\nimport badtwo\n"}}, `if [ "$1" = '-c' ]; then printf '%s\n' badone badtwo; exit 0; fi
i=0
while [ "$i" -lt 3072 ]; do printf x; i=$((i+1)); done
printf '\nERROR: verbose-wheel-sentinel token=fixture-secret\n' >&2
if [ "$4" = 'badone' ]; then exit 23; fi
exit 24
`)
	result := runPackageJob(t, &InstallPackagesJob{PackagesJob: PackagesJob{PackageManager: m}, Packages: specs})
	if result.Status != job.StatusFailed || result.Error == nil {
		t.Fatal("verbose module failure did not reach job status")
	}
	for _, module := range []string{"badone", "badtwo"} {
		if strings.Count(*result.Error, "installing module "+module+":") != 1 {
			t.Fatal("job status lost or duplicated a failed module prefix")
		}
	}
	for _, cause := range []string{"exit status 23", "exit status 24", "verbose-wheel-sentinel"} {
		if !strings.Contains(*result.Error, cause) {
			t.Fatal("verbose output displaced concise per-module causes")
		}
	}
	_, output, found := strings.Cut(*result.Error, "\nOutput: ")
	if !found || len(output) > python.MaxDiagnosticBytes || strings.Contains(*result.Error, "fixture-secret") {
		t.Fatal("job output bypassed its independent bound or redaction")
	}
	events := received()
	if len(events) != 1 || events[0].Properties["package_failure_count"] != float64(2) {
		t.Fatal("summary fix changed package event multiplicity")
	}
}

func TestPythonPreparationAndStartFailuresReachJobAndSDK(t *testing.T) {
	for _, test := range []struct {
		name, stage, script string
		files               map[string]string
		exit                int
	}{
		{"requirements analysis", "requirements_analyze", "echo 'analysis-sentinel token=fixture-secret' >&2\nexit 31\n", map[string]string{"requirements.txt": "mystery-widget"}, 31},
		{"fallback module check", "module_check", "echo 'module-check-sentinel token=fixture-secret' >&2\nexit 32\n", map[string]string{"plugin.py": "import mysterywidget\n"}, 32},
		{"process start", "requirements_analyze", "exit 0\n", map[string]string{"requirements.txt": "mystery-widget"}, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			received := packageReceiver(t)
			m, specs, _ := packageFixture(t, map[string]map[string]string{"PythonTools": test.files}, test.script)
			if test.exit == 0 {
				if err := os.Chmod(m.PythonPath, 0600); err != nil {
					t.Fatal(err)
				}
			}
			result := runPackageJob(t, &InstallPackagesJob{PackagesJob: PackagesJob{PackageManager: m}, Packages: specs})
			events := received()
			if result.Status != job.StatusFailed || len(events) != 1 || events[0].Properties["package_stage"] != test.stage {
				t.Fatal("preparation/start failure did not reach job and SDK")
			}
			if test.exit != 0 && events[0].Properties["python_exit_code"] != float64(test.exit) {
				t.Fatal("lost preparation process exit status")
			}
			if test.exit == 0 {
				if _, present := events[0].Properties["python_exit_code"]; present || !strings.Contains(events[0].Properties["package_error_cause"].(string), "permission denied") {
					t.Fatal("process start invented an exit code or lost OS cause")
				}
			}
			encoded, _ := json.Marshal(events[0])
			if strings.Contains(string(encoded), "fixture-secret") || strings.Contains(*result.Error, "fixture-secret") {
				t.Fatal("preparation failure leaked credential fixture")
			}
		})
	}
}

func TestMissingPackageFailsLookupWithoutPanicAndContinues(t *testing.T) {
	received := packageReceiver(t)
	m, specs, _ := packageFixture(t, map[string]map[string]string{"FollowingPackage": {"readme.txt": "success"}}, "exit 0\n")
	packages := append([]*models.PackageSpecInput{{ID: "MissingPackage", SourceURL: specs[0].SourceURL}}, specs...)
	result := runPackageJob(t, &InstallPackagesJob{PackagesJob: PackagesJob{PackageManager: m}, Packages: packages})
	events := received()
	if result.Status != job.StatusFailed || len(events) != 1 || events[0].Properties["package_stage"] != "package_lookup" || events[0].Properties["package_files_installed"] != false || !strings.Contains(events[0].Properties["package_error_cause"].(string), "not found") {
		t.Fatal("missing package did not produce a normal lookup failure")
	}
	if _, err := os.Stat(filepath.Join(m.Local.BaseDir, "FollowingPackage", pkg.ManifestFile)); err != nil {
		t.Fatal("missing package aborted following package")
	}
}

func TestCancellationKeepsCancelledStatusAndEmitsNoFailure(t *testing.T) {
	received := packageReceiver(t)
	m, specs, root := packageFixture(t, map[string]map[string]string{"PythonTools": {"requirements.txt": "mystery-widget"}}, "exit 0\n")
	started := filepath.Join(root, "started")
	// A shell builtin loop has no child process to leave behind after cancellation.
	if err := os.WriteFile(m.PythonPath, []byte("#!/bin/sh\ntouch '"+started+"'\nwhile :; do :; done\n"), 0700); err != nil {
		t.Fatal(err)
	}
	jobs := job.NewManager()
	t.Cleanup(func() { jobs.StopAndWait(time.Second) })
	id := jobs.Add(context.Background(), "synthetic cancellation", &InstallPackagesJob{PackagesJob: PackagesJob{PackageManager: m}, Packages: specs})
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fake process did not start")
		}
		time.Sleep(time.Millisecond)
	}
	jobs.CancelJob(id)
	for time.Now().Before(deadline) {
		if result := jobs.GetJob(id); result != nil && result.EndTime != nil {
			if result.Status != job.StatusCancelled || result.Error != nil || len(received()) != 0 {
				t.Fatal("cancellation became failure status/telemetry")
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("cancelled fake process did not drain")
}
