package generationbudget

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"testing"
)

func TestProcessResourcesSeparateRegionsAndDeduplicateClients(t *testing.T) {
	client := "drm-driver: xe\ndrm-pdev: 0000:03:00.0\ndrm-client-id: 7\ndrm-total-vram0: 64 MiB\ndrm-resident-vram0: 32 MiB\ndrm-total-gtt: 4 MiB\ndrm-total-system: 2 MiB\n"
	r := processResources("VmHWM: 8192 kB\nVmRSS: 4096 kB\n", []string{client, client})
	if r.Memory != 14<<20 || r.GPU != 32<<20 {
		t.Fatal("duplicate descriptors or total/resident were double-counted", r)
	}
	if r := processResources("", []string{"drm-driver: xe\ndrm-total-vram0: 99 MiB\n"}); r.Memory != -1 || r.GPU != -1 {
		t.Fatal("unidentified/missing process counters became known", r)
	}
	for _, value := range []string{"-1", "17 ns", "9223372036854775807 MiB", "max"} {
		if byteCounter(value) != -1 {
			t.Fatal("invalid counter", value)
		}
	}
}

func TestMeasureAccountsOnlyRegisteredChildDespiteHostMemoryChange(t *testing.T) {
	b := newAdaptive(Settings{}, func() Resources { return Resources{CPUs: 4, MemoryAvailable: 6 << 30, GPUAvailable: -1} })
	b.processResources = func(pid int) ProcessResources {
		if pid != 101 {
			t.Fatal("read an unrelated process", pid)
		}
		return ProcessResources{Memory: 64 << 20, GPU: 256 << 20}
	}
	s, err := b.Measure(context.Background(), Workload{Key: "own"}, func(ctx context.Context) error { RecordProcess(ctx, 101); return nil })
	if err != nil || !s.MemoryKnown || !s.GPUKnown || s.MemoryPeak != 64<<20 || s.GPUPeak != 256<<20 {
		t.Fatal("owned process accounting", s, err)
	}
}

func TestProcessMemoryChild(t *testing.T) {
	if os.Getenv("VEXXX_TEST_PROCESS_MEMORY") != "1" {
		return
	}
	buffer := make([]byte, 32<<20)
	for i := 0; i < len(buffer); i += 4096 {
		buffer[i] = 1
	}
	runtime.KeepAlive(buffer)
	os.Exit(0)
}

func TestMeasureCapturesShortChildPeakAtWait(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("wait4 RSS accounting")
	}
	b := newAdaptive(Settings{}, func() Resources { return Resources{CPUs: 4, MemoryAvailable: 6 << 30, GPUAvailable: -1} })
	// Even if no /proc sample is possible, completed child high-water RSS is real.
	b.processResources = func(int) ProcessResources { return ProcessResources{Memory: -1, GPU: -1} }
	s, err := b.Measure(context.Background(), Workload{Key: "own"}, func(ctx context.Context) error {
		cmd := exec.Command(os.Args[0], "-test.run=^TestProcessMemoryChild$")
		cmd.Env = append(os.Environ(), "VEXXX_TEST_PROCESS_MEMORY=1")
		if err := cmd.Start(); err != nil {
			return err
		}
		RecordProcess(ctx, cmd.Process.Pid)
		err := cmd.Wait()
		RecordProcessResult(ctx, cmd.Process.Pid, cmd.ProcessState)
		return err
	})
	if err != nil || !s.MemoryKnown || s.MemoryPeak < 32<<20 || s.GPUKnown {
		t.Fatal("short child peak lost or absent GPU counter invented", s, err)
	}
}
