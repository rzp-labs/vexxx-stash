package ffmpeg

import (
	"unicode"
	"unicode/utf8"
)

// generationStderrRecord classifies every drained byte, independently of the
// retained record prefix. All text-dependent state has a fixed size.
type generationStderrRecord struct {
	indent                                      int
	indentDone, nonSpace, colon, bracket        bool
	metadataMatch                               int
	metadataMismatch, metadataTrailing          bool
	runeBytes                                   [utf8.UTFMax]byte
	runeCount                                   int
	beginPrefix, endPrefix, keyLabel, keySuffix int
	keyDashes                                   int
	keyRecord                                   bool
}

func (r *generationStderrRecord) scan(data []byte, privateKey *bool) {
	for _, b := range data {
		if !r.indentDone {
			if b == ' ' || b == '\t' {
				r.indent++
			} else {
				r.indentDone = true
			}
		}
		r.scanRuneByte(b)
		r.keyRecord = r.keyRecord || *privateKey
		r.scanKeyByte(b, privateKey)
	}
}

func (r *generationStderrRecord) scanRuneByte(b byte) {
	if r.runeCount == 0 && b < utf8.RuneSelf {
		r.scanRune(rune(b))
		return
	}
	r.runeBytes[r.runeCount] = b
	r.runeCount++
	for r.runeCount > 0 && utf8.FullRune(r.runeBytes[:r.runeCount]) {
		value, size := utf8.DecodeRune(r.runeBytes[:r.runeCount])
		r.scanRune(value)
		copy(r.runeBytes[:], r.runeBytes[size:r.runeCount])
		r.runeCount -= size
	}
}

func (r *generationStderrRecord) finish() {
	for r.runeCount > 0 {
		value, size := utf8.DecodeRune(r.runeBytes[:r.runeCount])
		r.scanRune(value)
		copy(r.runeBytes[:], r.runeBytes[size:r.runeCount])
		r.runeCount -= size
	}
}

func (r *generationStderrRecord) scanRune(value rune) {
	const metadata = "Metadata:"
	if unicode.IsSpace(value) {
		if r.nonSpace {
			if r.metadataMatch == len(metadata) {
				r.metadataTrailing = true
			} else {
				r.metadataMismatch = true
			}
		}
		return
	}
	if !r.nonSpace {
		r.bracket = value == '['
		r.nonSpace = true
	}
	r.colon = r.colon || value == ':'
	if r.metadataMismatch || r.metadataTrailing || r.metadataMatch == len(metadata) || value != rune(metadata[r.metadataMatch]) {
		r.metadataMismatch = true
	} else {
		r.metadataMatch++
	}
}

func (r *generationStderrRecord) metadataHeader() bool {
	return !r.metadataMismatch && r.metadataMatch == len("Metadata:")
}

// Delimiters follow diagnostics' -----BEGIN/END [A-Z ]*PRIVATE KEY-----
// grammar. The label can be arbitrarily long; only match positions are stored.
func (r *generationStderrRecord) scanKeyByte(b byte, privateKey *bool) {
	var begin, end bool
	r.beginPrefix, begin = generationKeyPrefix(r.beginPrefix, b, "-----BEGIN ")
	r.endPrefix, end = generationKeyPrefix(r.endPrefix, b, "-----END ")
	if begin || end {
		r.keyLabel = 1
		if end {
			r.keyLabel = 2
		}
		r.keySuffix, r.keyDashes = 0, 0
		return
	}
	if r.keyLabel == 0 {
		return
	}
	const suffix = "PRIVATE KEY"
	if b == '-' && (r.keyDashes > 0 || r.keySuffix == len(suffix)) {
		r.keyDashes++
		if r.keyDashes == 5 {
			*privateKey = r.keyLabel == 1
			r.keyRecord = r.keyRecord || *privateKey
			r.keyLabel = 0
			// A completed delimiter owns all of its bytes. Its closing
			// hyphens cannot also begin an overlapping next delimiter.
			r.beginPrefix, r.endPrefix = 0, 0
			r.keySuffix, r.keyDashes = 0, 0
		}
		return
	}
	if r.keyDashes > 0 || !(b >= 'A' && b <= 'Z' || b == ' ') {
		r.keyLabel = 0
		return
	}
	if r.keySuffix < len(suffix) && b == suffix[r.keySuffix] {
		r.keySuffix++
	} else if b == suffix[0] {
		r.keySuffix = 1
	} else {
		r.keySuffix = 0
	}
}

func generationKeyPrefix(match int, b byte, prefix string) (int, bool) {
	// Repeated leading hyphens can start overlapping delimiter prefixes.
	if b == '-' && match <= 5 {
		return min(match+1, 5), false
	}
	if b == prefix[match] {
		match++
		if match == len(prefix) {
			return 0, true
		}
		return match, false
	}
	if b == '-' {
		return 1, false
	}
	return 0, false
}
