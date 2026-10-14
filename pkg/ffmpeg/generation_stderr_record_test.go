package ffmpeg

import (
	"math/rand"
	"regexp"
	"strings"
	"testing"
)

func TestGenerationStderrRecordDelimitersMatchExclusiveGrammar(t *testing.T) {
	grammar := regexp.MustCompile(`-----(BEGIN|END) [A-Z ]*PRIVATE KEY-----`)
	fragments := []string{
		"-----BEGIN PRIVATE KEY-----", "------BEGIN RSA PRIVATE KEY-----", "-----END PRIVATE KEY-----",
		"-----BEGIN PRIVATE PRIVATE KEY-----", "-----BEGIN PPRIVATE KEY-----", "-----BEGIN X PRIVATE KEY------",
		"-----BEGIN PRIVATE KEY----", "-----BEGIN BAD", "-----END ", "x", "-", " A ", "-----BE-", "-----EN-",
		"-----BEGIN PRIVATE KEY---------END PRIVATE KEY-----", "-----END PRIVATE KEY---------BEGIN PRIVATE KEY-----",
	}
	inputs := []string{
		"-----BEGIN " + strings.Repeat("A ", 4096) + "PRIVATE KEY-----",
		"-----END " + strings.Repeat("A ", 4096) + "PRIVATE KEY-----",
	}
	inputs = append(inputs, fragments...)
	rng := rand.New(rand.NewSource(11004))
	for range 512 {
		var data strings.Builder
		for count := 1 + rng.Intn(6); count > 0; count-- {
			data.WriteString(fragments[rng.Intn(len(fragments))])
		}
		inputs = append(inputs, data.String())
	}
	cases := 0
	for index, data := range inputs {
		for _, initial := range []bool{false, true} {
			want, wantRecord := initial, initial
			for _, match := range grammar.FindAllStringSubmatch(data, -1) {
				want = match[1] == "BEGIN"
				wantRecord = wantRecord || want
			}
			for _, chunk := range []int{1, 2, 5, 9, 37, 32768} {
				var record generationStderrRecord
				private := initial
				for offset := 0; offset < len(data); {
					end := min(offset+chunk, len(data))
					record.scan([]byte(data[offset:end]), &private)
					offset = end
				}
				record.finish()
				if private != want || record.keyRecord != wantRecord {
					t.Fatalf("delimiter grammar mismatch: input=%d initial=%t chunk=%d state=%t/%t record=%t/%t", index, initial, chunk, private, want, record.keyRecord, wantRecord)
				}
				cases++
			}
		}
	}
	t.Logf("%d deterministic delimiter context cases; seed 11004", cases)
}

func TestGenerationStderrRecordMetadataMatchesCompleteText(t *testing.T) {
	fragments := []string{"Metadata:", "custom_tag:", "[driver]", "M", "e", "t", "a", "d", ":", "x", " ", "\t", "\r", "\v", "\f", "\u0085", "\u00a0", "\u2003", "\u2028", "\u3000", "\xff"}
	inputs := []string{"", " Metadata: ", "Metadata: x", "[driver]: cause", strings.Repeat(" ", 4096) + "Metadata:", "    " + strings.Repeat("A", 4096) + ": value", strings.Repeat("\u2003", 4096), "\xe2\x80"}
	rng := rand.New(rand.NewSource(11004))
	for range 1024 {
		var data strings.Builder
		for count := rng.Intn(20); count > 0; count-- {
			data.WriteString(fragments[rng.Intn(len(fragments))])
		}
		inputs = append(inputs, data.String())
	}
	cases := 0
	for index, data := range inputs {
		trimmed := strings.TrimSpace(data)
		indent := len(data) - len(strings.TrimLeft(data, " \t"))
		for _, chunk := range []int{1, 2, 37, 32768} {
			var record generationStderrRecord
			private := false
			for offset := 0; offset < len(data); {
				end := min(offset+chunk, len(data))
				record.scan([]byte(data[offset:end]), &private)
				offset = end
			}
			record.finish()
			if record.indent != indent || record.nonSpace != (trimmed != "") || record.colon != strings.Contains(trimmed, ":") || record.bracket != strings.HasPrefix(trimmed, "[") || record.metadataHeader() != (trimmed == "Metadata:") {
				t.Fatalf("complete metadata classification mismatch: input=%d chunk=%d", index, chunk)
			}
			cases++
		}
	}
	t.Logf("%d deterministic metadata/Unicode/invalid-UTF8 cases; seed 11004", cases)
}
