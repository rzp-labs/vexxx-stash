package diagnostics

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

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
