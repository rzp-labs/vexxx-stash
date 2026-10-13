package diagnostics

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func replayTestRecord() PreviewReplayRecord {
	return PreviewReplayRecord{SceneID: 17, FileID: 29, JobCorrelation: uuid.NewString(), Replay: PreviewReplay{Version: 1, Reference: uuid.NewString(), SourceReference: uuid.NewString(), Command: PreviewCommand{Stage: "filter"}}}
}

func TestPreviewReplaySnapshotsPreserveIdentityAndImmutableFacts(t *testing.T) {
	var saved []PreviewReplayRecord
	ctx := WithState(WithPreviewSource(context.Background(), 17, 29, uuid.NewString(), func(r PreviewReplayRecord) error { saved = append(saved, r); return nil }))
	seek, duration, nan := 7.125, 0.75, math.NaN()
	ctx = WithPreviewRequest(ctx, PreviewRequest{Segments: 3, SegmentSeconds: &duration, LimitStart: &seek, ExcludeEnd: &nan, ExcludeEndUnit: "seconds"})
	matrix := [9]int32{1, 2, 3}
	n, d := int64(4), int64(3)
	want := errors.New("encoder failure")
	err := WithPreviewFailure(ctx, want, PreviewCommand{Stage: "render", SeekSeconds: &seek, DurationSeconds: &duration, InputPixels: &PreviewPixels{Width: 720, Height: 406, SARNum: &n, SARDen: &d, DisplayMatrix: &matrix}, PlanFingerprint: strings.Repeat("a", 64)})
	seek, duration, n, matrix[0] = 99, 88, 7, 77
	r, ok := PreviewFailureSnapshot(err)
	if !ok || *r.Command.SeekSeconds != 7.125 || *r.Request.SegmentSeconds != 0.75 || r.Request.ExcludeEnd != nil || *r.Command.InputPixels.SARNum != 4 || r.Command.InputPixels.DisplayMatrix[0] != 1 {
		t.Fatalf("mutable/nonfinite facts: %+v", r)
	}
	if !r.SourceResolved || !r.Retained || r.Retention != "retained" || len(saved) != 1 || saved[0].SceneID != 17 || saved[0].FileID != 29 || err.Error() != want.Error() || !errors.Is(err, want) {
		t.Fatal("source/error identity", r, saved, err)
	}
	if WithPreviewFailure(ctx, fmt.Errorf("task: %w", err), PreviewCommand{Stage: "generation"}) == nil || len(saved) != 1 {
		t.Fatal("duplicate retained producer")
	}
	Mark(ctx, err)
	if !Reported(ctx, fmt.Errorf("job: %w", err)) {
		t.Fatal("wrapper changed deduplication")
	}
	*r.Command.SeekSeconds = 66
	r2, _ := PreviewFailureSnapshot(err)
	if *r2.Command.SeekSeconds != 7.125 {
		t.Fatal("snapshot exposed mutable pointers")
	}
	data, _ := json.Marshal(r2)
	if strings.Contains(string(data), "scene_id") || strings.Contains(string(data), "file_id") {
		t.Fatal("IDs exported", string(data))
	}
}

