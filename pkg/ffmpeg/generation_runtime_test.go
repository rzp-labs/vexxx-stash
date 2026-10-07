package ffmpeg

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runtimeIdentityFixture(t *testing.T) (string, string, intelRuntimePaths) {
	t.Helper()
	root := t.TempDir()
	paths := intelRuntimePaths{SysClassDRM: filepath.Join(root, "sys"), KernelRelease: filepath.Join(root, "kernel"),
		Manifests: filepath.Join(root, "manifests"), IHDDriver: filepath.Join(root, "iHD_drv_video.so"), LibraryDir: filepath.Join(root, "lib")}
	binary, render := filepath.Join(root, "ffmpeg"), filepath.Join(root, "renderD128")
	for _, path := range []string{paths.Manifests, paths.LibraryDir, filepath.Join(paths.SysClassDRM, "renderD128"), filepath.Join(root, "pci", "0000:01:00.0"), filepath.Join(root, "drivers", "xe")} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for path, data := range map[string]string{binary: "binary metadata fixture", render: "device metadata fixture", paths.IHDDriver: "driver fixture", paths.KernelRelease: "6.12.42-unraid\n",
		filepath.Join(root, "pci", "0000:01:00.0", "vendor"): "0x8086\n", filepath.Join(root, "pci", "0000:01:00.0", "device"): "0xe20b\n"} {
		if err := os.WriteFile(path, []byte(data), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(root, "pci", "0000:01:00.0"), filepath.Join(paths.SysClassDRM, "renderD128", "device")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "drivers", "xe"), filepath.Join(root, "pci", "0000:01:00.0", "driver")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ffmpeg-strict-build.json", "ffmpeg-mapping-build.json", "ffmpeg-probe-build.json"} {
		data := fmt.Sprintf(`{"package_version":"8.1.2-r0","replacement_sha256":"%s"}`, strings.Repeat("a", 64))
		if err := os.WriteFile(filepath.Join(paths.Manifests, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return binary, render, paths
}

func TestIntelRuntimeFingerprintSeparatesIdentityChanges(t *testing.T) {
	for _, change := range []string{"binary", "iHD", "kernel", "vendor", "device", "driver", "strict", "mapping", "probe", "vulkan", "placebo"} {
		t.Run(change, func(t *testing.T) {
			binary, render, paths := runtimeIdentityFixture(t)
			before := intelRuntimeFingerprint(binary, render, paths)
			if len(before) != 64 || before != intelRuntimeFingerprint(binary, render, paths) {
				t.Fatal("complete runtime identity was absent or unstable")
			}
			path, data := "", "changed identity"
			switch change {
			case "binary":
				path = binary
			case "iHD":
				path = paths.IHDDriver
			case "kernel":
				path = paths.KernelRelease
			case "vendor", "device":
				path = filepath.Join(paths.SysClassDRM, "renderD128", "device", change)
			case "driver":
				path = filepath.Join(paths.SysClassDRM, "renderD128", "device", "driver")
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				newDriver := filepath.Join(t.TempDir(), "i915")
				if err := os.Mkdir(newDriver, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(newDriver, path); err != nil {
					t.Fatal(err)
				}
				path = ""
			case "strict", "mapping", "probe":
				path = filepath.Join(paths.Manifests, "ffmpeg-"+change+"-build.json")
				data = fmt.Sprintf(`{"package_version":"8.1.2-r1","replacement_sha256":"%s"}`, strings.Repeat("b", 64))
			case "vulkan":
				path = filepath.Join(paths.LibraryDir, "libvulkan.so.1")
			case "placebo":
				path = filepath.Join(paths.LibraryDir, "libplacebo.so.351")
			}
			if path != "" {
				if err := os.WriteFile(path, []byte(data), 0600); err != nil {
					t.Fatal(err)
				}
			}
			after := intelRuntimeFingerprint(binary, render, paths)
			if after == "" || before == after {
				t.Fatal("changed runtime reused prior fingerprint")
			}
		})
	}
}

func TestIntelRuntimeFingerprintRequiresEssentialIdentity(t *testing.T) {
	for _, missing := range []string{"binary", "render", "iHD", "kernel", "vendor", "driver", "manifest", "malformed-manifest", "oversized-manifest", "oversized-kernel"} {
		t.Run(missing, func(t *testing.T) {
			binary, render, paths := runtimeIdentityFixture(t)
			path := map[string]string{"binary": binary, "render": render, "iHD": paths.IHDDriver, "kernel": paths.KernelRelease,
				"vendor": filepath.Join(paths.SysClassDRM, "renderD128", "device", "vendor"), "driver": filepath.Join(paths.SysClassDRM, "renderD128", "device", "driver"),
				"manifest": filepath.Join(paths.Manifests, "ffmpeg-strict-build.json"), "malformed-manifest": filepath.Join(paths.Manifests, "ffmpeg-strict-build.json"),
				"oversized-manifest": filepath.Join(paths.Manifests, "ffmpeg-strict-build.json"), "oversized-kernel": paths.KernelRelease}[missing]
			if strings.HasPrefix(missing, "oversized-") || missing == "malformed-manifest" {
				data := "{}"
				if missing == "oversized-kernel" {
					data = strings.Repeat("k", 4097)
				} else if missing == "oversized-manifest" {
					data = strings.Repeat("m", (64<<10)+1)
				}
				if err := os.WriteFile(path, []byte(data), 0600); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if got := intelRuntimeFingerprint(binary, render, paths); got != "" {
				t.Fatalf("incomplete runtime yielded fingerprint: %s", got)
			}
		})
	}
}

func TestIntelRuntimeFingerprintSeparatesAliasesAndDevices(t *testing.T) {
	binary, render, paths := runtimeIdentityFixture(t)
	baseline := intelRuntimeFingerprint(binary, render, paths)
	alias := filepath.Join(filepath.Dir(binary), "custom-ffmpeg")
	if err := os.Symlink(binary, alias); err != nil {
		t.Fatal(err)
	}
	if alternate := intelRuntimeFingerprint(alias, render, paths); alternate == "" || alternate == baseline {
		t.Fatal("custom binary path shared packaged identity")
	}
	secondRender := filepath.Join(filepath.Dir(render), "renderD129")
	if err := os.WriteFile(secondRender, []byte("second device"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(paths.SysClassDRM, "renderD129"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(paths.SysClassDRM, "renderD128", "device"), filepath.Join(paths.SysClassDRM, "renderD129", "device")); err != nil {
		t.Fatal(err)
	}
	if second := intelRuntimeFingerprint(binary, secondRender, paths); second == "" || second == baseline {
		t.Fatal("distinct render node shared identity")
	}
}

func TestIntelRuntimeFingerprintResolvesActiveLibrarySymlink(t *testing.T) {
	binary, render, paths := runtimeIdentityFixture(t)
	first, second := filepath.Join(paths.LibraryDir, "libvulkan.so.1.2"), filepath.Join(paths.LibraryDir, "libvulkan.so.1.3")
	for _, path := range []string{first, second} {
		if err := os.WriteFile(path, []byte("same library size"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	soname := filepath.Join(paths.LibraryDir, "libvulkan.so.1")
	if err := os.Symlink(first, soname); err != nil {
		t.Fatal(err)
	}
	before := intelRuntimeFingerprint(binary, render, paths)
	if err := os.Remove(soname); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(second, soname); err != nil {
		t.Fatal(err)
	}
	if after := intelRuntimeFingerprint(binary, render, paths); before == "" || after == "" || before == after {
		t.Fatal("active SONAME target changed without invalidating identity")
	}
}

func TestIntelRuntimeFingerprintReturnsEmptyForIncompleteLibraryIdentity(t *testing.T) {
	binary, render, paths := runtimeIdentityFixture(t)
	if err := os.Symlink("missing-library", filepath.Join(paths.LibraryDir, "libvulkan.so.1")); err != nil {
		t.Fatal(err)
	}
	if got := intelRuntimeFingerprint(binary, render, paths); got != "" {
		t.Fatal("broken SONAME target reused an absent-library identity")
	}
}

func TestIntelRuntimeFingerprintDoesNotExecuteOrRequireBinaryContents(t *testing.T) {
	binary, render, paths := runtimeIdentityFixture(t)
	// Executable identity can be obtained even when the file is not a valid
	// program: this helper performs metadata reads, never a capability test.
	if err := os.WriteFile(binary, []byte("not an executable format"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := intelRuntimeFingerprint(binary, render, paths); len(got) != 64 {
		t.Fatal("runtime fingerprint tried to validate executable behavior")
	}
}
