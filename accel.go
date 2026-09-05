package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// accelDevices maps a /dev node prefix to the short badge shown against a
// process that has it open. Covers every Rockchip accelerator that userspace
// talks to through a character device.
var accelDevices = map[string]string{
	"/dev/rknpu":        "NPU",
	"/dev/mpp_service":  "VPU",
	"/dev/rga":          "RGA",
	"/dev/mali0":        "GPU",
	"/dev/dri/renderD":  "GPU",
	"/dev/dri/card":     "GPU",
	"/dev/video":        "V4L",
	"/dev/rkvdec":       "VPU",
	"/dev/rkvenc":       "VPU",
	"/dev/iep":          "IEP",
	"/dev/vpu_service":  "VPU",
	"/dev/hevc_service": "VPU",
}

// accelBadge classifies a resolved /proc/<pid>/fd symlink target. Prefix match
// rather than exact, since these nodes are numbered (rknpu0, video11, ...).
func accelBadge(target string) (string, bool) {
	for prefix, badge := range accelDevices {
		if strings.HasPrefix(target, prefix) {
			return badge, true
		}
	}
	return "", false
}

var (
	accelUsersCache   map[int32][]string
	accelUsersFetched time.Time
)

// getAccelUsers maps PID -> the accelerator badges it currently holds open,
// by resolving every /proc/<pid>/fd symlink. That's a lot of readlink calls,
// so results are cached; it's only refreshed when something actually asks
// (the badge column or the accelerator filter).
//
// ponytail: a full fd walk is O(processes × fds). Measured fine at a 3s
// cadence on RK3566; if it ever shows up in a profile, narrow it to the PIDs
// currently on screen.
func getAccelUsers() map[int32][]string {
	if accelUsersCache != nil && time.Since(accelUsersFetched) < 3*time.Second {
		return accelUsersCache
	}

	result := make(map[int32][]string)
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return result
	}
	for _, e := range entries {
		pid, err := parsePid(e.Name())
		if err != nil {
			continue
		}
		fdDir := "/proc/" + e.Name() + "/fd"
		fds, err := os.ReadDir(fdDir)
		if err != nil {
			continue // process exited, or not ours to inspect
		}
		seen := map[string]bool{}
		for _, fd := range fds {
			target, err := os.Readlink(filepath.Join(fdDir, fd.Name()))
			if err != nil {
				continue
			}
			if badge, ok := accelBadge(target); ok && !seen[badge] {
				seen[badge] = true
				result[int32(pid)] = append(result[int32(pid)], badge)
			}
		}
	}

	accelUsersCache = result
	accelUsersFetched = time.Now()
	return result
}

// coolingDevice is one entry from /sys/class/thermal/cooling_deviceN — the
// kernel's active mitigation for a thermal zone (a fan, or a cpufreq cap).
type coolingDevice struct {
	Type string `json:"type"`
	Cur  int    `json:"cur_state"`
	Max  int    `json:"max_state"`
}

// Active reports whether the kernel is currently mitigating through this
// device — i.e. the board is being held back right now.
func (c coolingDevice) Active() bool { return c.Cur > 0 }

func getCoolingDevices() []coolingDevice {
	entries, err := os.ReadDir("/sys/class/thermal")
	if err != nil {
		return nil
	}
	var out []coolingDevice
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "cooling_device") {
			continue
		}
		base := filepath.Join("/sys/class/thermal", e.Name())
		typ := readTrimmed(filepath.Join(base, "type"), "")
		if typ == "" {
			continue
		}
		cur, ok1 := readCachedI32(filepath.Join(base, "cur_state"))
		max, ok2 := readCachedI32(filepath.Join(base, "max_state"))
		if !ok1 || !ok2 {
			continue
		}
		out = append(out, coolingDevice{Type: typ, Cur: int(cur), Max: int(max)})
	}
	return out
}

// throttleState describes one cpufreq policy: the cluster's hardware ceiling
// versus the ceiling currently in force. On these boards "why is it slow" is
// almost always this, and nothing in the UI used to show it.
type throttleState struct {
	Policy string `json:"policy"`
	// CurMaxMHz is scaling_max_freq (what the governor may reach now);
	// HWMaxMHz is cpuinfo_max_freq (what the silicon can do).
	CurMaxMHz uint32 `json:"cur_max_mhz"`
	HWMaxMHz  uint32 `json:"hw_max_mhz"`
	CurMHz    uint32 `json:"cur_mhz"`
	Throttled bool   `json:"throttled"`
	// ThrottlePct is how far below the hardware ceiling the cap sits.
	ThrottlePct  int    `json:"throttle_pct"`
	GovernorName string `json:"governor"`
}

func getThrottleStates() []throttleState {
	policies, err := filepath.Glob("/sys/devices/system/cpu/cpufreq/policy*")
	if err != nil {
		return nil
	}
	var out []throttleState
	for _, p := range policies {
		readKHz := func(name string) uint32 {
			v, ok := readCachedU32(filepath.Join(p, name))
			if !ok {
				return 0
			}
			return v / 1000
		}
		t := throttleState{
			Policy:       strings.TrimPrefix(filepath.Base(p), "policy"),
			CurMaxMHz:    readKHz("scaling_max_freq"),
			HWMaxMHz:     readKHz("cpuinfo_max_freq"),
			CurMHz:       readKHz("scaling_cur_freq"),
			GovernorName: readTrimmed(filepath.Join(p, "scaling_governor"), "?"),
		}
		if t.HWMaxMHz > 0 && t.CurMaxMHz > 0 && t.CurMaxMHz < t.HWMaxMHz {
			t.Throttled = true
			t.ThrottlePct = int(100 - uint64(t.CurMaxMHz)*100/uint64(t.HWMaxMHz))
		}
		out = append(out, t)
	}
	return out
}

// getDiskIOPerDevice returns per-whole-disk read/write sector counters, so
// eMMC, SD and NVMe show separately instead of summed into one number that
// hides which device is actually busy.
func getDiskIOPerDevice() map[string][2]uint64 {
	result := make(map[string][2]uint64)
	data, err := os.ReadFile("/proc/diskstats")
	if err != nil {
		return result
	}
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(line)
		if len(f) < 10 {
			continue
		}
		name := f[2]
		if !isWholeDiskDevice(name) {
			continue
		}
		read, err1 := strconv.ParseUint(f[5], 10, 64)
		write, err2 := strconv.ParseUint(f[9], 10, 64)
		if err1 != nil || err2 != nil {
			continue
		}
		// A device that has never done any I/O is almost always an empty SD
		// slot; listing it just wastes a row.
		if read == 0 && write == 0 {
			continue
		}
		result[name] = [2]uint64{read, write}
	}
	return result
}
