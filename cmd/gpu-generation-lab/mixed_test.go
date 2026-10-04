package main

import (
	"context"
	"errors"
	"image"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stashapp/stash/pkg/ffmpeg"
	"github.com/stashapp/stash/pkg/fsutil"
	"github.com/stashapp/stash/pkg/generationbudget"
	"github.com/stashapp/stash/pkg/scene/generate"
)

func TestOwnMediaStatScope(t *testing.T) {
	for _, tc := range []struct {
		stat string
		want bool
	}{
		{"10 (ffmpeg) S 42 0", true},
		{"10 (ffprobe) S 42 0", true},
		{"10 (ffmpeg) S 43 0", false},
		{"10 (unrelated) S 42 0", false},
		{"10 (ffmpeg with spaces) S 42 0", false},
		{"10 (ffmpeg) S", false},
		{"10 ffmpeg S 42", false},
	} {
		if got := isOwnMediaStat(tc.stat, 42); got != tc.want {
			t.Errorf("%q = %v; want%v", tc.stat, got, tc.want)
		}
	}
}

func TestMixedFixtureBounds(t *testing.T) {
	valid := ffmpeg.IntelSource{Duration: "10.000000", Width: 640, Height: 360, FrameRate: "30/1", AverageFrameRate: "30/1", PixelFormat: "yuv420p"}
	if err := mixedFixtureContract(valid, true); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*ffmpeg.IntelSource){
		func(s *ffmpeg.IntelSource) { s.Duration = "NaN" },
		func(s *ffmpeg.IntelSource) { s.Duration = "+Inf" },
		func(s *ffmpeg.IntelSource) { s.Duration = "100" },
		func(s *ffmpeg.IntelSource) { s.Width = 1920; s.Height = 1080 },
		func(s *ffmpeg.IntelSource) { s.Rotation = 90 },
		func(s *ffmpeg.IntelSource) { s.PixelFormat = "yuv420p10le" },
		func(s *ffmpeg.IntelSource) { s.AverageFrameRate = "29/1" },
	} {
		s := valid
		mutate(&s)
		if err := mixedFixtureContract(s, true); err == nil {
			t.Errorf("unbounded/unsupported hash fixture accepted: %+v", s)
		}
	}
}

func TestMixedNonHashFixtureBounds(t *testing.T) {
	fixture := ffmpeg.IntelSource{Duration: "10", Width: 1920, Height: 1080, FrameRate: "30/1", AverageFrameRate: "30/1", PixelFormat: "yuv420p"}
	if err := mixedFixtureContract(fixture, false); err != nil {
		t.Fatalf("authorized 1080p synthetic fixture rejected: %v", err)
	}
	for _, size := range [][2]int{{1936, 1089}, {3840, 2160}, {7680, 4320}} {
		fixture.Width, fixture.Height = size[0], size[1]
		if err := mixedFixtureContract(fixture, false); err == nil {
			t.Errorf("oversize non-hash fixture accepted: %dx%d", size[0], size[1])
		}
	}
}

func TestMixedUnavailableProcessObservationReport(t *testing.T) {
	s := &mixedSampler{}
	report := s.Report()
	if report["process_limit_status"] != "untested" {
		t.Fatalf("missing procfs observations must be explicitly untested, got %+v", report)
	}
	if report["final_own_media_children"] != nil {
		t.Fatalf("no final observation must not be reported as zero children: %+v", report)
	}
	if report["peak_own_media_children"] != nil {
		t.Fatalf("no successful observations must not be reported as zero peak children: %+v", report)
	}
}

func TestMixedSamplerReadFailures(t *testing.T) {
	for _, tc := range []struct {
		name       string
		read       func(int) ([]int, error)
		wantStatus string
		wantFinal  bool
	}{
		{"unavailable", func(int) ([]int, error) { return nil, errors.New("procfs unavailable") }, "untested", false},
		{"intermittent", func(n int) ([]int, error) {
			if n == 1 {
				return nil, errors.New("task children temporarily unreadable")
			}
			return nil, nil
		}, "untested", true},
		{"final failure after success", func(n int) ([]int, error) {
			if n > 1 {
				return nil, errors.New("final stat read failed")
			}
			return nil, nil
		}, "untested", false},
		{"successful", func(int) ([]int, error) { return nil, nil }, "passed", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reads := 0
			s := startMixedSamplerWithRead(context.Background(), func() ([]int, error) {
				reads++
				return tc.read(reads)
			})
			s.Stop()
			if reads < 2 || s.FinalSampleValid != tc.wantFinal {
				t.Fatalf("initial/final samples not tracked: reads=%d final=%t", reads, s.FinalSampleValid)
			}
			status, reason, err := mixedAcceptanceOutcome(nil, nil, s)
			if status != tc.wantStatus || err != nil {
				t.Fatalf("got %s/%v; want %s", status, err, tc.wantStatus)
			}
			if status == "untested" && (!strings.Contains(reason, "rerun on Linux") || s.FailedSamples == 0 || s.LastSampleError == "") {
				t.Fatalf("incomplete observations lack actionable evidence: %+v / %s", s.Report(), reason)
			}
			if !tc.wantFinal && s.Report()["final_own_media_children"] != nil {
				t.Fatal("failed final read reported as zero children")
			}
		})
	}
}