func TestPreviewReplayJoinedSourcesCancellationAndMissingMapping(t *testing.T) {
	var mu sync.Mutex
	var saved []PreviewReplayRecord
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 1; i <= 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ctx := WithPreviewSource(context.Background(), i, i+10, "private job", func(r PreviewReplayRecord) error {
				mu.Lock()
				defer mu.Unlock()
				saved = append(saved, r)
				return ErrPreviewReplayQueued
			})
			seek := float64(i)
			results <- WithPreviewFailure(ctx, errors.New("filter"), PreviewCommand{Stage: "filter", SeekSeconds: &seek})
		}(i)
	}
	wg.Wait()
	close(results)
	var errs []error
	for err := range results {
		errs = append(errs, err)
	}
	joined := fmt.Errorf("task: %w", errors.Join(errs[0], context.Canceled, errs[1]))
	if _, ok := PreviewFailureSnapshot(joined); ok {
		t.Fatal("borrowed a sibling")
	}
	failures := Split(joined)
	if failures.Count != 2 || len(saved) != 2 {
		t.Fatal("join/cancellation", failures, saved)
	}
	a, _ := PreviewFailureSnapshot(failures.Entries[0].Err)
	b, _ := PreviewFailureSnapshot(failures.Entries[1].Err)
	if a.SourceReference == b.SourceReference || a.Reference == b.Reference || a.Retention != "queued" || a.Retained {
		t.Fatal("independent or queued facts", a, b)
	}
	ctx := WithPreviewSource(context.Background(), 0, 0, "bad job", func(r PreviewReplayRecord) error {
		if r.JobCorrelation != "" {
			t.Fatal("raw correlation")
		}
		return errors.New("disk unavailable")
	})
	if WithPreviewFailure(ctx, context.Canceled, PreviewCommand{Stage: "render"}) != context.Canceled {
		t.Fatal("pure cancellation changed")
	}
	err := WithPreviewFailure(ctx, context.DeadlineExceeded, PreviewCommand{Stage: "decode"})
	r, ok := PreviewFailureSnapshot(err)
	if !ok || r.SourceResolved || r.Retained || r.Retention != "unavailable" || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("missing/write-failed/deadline facts", r, err)
	}
	if WithPreviewFailure(ctx, joined, PreviewCommand{Stage: "generation"}) != joined {
		t.Fatal("rewrapped join")
	}
}

func TestPreviewReplayTypedAllowlist(t *testing.T) {
	ctx := WithPreviewSource(context.Background(), 1, 2, "", nil)
	value := 1.0
	err := WithPreviewFailure(ctx, errors.New("failure"), PreviewCommand{Stage: "/private/source", Projection: "private-title", PlanID: "private-plan", PlanFingerprint: "private-path", RuntimeFingerprint: "private-runtime", InputPixels: &PreviewPixels{ColorSpace: "private-title", ColorPrimaries: "token=secret"}})
	r, _ := PreviewFailureSnapshot(err)
	ctx = WithPreviewRequest(ctx, PreviewRequest{ExcludeStart: &value, ExcludeStartUnit: "private-unit"})
	data, _ := json.Marshal(r)
	if strings.Contains(string(data), "private") || strings.Contains(string(data), "secret") || r.Command.Stage != "unavailable" {
		t.Fatal("untrusted strings escaped", string(data))
	}
	if WithPreviewFailure(context.Background(), err, PreviewCommand{}) != err {
		t.Fatal("unrelated context changed")
	}
}

func TestPreviewReplayStoreResolveAfterReopenAndNonfatalFailure(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "preview-replay")
	r := replayTestRecord()
	if err := RetainPreviewReplay(dir, r); err != nil {
		t.Fatal(err)
	}
	found, err := FindPreviewReplay(dir, r.Replay.Reference)
	if err != nil || found.SceneID != 17 || found.FileID != 29 || !found.Replay.Retained || found.Replay.Retention != "retained" {
		t.Fatal("restart lookup", found, err)
	}
	for _, path := range []string{dir, filepath.Join(dir, "current.jsonl")} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm()&0077 != 0 {
			t.Fatal("private modes", path, info, err)
		}
	}
	if _, err := FindPreviewReplay(dir, uuid.NewString()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("missing reference", err)
	}
	blocked := filepath.Join(t.TempDir(), "blocked")
	os.WriteFile(blocked, []byte("existing"), 0600)
	ctx := WithPreviewSource(context.Background(), 17, 29, "", func(r PreviewReplayRecord) error { return RetainPreviewReplay(blocked, r) })
	want := errors.New("real GPU failure")
	err = WithPreviewFailure(ctx, want, PreviewCommand{Stage: "encode"})
	replay, _ := PreviewFailureSnapshot(err)
	if !errors.Is(err, want) || replay.Retention != "unavailable" {
		t.Fatal("write failure replaced media failure", err, replay)
	}
}

