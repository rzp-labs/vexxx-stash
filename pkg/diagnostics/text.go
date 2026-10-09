// Package diagnostics provides one targeted privacy policy for failure reports.
package diagnostics

import (
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const MaxTextBytes = 16 * 1024

// Text records loss explicitly; redaction always precedes byte truncation.
type Text struct {
	Value        string
	OmittedBytes int
	Redacted     bool
}

// An unquoted credential's end is ambiguous: spaces and semicolons can be part
// of the secret. Omit the remainder of that record, leaving other lines intact.
var diagnosticCredentials = regexp.MustCompile(`(?i)(?:["']?(?:password|passwd|pwd|token|api[_-]?key|access[_-]?token|refresh[_-]?token|session[_-]?token|client[_-]?secret|(?:aws[_-]?)?secret[_-]?access[_-]?key|secret|credential|signature|x-amz-signature)["']?[ \t]*[:=][ \t]*(?:"[^"\r\n]*"|'[^'\r\n]*'|[^\r\n]*))`)
var diagnosticHeaders = regexp.MustCompile(`(?im)\b(?:authorization|proxy-authorization|cookie|set-cookie)["']?[ \t]*[:=][ \t]*[^\r\n]*`)
var diagnosticBearer = regexp.MustCompile(`(?i)\b(?:bearer|basic)\s+[A-Za-z0-9_./+=-]+`)
var diagnosticTokens = regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9_]+|github_pat_[A-Za-z0-9_]+|(?:sk|phx|phc|phs)[_-][A-Za-z0-9_-]+|(?:AKIA|ASIA)[A-Z0-9]{16}|eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+)\b`)
var diagnosticPrivateKey = regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?(?:-----END [A-Z ]*PRIVATE KEY-----|$)`)
var diagnosticURL = regexp.MustCompile(`(?i)\b(?:https?|rtsp|rtmp|ftp|s3|file|git\+https?|ssh)://[^\s'"<>]+`)
var diagnosticEmail = regexp.MustCompile(`\b[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}\b`)
var diagnosticMediaFilename = regexp.MustCompile(`(?i)(?:[\p{L}\p{M}\p{N}\p{S}_.-]+\.(?:mp4|mkv|avi|mov|webm|jpg|jpeg|png|webp|vtt|m3u8|ts|ffconcat|funscript))\b`)
var diagnosticQuotedPath = regexp.MustCompile(`(?:"(?:[A-Za-z]:[\\/]|/|\\\\)[^"]*"|'(?:[A-Za-z]:[\\/]|/|\\\\)[^']*')`)
var diagnosticPath = regexp.MustCompile(`(^|[\s=:"'(\[])((?:[A-Za-z]:[\\/]|\\\\|/))`)
var diagnosticOSCause = regexp.MustCompile(`(?i): (?:permission denied|no such file or directory|input/output error|cannot allocate memory|invalid argument|operation not permitted|read-only file system|no space left on device|file exists|is a directory|not a directory)[.!]?$`)

var device = regexp.MustCompile(`^/dev/dri/renderD[0-9]+$`)
var commandArguments = regexp.MustCompile(`(?s)(?:error running ffmpeg command|ffmpeg command produced no output):?\s*<[^>]*>`)
var unicodeMedia = regexp.MustCompile(`(?i)(?:[\p{L}\p{M}\p{N}\p{S}_. -]*[^\x00-\x7F][\p{L}\p{M}\p{N}\p{S}_. -]*\.(?:mp4|mkv|avi|mov|webm|jpg|jpeg|png|webp|vtt|m3u8|ts|ffconcat|funscript))\b`)
var quotedMedia = regexp.MustCompile(`(?:"[^"\r\n]*\.(?:mp4|mkv|avi|mov|webm|jpg|jpeg|png|webp|vtt|m3u8|ts|ffconcat|funscript)"|'[^'\r\n]*\.(?:mp4|mkv|avi|mov|webm|jpg|jpeg|png|webp|vtt|m3u8|ts|ffconcat|funscript)')`)

