package ffmpeg

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestIntelStrictWorkNeverFallsBack(t *testing.T) {
	for _, stage := range []string{"device", "decode", "filter", "encode", "generation", "none"} {
		t.Run(stage, func(t *testing.T) {
			cause := errors.New("capability unavailable")
			p := IntelGenerationPlan{Config: IntelGenerationConfig{Backend: "vaapi", Device: "/dev/dri/renderD128"}, Probes: []IntelProbeStep{{Stage: "decode", Args: Args{"decode"}}, {Stage: "filter", Args: Args{"filter"}}, {Stage: "encode", Args: Args{"encode"}}}}
			hardwareCalls := 0
			d, err := runIntelGenerationWork(context.Background(), p, func(context.Context) error {
				hardwareCalls++
				if stage == "generation" {
					return cause
				}
				return nil
			}, nil, func(_ context.Context, args Args) error {
				if args[0] == stage {
					return cause
				}
				return nil
			}, func(string) error {
				if stage == "device" {
					return cause
				}
				return nil
			})
			if stage == "none" {
				if err != nil || d.Actual != "vaapi" || hardwareCalls != 1 {
					t.Fatalf("%+v %v calls%d", d, err, hardwareCalls)
				}
				return
			}
			if !errors.Is(err, cause) || d.Actual != "none" || d.Stage != stage || !strings.Contains(err.Error(), "software rendering disabled") {
				t.Fatalf("%+v %v", d, err)
			}
			if stage != "generation" && hardwareCalls != 0 {
				t.Fatal("hardware started after failed capability probe")
			}
		})
	}
}