func TestPreviewReplayStoreConcurrentRotationAndExpiry(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "preview-replay")
	first := replayTestRecord()
	if err := RetainPreviewReplay(dir, first); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "current.jsonl"))
	count := 2*PreviewReplayFileBytes/len(data) + 20
	var wg sync.WaitGroup
	fail := make(chan error, 8)
	latest := make(chan PreviewReplayRecord, 8)
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var r PreviewReplayRecord
			for n := 0; n < count/8+1; n++ {
				r = replayTestRecord()
				if err := RetainPreviewReplay(dir, r); err != nil {
					fail <- err
					return
				}
			}
			latest <- r
		}()
	}
	wg.Wait()
	close(fail)
	close(latest)
	for err := range fail {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 2 {
		t.Fatal("file bound", entries, err)
	}
	for _, entry := range entries {
		info, _ := entry.Info()
		if info.Size() > PreviewReplayFileBytes {
			t.Fatal("rotation exceeded bound", info.Size())
		}
	}
	for r := range latest {
		found, err := FindPreviewReplay(dir, r.Replay.Reference)
		if err != nil || found.SceneID != r.SceneID {
			t.Fatal("concurrent record lost", r.Replay.Reference, err)
		}
	}
	if _, err := FindPreviewReplay(dir, first.Replay.Reference); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("old reference did not expire", err)
	}
}

func TestPreviewReplayStoreCorruptionAndInterruptedTail(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "preview-replay")
	os.Mkdir(dir, 0700)
	path := filepath.Join(dir, "current.jsonl")
	os.WriteFile(path, []byte("{broken\n"+strings.Repeat("x", PreviewReplayRecordBytes)+"\n{partial"), 0600)
	r := replayTestRecord()
	if err := RetainPreviewReplay(dir, r); err != nil {
		t.Fatal(err)
	}
	if found, err := FindPreviewReplay(dir, r.Replay.Reference); err != nil || found.FileID != 29 {
		t.Fatal("corrupt preceding record poisoned valid lookup", found, err)
	}
	if _, err := FindPreviewReplay(dir, uuid.NewString()); err == nil || errors.Is(err, os.ErrNotExist) {
		t.Fatal("corruption hidden on missing lookup", err)
	}
	os.WriteFile(path, make([]byte, PreviewReplayFileBytes+1), 0600)
	if err := RetainPreviewReplay(dir, r); err == nil {
		t.Fatal("oversized existing file accepted")
	}
	if _, err := FindPreviewReplay(dir, r.Replay.Reference); err == nil {
		t.Fatal("oversized file read")
	}
}

func TestPreviewReplayStoreRejectsUnexpectedNamespace(t *testing.T) {
	for _, kind := range []string{"directory-symlink", "file-symlink", "nonregular", "permissions"} {
		t.Run(kind, func(t *testing.T) {
			parent := t.TempDir()
			dir := filepath.Join(parent, "preview-replay")
			target := filepath.Join(parent, "target")
			os.Mkdir(target, 0700)
			if kind == "directory-symlink" {
				if err := os.Symlink(target, dir); err != nil {
					t.Skip(err)
				}
			} else {
				os.Mkdir(dir, 0700)
				path := filepath.Join(dir, "current.jsonl")
				switch kind {
				case "file-symlink":
					if err := os.Symlink(filepath.Join(target, "file"), path); err != nil {
						t.Skip(err)
					}
				case "nonregular":
					os.Mkdir(path, 0700)
				case "permissions":
					os.Chmod(dir, 0755)
				}
			}
			if err := RetainPreviewReplay(dir, replayTestRecord()); err == nil {
				t.Fatal("unexpected namespace accepted")
			}
		})
	}
}

