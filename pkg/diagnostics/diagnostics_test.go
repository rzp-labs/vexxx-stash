package diagnostics

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"sync"
	"syscall"
	"testing"
	"unicode/utf8"
)

func TestTargetedPolicyPreservesUnknownMessagesAndRemovesFixtures(t *testing.T) {
	input := "new diagnostic failed converting NV12/P010 on '/dev/dri/renderD128'\npassword=correct horse; battery staple\nCookie: sid=opaque-session; theme=dark\n-----BEGIN PRIVATE KEY-----\nprivate-key-body\n-----END PRIVATE KEY-----\n'私密 Café.mp4'\nhttps://user:pass@example.invalid/file?token=secret\nunknown useful cause remains"
	safe := Sanitize(input, nil, MaxTextBytes)
	for _, secret := range []string{"correct", "horse", "opaque-session", "private-key-body", "私密", "Café", "example.invalid"} {
		if strings.Contains(safe.Value, secret) {
			t.Fatalf("fixture retained %s", secret)
		}
	}
	for _, context := range []string{"new diagnostic", "NV12/P010", "/dev/dri/renderD128", "unknown useful cause remains"} {
		if !strings.Contains(safe.Value, context) {
			t.Errorf("context lost %s", context)
		}
	}
	if !safe.Redacted {
		t.Fatal("redaction not observable")
	}
}
func TestCauseJoinLimitsAndIdentity(t *testing.T) {
	a, b := errors.New("first cause"), errors.New("second cause")
	root := fmt.Errorf("outer-prefix: %w: outer-suffix", errors.Join(fmt.Errorf("first operation: %w", a), context.Canceled, fmt.Errorf("second operation: %w", b)))
	result := Split(root)
	if result.Count != 2 || len(result.Entries) != 2 {
		t.Fatal("cancel swallowed real causes")
	}
	for _, entry := range result.Entries {
		if !strings.Contains(entry.Cause, "outer-prefix") || !strings.Contains(entry.Cause, "outer-suffix") {
			t.Fatal("wrapper lost")
		}
	}
	if !errors.Is(result.Entries[0].Err, a) || !errors.Is(result.Entries[1].Err, b) {
		t.Fatal("error identity changed")
	}
	many := make([]error, 100)
	for i := range many {
		many[i] = fmt.Errorf("module %d cause", i)
	}
	bounded := Split(errors.Join(many...))
	if bounded.Count != 100 || bounded.Omitted != 36 || len(bounded.Entries) != 64 {
		t.Fatal("join loss not bounded/observable")
	}
}
func TestConcurrentReservationAndRejectedRetry(t *testing.T) {
	ctx := WithState(context.Background())
	err := errors.New("cause")
	ok, finish := Claim(ctx, err)
	if !ok {
		t.Fatal("first reservation failed")
	}
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if yes, _ := Claim(ctx, err); yes {
				t.Error("duplicate reservation")
			}
		}()
	}
	wg.Wait()
	finish(false)
	yes, commit := Claim(ctx, err)
	if !yes {
		t.Fatal("rejected enqueue did not release")
	}
	commit(true)
	if !Reported(ctx, fmt.Errorf("aggregate: %w", err)) {
		t.Fatal("wrapper identity not deduplicated")
	}
	if Reported(WithState(context.Background()), err) {
		t.Fatal("dedup escaped operation context")
	}
}
func TestTextBoundsPreserveUTF8AndRecordLoss(t *testing.T) {
	safe := Sanitize(strings.Repeat("é", 30000)+"\nfinal-cause-sentinel", nil, 4096)
	if len(safe.Value) > 4096 || !utf8.ValidString(safe.Value) || !strings.Contains(safe.Value, "final-cause-sentinel") || safe.OmittedBytes == 0 {
		t.Fatal("bound lost cause or accounting")
	}
}
func BenchmarkDiagnosticRedaction(b *testing.B) {
	input := strings.Repeat("[hevc] unfamiliar decoder failure NV12/P010\n", 1000) + "password=fixture-secret\nfinal cause"
	b.ReportAllocs()
	b.SetBytes(int64(len(input)))
	for i := 0; i < b.N; i++ {
		Sanitize(input, nil, MaxTextBytes)
	}
}
func TestOversizedRecordsNeverExposeCutCredentialsOrPEM(t *testing.T) {
	for _, input := range []string{"password=" + strings.Repeat("secret-segment", 50000) + "\nfinal cause", "first cause\n-----BEGIN PRIVATE KEY-----\n" + strings.Repeat("key-body\n", 50000) + "-----END PRIVATE KEY-----\nfinal cause"} {
		safe := Sanitize(input, nil, 4096)
		if safe.OmittedBytes == 0 || len(safe.Value) > 4096 || strings.Contains(safe.Value, "secret-segment") || strings.Contains(safe.Value, "key-body") || !strings.Contains(safe.Value, "final cause") {
			t.Fatal("oversized record privacy/budget failed")
		}
	}
}

func TestDistinctOccurrencesSharingSentinelRemainReportable(t *testing.T) {
	for _, shared := range []error{syscall.ENOENT, errors.New("shared pointer sentinel")} {
		ctx := WithState(context.Background())
		first := fmt.Errorf("first operation: %w", &fs.PathError{Op: "open", Path: "/private/one", Err: shared})
		second := fmt.Errorf("second operation: %w", &fs.PathError{Op: "open", Path: "/private/two", Err: shared})
		accepted, finish := Claim(ctx, first)
		if !accepted {
			t.Fatal("first occurrence rejected")
		}
		finish(true)
		if Reported(ctx, second) {
			t.Fatal("shared semantic cause suppressed a distinct occurrence")
		}
		accepted, finish = Claim(ctx, second)
		if !accepted {
			t.Fatal("second occurrence rejected")
		}
		finish(true)
		if !Reported(ctx, fmt.Errorf("later observer: %w", first)) || !Reported(ctx, fmt.Errorf("later observer: %w", second)) {
			t.Fatal("same occurrence failed to deduplicate")
		}
		var path *fs.PathError
		if !errors.As(second, &path) || !errors.Is(second, shared) {
			t.Fatal("cause identity changed")
		}
	}
}

func TestShortPrivateTokensDoNotEraseDiagnosticWords(t *testing.T) {
	got := Sanitize("encoder failed; scene title: e; rune: é; opaque: abc; alphabet; échec", []string{"e", "é", "abc"}, 4096).Value
	for _, retained := range []string{"encoder failed", "alphabet", "échec"} {
		if !strings.Contains(got, retained) {
			t.Errorf("diagnostic word corrupted: %q in %q", retained, got)
		}
	}
	for _, private := range []string{"title: e", "rune: é", "opaque: abc"} {
		if strings.Contains(got, private) {
			t.Errorf("standalone private token retained: %q", private)
		}
	}
}
