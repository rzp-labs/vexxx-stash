package ffmpeg

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func intelTestSource() IntelSource {
	return IntelSource{Codec: "h264", PixelFormat: "yuv420p", Width: 1920, Height: 1080, StreamIndex: 0}
}
func TestIntelPlanCommands(t *testing.T) {
	for _, backend := range []string{"vaapi", "qsv"} {
		t.Run(backend, func(t *testing.T) {
			p, err := NewIntelGenerationPlan(IntelGenerationConfig{Backend: backend, Device: "/dev/dri/renderD128"}, intelTestSource(), "fixture.mp4", 2.75, 640, true)
			if err != nil {
				t.Fatal(err)
			}
			if p.Filter != "scale_"+backend+"=w=640:h=360:format=nv12,hwdownload,format=nv12" {
				t.Fatal(p.Filter)
			}
			stages := []string{}
			for _, s := range p.Probes {
				stages = append(stages, s.Stage)
				a := strings.Join(s.Args, " ")
				if !strings.Contains(a, "-frames:v 1") || !strings.Contains(a, "/dev/dri/renderD128") || !strings.Contains(a, "-abort_on empty_output") {
					t.Fatalf("unbounded/unselected probe: %s", a)
				}
			}
			if !reflect.DeepEqual(stages, []string{"decode", "scale", "download", "encode"}) {
				t.Fatal(stages)
			}
			decode := strings.Join(p.Probes[0].Args, " ")
			if !strings.Contains(decode, "-ss 2.75") || strings.Contains(decode, "-vf") {
				t.Fatal(decode)
			}
			if backend == "qsv" && !strings.Contains(decode, "-c:v h264_qsv") {
				t.Fatal(decode)
			}
		})
	}
}
func TestIntelEligibility(t *testing.T) {
	cases := []struct {
		name string
		edit func(*IntelSource)
	}{
		{"codec", func(s *IntelSource) { s.Codec = "vp9" }},
		{"10bit", func(s *IntelSource) { s.PixelFormat = "yuv420p10le" }},
		{"rotation", func(s *IntelSource) { s.Rotation = 180 }},
		{"pq", func(s *IntelSource) { s.ColorTransfer = "smpte2084" }},
		{"hlg", func(s *IntelSource) { s.ColorTransfer = "arib-std-b67" }},
		{"widegamut", func(s *IntelSource) { s.ColorPrimaries = "bt2020" }},
		{"missingdimensions", func(s *IntelSource) { s.Width = 0 }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := intelTestSource()
			c.edit(&s)
			if _, err := NewIntelGenerationPlan(IntelGenerationConfig{Backend: "vaapi"}, s, "fixture", 0, 640, false); err == nil {
				t.Fatal("unsupported source accepted")
			}
		})
	}
	if _, err := NewIntelGenerationPlan(IntelGenerationConfig{Backend: "auto"}, intelTestSource(), "fixture", 0, 640, false); err == nil {
		t.Fatal("invalid backend accepted")
	}
}
func TestIntelDeviceSelection(t *testing.T) {
	for _, d := range []string{"", "/dev/dri/card0", "/dev/null", "/dev/dri/renderD99999", filepath.Join(t.TempDir(), "renderD128")} {
		if err := ValidateIntelDevice(d); err == nil {
			t.Fatalf("accepted %q", d)
		}
	}
}
func TestIntelStagesFallbackAndCancel(t *testing.T) {
	for _, failure := range []string{"device", "decode", "scale", "download", "encode", "generation", "none"} {
		t.Run(failure, func(t *testing.T) {
			p, _ := NewIntelGenerationPlan(IntelGenerationConfig{Backend: "vaapi", Device: "/dev/dri/renderD128"}, intelTestSource(), "fixture", 0, 640, true)
			calls := 0
			hardwareCalls := 0
			softwareCalls := 0
			probe := func(ctx context.Context, args Args) error {
				calls++
				if failure == p.Probes[calls-1].Stage {
					return errors.New("unsupported stage")
				}
				return nil
			}
			hw := func(context.Context) error {
				hardwareCalls++
				if failure == "generation" {
					return errors.New("generation failed")
				}
				return nil
			}
			sw := func(context.Context) error { softwareCalls++; return nil }
			check := func(string) error {
				if failure == "device" {
					return os.ErrPermission
				}
				return nil
			}
			d, err := runIntelGenerationWork(context.Background(), p, hw, sw, probe, check)
			if err != nil {
				t.Fatal(err)
			}
			if failure == "none" {
				if softwareCalls != 0 || hardwareCalls != 1 || d.Actual != "vaapi" {
					t.Fatalf("%+v hw%d sw%d", d, hardwareCalls, softwareCalls)
				}
			} else {
				if softwareCalls != 1 || d.Actual != "software" || d.Stage != failure || d.Reason == "" {
					t.Fatalf("%+v sw%d", d, softwareCalls)
				}
			}
		})
	}
	for _, stage := range []string{"before", "probe", "generation"} {
		t.Run("cancel_"+stage, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			p, _ := NewIntelGenerationPlan(IntelGenerationConfig{Backend: "qsv"}, intelTestSource(), "fixture", 0, 640, false)
			sw := 0
			if stage == "before" {
				cancel()
			}
			probe := func(context.Context, Args) error {
				if stage == "probe" {
					cancel()
					return context.Canceled
				}
				return nil
			}
			hw := func(context.Context) error { cancel(); return context.Canceled }
			_, err := runIntelGenerationWork(ctx, p, hw, func(context.Context) error { sw++; return nil }, probe, func(string) error { return nil })
			if !errors.Is(err, context.Canceled) || sw != 0 {
				t.Fatalf("err=%v fallback=%d", err, sw)
			}
		})
	}
}
func TestIntelProbeTimeoutFallback(t *testing.T) {
	p, _ := NewIntelGenerationPlan(IntelGenerationConfig{Backend: "vaapi", ProbeTimeout: time.Millisecond}, intelTestSource(), "fixture", 0, 640, false)
	sw := 0
	d, err := runIntelGenerationWork(context.Background(), p, func(context.Context) error { t.Fatal("hardware after failed probe"); return nil }, func(context.Context) error { sw++; return nil }, func(ctx context.Context, _ Args) error {
		execCtx, cancel := IntelProbeExecutionContext(ctx)
		defer cancel()
		<-execCtx.Done()
		return execCtx.Err()
	}, func(string) error { return nil })
	if err != nil || sw != 1 || d.Stage != "decode" {
		t.Fatalf("%+v err=%v sw=%d", d, err, sw)
	}
}
func TestIntelFallbackFailureNotRetried(t *testing.T) {
	p, _ := NewIntelGenerationPlan(IntelGenerationConfig{Backend: "vaapi"}, intelTestSource(), "fixture", 0, 640, false)
	sw := 0
	want := errors.New("cpu failure")
	_, err := runIntelGenerationWork(context.Background(), p, func(context.Context) error { return nil }, func(context.Context) error { sw++; return want }, func(context.Context, Args) error { return errors.New("decode failure") }, func(string) error { return nil })
	if !errors.Is(err, want) || sw != 1 {
		t.Fatalf("err=%v sw=%d", err, sw)
	}
}

