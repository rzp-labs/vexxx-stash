package generationbudget

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// Resources reports currently available headroom, not a concurrency guarantee.
// Negative memory means the kernel/driver does not expose that measurement.
type Resources struct {
	CPUs                          int
	MemoryAvailable, GPUAvailable int64
}

// DetectResources uses Go's quota/affinity-aware execution capacity, Linux
// memory/cgroup headroom and the selected DRM device's driver memory counters.
// No subprocess, benchmark, or privileged device mutation is required.
func DetectResources(device string) Resources {
	r := Resources{CPUs: runtime.GOMAXPROCS(0), MemoryAvailable: -1, GPUAvailable: -1}
	data, _ := os.ReadFile("/proc/meminfo")
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "MemAvailable:" {
			value, err := strconv.ParseInt(fields[1], 10, 64)
			if err == nil {
				r.MemoryAvailable = value * 1024
			}
		}
	}
	// Resolve the process's controller mount/path, including nested v1 groups
	// and cgroup namespaces. Both the child and ancestor limits constrain headroom.
	groups, _ := os.ReadFile("/proc/self/cgroup")
	mounts, _ := os.ReadFile("/proc/self/mountinfo")
	if available := cgroupMemoryAvailable(string(groups), string(mounts), readCounter); available >= 0 {
		r.MemoryAvailable = lesserKnown(r.MemoryAvailable, available)
	}
	if device != "" {
		root := filepath.Join("/sys/class/drm", filepath.Base(device), "device")
		total, used := readCounter(filepath.Join(root, "mem_info_vram_total")), readCounter(filepath.Join(root, "mem_info_vram_used"))
		if total > 0 && used >= 0 {
			r.GPUAvailable = max(int64(0), total-used)
		}
	}
	return r
}

func readCounter(path string) int64 {
	value, err := os.ReadFile(path)
	if err != nil {
		return -1
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(value)), 10, 64)
	if err != nil {
		return -1
	}
	return n
}
func lesserKnown(current, next int64) int64 {
	if current < 0 {
		return next
	}
	return min(current, next)
}

// Resolve preserves explicit limits and sizes only Auto fields. GPU starts at
// one until the actual source/filter/encoder workload proves higher capacity.
func (s Settings) Resolve(r Resources) Settings {
	cpus := max(1, r.CPUs)
	processes := cpus
	if r.MemoryAvailable >= 0 {
		processes = min(processes, max(1, int(r.MemoryAvailable/(256<<20))))
	}
	if s.MaxProcesses == 0 {
		s.MaxProcesses = max(s.MaxGPUProcesses, processes)
	}
	if s.MaxGPUProcesses == 0 {
		s.MaxGPUProcesses = 1
	}
	if s.Threads == 0 {
		s.Threads = max(1, cpus/s.MaxProcesses)
	}
	return s
}

func cgroupMemoryAvailable(groups, mountInfo string, counter func(string) int64) int64 {
	available := int64(-1)
	unescape := strings.NewReplacer("\\040", " ", "\\011", "\t", "\\012", "\n", "\\134", "\\")
	for _, groupLine := range strings.Split(groups, "\n") {
		group := strings.SplitN(groupLine, ":", 3)
		if len(group) != 3 {
			continue
		}
		unified := group[0] == "0" && group[1] == ""
		if !unified && !commaContains(group[1], "memory") {
			continue
		}
		for _, mountLine := range strings.Split(mountInfo, "\n") {
			fields := strings.Fields(mountLine)
			separator := -1
			for i, field := range fields {
				if field == "-" {
					separator = i
					break
				}
			}
			if separator < 6 || separator+3 >= len(fields) {
				continue
			}
			fsType := fields[separator+1]
			if unified && fsType != "cgroup2" || !unified && (fsType != "cgroup" || !commaContains(fields[separator+3], "memory")) {
				continue
			}
			root, mount := filepath.Clean(unescape.Replace(fields[3])), filepath.Clean(unescape.Replace(fields[4]))
			path := filepath.Clean(unescape.Replace(group[2]))
			relative := strings.TrimPrefix(path, "/")
			if root != "/" && path != "/" {
				if path != root && !strings.HasPrefix(path, root+"/") {
					continue
				}
				relative = strings.TrimPrefix(strings.TrimPrefix(path, root), "/")
			}
			current := filepath.Join(mount, relative)
			limitName, usedName := "memory.limit_in_bytes", "memory.usage_in_bytes"
			if unified {
				limitName, usedName = "memory.max", "memory.current"
			}
			for {
				limit, used := counter(filepath.Join(current, limitName)), counter(filepath.Join(current, usedName))
				if limit >= 0 && used >= 0 {
					available = lesserKnown(available, max(int64(0), limit-used))
				}
				if current == mount {
					break
				}
				parent := filepath.Dir(current)
				if parent == current || parent != mount && !strings.HasPrefix(parent, mount+string(filepath.Separator)) {
					break
				}
				current = parent
			}
		}
	}
	return available
}
func commaContains(list, value string) bool {
	for _, entry := range strings.Split(list, ",") {
		if entry == value {
			return true
		}
	}
	return false
}