func TestPreviewReplayQueueDoesNotWaitForStorageAndCopiesValues(t *testing.T) {
	queue := make(chan previewReplayWrite, 1)
	r := replayTestRecord()
	seek := 3.5
	r.Replay.Command.SeekSeconds = &seek
	if err := enqueuePreviewReplay(queue, previewReplayWrite{"missing-parent", r}); !errors.Is(err, ErrPreviewReplayQueued) {
		t.Fatal(err)
	}
	seek = 99
	if err := enqueuePreviewReplay(queue, previewReplayWrite{"blocked", r}); err == nil || errors.Is(err, ErrPreviewReplayQueued) {
		t.Fatal("full queue did not immediately refuse")
	}
	write := <-queue
	if *write.record.Replay.Command.SeekSeconds != 3.5 {
		t.Fatal("queued mutable values")
	}
}

type startedError struct{ started bool }

func (e *startedError) Error() string                  { return "command failed" }
func (e *startedError) DiagnosticCommandStarted() bool { return e.started }

// Started facts come from typed execution errors or a real ExitError.
func TestPreviewReplayStartedFactsAndTypedCopies(t *testing.T) {
	for _, started := range []bool{false, true} {
		ctx := WithPreviewSource(context.Background(), 3, 4, uuid.NewString(), nil)
		err := WithPreviewFailure(ctx, &startedError{started}, PreviewCommand{Stage: "render"})
		r, ok := PreviewFailureSnapshot(err)
		if !ok || r.Command.Started == nil || *r.Command.Started != started {
			t.Fatal("typed started fact", r.Command.Started, started)
		}
	}
	ctx := WithPreviewSource(context.Background(), 3, 4, "", nil)
	exit := &exec.ExitError{ProcessState: &os.ProcessState{}}
	err := WithPreviewFailure(ctx, exit, PreviewCommand{Stage: "decode"})
	r, ok := PreviewFailureSnapshot(err)
	if !ok || r.Command.Started == nil || !*r.Command.Started {
		t.Fatal("exit error started fact", r.Command.Started)
	}
	if _, ok := PreviewFailureSnapshot(errors.New("plain")); ok {
		t.Fatal("untyped error gave snapshot")
	}
	index, width, audio := 2, 640, true
	command := PreviewCommand{Stage: "filter", StreamIndex: &index, TargetWidth: &width, Audio: &audio,
		PlanID: "intel-scene-preview-v1", RuntimeFingerprint: strings.Repeat("9", 64), VideoSelection: "mapped"}
	err = WithPreviewFailure(ctx, errors.New("filter"), command)
	r, _ = PreviewFailureSnapshot(err)
	index, width, audio = 9, 9, false
	if *r.Command.StreamIndex != 2 || *r.Command.TargetWidth != 640 || *r.Command.Audio != true {
		t.Fatal("typed copies aliased", r.Command)
	}
	if r.Command.PlanID != "intel-scene-preview-v1" || r.Command.RuntimeFingerprint != strings.Repeat("9", 64) || r.Command.VideoSelection != "mapped" {
		t.Fatal("allowed facts dropped", r.Command)
	}
}

func TestPreviewReplayStoreRejectsInvalidAndUnmarshalableRecords(t *testing.T) {
	r := replayTestRecord()
	r.Replay.Reference = "not-a-uuid"
	if err := RetainPreviewReplay(filepath.Join(t.TempDir(), "dir"), r); err == nil {
		t.Fatal("invalid reference retained")
	}
	r = replayTestRecord()
	r.Replay.SourceReference = ""
	if err := RetainPreviewReplay(filepath.Join(t.TempDir(), "dir"), r); err == nil {
		t.Fatal("missing source reference retained")
	}
	r = replayTestRecord()
	r.RecordedAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := RetainPreviewReplay(filepath.Join(t.TempDir(), "dir"), r); err == nil {
		t.Fatal("unmarshalable record retained")
	}
	if _, err := FindPreviewReplay(filepath.Join(t.TempDir(), "dir"), "not-a-uuid"); err == nil {
		t.Fatal("invalid lookup reference accepted")
	}
	if err := enqueuePreviewReplay(make(chan previewReplayWrite, 1), previewReplayWrite{"d", replayTestRecord()}); !errors.Is(err, ErrPreviewReplayQueued) {
		t.Fatal("baseline enqueue", err)
	}
	bad := replayTestRecord()
	bad.Replay.SourceReference = "raw"
	if err := enqueuePreviewReplay(make(chan previewReplayWrite, 1), previewReplayWrite{"d", bad}); err == nil || errors.Is(err, ErrPreviewReplayQueued) {
		t.Fatal("invalid record queued", err)
	}
	bad = replayTestRecord()
	bad.RecordedAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := enqueuePreviewReplay(make(chan previewReplayWrite, 1), previewReplayWrite{"d", bad}); err == nil || errors.Is(err, ErrPreviewReplayQueued) {
		t.Fatal("unmarshalable record queued", err)
	}
}

