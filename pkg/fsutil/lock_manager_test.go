package fsutil

import (
	"context"
	"os/exec"
	"runtime"
	"testing"
	"time"
)

func TestTrackedReadLockRetainsCleanupAfterCallerCancellation(t *testing.T) {
	manager := NewReadLockManager()
	ctx, cancel := context.WithCancel(context.Background())
	workDone := make(chan struct{})
	lock := manager.ReadLockWithCompletion(ctx, "synthetic.mp4", workDone)
	cancel()
	<-lock.Done()
	deleted := make(chan struct{})
	go func() { manager.Cancel("synthetic.mp4"); close(deleted) }()
	select {
	case <-deleted:
		t.Error("cancelled read lock disappeared before owner cleanup")
	case <-time.After(20 * time.Millisecond):
	}
	close(workDone)
	select {
	case <-deleted:
	case <-time.After(time.Second):
		t.Fatal("finished owner retained deletion lock")
	}
	lock.Cancel()
}

func TestCommandCompletionWaitHasSingleOwner(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix command fixture")
	}
	manager := NewReadLockManager()
	lock := manager.ReadLock(context.Background(), "synthetic.mp4")
	ownerDone := make(chan struct{})
	cmd := exec.CommandContext(lock, "/usr/bin/true")
	lock.AttachCommandWithCompletion(cmd, ownerDone)
	if err := cmd.Run(); err != nil {
		close(ownerDone)
		lock.Cancel()
		t.Fatal(err)
	}
	deleted := make(chan struct{})
	go func() { manager.Cancel("synthetic.mp4"); close(deleted) }()
	select {
	case <-deleted:
		t.Error("deletion bypassed owner completion by calling Wait again")
	case <-time.After(20 * time.Millisecond):
	}
	close(ownerDone)
	select {
	case <-deleted:
	case <-time.After(time.Second):
		t.Fatal("completed command retained deletion lock")
	}
	lock.Cancel()
}

func TestTrackedParentCancellationDoesNotWaitOnItsChild(t *testing.T) {
	manager := NewReadLockManager()
	workDone := make(chan struct{})
	parent := manager.ReadLockWithCompletion(context.Background(), "synthetic.mp4", workDone)
	child := manager.ReadLock(parent, "synthetic.mp4")
	done := make(chan struct{})
	go func() { child.Cancel(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		close(workDone)
		t.Fatal("child cancellation waited on its enclosing operation")
	}
	if parent.Err() != context.Canceled {
		t.Error("child cancellation failed to signal its parent")
	}
	close(workDone)
	parent.Cancel()
}

func TestCommandCancellationBeforeStartWaitsForOwner(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix command fixture")
	}
	manager := NewReadLockManager()
	lock := manager.ReadLock(context.Background(), "synthetic.mp4")
	ownerDone := make(chan struct{})
	cmd := exec.CommandContext(lock, "/usr/bin/true")
	lock.AttachCommandWithCompletion(cmd, ownerDone)
	deleted := make(chan struct{})
	go func() { manager.Cancel("synthetic.mp4"); close(deleted) }()
	select {
	case <-lock.Done():
	case <-time.After(time.Second):
		t.Fatal("deletion did not signal pending command")
	}
	if err := cmd.Start(); err != context.Canceled {
		t.Errorf("pending Start error = %v", err)
	}
	select {
	case <-deleted:
		t.Error("deletion bypassed failed Start cleanup")
	default:
	}
	close(ownerDone)
	select {
	case <-deleted:
	case <-time.After(time.Second):
		t.Fatal("failed Start owner retained deletion lock")
	}
	lock.Cancel()
}
