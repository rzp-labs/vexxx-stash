package python

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestDiagnosticRedactionRetainsUnfamiliarCause(t *testing.T) {
	for _, secret := range []string{
		`password=fixture-secret`, `"access_token": "fixture-secret"`,
		`client_secret='fixture-secret'`, `Authorization: Bearer fixture-secret`,
		`Cookie: session=fixture-secret; other=fixture-secret`,
		`"Cookie": "session=fixture-secret; other=fixture-secret"`, `AWS_SECRET_ACCESS_KEY=fixture-secret`,
		`https://user:fixture-secret@example.invalid/simple?token=fixture-secret`,
		`Bearer fixture-secret`, `ghp_fixturesecret`, `phx_fixturesecret`,
		`-----BEGIN PRIVATE KEY-----` + "\nfixture-secret\n-----END PRIVATE KEY-----",
		`/Users/private person/plugin.py: permission denied`, `C:\Users\private person\plugin.py`,
		`private person.mp4`, `private.person@example.invalid`,
	} {
		text := SanitizeDiagnostic(secret+"\nERROR: wheel-build-sentinel failed for mystery-widget==6", nil)
		for _, private := range []string{"fixture-secret", "ghp_fixturesecret", "phx_fixturesecret", "private person"} {
			if strings.Contains(text, private) {
				t.Fatal("secret/location fixture survived redaction")
			}
		}
		if !strings.Contains(text, "wheel-build-sentinel failed for mystery-widget==6") {
			t.Fatal("unfamiliar diagnostic was discarded")
		}
	}
}

func TestDiagnosticBoundAppliesAfterRedaction(t *testing.T) {
	text := SanitizeDiagnostic(strings.Repeat("界", 5000)+" token="+strings.Repeat("fixture-secret", 500)+"\nERROR: final wheel-build-sentinel", nil)
	if len(text) > MaxDiagnosticBytes || !utf8.ValidString(text) || !strings.Contains(text, "final wheel-build-sentinel") || strings.Contains(text, "fixture-secret") || !strings.HasPrefix(text, "[truncated]") {
		t.Fatal("diagnostic truncation lost cause, leaked a cut secret, or broke its byte bound")
	}
}

func TestReviewedCredentialAndUnicodeFixturesAreRedacted(t *testing.T) {
	for _, test := range []struct {
		name, input string
		private     []string
	}{
		{"unquoted password with spaces", "password=correct horse battery staple", []string{"correct", "horse", "battery", "staple"}},
		{"JSON unquoted password", `"password": correct horse battery staple`, []string{"correct", "horse", "battery", "staple"}},
		{"unquoted password with delimiters", "password=correct horse; battery staple", []string{"correct", "horse", "battery", "staple"}},
		{"Unicode media filename", "'私密录像.mp4'", []string{"私密录像"}},
		{"Unicode symbols and combining marks", "📽私密 Cafe\u0301.mp4", []string{"📽", "私密", "Cafe\u0301"}},
		{"complete cookie header", "Request failed; Cookie: theme=dark; sid=opaque-session-value", []string{"theme=dark", "opaque-session-value"}},
		{"complete set-cookie header", "Set-Cookie: theme=dark; sid=opaque-session-value; Secure", []string{"theme=dark", "opaque-session-value"}},
		{"PEM privateKey context", "privateKey=-----BEGIN PRIVATE KEY-----\nfixture-key-body\n-----END PRIVATE KEY-----", []string{"fixture-key-body", "BEGIN PRIVATE KEY"}},
		{"RSA PEM", "-----BEGIN RSA PRIVATE KEY-----\nfixture-key-body\n-----END RSA PRIVATE KEY-----", []string{"fixture-key-body"}},
		{"EC PEM", "-----BEGIN EC PRIVATE KEY-----\nfixture-key-body\n-----END EC PRIVATE KEY-----", []string{"fixture-key-body"}},
		{"OPENSSH PEM", "-----BEGIN OPENSSH PRIVATE KEY-----\nfixture-key-body\n-----END OPENSSH PRIVATE KEY-----", []string{"fixture-key-body"}},
		{"unterminated PEM", "-----BEGIN PRIVATE KEY-----\nfixture-key-body", []string{"fixture-key-body"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := test.input
			// An unterminated PEM consumes the rest of its record/block. Complete
			// records must preserve an unrelated, unfamiliar later diagnostic.
			if test.name != "unterminated PEM" {
				input += "\nERROR: unusual-wheel-sentinel for mystery-widget==6"
			}
			text := SanitizeDiagnostic(input, nil)
			for _, value := range test.private {
				if strings.Contains(text, value) {
					t.Fatal("reviewed private fixture survived redaction")
				}
			}
			if test.name != "unterminated PEM" && !strings.Contains(text, "unusual-wheel-sentinel for mystery-widget==6") {
				t.Fatal("unrelated diagnostic was lost during targeted redaction")
			}
		})
	}
	text := SanitizeDiagnostic("password=\nERROR: unusual-wheel-sentinel", nil)
	if !strings.Contains(text, "unusual-wheel-sentinel") {
		t.Fatal("empty credential crossed a diagnostic record boundary")
	}
}

func TestCommandFailurePreservesExitAndOrigin(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	binary := filepath.Join(t.TempDir(), "fake-python")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf '%s\\n' 'ERROR: wheel-build-sentinel' 'token=fixture-secret' >&2\nexit 23\n"), 0700); err != nil {
		t.Fatal(err)
	}
	err := New(binary).PipInstall(context.Background(), "mystery-widget")
	var command *CommandError
	var exit *exec.ExitError
	if !errors.As(err, &command) || !errors.As(err, &exit) || exit.ExitCode() != 23 || command.Stage != "module_install" || command.Module != "mystery-widget" || len(command.Stack) == 0 {
		t.Fatal("lost typed failure, exit status, stage or stack")
	}
	if !strings.Contains(command.Output, "wheel-build-sentinel") || strings.Contains(err.Error(), "fixture-secret") {
		t.Fatal("failure formatting lost cause or leaked credentials")
	}
}
