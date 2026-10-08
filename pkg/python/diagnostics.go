package python

import (
	"fmt"
	"regexp"
	"runtime"
	"strings"
	"unicode/utf8"
)

const MaxDiagnosticBytes = 4096

// CommandError keeps structured process diagnostics without serializing command
// arguments. Err remains wrapped for errors.As/Is (including exec.ExitError).
// Output is redacted and bounded before logging or job-status formatting.
type CommandError struct {
	Stage  string
	Module string
	Err    error
	Output string
	Stack  []uintptr
}

func (e *CommandError) Error() string {
	return fmt.Sprintf("%s failed: %s\nOutput: %s", e.Stage, SanitizeDiagnostic(e.Err.Error(), nil), e.Output)
}
func (e *CommandError) Unwrap() error { return e.Err }

func newCommandError(stage string, err error, output []byte, private []string) *CommandError {
	stack := make([]uintptr, 32)
	n := runtime.Callers(2, stack)
	return &CommandError{Stage: stage, Err: err, Output: SanitizeDiagnostic(string(output), private), Stack: stack[:n]}
}

// An unquoted credential's end is ambiguous: spaces and semicolons can be part
// of the secret. Omit the remainder of that record, leaving other lines intact.
var diagnosticCredentials = regexp.MustCompile(`(?i)(?:["']?(?:password|passwd|pwd|token|api[_-]?key|access[_-]?token|refresh[_-]?token|session[_-]?token|client[_-]?secret|(?:aws[_-]?)?secret[_-]?access[_-]?key|secret|credential|signature|x-amz-signature)["']?[ \t]*[:=][ \t]*(?:"[^"\r\n]*"|'[^'\r\n]*'|[^\r\n]*))`)
var diagnosticHeaders = regexp.MustCompile(`(?im)\b(?:authorization|proxy-authorization|cookie|set-cookie)["']?[ \t]*[:=][ \t]*[^\r\n]*`)
var diagnosticBearer = regexp.MustCompile(`(?i)\b(?:bearer|basic)\s+[A-Za-z0-9_./+=-]+`)
var diagnosticTokens = regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9_]+|github_pat_[A-Za-z0-9_]+|(?:sk|phx|phc)[_-][A-Za-z0-9_-]+|(?:AKIA|ASIA)[A-Z0-9]{16}|eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+)\b`)
var diagnosticPrivateKey = regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?(?:-----END [A-Z ]*PRIVATE KEY-----|$)`)
var diagnosticURL = regexp.MustCompile(`(?i)\b(?:https?|ftp|file|git\+https?|ssh)://[^\s'"<>]+`)
var diagnosticEmail = regexp.MustCompile(`\b[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}\b`)
var diagnosticMediaFilename = regexp.MustCompile(`(?i)(?:[\p{L}\p{M}\p{N}\p{S}_. -]+\.(?:mp4|mkv|avi|mov|webm|jpg|jpeg|png|webp|vtt|m3u8|ts|ffconcat|funscript))\b`)
var diagnosticQuotedPath = regexp.MustCompile(`(?:"(?:[A-Za-z]:[\\/]|/|\\\\)[^"]*"|'(?:[A-Za-z]:[\\/]|/|\\\\)[^']*')`)
var diagnosticPath = regexp.MustCompile(`(^|[\s=:"'(\[])((?:[A-Za-z]:[\\/]|\\\\|/))`)
var diagnosticOSCause = regexp.MustCompile(`(?i): (?:permission denied|no such file or directory|input/output error|invalid argument|operation not permitted|read-only file system|no space left on device|file exists|is a directory|not a directory)[.!]?$`)

// SanitizeDiagnostic preserves unfamiliar pip/OS diagnostic text. It removes
// credential forms and private locations rather than allowlisting messages.
// Redaction precedes truncation so credentials cannot survive as cut fragments.
func SanitizeDiagnostic(message string, private []string) string {
	for _, value := range private {
		// Plain requirement/module names are diagnostic context, not private paths.
		if strings.ContainsAny(value, "/\\") {
			message = strings.ReplaceAll(message, value, "[location redacted]")
		}
	}
	message = diagnosticPrivateKey.ReplaceAllString(message, "[credential redacted]")
	message = diagnosticHeaders.ReplaceAllString(message, "[credential redacted]")
	message = diagnosticURL.ReplaceAllString(message, "[URL redacted]")
	message = diagnosticCredentials.ReplaceAllString(message, "[credential redacted]")
	message = diagnosticBearer.ReplaceAllString(message, "[credential redacted]")
	message = diagnosticTokens.ReplaceAllString(message, "[credential redacted]")
	message = diagnosticEmail.ReplaceAllString(message, "[identity redacted]")
	message = diagnosticMediaFilename.ReplaceAllString(message, "[media filename redacted]")
	message = diagnosticQuotedPath.ReplaceAllString(message, "[path redacted]")
	lines := strings.Split(message, "\n")
	for i, line := range lines {
		if match := diagnosticPath.FindStringSubmatchIndex(line); match != nil {
			start := match[4]
			// Unquoted paths can contain spaces. Retain a terminal OS cause while
			// omitting the ambiguous remainder of that one record.
			suffix := ""
			if span := diagnosticOSCause.FindStringIndex(line[start:]); span != nil {
				suffix = line[start+span[0]:]
			}
			lines[i] = line[:start] + "[path redacted]" + suffix
		}
	}
	message = strings.TrimSpace(strings.ToValidUTF8(strings.Join(lines, "\n"), "?"))
	if len(message) > MaxDiagnosticBytes {
		const marker = "[truncated]\n"
		message = message[len(message)-(MaxDiagnosticBytes-len(marker)):]
		for len(message) > 0 && !utf8.RuneStart(message[0]) {
			message = message[1:]
		}
		message = marker + message
	}
	return message
}