func TestIntelRuntimeDiagnosticIncludesStderr(t *testing.T) {
	err := fmt.Errorf("runner: %w", &exec.ExitError{Stderr: []byte("Error while opening encoder: unsupported profile")})
	if got := intelFailureReason(err); !strings.Contains(got, "unsupported profile") {
		t.Fatal(got)
	}
	if got := intelFailureStage(err); got != "encode" {
		t.Fatal(got)
	}
	long := &exec.ExitError{Stderr: []byte(strings.Repeat("x", 8192))}
	if got := intelFailureReason(long); len(got) > 4300 || !strings.Contains(got, "truncated") {
		t.Fatal(len(got))
	}
}

func TestIntelProbeTimeoutExcludesQueueWait(t *testing.T) {
	p, _ := NewIntelGenerationPlan(IntelGenerationConfig{Backend: "vaapi", ProbeTimeout: time.Millisecond}, intelTestSource(), "fixture", 0, 640, false)
	calls := 0
	d, err := runIntelGenerationWork(context.Background(), p, func(context.Context) error { return nil }, func(context.Context) error { t.Fatal("queue wait caused fallback"); return nil }, func(ctx context.Context, _ Args) error {
		calls++
		// Simulate admission delay before execution context exists.
		time.Sleep(3 * time.Millisecond)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		execCtx, cancel := IntelProbeExecutionContext(ctx)
		defer cancel()
		return execCtx.Err()
	}, func(string) error { return nil })
	if err != nil || d.Actual != "vaapi" || calls != len(p.Probes) {
		t.Fatalf("%+v err=%v calls=%d", d, err, calls)
	}
}