func TestMixedObservationOutcomePreservesFailures(t *testing.T) {
	for _, tc := range []struct {
		name      string
		pids      []int
		final     []int
		readErr   error
		jobErr    string
		drainErr  error
		wantError string
	}{
		{name: "concurrent children", pids: []int{10, 11}, wantError: "concurrent"},
		{name: "partial read still proves violation", pids: []int{10, 11}, readErr: errors.New("partial stat read"), wantError: "concurrent"},
		{name: "final child remains", pids: []int{10}, final: []int{10}, wantError: "remain"},
		{name: "generator error with missing procfs", readErr: errors.New("procfs unavailable"), jobErr: "generator failed", wantError: "marker: generator failed"},
		{name: "budget drain error with missing procfs", readErr: errors.New("procfs unavailable"), drainErr: errors.New("drain failed"), wantError: "budget drain: drain failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &mixedSampler{}
			s.recordSample(context.Background(), tc.pids, tc.readErr, false)
			s.recordSample(context.Background(), tc.final, tc.readErr, true)
			status, _, err := mixedAcceptanceOutcome([]mixedJob{{Name: "marker", Error: tc.jobErr}}, tc.drainErr, s)
			if status != "failed" || err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("failure masked: status=%s error=%v", status, err)
			}
		})
	}
	s := &mixedSampler{}
	s.recordSample(context.Background(), []int{10}, nil, false)
	s.recordSample(context.Background(), nil, nil, true)
	status, _, err := mixedAcceptanceOutcome(nil, nil, s)
	if status != "passed" || err != nil || s.PeakChildren != 1 || s.FinalChildren != 0 {
		t.Fatalf("successful one-child/drained observations rejected: %s/%v", status, err)
	}
}

func TestMixedProcfsReadFailuresAreNotEmptyObservations(t *testing.T) {
	base := t.TempDir()
	if err := os.Mkdir(filepath.Join(base, "42"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name      string
		children  string
		stat      string
		missing   string
		wantCount int
		wantError bool
	}{
		{name: "missing children", missing: "children", wantError: true},
		{name: "missing child stat", children: "10", missing: "stat", wantError: true},
		{name: "malformed child PID", children: "bad", wantError: true},
		{name: "malformed child stat", children: "10", stat: "10 (ffmpeg) S", wantError: true},
		{name: "partial child stats", children: "10 11", stat: "10 (ffmpeg) S 42 0", wantCount: 1, wantError: true},
		{name: "valid media child", children: "10", stat: "10 (ffmpeg) S 42 0", wantCount: 1},
		{name: "valid unrelated child", children: "10", stat: "10 (unrelated) S 42 0"},
		{name: "valid empty children"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			readFile := func(path string) ([]byte, error) {
				if filepath.Base(path) == "children" && tc.missing != "children" {
					return []byte(tc.children), nil
				}
				if path == "/proc/10/stat" && tc.missing != "stat" {
					return []byte(tc.stat), nil
				}
				return nil, os.ErrPermission
			}
			pids, err := readOwnMediaChildren(base, 42, os.ReadDir, readFile)
			if (err != nil) != tc.wantError || len(pids) != tc.wantCount {
				t.Fatalf("got %v/%v; want count %d error %t", pids, err, tc.wantCount, tc.wantError)
			}
		})
	}
	if _, err := readOwnMediaChildren(filepath.Join(base, "missing"), 42, os.ReadDir, os.ReadFile); err == nil {
		t.Fatal("unavailable procfs directory treated as empty children")
	}
	if _, err := readOwnMediaChildren(t.TempDir(), 42, os.ReadDir, os.ReadFile); err == nil {
		t.Fatal("empty procfs task directory treated as valid sample")
	}
}

func TestMixedPreCancelledDoesNotProbeOrAcquire(t *testing.T) {
	budget, _ := generationbudget.New(generationbudget.Settings{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	g := generate.Generator{Budget: budget} // Nil probe would panic if called.
	if _, err := runMixed(ctx, &g, labPaths{t.TempDir()}, "fixture", "hash-fixture", t.TempDir()); err != context.Canceled {
		t.Fatalf("got %v", err)
	}
	release, err := budget.Acquire(context.Background(), generationbudget.CPU)
	if err != nil {
		t.Fatal(err)
	}
	release()
}

func TestMixedVTTValidatesRealGeneratorAndRejectsDrift(t *testing.T) {
	dir := t.TempDir()
	budget, _ := generationbudget.New(generationbudget.Settings{})
	g := generate.Generator{Budget: budget, ScenePaths: labScenePaths{dir}, LockManager: fsutil.NewReadLockManager()}
	images := make([]image.Image, 81)
	for i := range images {
		images[i] = image.NewNRGBA(image.Rect(0, 0, 160, 90))
	}
	sprite := filepath.Join(dir, "sprite.jpg")
	if err := g.SaveSprite(context.Background(), images, sprite); err != nil {
		t.Fatal(err)
	}
	vtt := filepath.Join(dir, "sprite.vtt")
	if err := g.SpriteVTT(context.Background(), vtt, sprite, 2.0/81, 1); err != nil {
		t.Fatal(err)
	}
	if err := validateMixedVTT(vtt); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(vtt)
	if err != nil {
		t.Fatal(err)
	}
	data = []byte(strings.Replace(string(data), "#xywh=0,0,160,90", "#xywh=160,0,160,90", 1))
	if err := os.WriteFile(vtt, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := validateMixedVTT(vtt); err == nil {
		t.Fatal("coordinate drift accepted")
	}
}
