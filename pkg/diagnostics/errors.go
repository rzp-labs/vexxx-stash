package diagnostics

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"sync"
)

// Entry keeps a cause separate from process output; Err retains As/Is semantics.
type Entry struct {
	Err           error
	Cause, Output string
}
type Failures struct {
	Entries        []Entry
	Count, Omitted int
}

// Split preserves ordinary wrapper context around joins without duplicating output.
// Limits constrain pathological error trees, not the application's execution.
func Split(err error) Failures {
	result := Failures{}
	var visit func(error, string, string, int)
	visit = func(e error, prefix, suffix string, depth int) {
		if e == nil {
			return
		}
		if depth > 128 {
			result.Omitted++
			return
		}
		if joined, ok := e.(interface{ Unwrap() []error }); ok {
			for _, child := range joined.Unwrap() {
				visit(child, prefix, suffix, depth+1)
			}
			return
		}
		if child := errors.Unwrap(e); child != nil && containsJoin(child) {
			p, s, ok := strings.Cut(e.Error(), child.Error())
			if transparent, yes := e.(interface{ DiagnosticTransparent() bool }); yes && transparent.DiagnosticTransparent() {
				p, s, ok = "", "", true
			}
			if !ok {
				p = e.Error() + ": "
				s = ""
			}
			visit(child, prefix+p, s+suffix, depth+1)
			return
		}
		if errors.Is(e, context.Canceled) {
			return
		}
		result.Count++
		if len(result.Entries) >= 64 {
			result.Omitted++
			return
		}
		cause, output := summary(e, 0)
		result.Entries = append(result.Entries, Entry{Err: e, Cause: prefix + cause + suffix, Output: output})
	}
	visit(err, "", "", 0)
	return result
}
func containsJoin(e error) bool {
	for i := 0; e != nil && i < 128; i++ {
		if _, ok := e.(interface{ Unwrap() []error }); ok {
			return true
		}
		e = errors.Unwrap(e)
	}
	return false
}
func summary(e error, depth int) (string, string) {
	if e == nil {
		return "", ""
	}
	if depth > 128 {
		return "[cause depth omitted]", ""
	}
	if d, ok := e.(interface {
		Summary() string
		DiagnosticOutput() string
	}); ok {
		return d.Summary(), d.DiagnosticOutput()
	}
	if child := errors.Unwrap(e); child != nil {
		p, s, ok := strings.Cut(e.Error(), child.Error())
		if transparent, yes := e.(interface{ DiagnosticTransparent() bool }); yes && transparent.DiagnosticTransparent() {
			p, s, ok = "", "", true
		}
		c, o := summary(child, depth+1)
		if ok {
			return p + c + s, o
		}
	}
	return e.Error(), ""
}

type stateKey struct{}
type State struct {
	mu   sync.Mutex
	seen map[error]bool
}

// WithState scopes deduplication to one job or request, and releases it with that context.
func WithState(ctx context.Context) context.Context {
	return context.WithValue(ctx, stateKey{}, &State{seen: map[error]bool{}})
}
func identities(e error) []error {
	var ids []error
	for depth := 0; e != nil && depth < 128; depth++ {
		if reflect.TypeOf(e).Kind() == reflect.Pointer {
			ids = append(ids, e)
		}
		e = errors.Unwrap(e)
	}
	return ids
}

// Register pointer occurrences and their contextual wrappers, not terminal
// shared causes (for example syscall.ENOENT or an errors.New sentinel). Lookup
// may descend through wrappers to find an occurrence accepted at an earlier boundary.
func occurrenceIdentities(e error) []error {
	var ids []error
	for depth := 0; e != nil && depth < 128; depth++ {
		child := errors.Unwrap(e)
		if reflect.TypeOf(e).Kind() == reflect.Pointer && (depth == 0 || child != nil) {
			ids = append(ids, e)
		}
		e = child
	}
	return ids
}

func Reported(ctx context.Context, e error) bool {
	s, _ := ctx.Value(stateKey{}).(*State)
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range identities(e) {
		if s.seen[id] {
			return true
		}
	}
	return false
}
func Mark(ctx context.Context, e error) {
	s, _ := ctx.Value(stateKey{}).(*State)
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, entry := range Split(e).Entries {
		for _, id := range occurrenceIdentities(entry.Err) {
			if len(s.seen) < 4096 {
				s.seen[id] = true
			}
		}
	}
}

// Claim reserves a failure across concurrent boundaries. A rejected enqueue
// releases the reservation, so a later observer can try again.
func Claim(ctx context.Context, e error) (bool, func(bool)) {
	s, _ := ctx.Value(stateKey{}).(*State)
	if s == nil {
		return true, func(bool) {}
	}
	ids := occurrenceIdentities(e)
	s.mu.Lock()
	for _, id := range identities(e) {
		if s.seen[id] {
			s.mu.Unlock()
			return false, func(bool) {}
		}
	}
	var added []error
	for _, id := range ids {
		if len(s.seen) < 4096 {
			s.seen[id] = true
			added = append(added, id)
		}
	}
	s.mu.Unlock()
	return true, func(accepted bool) {
		if accepted {
			Mark(ctx, e)
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		for _, id := range added {
			delete(s.seen, id)
		}
	}
}

type privateKey struct{}

func WithPrivate(ctx context.Context, values []string) context.Context {
	return context.WithValue(ctx, privateKey{}, values)
}
func Private(ctx context.Context) []string {
	values, _ := ctx.Value(privateKey{}).([]string)
	return values
}

// Typed filesystem failures supply exact private paths, preserving arbitrary OS
// causes that a plain unquoted path record cannot safely delimit.
func ErrorPrivate(err error) []string {
	var private []string
	for _, entry := range Split(err).Entries {
		var path *fs.PathError
		if errors.As(entry.Err, &path) {
			private = append(private, path.Path)
		}
		var link *os.LinkError
		if errors.As(entry.Err, &link) {
			private = append(private, link.Old, link.New)
		}
		var executable *exec.Error
		if errors.As(entry.Err, &executable) && strings.ContainsAny(executable.Name, "/\\") {
			private = append(private, executable.Name)
		}
	}
	return private
}
