package generationbudget

import "testing"

func TestCgroupMemoryResolvesActualControllerChildAndAncestors(t *testing.T) {
	for _, tt := range []struct {
		name, groups, mounts string
		counters             map[string]int64
		want                 int64
	}{
		{"v1 limited child unbounded root", "3:cpu:/other\n5:memory:/service/vex\n", "28 20 0:24 / /sys/fs/cgroup/memory rw - cgroup cgroup rw,memory\n", map[string]int64{
			"/sys/fs/cgroup/memory/memory.limit_in_bytes": 1 << 60, "/sys/fs/cgroup/memory/memory.usage_in_bytes": 100,
			"/sys/fs/cgroup/memory/service/vex/memory.limit_in_bytes": 1 << 30, "/sys/fs/cgroup/memory/service/vex/memory.usage_in_bytes": 256 << 20,
		}, 768 << 20},
		{"v1 restrictive ancestor nonstandard mount", "5:memory:/service/vex\n", "28 20 0:24 / /controller rw - cgroup cgroup rw,memory\n", map[string]int64{
			"/controller/service/vex/memory.limit_in_bytes": 2 << 30, "/controller/service/vex/memory.usage_in_bytes": 256 << 20,
			"/controller/service/memory.limit_in_bytes": 1 << 30, "/controller/service/memory.usage_in_bytes": 512 << 20,
		}, 512 << 20},
		{"v1 mounted controller subtree", "5:cpu,memory:/service/vex\n", "28 20 0:24 /service /controller rw - cgroup cgroup rw,cpu,memory\n", map[string]int64{
			"/controller/vex/memory.limit_in_bytes": 1 << 30, "/controller/vex/memory.usage_in_bytes": 1 << 30,
		}, 0},
		{"v1 namespace root", "5:memory:/\n", "28 20 0:24 /service/vex /controller rw - cgroup cgroup rw,memory\n", map[string]int64{
			"/controller/memory.limit_in_bytes": 1 << 30, "/controller/memory.usage_in_bytes": 256 << 20,
		}, 768 << 20},
		{"v2 child and ancestor", "0::/service/vex\n", "28 20 0:24 / /sys/fs/cgroup rw - cgroup2 cgroup rw\n", map[string]int64{
			"/sys/fs/cgroup/service/vex/memory.max": 2 << 30, "/sys/fs/cgroup/service/vex/memory.current": 256 << 20,
			"/sys/fs/cgroup/service/memory.max": 1 << 30, "/sys/fs/cgroup/service/memory.current": 512 << 20,
		}, 512 << 20},
		{"unavailable counters are unknown", "5:memory:/service/vex\n", "28 20 0:24 / /controller rw - cgroup cgroup rw,memory\n", map[string]int64{}, -1},
		{"no memory controller", "3:cpu:/service/vex\n", "28 20 0:24 / /controller rw - cgroup cgroup rw,cpu\n", map[string]int64{}, -1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			counter := func(path string) int64 {
				if n, ok := tt.counters[path]; ok {
					return n
				}
				return -1
			}
			if got := cgroupMemoryAvailable(tt.groups, tt.mounts, counter); got != tt.want {
				t.Fatalf("headroom=%d want%d", got, tt.want)
			}
		})
	}
}
