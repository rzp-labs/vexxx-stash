package generationbudget

import (
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ProcessResources accounts only the registered FFmpeg child. Negative means
// unobservable; a missing fdinfo key must never be interpreted as zero GPU use.
type ProcessResources struct{ Memory, GPU int64 }

func DetectProcessResources(pid int) ProcessResources {
	root := filepath.Join("/proc", strconv.Itoa(pid))
	status, _ := os.ReadFile(filepath.Join(root, "status"))
	entries, _ := os.ReadDir(filepath.Join(root, "fdinfo"))
	var clients []string
	for _, entry := range entries {
		if data, err := os.ReadFile(filepath.Join(root, "fdinfo", entry.Name())); err == nil {
			clients = append(clients, string(data))
		}
	}
	return processResources(string(status), clients)
}

// Standard DRM counters are documented at docs.kernel.org/gpu/drm-usage-stats.html.
// Xe exposes vram0/system/gtt regions. De-duplicate drm-client-id plus drm-pdev;
// duplicated descriptors must not multiply allocations. Resident bytes take
// precedence over possibly unbacked total bytes; cross-client shared buffers are conservatively
// included. Host RSS plus DRM system allocations is an upper bound, not exact RSS.
func processResources(status string, clients []string) ProcessResources {
	r := ProcessResources{Memory: -1, GPU: -1}
	for _, line := range strings.Split(status, "\n") {
		key, value, _ := strings.Cut(line, ":")
		if key == "VmHWM" || key == "VmRSS" {
			if n := byteCounter(value); n >= 0 {
				r.Memory = max(r.Memory, n)
			}
		}
	}
	seen := map[string]bool{}
	for _, client := range clients {
		fields := map[string]string{}
		for _, line := range strings.Split(client, "\n") {
			key, value, found := strings.Cut(line, ":")
			if found {
				fields[key] = strings.TrimSpace(value)
			}
		}
		if fields["drm-client-id"] == "" || fields["drm-driver"] == "" {
			continue
		}
		id := fields["drm-driver"] + "/" + fields["drm-pdev"] + "/" + fields["drm-client-id"]
		if seen[id] {
			continue
		}
		seen[id] = true
		type regionUsage struct{ total, resident int64 }
		regions := map[string]regionUsage{}
		for key, value := range fields {
			for _, prefix := range []string{"drm-total-", "drm-resident-", "drm-memory-"} {
				if region, found := strings.CutPrefix(key, prefix); found {
					if n := byteCounter(value); n >= 0 {
						usage, present := regions[region]
						if !present {
							usage = regionUsage{total: -1, resident: -1}
						}
						if prefix == "drm-total-" {
							usage.total = n
						} else {
							usage.resident = max(usage.resident, n)
						}
						regions[region] = usage
					}
				}
			}
		}
		for region, usage := range regions {
			bytes := usage.resident
			if bytes < 0 {
				bytes = usage.total
			}
			if strings.HasPrefix(region, "vram") || region == "local" {
				r.GPU = saturatingAdd(max(int64(0), r.GPU), bytes)
			}
			if region == "system" || region == "gtt" || region == "memory" {
				r.Memory = saturatingAdd(max(int64(0), r.Memory), bytes)
			}
		}
	}
	return r
}

func byteCounter(value string) int64 {
	parts := strings.Fields(value)
	if len(parts) < 1 || len(parts) > 2 {
		return -1
	}
	n, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || n < 0 {
		return -1
	}
	unit := int64(1)
	if len(parts) == 2 {
		switch parts[1] {
		case "kB", "KiB":
			unit = 1024
		case "MiB":
			unit = 1 << 20
		default:
			return -1
		}
	}
	if n > math.MaxInt64/unit {
		return -1
	}
	return n * unit
}

func saturatingAdd(a, b int64) int64 {
	if a > math.MaxInt64-b {
		return math.MaxInt64
	}
	return a + b
}