// Exercise the production probes' null-output contract with real FFmpeg and a
// bounded CPU fixture. Device/decode/filter choices alone are projected to CPU;
// seek, stream mapping, frame bound, output and empty-output guard are unchanged.
func TestIntelProbeRejectsEmptyOutput(t *testing.T) {
	bin, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	input := filepath.Join(t.TempDir(), "probe.mkv")
	if output, err := exec.CommandContext(ctx, bin, "-v", "error", "-nostdin", "-f", "lavfi", "-i", "color=size=64x64:rate=5", "-t", "1", "-c:v", "ffv1", input).CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v: %s", err, output)
	}
	for _, backend := range []string{"vaapi", "qsv"} {
		for _, start := range []float64{0, 30} {
			p, err := NewIntelGenerationPlan(IntelGenerationConfig{Backend: backend}, intelTestSource(), input, start, 64, true)
			if err != nil {
				t.Fatal(err)
			}
			for _, probe := range p.Probes {
				if probe.Stage == "encode" {
					continue // Synthetic encode is not affected by the source seek.
				}
				t.Run(fmt.Sprintf("%s/%s/seek_%g", backend, probe.Stage, start), func(t *testing.T) {
					p.Probes = []IntelProbeStep{probe}
					hw, sw := 0, 0
					d, err := runIntelGenerationWork(ctx, p,
						func(context.Context) error { hw++; return nil },
						func(context.Context) error { sw++; return nil },
						func(ctx context.Context, args Args) error {
							var cpu Args
							for i := 0; i < len(args); i++ {
								switch args[i] {
								case "-init_hw_device", "-filter_hw_device", "-hwaccel", "-hwaccel_device", "-hwaccel_output_format", "-c:v":
									i++
								case "-vf":
									i++
									cpu = append(cpu, "-vf", "scale=64:64")
								default:
									cpu = append(cpu, args[i])
								}
							}
							output, err := exec.CommandContext(ctx, bin, cpu...).CombinedOutput()
							if err != nil {
								return fmt.Errorf("probe: %w: %s", err, output)
							}
							return nil
						}, func(string) error { return nil })
					if err != nil {
						t.Fatal(err)
					}
					if start == 0 {
						if hw != 1 || sw != 0 || d.Actual != backend {
							t.Fatalf("nonempty probe rejected: %+v hw=%d sw=%d", d, hw, sw)
						}
					} else if hw != 0 || sw != 1 || d.Actual != "software" || d.Stage != probe.Stage || !strings.Contains(d.Reason, "empty") {
						t.Fatalf("empty probe accepted or wrong fallback: %+v hw=%d sw=%d", d, hw, sw)
					}
				})
			}
		}
	}
}

func TestIntelOutputFailureFallbackOnce(t *testing.T) {
	for _, validCPU := range []bool{false, true} {
		p, _ := NewIntelGenerationPlan(IntelGenerationConfig{Backend: "vaapi"}, intelTestSource(), "fixture", 0, 640, false)
		hw, sw := 0, 0
		d, err := runIntelGenerationWork(context.Background(), p, func(context.Context) error { hw++; return &GenerationOutputError{Err: errors.New("no video packets")} }, func(context.Context) error {
			sw++
			if validCPU {
				return nil
			}
			return &GenerationOutputError{Err: errors.New("fallback has no video packets")}
		}, func(context.Context, Args) error { return nil }, func(string) error { return nil })
		if hw != 1 || sw != 1 || d.Stage != "output" || d.Actual != "software" || (err == nil) != validCPU {
			t.Fatalf("%+v err=%v hw=%d sw=%d", d, err, hw, sw)
		}
	}
}