func Sanitize(message string, private []string, limit int) Text {
	original := message
	for _, value := range private {
		if value == "" || value == "-" || device.MatchString(value) {
			continue
		}
		message = redactPrivateValue(message, value)
		if strings.ContainsAny(value, "/\\") {
			base := filepath.Base(strings.ReplaceAll(value, "\\", "/"))
			if base != "." && base != "/" && len(base) > 3 {
				message = strings.ReplaceAll(message, base, "[media filename redacted]")
			}
		}
	}
	message, inputOmitted := boundedRecords(message)
	if strings.Contains(message, "ffmpeg command") {
		message = commandArguments.ReplaceAllStringFunc(message, func(command string) string {
			if strings.HasPrefix(command, "ffmpeg command produced no output") {
				return "ffmpeg command produced no output: [command arguments redacted]"
			}
			return "error running ffmpeg [command arguments redacted]"
		})
	}
	if strings.Contains(message, "PRIVATE KEY-----") {
		message = diagnosticPrivateKey.ReplaceAllString(message, "[credential redacted]")
	}
	lines := strings.Split(message, "\n")
	for i, line := range lines {
		lower := strings.ToLower(line)
		if strings.Contains(lower, "authorization") || strings.Contains(lower, "cookie") {
			line = diagnosticHeaders.ReplaceAllString(line, "[credential redacted]")
		}
		if strings.Contains(line, "://") {
			line = diagnosticURL.ReplaceAllString(line, "[URL redacted]")
		}
		if strings.ContainsAny(line, ":=") {
			line = diagnosticCredentials.ReplaceAllString(line, "[credential redacted]")
		}
		if strings.Contains(lower, "bearer") || strings.Contains(lower, "basic") {
			line = diagnosticBearer.ReplaceAllString(line, "[credential redacted]")
		}
		if strings.ContainsAny(line, "_-") || strings.Contains(line, "eyJ") || strings.Contains(line, "AKIA") || strings.Contains(line, "ASIA") {
			line = diagnosticTokens.ReplaceAllString(line, "[credential redacted]")
		}
		if strings.Contains(line, "@") && strings.Contains(line, ".") {
			line = diagnosticEmail.ReplaceAllString(line, "[identity redacted]")
		}
		if strings.Contains(line, ".") {
			if strings.IndexFunc(line, func(r rune) bool { return r > 127 }) >= 0 {
				line = unicodeMedia.ReplaceAllString(line, "[media filename redacted]")
			}
			line = quotedMedia.ReplaceAllString(line, "[media filename redacted]")
			line = diagnosticMediaFilename.ReplaceAllString(line, "[media filename redacted]")
		}
		if strings.ContainsAny(line, "\"'") {
			line = diagnosticQuotedPath.ReplaceAllStringFunc(line, func(s string) string {
				if device.MatchString(s[1 : len(s)-1]) {
					return s
				}
				return "[path redacted]"
			})
		}
		lines[i] = line
		offset := 0
		for offset < len(line) {
			match := diagnosticPath.FindStringSubmatchIndex(line[offset:])
			if match == nil {
				break
			}
			start := offset + match[4]
			end := strings.IndexAny(line[start:], " \t\r\"'<>[](),;")
			if end < 0 {
				end = len(line)
			} else {
				end += start
			}
			if device.MatchString(line[start:end]) {
				offset = end
				continue
			}
			suffix := ""
			if span := diagnosticOSCause.FindStringIndex(line[start:]); span != nil {
				suffix = line[start+span[0]:]
			}
			lines[i] = line[:start] + "[path redacted]" + suffix
			break
		}
	}
	message = strings.TrimSpace(strings.ToValidUTF8(strings.Join(lines, "\n"), "?"))
	result := Text{Value: message, OmittedBytes: inputOmitted, Redacted: message != strings.TrimSpace(original)}
	if limit > 0 && len(message) > limit {
		const marker = "[truncated]\n"
		offset := len(message) - (limit - len(marker))
		for offset < len(message) && !utf8.RuneStart(message[offset]) {
			offset++
		}
		result.OmittedBytes += offset
		result.Value = marker + message[offset:]
	}
	return result
}

// Very short titles/names are private as standalone tokens, but replacing their
// letters inside ordinary words destroys the diagnostic and its stage inference.
func redactPrivateValue(message, value string) string {
	word := func(r rune) bool { return unicode.IsLetter(r) || unicode.IsMark(r) || unicode.IsDigit(r) || r == '_' }
	if utf8.RuneCountInString(value) > 3 || strings.IndexFunc(value, func(r rune) bool { return !word(r) }) >= 0 {
		return strings.ReplaceAll(message, value, "[location redacted]")
	}
	var out strings.Builder
	for offset := 0; offset < len(message); {
		i := strings.Index(message[offset:], value)
		if i < 0 {
			out.WriteString(message[offset:])
			break
		}
		i += offset
		end := i + len(value)
		before, _ := utf8.DecodeLastRuneInString(message[:i])
		after, _ := utf8.DecodeRuneInString(message[end:])
		out.WriteString(message[offset:i])
		if (i == 0 || !word(before)) && (end == len(message) || !word(after)) {
			out.WriteString("[location redacted]")
		} else {
			out.WriteString(value)
		}
		offset = end
	}
	return out.String()
}
func Safe(message string, private []string) string {
	return Sanitize(message, private, MaxTextBytes).Value
}

// Work on complete head/tail records only. Never cut through credentials or an
// unquoted path record; omit a partial PEM tail until its closing boundary.
func boundedRecords(message string) (string, int) {
	const budget = 256 * 1024
	if len(message) <= budget {
		return message, 0
	}
	headEnd := strings.LastIndexByte(message[:budget/2], '\n')
	if headEnd < 0 {
		headEnd = 0
	}
	tailStart := len(message) - budget/2
	if next := strings.IndexByte(message[tailStart:], '\n'); next >= 0 {
		tailStart += next + 1
	} else {
		tailStart = len(message)
	}
	prefix := message[:tailStart]
	begin, end := strings.LastIndex(prefix, "-----BEGIN "), strings.LastIndex(prefix, "-----END ")
	if begin > end && strings.Contains(prefix[begin:min(begin+128, len(prefix))], "PRIVATE KEY-----") {
		if close := regexp.MustCompile(`-----END [A-Z ]*PRIVATE KEY-----`).FindStringIndex(message[tailStart:]); close != nil {
			tailStart += close[1]
		} else {
			tailStart = len(message)
		}
	}
	head := diagnosticPrivateKey.ReplaceAllString(message[:headEnd], "[credential redacted]")
	return head + "\n[records omitted]\n" + message[tailStart:], tailStart - headEnd
}

// Summary keeps both operation prefixes and final causes. Process output uses
// Sanitize's tail bound instead; joined cause lists have their own structured fields.
func Summary(message string, private []string, limit int) Text {
	result := Sanitize(message, private, 0)
	if limit <= 0 || len(result.Value) <= limit {
		return result
	}
	const marker = "\n[truncated middle]\n"
	budget := (limit - len(marker)) / 2
	head, tail := budget, len(result.Value)-budget
	for head > 0 && !utf8.RuneStart(result.Value[head]) {
		head--
	}
	for tail < len(result.Value) && !utf8.RuneStart(result.Value[tail]) {
		tail++
	}
	result.OmittedBytes += tail - head
	result.Value = result.Value[:head] + marker + result.Value[tail:]
	return result
}