func TestPreviewReplayStoreRejectsUnreachableAndOpaquePaths(t *testing.T) {
	if _, err := FindPreviewReplay(filepath.Join(t.TempDir(), "never-created"), uuid.NewString()); err == nil {
		t.Fatal("missing directory resolved")
	}
	// A regular file as a path component makes mkdir fail with ENOTDIR.
	file := filepath.Join(t.TempDir(), "regular")
	os.WriteFile(file, []byte("x"), 0600)
	if err := RetainPreviewReplay(filepath.Join(file, "dir"), replayTestRecord()); err == nil {
		t.Fatal("file-component dir created")
	}
	empty := filepath.Join(t.TempDir(), "preview-replay")
	os.Mkdir(empty, 0700)
	if _, err := FindPreviewReplay(empty, uuid.NewString()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("empty store", err)
	}
	if os.Geteuid() == 0 {
		t.Skip("root bypasses permission checks")
	}
	sealed := filepath.Join(t.TempDir(), "preview-replay")
	os.Mkdir(sealed, 0700)
	sealedFile := filepath.Join(sealed, "current.jsonl")
	os.WriteFile(sealedFile, []byte("{}\n"), 0600)
	os.Chmod(sealedFile, 0000)
	if _, err := FindPreviewReplay(sealed, uuid.NewString()); err == nil {
		t.Fatal("unreadable file read")
	}
	os.Chmod(sealed, 0000)
	defer os.Chmod(sealed, 0700)
	if err := RetainPreviewReplay(sealed, replayTestRecord()); err == nil {
		t.Fatal("unreadable directory retained")
	}
	if _, err := FindPreviewReplay(sealed, uuid.NewString()); err == nil {
		t.Fatal("unreadable directory resolved")
	}
}

// Rotation failures: an unwritable directory refuses rename; a corrupt
// previous file refuses eviction; blank lines never hide a stored record.
func TestPreviewReplayStoreRotationFailuresAndBlankLines(t *testing.T) {
	recordSize := func() int {
		data, err := json.Marshal(replayTestRecord())
		if err != nil {
			t.Fatal(err)
		}
		return len(data) + 1
	}()
	full := PreviewReplayFileBytes - recordSize + 1
	if os.Geteuid() != 0 {
		dir := filepath.Join(t.TempDir(), "preview-replay")
		os.Mkdir(dir, 0700)
		os.WriteFile(filepath.Join(dir, "current.jsonl"), append(bytes.Repeat([]byte{'x'}, full), '\n'), 0600)
		os.Chmod(dir, 0500)
		defer os.Chmod(dir, 0700)
		if err := RetainPreviewReplay(dir, replayTestRecord()); err == nil {
			t.Fatal("rotation allowed without directory write")
		}
	}
	dir := filepath.Join(t.TempDir(), "preview-replay")
	os.Mkdir(dir, 0700)
	os.WriteFile(filepath.Join(dir, "current.jsonl"), append(bytes.Repeat([]byte{'x'}, full), '\n'), 0600)
	os.Mkdir(filepath.Join(dir, "previous.jsonl"), 0700)
	if err := RetainPreviewReplay(dir, replayTestRecord()); err == nil {
		t.Fatal("corrupt previous evicted")
	}
	dir = filepath.Join(t.TempDir(), "preview-replay")
	if err := RetainPreviewReplay(dir, replayTestRecord()); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "current.jsonl")
	data, _ := os.ReadFile(path)
	os.WriteFile(path, append([]byte{'\n'}, data...), 0600)
	r := replayTestRecord()
	if err := RetainPreviewReplay(dir, r); err != nil {
		t.Fatal(err)
	}
	if found, err := FindPreviewReplay(dir, r.Replay.Reference); err != nil || found.FileID != 29 {
		t.Fatal("blank lines hid record", found, err)
	}
}

