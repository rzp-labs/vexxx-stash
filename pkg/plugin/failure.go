package plugin

import (
	"bytes"
	"errors"
	"reflect"

	"github.com/stashapp/stash/pkg/diagnostics"
	"github.com/stashapp/stash/pkg/logger"
	"github.com/stashapp/stash/pkg/plugin/common"
)

const maxPluginDiagnosticBytes = 16 * 1024

// ExecutionError preserves process identity and its final diagnostic without argv or plugin input.
type ExecutionError struct {
	PluginID, Operation, Hook string
	Err                       error
	Message, Output           string
	OutputOmittedBytes        int
	safeCause                 string
}

func (e *ExecutionError) Summary() string {
	cause := e.safeCause
	if cause == "" {
		cause = diagnostics.Summary(e.Err.Error(), diagnostics.ErrorPrivate(e.Err), 4096).Value
	}
	text := "plugin execution failed: " + cause
	if e.Message != "" && e.Message != e.Err.Error() {
		text += ": " + e.Message
	}
	return diagnostics.Summary(text, diagnostics.ErrorPrivate(e.Err), 4096).Value
}
func (e *ExecutionError) Error() string {
	text := e.Summary()
	if e.Output != "" {
		text += "\nOutput: " + e.Output
	}
	return text
}
func (e *ExecutionError) Unwrap() error            { return e.Err }
func (e *ExecutionError) DiagnosticOutput() string { return e.Output }

func (t *pluginTask) complete(result *common.PluginOutput, cause error, stderr *stderrTail) {
	if result == nil {
		result = &common.PluginOutput{}
	}
	if cause != nil || result.Error != nil {
		private, inspected := t.privateValues()
		message := ""
		if result.Error != nil {
			message = diagnostics.Summary(*result.Error, private, 4096).Value
		}
		if cause == nil {
			cause = errors.New(message)
		}
		failure := &ExecutionError{PluginID: t.plugin.id, Operation: t.kind, Hook: t.hook, Err: cause, Message: message}
		if stderr != nil {
			retained, omitted := stderr.diagnostic()
			output := diagnostics.Sanitize(string(retained), private, maxPluginDiagnosticBytes)
			failure.Output, failure.OutputOmittedBytes = output.Value, omitted+output.OmittedBytes
		}
		if !inspected {
			failure.Message, failure.Output = "[plugin diagnostics omitted: private input exceeds inspection budget]", ""
			if result.Error != nil && cause.Error() == message {
				failure.Err = errors.New("plugin reported an error")
			}
		}
		// Also sanitize typed causes (JS/RPC errors can echo request values).
		failure.safeCause = diagnostics.Summary(failure.Err.Error(), private, 4096).Value
		if !inspected {
			failure.safeCause = "plugin execution failed"
		}
		result.Failure = failure
		text := failure.Error()
		result.Error = &text
		if t.onError != nil {
			func() {
				defer func() {
					if recover() != nil {
						logger.Warn("plugin error observer failed")
					}
				}()
				t.onError(t.ctx, failure)
			}()
		}
	}
	t.result = result
}

// Values are for exact redaction only; a bounded, incomplete traversal fails closed.
func (t *pluginTask) privateValues() ([]string, bool) {
	private := append([]string(nil), diagnostics.Private(t.ctx)...)
	private = append(private, t.plugin.path, t.input.ServerConnection.Dir, t.input.ServerConnection.PluginDir)
	if cookie := t.input.ServerConnection.SessionCookie; cookie != nil {
		private = append(private, cookie.Value)
	}
	nodes, size, complete := 0, 0, true
	var visit func(reflect.Value, int)
	visit = func(v reflect.Value, depth int) {
		nodes++
		if nodes > 4096 || size > 128*1024 || depth > 16 {
			complete = false
			return
		}
		if !v.IsValid() {
			return
		}
		// Kind accessors inspect unexported fields without calling Interface.
		// Custom JSON serializers and JS methods may expose these stored strings.
		switch v.Kind() {
		case reflect.String:
			private = append(private, v.String())
			size += v.Len()
		case reflect.Interface, reflect.Pointer:
			if !v.IsNil() {
				visit(v.Elem(), depth+1)
			}
		case reflect.Map:
			iter := v.MapRange()
			for iter.Next() && complete {
				visit(iter.Value(), depth+1)
			}
		case reflect.Slice, reflect.Array:
			for i := 0; i < v.Len() && complete; i++ {
				visit(v.Index(i), depth+1)
			}
		case reflect.Struct:
			for i := 0; i < v.NumField() && complete; i++ {
				visit(v.Field(i), depth+1)
			}
		}
	}
	visit(reflect.ValueOf(t.input.Args), 0)
	if size > 128*1024 {
		complete = false
	}
	return private, complete
}

// Keep complete stderr records so trimming cannot expose a credential's torn suffix.
type stderrTail struct {
	data     []byte
	omitted  int
	dropping bool
	// Remember PEM context in discarded input, including delimiters split across writes.
	insidePEM    bool
	boundary     [11]byte
	boundarySize int
}

func (b *stderrTail) discard(p []byte) {
	b.omitted += len(p)
	for _, c := range p {
		if b.boundarySize == len(b.boundary) {
			copy(b.boundary[:], b.boundary[1:])
			b.boundarySize--
		}
		b.boundary[b.boundarySize] = c
		b.boundarySize++
		window := b.boundary[:b.boundarySize]
		if bytes.HasSuffix(window, []byte("-----BEGIN ")) {
			b.insidePEM = true
		} else if bytes.HasSuffix(window, []byte("-----END ")) {
			b.insidePEM = false
		}
	}
}

func (b *stderrTail) diagnostic() ([]byte, int) {
	if !b.insidePEM {
		return b.data, b.omitted
	}
	// An opening delimiter was evicted. Omit its surviving body and closing
	// record while keeping diagnostics after the block. Incomplete blocks fail closed.
	end := bytes.Index(b.data, []byte("-----END "))
	if end >= 0 {
		if newline := bytes.IndexByte(b.data[end:], '\n'); newline >= 0 {
			end += newline + 1
			return b.data[end:], b.omitted + end
		}
	}
	return nil, b.omitted + len(b.data)
}

func (b *stderrTail) Write(p []byte) (int, error) {
	n := len(p)
	if b.dropping {
		end := bytes.IndexByte(p, '\n')
		if end < 0 {
			b.discard(p)
			return n, nil
		}
		b.discard(p[:end+1])
		p, b.dropping = p[end+1:], false
	}
	for len(p) > 0 {
		part := min(len(p), 4096)
		b.data = append(b.data, p[:part]...)
		p = p[part:]
		for len(b.data) > maxPluginDiagnosticBytes {
			end := bytes.IndexByte(b.data, '\n')
			if end < 0 {
				b.discard(b.data)
				b.data, b.dropping = nil, true
				break
			}
			b.discard(b.data[:end+1])
			b.data = b.data[end+1:]
		}
		if b.dropping && len(p) > 0 {
			_, _ = b.Write(p)
			break
		}
	}
	return n, nil
}
