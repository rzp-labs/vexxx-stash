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
	slots   int
	upTo    bool
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
	return b.AcquireN(ctx, class, 1)
}

// AcquireN atomically reserves slots for independent stages sharing a subprocess,
// such as parallel hardware decoders in one resident sprite render. A stage never
// holds a partial reservation while waiting for the rest of its slots.
func (b *Budget) AcquireN(ctx context.Context, class Class, slots int) (func(), error) {
	_, release, err := b.acquire(ctx, class, slots, false)
	return release, err
}

// AcquireUpTo reserves between one and maxSlots in a single admission. At the
// head of the FIFO queue it takes the currently available capacity, allowing an
// adaptive stage to start alongside existing work rather than wait for every
// requested slot. The returned count remains fixed until release.
func (b *Budget) AcquireUpTo(ctx context.Context, class Class, maxSlots int) (int, func(), error) {
	return b.acquire(ctx, class, maxSlots, true)
}

func (b *Budget) acquire(ctx context.Context, class Class, slots int, upTo bool) (int, func(), error) {
	if err := ctx.Err(); err != nil {
		return 0, nil, err
	}
	if class != CPU && class != GPU {
		return 0, nil, errors.New("invalid generation budget class")
	}
	if slots < 1 {
		return 0, nil, errors.New("generation budget reservation must request at least one slot")
	}
	if b == nil {
		return slots, func() {}, nil
	}
	if slots > b.settings.MaxProcesses || (class == GPU && slots > b.settings.MaxGPUProcesses) {
		return 0, nil, errors.New("generation budget reservation exceeds configured limits")
	}
	if ctx.Value(scopeKey{}) == b {
		return 0, nil, errors.New("nested generation budget acquisition; acquire only at leaf stages")
	}
	w := &waiter{class: class, slots: slots, upTo: upTo, ready: make(chan struct{})}
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
			b.finish(class, w.slots)
		}
		b.dispatch()
		b.mu.Unlock()
		return 0, nil, ctx.Err()
	}
	// Closing ready synchronizes the granted count written by dispatch.
	granted := w.slots
	var once sync.Once
	release := func() {
		once.Do(func() {
			b.mu.Lock()
			defer b.mu.Unlock()
			b.finish(class, granted)
			b.dispatch()
		})
	}
	// Cancellation racing with admission must not launch an already cancelled job.
	if err := ctx.Err(); err != nil {
		release()
		return 0, nil, err
	}
	return granted, release, nil
}

func (b *Budget) finish(class Class, slots int) {
	b.active -= slots
	if class == GPU {
		b.gpuActive -= slots
	}
}

func (b *Budget) dispatch() {
	for len(b.queue) > 0 {
		w := b.queue[0]
		available := b.settings.MaxProcesses - b.active
		if w.class == GPU {
			available = min(available, b.settings.MaxGPUProcesses-b.gpuActive)
		}
		if available < 1 || (!w.upTo && w.slots > available) {
			return
		}
		if w.upTo {
			w.slots = min(w.slots, available)
		}
		b.queue = b.queue[1:]
		b.active += w.slots
		if w.class == GPU {
			b.gpuActive += w.slots
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
		// FFmpeg permits stream specifiers, including -c:v:0 and -hwaccel:v:0.
		option, _, _ = strings.Cut(option, ":")
		switch option {
		case "-hwaccel":
			// Auto can select hardware at runtime. Reserve a slot conservatively;
			// an explicit software-only request does not use a hardware slot.
			if value != "none" && value != "" {
				return GPU
			}
		case "-init_hw_device", "-filter_hw_device", "-hwaccel_device", "-vaapi_device":
			if value != "" {
				return GPU
			}
		case "-c", "-codec", "-vcodec":
			for _, suffix := range []string{"_qsv", "_vaapi", "_nvenc", "_cuvid", "_amf", "_videotoolbox", "_rkmpp", "_v4l2m2m", "_omx"} {
				if strings.HasSuffix(value, suffix) {
					return GPU
				}
			}
		case "-vf", "-filter", "-filter_complex", "-lavfi":
			if hasHardwareFilter(value) {
				return GPU
			}
		}
	}
	return CPU
}

// Match filter names, never option text, paths, link labels or instance IDs.
// The token boundaries follow FFmpeg's filtergraph syntax: quotes and escapes
// protect separators within arguments, and brackets surround link labels.
func hasHardwareFilter(graph string) bool {
	skipLinks := func() bool {
		graph = strings.TrimSpace(graph)
		for strings.HasPrefix(graph, "[") {
			_, rest := filterToken(graph[1:], "]")
			if !strings.HasPrefix(rest, "]") {
				return false
			}
			graph = strings.TrimSpace(rest[1:])
		}
		return true
	}
	for graph != "" {
		if !skipLinks() {
			return false
		}
		name, rest := filterToken(graph, "=,;[")
		name, _, _ = strings.Cut(name, "@")
		if name == "hwupload" || name == "hwupload_cuda" || name == "hwdownload" || name == "hwmap" || name == "scale_vt" {
			return true
		}
		for _, suffix := range []string{"_qsv", "_vaapi", "_cuda", "_npp", "_rkrga", "_opencl", "_vulkan", "_d3d11", "_amf"} {
			if strings.HasSuffix(name, suffix) {
				return true
			}
		}
		graph = rest
		if strings.HasPrefix(graph, "=") {
			_, graph = filterToken(graph[1:], "[],;")
		}
		if !skipLinks() || len(graph) == 0 || (graph[0] != ',' && graph[0] != ';') {
			return false
		}
		graph = graph[1:]
	}
	return false
}

// FFmpeg token quoting: backslash protects the next unquoted byte; a single
// quote protects everything until its closing quote (including backslashes).
func filterToken(input, delimiters string) (string, string) {
	input = strings.TrimLeft(input, " \n\t\r")
	var token strings.Builder
	i := 0
	for i < len(input) && !strings.ContainsRune(delimiters, rune(input[i])) {
		c := input[i]
		i++
		if c == '\\' && i < len(input) {
			token.WriteByte(input[i])
			i++
		} else if c == '\'' {
			for i < len(input) && input[i] != '\'' {
				token.WriteByte(input[i])
				i++
			}
			if i < len(input) {
				i++
			}
		} else {
			token.WriteByte(c)
		}
	}
	return strings.TrimSpace(token.String()), input[i:]
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
