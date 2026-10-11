package diagnostics

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/stashapp/stash/pkg/logger"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

const PreviewReplayFileBytes = 512 * 1024
const PreviewReplayRecordBytes = 16 * 1024

var previewReplayStoreMu sync.Mutex
var previewReplayNames = []string{"current.jsonl", "previous.jsonl"}

var ErrPreviewReplayQueued = errors.New("preview replay retention queued")

type previewReplayWrite struct {
	dir    string
	record PreviewReplayRecord
}

var previewReplayQueue = make(chan previewReplayWrite, 32)
var previewReplayWorker sync.Once
var previewReplayLastWarning atomic.Int64

// QueuePreviewReplay never waits for disk IO. A single worker and a bounded
// queue are shared across requests; overload is explicitly unavailable.
func QueuePreviewReplay(dir string, record PreviewReplayRecord) error {
	previewReplayWorker.Do(func() {
		go func() {
			for write := range previewReplayQueue {
				if err := RetainPreviewReplay(write.dir, write.record); err != nil {
					now, previous := time.Now().Unix(), previewReplayLastWarning.Load()
					if now-previous >= 60 && previewReplayLastWarning.CompareAndSwap(previous, now) {
						logger.Warn("preview replay retention failed; local lookup may be unavailable")
					}
				}
			}
		}()
	})
	return enqueuePreviewReplay(previewReplayQueue, previewReplayWrite{dir, record})
}
func enqueuePreviewReplay(queue chan<- previewReplayWrite, write previewReplayWrite) error {
	write.record.Replay.Reference, write.record.Replay.SourceReference = replayUUID(write.record.Replay.Reference), replayUUID(write.record.Replay.SourceReference)
	if write.record.Replay.Reference == "" || write.record.Replay.SourceReference == "" {
		return errors.New("invalid preview replay reference")
	}
	write.record.Replay.Retention, write.record.Replay.Retained = "queued", false
	write.record.JobCorrelation = replayUUID(write.record.JobCorrelation)
	write.record.Replay.Request, write.record.Replay.Command = write.record.Replay.Request.safe(), write.record.Replay.Command.safe()
	data, err := json.Marshal(write.record)
	if err != nil {
		return err
	}
	if len(data)+1 > PreviewReplayRecordBytes {
		return errors.New("preview replay record exceeds bound")
	}
	select {
	case queue <- write:
		return ErrPreviewReplayQueued
	default:
		return errors.New("preview replay queue full")
	}
}

// Open a private directory through a stable root handle. Files are checked
// before any IO and again after opening, so a replacement cannot redirect IO.
func previewReplayRoot(dir string, create bool) (*os.Root, error) {
	if create {
		// The parent is the existing persistent config volume. Only our final
		// namespace is created; the configured parent is never chmodded.
		if err := os.Mkdir(dir, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return nil, err
		}
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("preview replay directory is not private")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		root.Close()
		return nil, fmt.Errorf("preview replay directory changed")
	}
	return root, nil
}

func previewReplayFile(root *os.Root, name string, write bool) (*os.File, error) {
	before, err := root.Lstat(name)
	missing := errors.Is(err, os.ErrNotExist)
	if err != nil && !missing {
		return nil, err
	}
	if !missing && (!before.Mode().IsRegular() || before.Mode().Perm()&0077 != 0 || before.Size() > PreviewReplayFileBytes) {
		return nil, fmt.Errorf("preview replay file is not bounded and private")
	}
	if missing && !write {
		return nil, os.ErrNotExist
	}
	flags := os.O_RDONLY
	if write {
		flags = os.O_RDWR | os.O_APPEND | os.O_CREATE
		if missing {
			flags |= os.O_EXCL
		}
	}
	f, err := root.OpenFile(name, flags, 0600)
	if err != nil {
		return nil, err
	}
	after, err := f.Stat()
	if err != nil || !after.Mode().IsRegular() || after.Mode().Perm()&0077 != 0 || after.Size() > PreviewReplayFileBytes || (!missing && !os.SameFile(before, after)) {
		f.Close()
		return nil, fmt.Errorf("preview replay file changed")
	}
	// Check the name even on initial creation (O_CREATE never truncates).
	named, err := root.Lstat(name)
	if err != nil || !named.Mode().IsRegular() || !os.SameFile(named, after) {
		f.Close()
		return nil, fmt.Errorf("preview replay file changed")
	}
	return f, nil
}

