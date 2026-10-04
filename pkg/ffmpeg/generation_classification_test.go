package ffmpeg

import (
	"testing"

	"github.com/stashapp/stash/pkg/generationbudget"
)

// Check the actual codec/device/filter builders without requiring any device.
// Generation accepts caller-supplied FFmpeg arguments using these same names.
func TestGenerationClassifiesHardwareCommands(t *testing.T) {
	f := &FFMpeg{version: Version{major: 7}}
	for _, codec := range []VideoCodec{
		VideoCodecN264, VideoCodecN264H, VideoCodecI264, VideoCodecI264C,
		VideoCodecA264, VideoCodecM264, VideoCodecV264, VideoCodecR264,
		VideoCodecO264, VideoCodecIVP9, VideoCodecVVP9, VideoCodecVVPX, VideoCodecRK264,
	} {
		t.Run(codec.Name, func(t *testing.T) {
			if generationbudget.ClassifyFFMpeg(codec.Args()) != generationbudget.GPU {
				t.Errorf("hardware encoder classified CPU: %v", codec.Args())
			}
			for _, fullHW := range []bool{false, true} {
				if args := f.hwDeviceInit(nil, codec, fullHW); len(args) > 0 {
					if generationbudget.ClassifyFFMpeg(args) != generationbudget.GPU {
						t.Errorf("hardware device/decode classified CPU: %v", args)
					}
				}
				if args := f.hwFilterInit(codec, fullHW).Args(); len(args) > 0 {
					// AMF's non-fullHW filter only converts software pixel format.
					if codec != VideoCodecA264 && generationbudget.ClassifyFFMpeg(args) != generationbudget.GPU {
						t.Errorf("hardware filter classified CPU: %v", args)
					}
				}
			}
		})
	}
}
