package main

import (
	"encoding/binary"
	"testing"
)

func webPTestChunk(name string, payload []byte) []byte {
	data := make([]byte, 8+len(payload)+(len(payload)&1))
	copy(data, name)
	binary.LittleEndian.PutUint32(data[4:8], uint32(len(payload)))
	copy(data[8:], payload)
	return data
}

func putWebP24(dst []byte, value int) {
	dst[0] = byte(value)
	dst[1] = byte(value >> 8)
	dst[2] = byte(value >> 16)
}

// Structural fixture only: the validator checks headers and never claims pixel
// decoding. Real generator bitstreams are verified by the CT102 driver runs.
func webPTestAnimation(frames, duration, loop int, lossy, badBounds, badDimensions bool) []byte {
	extended := make([]byte, 10)
	extended[0] = 2
	putWebP24(extended[4:7], 639)
	putWebP24(extended[7:10], 359)
	chunks := webPTestChunk("VP8X", extended)
	anim := make([]byte, 6)
	binary.LittleEndian.PutUint16(anim[4:6], uint16(loop))
	chunks = append(chunks, webPTestChunk("ANIM", anim)...)
	for i := 0; i < frames; i++ {
		frame := make([]byte, 16)
		putWebP24(frame[6:9], 639)
		putWebP24(frame[9:12], 359)
		ms := duration / frames
		if i < duration%frames {
			ms++
		}
		putWebP24(frame[12:15], ms)
		if badBounds && i == 0 {
			putWebP24(frame[:3], 1)
		}
		bits := make([]byte, 6)
		bits[0] = 0x2f
		header := uint32(639 | (359 << 14))
		if badDimensions && i == 0 {
			header++
		}
		binary.LittleEndian.PutUint32(bits[1:5], header)
		kind := "VP8L"
		if lossy && i == 0 {
			kind = "VP8 "
		}
		frame = append(frame, webPTestChunk(kind, bits)...)
		chunks = append(chunks, webPTestChunk("ANMF", frame)...)
	}
	data := make([]byte, 12)
	copy(data, "RIFF")
	copy(data[8:], "WEBP")
	data = append(data, chunks...)
	binary.LittleEndian.PutUint32(data[4:8], uint32(len(data)-8))
	return data
}

func TestAnimatedWebPRequiredSemantics(t *testing.T) {
	valid := webPTestAnimation(60, 5000, 0, false, false, false)
	report, err := validateAnimatedWebP(valid)
	if err != nil {
		t.Fatal(err)
	}
	if report["frames"] != 60 || report["duration_ms"] != 5000 || report["loop"] != 0 {
		t.Fatalf("unexpected report%v", report)
	}
	for name, data := range map[string][]byte{
		"frame_count": webPTestAnimation(59, 5000, 0, false, false, false),
		"duration":    webPTestAnimation(60, 4999, 0, false, false, false),
		"loop":        webPTestAnimation(60, 5000, 1, false, false, false),
		"lossy":       webPTestAnimation(60, 5000, 0, true, false, false),
		"bounds":      webPTestAnimation(60, 5000, 0, false, true, false),
		"dimensions":  webPTestAnimation(60, 5000, 0, false, false, true),
		"truncated":   valid[:len(valid)-1],
		"trailing":    append(append([]byte{}, valid...), 0),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := validateAnimatedWebP(data); err == nil {
				t.Fatal("invalid animation accepted")
			}
		})
	}
}

func TestWebPChunkPaddingAndBounds(t *testing.T) {
	valid := webPTestChunk("TEST", []byte{1})
	if err := walkWebPChunks(valid, func(string, []byte) error { return nil }); err != nil {
		t.Fatal(err)
	}
	valid[len(valid)-1] = 1
	if err := walkWebPChunks(valid, func(string, []byte) error { return nil }); err == nil {
		t.Fatal("nonzero padding accepted")
	}
	if err := walkWebPChunks([]byte("TEST\xff\xff\xff\xff"), func(string, []byte) error { return nil }); err == nil {
		t.Fatal("oversized chunk accepted")
	}
}

func FuzzAnimatedWebPBounds(f *testing.F) {
	f.Add(webPTestAnimation(60, 5000, 0, false, false, false))
	f.Add([]byte("RIFF"))
	f.Fuzz(func(t *testing.T, data []byte) { _, _ = validateAnimatedWebP(data) })
}