// RetainPreviewReplay persists only a typed failed-producer snapshot. Two
// bounded JSONL files survive restart; no successful request writes this store.
func RetainPreviewReplay(dir string, record PreviewReplayRecord) error {
	if replayUUID(record.Replay.Reference) == "" || replayUUID(record.Replay.SourceReference) == "" {
		return fmt.Errorf("invalid preview replay reference")
	}
	record.Replay.Request, record.Replay.Command = record.Replay.Request.safe(), record.Replay.Command.safe()
	record.JobCorrelation = replayUUID(record.JobCorrelation)
	record.SceneID, record.FileID = max(record.SceneID, 0), max(record.FileID, 0)
	record.Replay.SourceResolved = record.SceneID > 0 && record.FileID > 0
	record.Replay.Retained = true
	record.Replay.Retention = "retained"
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if len(data) > PreviewReplayRecordBytes {
		return fmt.Errorf("preview replay record exceeds bound")
	}
	previewReplayStoreMu.Lock()
	defer previewReplayStoreMu.Unlock()
	root, err := previewReplayRoot(dir, true)
	if err != nil {
		return err
	}
	defer root.Close()
	f, err := previewReplayFile(root, previewReplayNames[0], true)
	if err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	// Separate an interrupted final write from the next valid record. Lookup
	// can still resolve later records while reporting corruption if absent.
	if info.Size() > 0 {
		last := []byte{0}
		if _, err := f.ReadAt(last, info.Size()-1); err != nil {
			f.Close()
			return err
		}
		if last[0] != '\n' {
			data = append([]byte{'\n'}, data...)
		}
	}
	if info.Size()+int64(len(data)) > PreviewReplayFileBytes {
		if err := f.Close(); err != nil {
			return err
		}
		previous, err := previewReplayFile(root, previewReplayNames[1], false)
		if err == nil {
			if err := previous.Close(); err != nil {
				return err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := root.Remove(previewReplayNames[1]); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := root.Rename(previewReplayNames[0], previewReplayNames[1]); err != nil {
			return err
		}
		f, err = previewReplayFile(root, previewReplayNames[0], true)
		if err != nil {
			return err
		}
	}
	n, err := f.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}

// FindPreviewReplay resolves one opaque execution reference with a bounded
// local read. Missing means expired/not retained; it never scans the library.
func FindPreviewReplay(dir, reference string) (*PreviewReplayRecord, error) {
	if replayUUID(reference) == "" {
		return nil, fmt.Errorf("invalid preview replay reference")
	}
	previewReplayStoreMu.Lock()
	defer previewReplayStoreMu.Unlock()
	root, err := previewReplayRoot(dir, false)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	corrupt := false
	for _, name := range previewReplayNames {
		f, err := previewReplayFile(root, name, false)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		data, readErr := io.ReadAll(io.LimitReader(f, PreviewReplayFileBytes+1))
		closeErr := f.Close()
		if readErr != nil {
			return nil, readErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
		if len(data) > PreviewReplayFileBytes {
			return nil, fmt.Errorf("preview replay file exceeds bound")
		}
		for len(data) > 0 {
			line, rest, _ := bytes.Cut(data, []byte{'\n'})
			data = rest
			if len(line) == 0 {
				continue
			}
			var record PreviewReplayRecord
			if len(line)+1 > PreviewReplayRecordBytes || json.Unmarshal(line, &record) != nil || replayUUID(record.Replay.Reference) == "" || replayUUID(record.Replay.SourceReference) == "" {
				corrupt = true
				continue
			}
			if record.Replay.Reference == reference {
				return &record, nil
			}
		}
	}
	if corrupt {
		return nil, fmt.Errorf("preview replay lookup unavailable: corrupt records")
	}
	return nil, os.ErrNotExist
}
