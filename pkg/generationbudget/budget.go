// Package generationbudget limits concurrent media generation work. Permits
// belong to individual execution stages, never to a scene or marker coordinator:
// a parent holding a permit while waiting for children can deadlock a budget of 1.
package generationbudget

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
)

// Settings are opt-in. Zero means conservative automatic sizing (one), not
// unlimited work. Negative and excessively large values are rejected.
type Settings struct {
	MaxProcesses    int
	MaxGPUProcesses int
	Threads         int
}

func (s Settings) Normalize() (Settings, error) {
	for _, entry := range []struct {
		name  string
		value *int
	}{
		{"max processes", &s.MaxProcesses}, {"max GPU processes", &s.MaxGPUProcesses}, {"threads", &s.Threads},
	} {
		if *entry.value < 0 || *entry.value > 64 {
			return Settings{}, fmt.Errorf("generation budget %s must be between 0 (auto) and 64", entry.name)
		}
		if *entry.value == 0 {
			*entry.value = 1
		}
	}
	if s.MaxGPUProcesses > s.MaxProcesses {
		return Settings{}, errors.New("generation budget GPU processes cannot exceed total processes")
	}
	return s, nil
}

type Class uint8

const (
	CPU Class = iota
	GPU
)

type waiter struct {
	class   Class
	ready   chan struct{}
	granted bool
}

// Budget atomically allocates total and GPU slots, avoiding lock-order deadlocks.
// FIFO admission prevents continuous CPU traffic from starving GPU work (and
// vice versa). All GPU work consumes a total slot as it also uses CPU resources.
type Budget struct {
	settings          Settings
	mu                sync.Mutex
	active, gpuActive int
	queue             []*waiter
}

func New(settings Settings) (*Budget, error) {
	normalized, err := settings.Normalize()
	if err != nil {
		return nil, err
	}
	return &Budget{settings: normalized}, nil
}

func (b *Budget) Settings() Settings { return b.settings }

// Acquire waits cancellably for a leaf execution stage. Release is idempotent
// and must be deferred immediately, covering success, failure and cancellation.
// A nil budget preserves the legacy unbounded admission behavior.
func (b *Budget) Acquire(ctx context.Context, class Class) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if b == nil {
		return func() {}, nil
	}
	if class != CPU && class != GPU {
		return nil, errors.New("invalid generation budget class")
	}
	if ctx.Value(scopeKey{}) == b {
		return nil, errors.New("nested generation budget acquisition; acquire only at leaf stages")
	}
	w := &waiter{class: class, ready: make(chan struct{})}
	b.mu.Lock()
	b.queue = append(b.queue, w)
	b.dispatch()
	b.mu.Unlock()
	select {
	case <-w.ready:
	case <-ctx.Done():
		b.mu.Lock()
		if !w.granted {
			for i, pending := range b.queue {
				if pending == w {
					b.queue = append(b.queue[:i], b.queue[i+1:]...)
					break
				}
			}
		} else {
			b.finish(class)
		}
		b.dispatch()
		b.mu.Unlock()
		return nil, ctx.Err()
	}
	var once sync.Once
	release := func() {
		once.Do(func() {
			b.mu.Lock()
			defer b.mu.Unlock()
			b.finish(class)
			b.dispatch()
		})
	}
	// Cancellation racing with admission must not launch an already cancelled job.
	if err := ctx.Err(); err != nil {
		release()
		return nil, err
	}
	return release, nil
}

func (b *Budget) finish(class Class) {
	b.active--
	if class == GPU {
		b.gpuActive--
	}
}

func (b *Budget) dispatch() {
	for len(b.queue) > 0 {
		w := b.queue[0]
		if b.active >= b.settings.MaxProcesses || (w.class == GPU && b.gpuActive >= b.settings.MaxGPUProcesses) {
			return
		}
		b.queue = b.queue[1:]
		b.active++
		if w.class == GPU {
			b.gpuActive++
		}
		w.granted = true
		close(w.ready)
	}
}

type scopeKey struct{}

// Run wraps a single stage and rejects recursive Run/Acquire instead of hanging.
// Coordinators must call Run separately for each hardware or fallback attempt.
func (b *Budget) Run(ctx context.Context, class Class, fn func(context.Context) error) error {
	release, err := b.Acquire(ctx, class)
	if err != nil {
		return err
	}
	defer release()
	if b != nil {
		ctx = context.WithValue(ctx, scopeKey{}, b)
	}
	return fn(ctx)
}

// ClassifyFFMpeg includes hardware decode as well as encode and scaling. This is
// admission accounting, not a claim that the device actually performed work.
func ClassifyFFMpeg(args []string) Class {
	for i := 0; i+1 < len(args); i++ {
		option, value := args[i], args[i+1]
		if option != "-hwaccel" && option != "-init_hw_device" && option != "-c:v" && option != "-codec:v" && option != "-vcodec" && option != "-vf" && option != "-filter:v" && option != "-filter_complex" {
			continue
		}
		if strings.Contains(value, "qsv") || strings.Contains(value, "vaapi") || strings.Contains(value, "cuda") || strings.Contains(value, "_nvenc") || strings.Contains(value, "_amf") || strings.Contains(value, "videotoolbox") {
			return GPU
		}
	}
	return CPU
}

// FFMpegArgs bounds decoder, encoder and filter thread requests for generation.
// A thread limit is not an OS CPU quota: codecs may use internal helper threads.
// The concurrency budget remains shared even for software-only/fallback work.
// Generation commands have a single final output; playback commands are excluded.
func (b *Budget) FFMpegArgs(args []string) []string {
	if b == nil || len(args) == 0 {
		return args
	}
	threads := strconv.Itoa(b.settings.Threads)
	out := []string{"-filter_threads", threads, "-filter_complex_threads", threads}
	for _, arg := range args {
		// Thread options apply to the next input: repeat for each decoder.
		if arg == "-i" {
			out = append(out, "-threads", threads)
		}
		out = append(out, arg)
	}
	for i := 0; i+1 < len(out); i++ {
		if out[i] == "-threads" || strings.HasPrefix(out[i], "-threads:") || out[i] == "-filter_threads" || out[i] == "-filter_complex_threads" {
			out[i+1] = threads
			i++
		}
	}
	// Also set output codec threading when the original command left it on auto.
	last := out[len(out)-1]
	out = append(out[:len(out)-1], "-threads", threads, last)
	return out
}
