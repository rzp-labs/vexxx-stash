package main

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// validateAnimatedWebP checks the RIFF/animation structure and each VP8L header,
// following https://developers.google.com/speed/webp/docs/riff_container.
// It does not decode pixels or claim visual/playback acceptance.
func validateAnimatedWebP(data []byte) (map[string]any, error) {
	failed := map[string]any{"status": "failed"}
	if len(data) < 12 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WEBP" || uint64(binary.LittleEndian.Uint32(data[4:8]))+8 != uint64(len(data)) {
		return failed, errors.New("invalid WebP RIFF signature/container size")
	}
	width, height, frames, duration, loop := 0, 0, 0, 0, -1
	extended, animation := false, false
	err := walkWebPChunks(data[12:], func(name string, payload []byte) error {
		switch name {
		case "VP8X":
			if extended || animation || frames != 0 || len(payload) != 10 || payload[0]&2 == 0 || payload[0]&0xc1 != 0 || payload[1] != 0 || payload[2] != 0 || payload[3] != 0 {
				return errors.New("invalid animated VP8X header/order")
			}
			extended = true
			width, height = webPUint24(payload[4:7])+1, webPUint24(payload[7:10])+1
			if width != 640 || height != 360 {
				return fmt.Errorf("unexpected WebP canvas %dx%d; want640x360", width, height)
			}
		case "ANIM":
			if !extended || animation || frames != 0 || len(payload) != 6 {
				return errors.New("invalid ANIM header/order")
			}
			animation = true
			loop = int(binary.LittleEndian.Uint16(payload[4:6]))
		case "ANMF":
			if !animation || len(payload) < 16 || payload[15]&0xfc != 0 {
				return errors.New("invalid ANMF header/order")
			}
			x, y := webPUint24(payload[:3])*2, webPUint24(payload[3:6])*2
			fw, fh := webPUint24(payload[6:9])+1, webPUint24(payload[9:12])+1
			ms := webPUint24(payload[12:15])
			if x+fw > width || y+fh > height || ms <= 0 {
				return errors.New("ANMF bounds/duration invalid")
			}
			lossless := 0
			if err := walkWebPChunks(payload[16:], func(name string, bits []byte) error {
				if name == "VP8 " {
					return errors.New("lossy VP8 animation frame rejected")
				}
				if name != "VP8L" {
					return fmt.Errorf("unexpected lossless-frame subchunk %q", name)
				}
				if lossless != 0 || len(bits) < 5 || bits[0] != 0x2f {
					return errors.New("invalid VP8L frame header")
				}
				header := binary.LittleEndian.Uint32(bits[1:5])
				if header>>29 != 0 || int(header&0x3fff)+1 != fw || int((header>>14)&0x3fff)+1 != fh {
					return errors.New("VP8L dimensions/version do not match ANMF frame")
				}
				lossless++
				return nil
			}); err != nil {
				return err
			}
			if lossless != 1 {
				return errors.New("ANMF missing VP8L frame")
			}
			frames++
			duration += ms
		case "VP8 ", "VP8L", "ALPH":
			return fmt.Errorf("unexpected top-level frame chunk %q", name)
		}
		return nil
	})
	if err != nil {
		return failed, err
	}
	if !extended || !animation || frames != 60 || duration != 5000 || loop != 0 {
		return failed, fmt.Errorf("unexpected animation: frames%d duration%dms loop%d; want60/5000/0", frames, duration, loop)
	}
	return map[string]any{"status": "passed", "bytes": len(data), "width": width, "height": height, "frames": frames, "duration_ms": duration, "loop": loop, "all_frames": "VP8L lossless; no VP8 lossy frames", "container_size": "exact", "visual": "untested", "playback": "untested; container/bitstream headers checked only", "encoder_semantics": "lossless1/compression6; presetnone avoids resetting encoder configuration", "compatibility": "corrects legacy presetdefault lossy-output bug; bitstream compatibility with those older outputs is not claimed"}, nil
}

func webPUint24(b []byte) int { return int(b[0]) | int(b[1])<<8 | int(b[2])<<16 }

func walkWebPChunks(data []byte, visit func(string, []byte) error) error {
	for len(data) > 0 {
		if len(data) < 8 {
			return errors.New("truncated WebP chunk header")
		}
		size := uint64(binary.LittleEndian.Uint32(data[4:8]))
		padded := size + (size & 1)
		if padded > uint64(len(data)-8) {
			return errors.New("truncated WebP chunk payload/padding")
		}
		if size&1 != 0 && data[8+int(size)] != 0 {
			return errors.New("nonzero WebP chunk padding")
		}
		if err := visit(string(data[:4]), data[8:8+int(size)]); err != nil {
			return err
		}
		data = data[8+int(padded):]
	}
	return nil
}
