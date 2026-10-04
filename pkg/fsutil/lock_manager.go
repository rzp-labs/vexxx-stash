package fsutil

import (
	"context"
	"os/exec"
	"sync"
	"time"
)

type Cancellable interface {
	Cancel()
}

type LockContext struct {
	context.Context
	cancel context.CancelFunc

	commandMutex sync.Mutex
	cmd          *exec.Cmd
	commandDone  <-chan struct{}
	workDone     <-chan struct{}
}

func (c *LockContext) AttachCommand(cmd *exec.Cmd) {
	c.attachCommand(cmd, nil)
}

// AttachCommandWithCompletion registers before Start. The command owner alone
// calls Wait and closes done on every exit, including a failed Start.
func (c *LockContext) AttachCommandWithCompletion(cmd *exec.Cmd, done <-chan struct{}) {
	c.attachCommand(cmd, done)
}

func (c *LockContext) attachCommand(cmd *exec.Cmd, done <-chan struct{}) {
	c.commandMutex.Lock()
	defer c.commandMutex.Unlock()
	c.cmd = cmd
	c.commandDone = done
}

func (c *LockContext) trackedCompletion() <-chan struct{} {
	if c.workDone != nil {
		return c.workDone
	}
	c.commandMutex.Lock()
	defer c.commandMutex.Unlock()
	return c.commandDone
}

func (c *LockContext) Cancel() {
	c.cancel()

	if done := c.trackedCompletion(); done != nil {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
		return
	}

	c.commandMutex.Lock()
	cmd := c.cmd
	c.commandMutex.Unlock()
	if cmd != nil {
		// wait for the process to die before returning
		// don't wait more than a few seconds
		done := make(chan error, 1)
		go func() {
			err := cmd.Wait()
			done <- err
		}()

		select {
		case <-done:
			return
		case <-time.After(5 * time.Second):
			return
		}
	}
}

// ReadLockManager manages read locks on file paths.
type ReadLockManager struct {
	readLocks map[string][]*LockContext
	mutex     sync.RWMutex
}

// NewReadLockManager creates a new ReadLockManager.
func NewReadLockManager() *ReadLockManager {
	return &ReadLockManager{
		readLocks: make(map[string][]*LockContext),
	}
}

// ReadLock adds a pending file read lock for fn to its storage, returning a context and cancel function.
// Per standard WithCancel usage, cancel must be called when the lock is freed.
func (m *ReadLockManager) ReadLock(ctx context.Context, fn string) *LockContext {
	return m.readLock(ctx, fn, nil)
}

// ReadLockWithCompletion retains registration through the operation's cleanup.
// Close done before calling the returned lock's own Cancel, to avoid waiting on
// the calling operation itself. Cancellation keeps the existing five-second wait
// bound; registration remains until the owner really finishes.
func (m *ReadLockManager) ReadLockWithCompletion(ctx context.Context, fn string, done <-chan struct{}) *LockContext {
	return m.readLock(ctx, fn, done)
}

func (m *ReadLockManager) readLock(ctx context.Context, fn string, done <-chan struct{}) *LockContext {
	retCtx, cancel := context.WithCancel(ctx)

	// if Cancellable, call Cancel() when cancelled
	cancellable, ok := ctx.(Cancellable)
	if ok {
		origCancel := cancel
		cancel = func() {
			origCancel()
			if parent, ok := ctx.(*LockContext); ok && parent.workDone != nil {
				// Signal a tracked parent without waiting for the enclosing
				// operation that needs this child to return first.
				parent.cancel()
			} else {
				cancellable.Cancel()
			}
		}
	}

	m.mutex.Lock()
	defer m.mutex.Unlock()

	locks := m.readLocks[fn]

	cc := &LockContext{
		Context:  retCtx,
		cancel:   cancel,
		workDone: done,
	}
	m.readLocks[fn] = append(locks, cc)

	go m.waitAndUnlock(fn, cc)

	return cc
}

func (m *ReadLockManager) waitAndUnlock(fn string, cc *LockContext) {
	<-cc.Done()
	if done := cc.trackedCompletion(); done != nil {
		<-done
	}

	m.mutex.Lock()
	defer m.mutex.Unlock()

	locks := m.readLocks[fn]
	for i, v := range locks {
		if v == cc {
			m.readLocks[fn] = append(locks[:i], locks[i+1:]...)
			return
		}
	}
}

// Cancel cancels all read lock contexts associated with fn.
func (m *ReadLockManager) Cancel(fn string) {
	m.mutex.RLock()
	locks := append([]*LockContext(nil), m.readLocks[fn]...)
	m.mutex.RUnlock()

	for _, l := range locks {
		l.Cancel()
		<-l.Done()
	}
}
