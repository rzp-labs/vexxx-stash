package ffmpeg

import (
	"bytes"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stashapp/stash/pkg/generationbudget"
)

func TestGenerationStderrKeepsCompleteRecordsAndBoundedHeadTail(t *testing.T) {
	for _, size := range []int{0, 4096, 8192, 8193, 12 << 20} {
		for _, chunk := range []int{1, 1031, 32768} {
			if size > 8193 && chunk == 1 {
				continue
			}
			t.Run(strings.Join([]string{strconv.Itoa(size), strconv.Itoa(chunk)}, "/"), func(t *testing.T) {
				data := bytes.Repeat([]byte("x"), size)
				for i := 63; i < size; i += 64 {
					data[i] = '\n'
				}
				if size > 20 {
					copy(data, "initial driver cause\n")
					copy(data[len(data)-22:], "\nterminal VAAPI error\n")
				}
				var capture GenerationStderr
				for offset := 0; offset < len(data); {
					end := min(offset+chunk, len(data))
					n, err := capture.Write(data[offset:end])
					if n != end-offset || err != nil {
						t.Fatal("pipe did not drain", n, err)
					}
					offset = end
				}
				got, info := capture.Capture()
				if info.Total != int64(size) || info.Retained > min(size, 8192) || info.Omitted != int64(size-info.Retained) {
					t.Fatal(info)
				}
				if len(capture.head)+len(capture.tail) > 8192 || len(got) > 8300 {
					t.Fatal("capture grew", len(got))
				}
				if info.Omitted == 0 {
					if !bytes.Equal(got, data) {
						t.Fatal("small stderr changed")
					}
				} else if !bytes.HasPrefix(got, []byte("initial driver cause\n")) || !bytes.HasSuffix(got, []byte("terminal VAAPI error\n")) || !bytes.Contains(got, []byte("stderr bytes omitted")) {
					t.Fatal("head/tail or omission marker lost")
				}
			})
		}
	}
}

func TestGenerationStderrDropsOverlongRecordsWithoutLosingTerminalCause(t *testing.T) {
	for _, size := range []int{4095, 4096, 4097, 12 << 20} {
		var capture GenerationStderr
		capture.Write(bytes.Repeat([]byte("x"), size))
		capture.Write([]byte("\n[driver] terminal refusal\n"))
		data, info := capture.Capture()
		if !bytes.Contains(data, []byte("[driver] terminal refusal")) || (size >= 4096 && bytes.Contains(data, []byte("xxx"))) {
			t.Fatal("partial overlong record retained or terminal cause lost", size)
		}
		if cap(capture.head)+cap(capture.tail)+cap(capture.record) > 3*generationStderrHalf || info.Omitted != info.Total-int64(info.Retained) {
			t.Fatal("storage or counts incorrect", info)
		}
	}
}

func TestGenerationStderrReasonBoundsAfterSanitizingAndPreservesUTF8(t *testing.T) {
	for _, prefix := range []int{0, 2040, 4096, 8000} {
		data := []byte(strings.Repeat("é", prefix) + "\ntoken: " + strings.Repeat("PRIVATEVALUE", 300) + "\n[driver] terminal refusal")
		text := GenerationStderrText(data, 4096)
		if len(text) > 4096 || !utf8.ValidString(text) || strings.Contains(text, "PRIVATEVALUE") || !strings.Contains(text, "[driver] terminal refusal") {
			t.Fatal("unsafe or incomplete reason", prefix)
		}
	}
}

func TestGenerationStderrPressureSurvivesDiscardedMiddleAndSplitWrites(t *testing.T) {
	for _, message := range []string{"Cannot allocate memory", "VA_STATUS_ERROR_ALLOCATION_FAILED", "VAAPI encode failed: 24"} {
		var capture GenerationStderr
		capture.Write(bytes.Repeat([]byte("x"), 10000))
		for i := range message {
			capture.Write([]byte(message[i : i+1]))
		}
		capture.Write(bytes.Repeat([]byte("x"), 10000))
		data, info := capture.Capture()
		if bytes.Contains(data, []byte(message)) {
			t.Fatal("fixture did not discard middle")
		}
		exit := &exec.ExitError{Stderr: data}
		err := &GenerationCommandError{Err: exit, Stderr: info}
		got := GenerationPressure(err)
		if generationbudget.IsPressure(got) != (message != "VAAPI encode failed: 24") || !errors.Is(got, exit) {
			t.Fatal(message, got)
		}
	}
}

func TestGenerationStderrReasonKeepsTerminalDriverError(t *testing.T) {
	data := []byte("initial decode refusal\n" + strings.Repeat("x", 10000) + "\nVAAPI encode failed: 24; Input/output error")
	text := GenerationStderrText(data, 4096)
	if len(text) > 4096 || !strings.Contains(text, "initial decode refusal") || !strings.Contains(text, "VAAPI encode failed: 24; Input/output error") || !strings.Contains(text, "[truncated]") {
		t.Fatal("bounded reason lost cause", len(text))
	}
}
