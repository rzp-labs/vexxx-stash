package ffmpeg

import (
	"bytes"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/stashapp/stash/pkg/diagnostics"
)

const generationStderrHalf = 4096

// GenerationStderrCapture counts source bytes, excluding the omission marker.
type GenerationStderrCapture struct {
	Total, Omitted         int64
	Retained               int
	Pressure               bool
	MetadataOmittedRecords int
}

// GenerationStderr drains the entire pipe while keeping at most 8 KiB of source
// text plus one record of at most 4 KiB while draining. Overlong records are
// omitted entirely. os/exec owns its single writer; read it only after Wait has drained IO.
type GenerationStderr struct {
	head, tail                                       []byte
	record                                           []byte
	recordInfo                                       generationStderrRecord
	recordOverflow, headClosed, metadata, privateKey bool
	metadataIndent, metadataOmittedRecords           int
	total                                            int64
	pressure                                         bool
	overlap                                          string
}

func (s *GenerationStderr) Write(p []byte) (int, error) {
	n := len(p)
	s.total += int64(n)
	// Preserve the existing explicit-pressure classification even when its
	// message occurs in discarded text or spans pipe writes. Scan in bounded
	// pieces; a caller's large Write never creates another full-size copy.
	for rest := p; len(rest) > 0 && !s.pressure; {
		piece := min(len(rest), 1024)
		text := s.overlap + strings.ToLower(string(rest[:piece]))
		s.pressure = generationPressureMessage(text)
		keep := min(len(text), 64)
		s.overlap = text[len(text)-keep:]
		rest = rest[piece:]
	}
	for len(p) > 0 {
		end := bytes.IndexByte(p, '\n')
		piece := len(p)
		if end >= 0 {
			piece = end + 1
		}
		if s.record == nil {
			s.record = make([]byte, 0, generationStderrHalf)
		}
		s.recordInfo.scan(p[:piece], &s.privateKey)
		remaining := generationStderrHalf - len(s.record)
		if piece > remaining {
			s.recordOverflow = true
		}
		s.record = append(s.record, p[:min(piece, remaining)]...)
		p = p[piece:]
		if end >= 0 {
			s.finishRecord()
		}
	}
	return n, nil
}

// Apply contextual omissions before a head/tail cut can separate the block
// header from its values. Unknown diagnostic records remain eligible unchanged.
func (s *GenerationStderr) finishRecord() {
	line := s.record
	s.recordInfo.finish()
	info := s.recordInfo
	omit := info.keyRecord
	if info.metadataHeader() {
		s.metadata, s.metadataIndent = true, info.indent
		s.metadataOmittedRecords++
		omit = true
	} else if s.metadata {
		if info.indent > s.metadataIndent && info.colon && !info.bracket {
			s.metadataOmittedRecords++
			omit = true
		} else if info.nonSpace {
			s.metadata = false
		}
	}
	if !omit && !s.recordOverflow {
		if !s.headClosed && len(s.head)+len(line) <= generationStderrHalf {
			if s.head == nil {
				s.head = make([]byte, 0, generationStderrHalf)
			}
			s.head = append(s.head, line...)
		} else {
			s.headClosed = true
			// Drop whole oldest records until this complete record fits.
			for len(s.tail)+len(line) > generationStderrHalf {
				end := bytes.IndexByte(s.tail, '\n')
				copy(s.tail, s.tail[end+1:])
				s.tail = s.tail[:len(s.tail)-end-1]
			}
			if s.tail == nil {
				s.tail = make([]byte, 0, generationStderrHalf)
			}
			s.tail = append(s.tail, line...)
		}
	}
	s.record = s.record[:0]
	s.recordOverflow = false
	s.recordInfo = generationStderrRecord{}
}

func (s *GenerationStderr) Capture() ([]byte, GenerationStderrCapture) {
	if len(s.record) > 0 {
		s.finishRecord()
	}
	info := GenerationStderrCapture{Total: s.total, Retained: len(s.head) + len(s.tail), Pressure: s.pressure, MetadataOmittedRecords: s.metadataOmittedRecords}
	info.Omitted = info.Total - int64(info.Retained)
	text := append([]byte(nil), s.head...)
	if info.Omitted > 0 {
		text = append(text, fmt.Sprintf("\n[... %d stderr bytes omitted ...]\n", info.Omitted)...)
	}
	return append(text, s.tail...), info
}

// GenerationStderrText bounds subsequent reasons while retaining the terminal
// error. Sanitize before cutting so a credential prefix cannot be separated
// from its value by the second bound.
func GenerationStderrText(data []byte, limit int) string {
	data = []byte(diagnostics.Sanitize(string(data), nil, 0).Value)
	if len(data) <= limit {
		return strings.TrimSpace(string(data))
	}
	const marker = "\n[truncated]\n"
	if limit <= len(marker) {
		return "[truncated]"
	}
	head := (limit - len(marker)) / 2
	tail := len(data) - (limit - len(marker) - head)
	for head > 0 && !utf8.RuneStart(data[head]) {
		head--
	}
	for tail < len(data) && !utf8.RuneStart(data[tail]) {
		tail++
	}
	return strings.TrimSpace(string(data[:head])) + marker + strings.TrimSpace(string(data[tail:]))
}
