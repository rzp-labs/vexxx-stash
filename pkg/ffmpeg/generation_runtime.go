package ffmpeg

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
)

type intelRuntimePaths struct {
	SysClassDRM, KernelRelease, Manifests, IHDDriver, LibraryDir string
}

// IntelRuntimeFingerprint identifies a runtime for workload/capacity learning.
// It does not test hardware support or capacity, execute commands, or hash
// binaries. Missing essential identity returns an empty result; callers decide
// whether incomplete identity permits reuse of their own observations.
func IntelRuntimeFingerprint(ffmpegPath, renderDevice string) string {
	return intelRuntimeFingerprint(ffmpegPath, renderDevice, intelRuntimePaths{
		SysClassDRM: "/sys/class/drm", KernelRelease: "/proc/sys/kernel/osrelease",
		Manifests: "/usr/share/vexxx", IHDDriver: "/usr/lib/dri/iHD_drv_video.so", LibraryDir: "/usr/lib",
	})
}

func intelRuntimeFingerprint(ffmpegPath, renderDevice string, paths intelRuntimePaths) string {
	parts := []string{"intel-runtime-v1", "configured-ffmpeg:" + ffmpegPath}
	binaryPath := ffmpegPath
	if !strings.ContainsAny(ffmpegPath, `/\`) {
		resolved, err := exec.LookPath(ffmpegPath)
		if err != nil {
			return ""
		}
		binaryPath = resolved
	}
	for _, path := range []string{binaryPath, paths.IHDDriver} {
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			return ""
		}
	}
	for _, path := range []string{binaryPath, renderDevice, paths.IHDDriver} {
		identity, err := runtimeFileIdentity(path)
		if err != nil {
			return ""
		}
		parts = append(parts, identity)
	}
	render, err := filepath.EvalSymlinks(renderDevice)
	if err != nil {
		return ""
	}
	device := filepath.Join(paths.SysClassDRM, filepath.Base(render), "device")
	for _, path := range []string{device, filepath.Join(device, "driver")} {
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			return ""
		}
		parts = append(parts, resolved)
	}
	for _, path := range []string{filepath.Join(device, "vendor"), filepath.Join(device, "device"), paths.KernelRelease} {
		data, err := runtimeReadBounded(path, 4096)
		value := strings.TrimSpace(string(data))
		if err != nil || value == "" {
			return ""
		}
		parts = append(parts, value)
	}
	for _, name := range []string{"ffmpeg-strict-build.json", "ffmpeg-mapping-build.json", "ffmpeg-probe-build.json"} {
		data, err := runtimeReadBounded(filepath.Join(paths.Manifests, name), 64<<10)
		var manifest struct {
			PackageVersion string `json:"package_version"`
			ReplacementSHA string `json:"replacement_sha256"`
		}
		if err != nil || json.Unmarshal(data, &manifest) != nil || manifest.PackageVersion == "" {
			return ""
		}
		digest, err := hex.DecodeString(manifest.ReplacementSHA)
		if err != nil || len(digest) != sha256.Size {
			return ""
		}
		// The bounded manifest digest includes patch/feature identity as well as
		// its declared binary hash, without reading the binary itself.
		parts = append(parts, name+":"+fmt.Sprintf("%x", sha256.Sum256(data)))
	}
	libraries := []string{filepath.Join(paths.LibraryDir, "libvulkan.so.1"), filepath.Join(paths.LibraryDir, "libvulkan_intel.so")}
	placebo, err := runtimePlaceboLibraries(paths.LibraryDir)
	if err != nil {
		return ""
	}
	libraries = append(libraries, placebo...)
	for _, path := range libraries {
		identity, err := runtimeFileIdentity(path)
		if os.IsNotExist(err) {
			if _, linkErr := os.Lstat(path); !os.IsNotExist(linkErr) {
				return ""
			}
			parts = append(parts, path+":absent")
		} else if err != nil {
			return ""
		} else {
			parts = append(parts, identity)
		}
	}
	if len(placebo) == 0 {
		parts = append(parts, "libplacebo:absent")
	}
	data, _ := json.Marshal(parts)
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

// Stat identity is bounded and resolves SONAME symlinks. Configured paths remain
// part of the identity even when two aliases point to the same executable.
func runtimeFileIdentity(path string) (string, error) {
	configured, err := filepath.Abs(path)
	if err != nil || path == "" {
		return "", fmt.Errorf("runtime path unavailable")
	}
	resolved, err := filepath.EvalSymlinks(configured)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	var fileID []string
	// Linux and Darwin expose these fields. Reflection keeps this read-only
	// identity helper buildable on other platforms, where sysfs is unavailable.
	system := reflect.ValueOf(info.Sys())
	if system.Kind() == reflect.Pointer && !system.IsNil() {
		system = system.Elem()
	}
	if system.Kind() == reflect.Struct {
		for _, name := range []string{"Dev", "Ino", "Rdev", "Ctim", "Ctimespec"} {
			field := system.FieldByName(name)
			if field.IsValid() && field.CanInterface() {
				fileID = append(fileID, name+":"+fmt.Sprint(field.Interface()))
			}
		}
	}
	data, err := json.Marshal([]any{configured, resolved, info.Size(), uint32(info.Mode()), info.ModTime().UnixNano(), fileID})
	return string(data), err
}

func runtimeReadBounded(path string, limit int64) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("runtime identity is not a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err = file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("runtime identity is not a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, fmt.Errorf("runtime identity exceeds read bound")
	}
	return data, nil
}

func runtimePlaceboLibraries(directory string) ([]string, error) {
	file, err := os.Open(directory)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var result []string
	// Bound directory discovery and retained candidates independently.
	for scanned := 0; scanned < 16384; scanned += 128 {
		names, err := file.Readdirnames(128)
		for _, name := range names {
			if name == "libplacebo.so" || strings.HasPrefix(name, "libplacebo.so.") {
				result = append(result, filepath.Join(directory, name))
				if len(result) > 32 {
					return nil, fmt.Errorf("runtime library discovery exceeds bound")
				}
			}
		}
		if err == io.EOF {
			sort.Strings(result)
			return result, nil
		}
		if err != nil {
			return nil, err
		}
	}
	return nil, fmt.Errorf("runtime library directory exceeds bound")
}