// The shared worker retains queued writes and rate-limits failure warnings.
func TestPreviewReplayQueueWorkerRetainsAndWarns(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "preview-replay")
	r := replayTestRecord()
	if err := QueuePreviewReplay(dir, r); !errors.Is(err, ErrPreviewReplayQueued) {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		found, err := FindPreviewReplay(dir, r.Replay.Reference)
		if err == nil {
			if !found.Replay.Retained || found.Replay.Retention != "retained" {
				t.Fatal("worker retained facts", found.Replay)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("worker never retained queued write", err)
		}
		time.Sleep(time.Millisecond)
	}
	previewReplayLastWarning.Store(0)
	blocked := filepath.Join(t.TempDir(), "blocked")
	if err := os.WriteFile(blocked, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := QueuePreviewReplay(blocked, replayTestRecord()); !errors.Is(err, ErrPreviewReplayQueued) {
		t.Fatal(err)
	}
	deadline = time.Now().Add(10 * time.Second)
	for previewReplayLastWarning.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("worker never warned on retention failure")
		}
		time.Sleep(time.Millisecond)
	}
	warned := previewReplayLastWarning.Load()
	if err := QueuePreviewReplay(blocked, replayTestRecord()); !errors.Is(err, ErrPreviewReplayQueued) {
		t.Fatal(err)
	}
	deadline = time.Now().Add(2 * time.Second)
	for previewReplayLastWarning.Load() == warned {
		if time.Now().After(deadline) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("rate limit allowed a second warning")
}

func TestPreviewReplaySourcePresenceAndRequestPassthrough(t *testing.T) {
	if HasPreviewSource(context.Background()) {
		t.Fatal("source without binding")
	}
	bound := WithPreviewSource(context.Background(), 1, 2, "", nil)
	if !HasPreviewSource(bound) {
		t.Fatal("source binding lost")
	}
	if ctx := WithPreviewRequest(context.Background(), PreviewRequest{Segments: 5}); HasPreviewSource(ctx) {
		t.Fatal("request acquired source")
	}
}

// Directory execute permission alone distinguishes open from lookup.
func TestPreviewReplayStoreRejectsUnsearchableDirectoryAndSealedRotation(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses permission checks")
	}
	dir := filepath.Join(t.TempDir(), "preview-replay")
	os.Mkdir(dir, 0400)
	defer os.Chmod(dir, 0700)
	if _, err := FindPreviewReplay(dir, uuid.NewString()); err == nil {
		t.Fatal("unsearchable directory resolved")
	}
	recordSize := func() int {
		data, err := json.Marshal(replayTestRecord())
		if err != nil {
			t.Fatal(err)
		}
		return len(data) + 1
	}()
	full := PreviewReplayFileBytes - recordSize + 1
	sealed := filepath.Join(t.TempDir(), "preview-replay")
	os.Mkdir(sealed, 0700)
	os.WriteFile(filepath.Join(sealed, "current.jsonl"), append(bytes.Repeat([]byte{'x'}, full), '\n'), 0600)
	os.WriteFile(filepath.Join(sealed, "previous.jsonl"), []byte("old\n"), 0600)
	os.Chmod(sealed, 0500)
	defer os.Chmod(sealed, 0700)
	if err := RetainPreviewReplay(sealed, replayTestRecord()); err == nil {
		t.Fatal("rotation removed previous without directory write")
	}
}
