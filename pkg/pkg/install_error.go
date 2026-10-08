package pkg

import "github.com/stashapp/stash/pkg/python"

// InstallError retains the failing phase and Go stack. A dependency failure can
// leave package files installed; callers must refresh state even on failure.
type InstallError struct {
	Stage string
	Err   error
	Stack []uintptr
}

func (e *InstallError) Error() string { return python.SanitizeDiagnostic(e.Err.Error(), nil) }
func (e *InstallError) Unwrap() error { return e.Err }
