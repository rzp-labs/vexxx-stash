package ffmpeg

import (
	"fmt"
	"strings"
	"testing"
)

func TestGenerationStderrRetainsTerminalCauseAfterCarriageReturnProgress(t *testing.T) {
	const progress = "frame=   10 fps=0.0 q=0.0 size=       0kB time=00:00:00.40 bitrate=   0.0kbits/s speed=0.01x\r"
	const cause = "Error while opening encoder\n"
	input := strings.Repeat(progress, 100) + cause
	for _, chunk := range []int{1, 37, len(input)} {
		t.Run(fmt.Sprint(chunk), func(t *testing.T) {
			var capture GenerationStderr
			for start := 0; start < len(input); start += chunk {
				capture.Write([]byte(input[start:min(start+chunk, len(input))]))
			}
			data, info := capture.Capture()
			if info.Total != int64(len(input)) || info.Total != int64(info.Retained)+info.Omitted {
				t.Fatal("source byte accounting changed", info)
			}
			if !strings.Contains(GenerationStderrText(data, 2048), strings.TrimSpace(cause)) {
				t.Fatal("terminal encoder cause lost after carriage-return progress", string(data))
			}
		})
	}
}

func TestGenerationStderrCarriageReturnsKeepPrivateRecordContext(t *testing.T) {
	const progress = "frame=   10 fps=0.0 q=0.0 size=       0kB time=00:00:00.40 bitrate=   0.0kbits/s speed=0.01x\r"
	for _, private := range []string{
		"Metadata:\n    private_tag:\rSECRET-CONTINUATION\n",
		"api_key=\rSECRET-CONTINUATION\n",
		"-----BEGIN PRIVATE KEY-----\n" + strings.Repeat(progress, 100) + "SECRET-CONTINUATION\r\n-----END PRIVATE KEY-----\n",
	} {
		for _, chunk := range []int{1, 37, 4097} {
			input := strings.Repeat(progress, 100) + private + strings.Repeat(progress, 100) + "terminal cause\n"
			var capture GenerationStderr
			for start := 0; start < len(input); start += chunk {
				capture.Write([]byte(input[start:min(start+chunk, len(input))]))
			}
			data, info := capture.Capture()
			text := GenerationStderrText(data, 2048)
			if strings.Contains(text, "SECRET-CONTINUATION") || !strings.Contains(text, "terminal cause") {
				t.Fatalf("privacy or cause lost: chunk=%d private=%q text=%q", chunk, private, text)
			}
			if info.Total != int64(info.Retained)+info.Omitted || info.Retained > 8192 {
				t.Fatal("source byte accounting or bound changed", info)
			}
		}
	}
}
