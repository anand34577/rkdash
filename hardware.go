package main

import (
	"debug/elf"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
)

func getDiskTotal() (used, total uint64, ok bool) {
	data, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return 0, 0, false
	}

	skip := map[string]bool{
		"tmpfs": true, "devtmpfs": true, "proc": true, "sysfs": true,
		"devpts": true, "cgroup": true, "cgroup2": true, "securityfs": true,
		"debugfs": true, "tracefs": true, "pstore": true, "bpf": true,
		"configfs": true, "hugetlbfs": true, "mqueue": true,
	}

	var totalSize, totalUsed uint64
	for _, line := range strings.Split(string(data), "\n") {
		parts := strings.Fields(line)
		if len(parts) < 3 {
			continue
		}
		mountPoint, fsType := parts[1], parts[2]
		if skip[fsType] {
			continue
		}

		var stat unix.Statfs_t
		if err := unix.Statfs(mountPoint, &stat); err != nil {
			continue
		}
		blockSize := uint64(stat.Bsize)
		totalBlocks := stat.Blocks
		freeBlocks := stat.Bfree

		totalSize += blockSize * totalBlocks
		totalUsed += blockSize * (totalBlocks - freeBlocks)
	}

	if totalSize > 0 {
		return totalUsed, totalSize, true
	}
	return 0, 0, false
}

func getNetworkAdapters() []string {
	var adapters []string
	entries, err := os.ReadDir("/sys/class/net")
	if err != nil {
		return adapters
	}
	for _, e := range entries {
		name := e.Name()
		if isVirtualInterface(name) {
			continue
		}
		adapters = append(adapters, name)
	}
	sort.Strings(adapters)
	return adapters
}

func isVirtualInterface(name string) bool {
	switch {
	case name == "lo":
		return true
	case strings.HasPrefix(name, "dummy"),
		strings.HasPrefix(name, "veth"),
		strings.HasPrefix(name, "br-"),
		strings.HasPrefix(name, "docker"),
		strings.HasPrefix(name, "virbr"),
		strings.HasPrefix(name, "podman"),
		strings.HasPrefix(name, "cni"),
		strings.HasPrefix(name, "flannel"),
		strings.HasPrefix(name, "tun"),
		strings.HasPrefix(name, "tap"),
		strings.HasPrefix(name, "sit"),
		strings.HasPrefix(name, "ifb"):
		return true
	default:
		return false
	}
}

func getInterfaceIPv4(name string) (string, bool) {
	iface, err := net.InterfaceByName(name)
	if err != nil {
		return "", false
	}
	addrs, err := iface.Addrs()
	if err != nil {
		return "", false
	}

	var fallbackV6 string
	for _, a := range addrs {
		ipNet, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		if v4 := ipNet.IP.To4(); v4 != nil {
			return v4.String(), true
		}
		if fallbackV6 == "" && !ipNet.IP.IsLinkLocalUnicast() {
			fallbackV6 = ipNet.IP.String()
		}
	}
	if fallbackV6 != "" {
		return fallbackV6, true
	}
	return "", false
}

type thermalZonePath struct {
	Label    string
	TempPath string
}

func getThermalZonePaths() []thermalZonePath {
	var paths []thermalZonePath
	entries, err := os.ReadDir("/sys/class/thermal")
	if err != nil {
		return paths
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "thermal_zone") {
			continue
		}
		base := filepath.Join("/sys/class/thermal", name)
		typeContent, err := os.ReadFile(filepath.Join(base, "type"))
		if err != nil {
			continue
		}
		label := strings.ReplaceAll(strings.TrimSpace(string(typeContent)), "_thermal", "")
		paths = append(paths, thermalZonePath{Label: label, TempPath: filepath.Join(base, "temp")})
	}
	return paths
}

func getThermalCached(paths []thermalZonePath) []struct {
	Name string
	Temp int32
} {
	var temps []struct {
		Name string
		Temp int32
	}
	for _, p := range paths {
		if millis, ok := readCachedI32(p.TempPath); ok {
			temps = append(temps, struct {
				Name string
				Temp int32
			}{p.Label, millis / 1000})
		}
	}
	return temps
}

func getGPUTemperature() (int32, bool) {
	entries, err := os.ReadDir("/sys/class/hwmon")
	if err != nil {
		return 0, false
	}
	for _, e := range entries {
		base := filepath.Join("/sys/class/hwmon", e.Name())
		nameData, err := os.ReadFile(filepath.Join(base, "name"))
		if err != nil {
			continue
		}
		name := strings.ToLower(strings.TrimSpace(string(nameData)))
		if !strings.Contains(name, "gpu") && !strings.Contains(name, "mali") {
			continue
		}
		if millis, ok := readCachedI32(filepath.Join(base, "temp1_input")); ok {
			return millis / 1000, true
		}
	}
	return 0, false
}

func getHwmonSensors() []struct{ Name, Value string } {
	var sensors []struct{ Name, Value string }
	entries, err := os.ReadDir("/sys/class/hwmon")
	if err != nil {
		return sensors
	}
	for _, e := range entries {
		base := filepath.Join("/sys/class/hwmon", e.Name())
		deviceName := "unknown"
		if nameData, err := os.ReadFile(filepath.Join(base, "name")); err == nil {
			deviceName = strings.TrimSpace(string(nameData))
		}

		for i := 1; i <= 10; i++ {
			p := filepath.Join(base, "fan"+strconv.Itoa(i)+"_input")
			if data, err := os.ReadFile(p); err == nil {
				if rpm, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 32); err == nil {
					sensors = append(sensors, struct{ Name, Value string }{
						deviceName + " Fan" + strconv.Itoa(i), strconv.FormatUint(rpm, 10) + " RPM",
					})
				}
			}
		}

		for i := 1; i <= 10; i++ {
			p := filepath.Join(base, "power"+strconv.Itoa(i)+"_input")
			if data, err := os.ReadFile(p); err == nil {
				if uw, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64); err == nil {
					watts := float64(uw) / 1_000_000.0
					sensors = append(sensors, struct{ Name, Value string }{
						deviceName + " Power" + strconv.Itoa(i), formatFloat(watts, 2) + " W",
					})
				}
			}
		}
	}
	return sensors
}

func globFirst(patterns ...string) (string, bool) {
	for _, pat := range patterns {
		matches, err := filepath.Glob(pat)
		if err == nil && len(matches) > 0 {
			return matches[0], true
		}
	}
	return "", false
}

func getGPUUsage() (float32, bool) {
	if content, err := readCachedFile("/sys/kernel/debug/mali0/dvfs_utilization"); err == nil {
		parts := strings.Fields(content)
		var busy, idle uint64
		for i := 0; i+1 < len(parts); i += 2 {
			key := strings.TrimSuffix(parts[i], ":")
			val, err := strconv.ParseUint(parts[i+1], 10, 64)
			if err != nil {
				return 0, false
			}
			switch key {
			case "busy_time":
				busy = val
			case "idle_time":
				idle = val
			}
		}
		if total := busy + idle; total > 0 {
			return float32(busy) / float32(total) * 100.0, true
		}
		return 0, false
	}

	// ponytail: fallback for kernels that expose a plain percentage instead
	// of dvfs_utilization's busy/idle pair.
	if content, err := readCachedFile("/sys/kernel/debug/mali0/utilization"); err == nil {
		if m := regexp.MustCompile(`\d+`).FindString(content); m != "" {
			if v, err := strconv.ParseUint(m, 10, 32); err == nil {
				return float32(v), true
			}
		}
	}
	return 0, false
}

var dmcFreqPathCache string

// getDMCFrequency reads the DDR memory controller's current clock, which
// matters for video pipelines since decode/encode/display all share it.
func getDMCFrequency() (uint32, bool) {
	path := dmcFreqPathCache
	if path == "" {
		found, ok := globFirst("/sys/class/devfreq/dmc/cur_freq", "/sys/class/devfreq/*dmc*/cur_freq")
		if !ok {
			return 0, false
		}
		dmcFreqPathCache = found
		path = found
	}
	content, err := readCachedFile(path)
	if err != nil {
		return 0, false
	}
	hz, err := strconv.ParseUint(strings.TrimSpace(content), 10, 64)
	if err != nil {
		return 0, false
	}
	return uint32(hz / 1_000_000), true
}

var vpuLoadIntervalOnce sync.Once

// The mpp_service driver reports 0% until a sampling interval is armed —
// cat /proc/mpp_service/load otherwise just prints instructions to do this.
// Root is already required to run rkdash at all (see main.go), so this
// write is always permitted.
func ensureVPULoadInterval() {
	vpuLoadIntervalOnce.Do(func() {
		_ = os.WriteFile("/proc/mpp_service/load_interval", []byte("1000"), 0644)
	})
}

// Device line, e.g. "fdf40000.rkvenc   load:   0.00% utilization:   0.00%".
// The block set isn't fixed: RK3566/76/88 each report a different roster
// (rkvenc, rkvdec, vepu, vdpu, jpegd/jpege, iep, avsd-plus, av1d, vdpp, ...),
// and some SoCs report multiple instances of the same block name
// (e.g. two "rkvenc-core" cores on RK3576) — so this parses whatever the
// running kernel lists rather than matching against a whitelist.
var vpuLoadLineRe = regexp.MustCompile(`(?i)^[0-9a-f]+\.([a-z0-9_-]+)\s+load:\s*([\d.]+)\s*%`)

func getVPULoad() []namedLoad {
	ensureVPULoadInterval()
	content, err := readCachedFile("/proc/mpp_service/load")
	if err != nil {
		return nil
	}
	seen := make(map[string]int)
	var result []namedLoad
	for _, rawLine := range strings.Split(content, "\n") {
		m := vpuLoadLineRe.FindStringSubmatch(strings.TrimSpace(rawLine))
		if m == nil {
			continue
		}
		load, err := strconv.ParseFloat(m[2], 32)
		if err != nil {
			continue
		}
		name := strings.ToLower(m[1])
		idx := seen[name]
		seen[name] = idx + 1
		result = append(result, namedLoad{fmt.Sprintf("%s_%d", name, idx), float32(load)})
	}
	return result
}

var mppSessionDeviceRe = regexp.MustCompile(`(?i)[0-9a-f]+\.([a-z0-9_-]+)`)

// ponytail: sessions-summary was empty (idle) on every board this was
// verified against, so the exact column layout for an active session is
// unconfirmed. Only trust an explicit "pid" field rather than guessing at
// bare numbers, so a wrong column never gets mislabeled as a PID.
var mppSessionPidRe = regexp.MustCompile(`(?i)pid[:=\s]+(\d+)`)

// getMPPSessions maps each VPU block base name (without the "_N" instance
// suffix getVPULoad adds) to the PIDs of processes currently holding a
// session on it, from /proc/mpp_service/sessions-summary.
func getMPPSessions() map[string][]int32 {
	content, err := readCachedFile("/proc/mpp_service/sessions-summary")
	if err != nil {
		return nil
	}
	result := make(map[string][]int32)
	for _, line := range strings.Split(content, "\n") {
		nameM := mppSessionDeviceRe.FindStringSubmatch(line)
		pidM := mppSessionPidRe.FindStringSubmatch(line)
		if nameM == nil || pidM == nil {
			continue
		}
		name := strings.ToLower(nameM[1])
		if pid, err := strconv.ParseUint(pidM[1], 10, 32); err == nil {
			result[name] = append(result[name], int32(pid))
		}
	}
	return result
}

func getCPUFrequencies() []uint32 {
	var freqs []uint32
	for cpuID := 0; ; cpuID++ {
		path := "/sys/devices/system/cpu/cpu" + strconv.Itoa(cpuID) + "/cpufreq/scaling_cur_freq"
		khz, ok := readCachedU32(path)
		if !ok {
			break
		}
		freqs = append(freqs, khz/1000)
	}
	return freqs
}

func getCPUFreqRanges() [][2]uint32 {
	var ranges [][2]uint32
	seen := make(map[[2]uint32]bool)
	for cpuID := 0; ; cpuID++ {
		base := "/sys/devices/system/cpu/cpu" + strconv.Itoa(cpuID) + "/cpufreq/"
		minData, err1 := os.ReadFile(base + "scaling_min_freq")
		maxData, err2 := os.ReadFile(base + "scaling_max_freq")
		if err1 != nil || err2 != nil {
			break
		}
		minV, e1 := strconv.ParseUint(strings.TrimSpace(string(minData)), 10, 32)
		maxV, e2 := strconv.ParseUint(strings.TrimSpace(string(maxData)), 10, 32)
		if e1 != nil || e2 != nil {
			break
		}
		r := [2]uint32{uint32(minV) / 1000, uint32(maxV) / 1000}
		if !seen[r] {
			seen[r] = true
			ranges = append(ranges, r)
		}
	}
	return ranges
}

func getGPUFrequency() (uint32, bool) {
	path, ok := globFirst(
		"/sys/devices/platform/*.gpu*/devfreq/*.gpu*/cur_freq",
		"/sys/devices/platform/*/*.gpu*/devfreq/*.gpu*/cur_freq",
		"/sys/class/devfreq/*.gpu*/cur_freq",
	)
	if !ok {
		return 0, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	hz, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return 0, false
	}
	return uint32(hz / 1_000_000), true
}

var npuFreqPathCache string

func getNPUFrequency() (uint32, bool) {
	path := npuFreqPathCache
	if path == "" {
		found, ok := globFirst("/sys/class/devfreq/*.npu/cur_freq", "/sys/class/devfreq/*npu*/cur_freq")
		if !ok {
			return 0, false
		}
		npuFreqPathCache = found
		path = found
	}
	content, err := readCachedFile(path)
	if err != nil {
		return 0, false
	}
	hz, err := strconv.ParseUint(strings.TrimSpace(content), 10, 64)
	if err != nil {
		return 0, false
	}
	return uint32(hz / 1_000_000), true
}

// Multi-core SoCs (e.g. RK3576) report "Core0: N%  Core1: N%"; single-core
// SoCs (e.g. RK3566) report just "NPU load:  N%" with no core label.
var npuLoadCoreRe = regexp.MustCompile(`Core(\d+):\s*(\d+)%`)
var npuLoadSingleRe = regexp.MustCompile(`NPU load:\s*(\d+)%`)

func getNPULoad() []uint8 {
	content, err := readCachedFile("/sys/kernel/debug/rknpu/load")
	if err != nil {
		return nil
	}
	var loads []uint8
	for _, m := range npuLoadCoreRe.FindAllStringSubmatch(content, -1) {
		v, err := strconv.ParseUint(m[2], 10, 8)
		if err == nil {
			loads = append(loads, uint8(v))
		}
	}
	if len(loads) == 0 {
		if m := npuLoadSingleRe.FindStringSubmatch(content); m != nil {
			if v, err := strconv.ParseUint(m[1], 10, 8); err == nil {
				loads = append(loads, uint8(v))
			}
		}
	}
	return loads
}

// namedLoad is a device block name paired with its utilization percentage —
// shared by every per-block accelerator load reader (RGA, VPU) so their
// results can go through one shared grid renderer in ui.go.
type namedLoad struct {
	Name string
	Load float32
}

func getRGALoad() []namedLoad {
	content, err := readCachedFile("/sys/kernel/debug/rkrga/load")
	if err != nil {
		return nil
	}

	var result []namedLoad
	currentScheduler := ""
	schedulerIndex := 0

	for _, rawLine := range strings.Split(content, "\n") {
		line := strings.TrimSpace(rawLine)
		if strings.Contains(line, "-") || strings.Contains(line, "= load =") {
			continue
		}
		if strings.HasPrefix(line, "scheduler[") {
			if end := strings.Index(line, "]"); end > 10 {
				if idx, err := strconv.Atoi(line[10:end]); err == nil {
					schedulerIndex = idx
				}
			}
			if colon := strings.Index(line, ":"); colon >= 0 {
				baseName := strings.TrimSpace(line[colon+1:])
				currentScheduler = baseName + "_" + strconv.Itoa(schedulerIndex)
			}
		} else if strings.HasPrefix(line, "load =") {
			if eq := strings.Index(line, "="); eq >= 0 {
				loadStr := strings.TrimSpace(strings.ReplaceAll(line[eq+1:], "%", ""))
				if load, err := strconv.ParseFloat(loadStr, 32); err == nil && currentScheduler != "" {
					result = append(result, namedLoad{currentScheduler, float32(load)})
				}
			}
		}
	}
	return result
}

func getBoardName() string {
	for _, path := range []string{"/proc/device-tree/model", "/sys/firmware/devicetree/base/model"} {
		if data, err := os.ReadFile(path); err == nil {
			model := strings.TrimSpace(strings.TrimRight(string(data), "\x00"))
			if model != "" {
				return model
			}
		}
	}
	return "Unknown Board"
}

var rkModelRe = regexp.MustCompile(`(?i)\bRK(\d{3,4}[A-Za-z]?)\b`)

func getRKModel() string {
	for _, path := range []string{"/proc/device-tree/compatible", "/sys/firmware/devicetree/base/compatible"} {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		for _, tok := range strings.Split(string(data), "\x00") {
			if !strings.Contains(strings.ToLower(tok), "rockchip,") {
				continue
			}
			if m := rkModelRe.FindStringSubmatch(tok); m != nil {
				return "RK" + strings.ToUpper(m[1])
			}
		}
	}

	if m := rkModelRe.FindStringSubmatch(getBoardName()); m != nil {
		return "RK" + strings.ToUpper(m[1])
	}
	if data, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		if m := rkModelRe.FindStringSubmatch(string(data)); m != nil {
			return "RK" + strings.ToUpper(m[1])
		}
	}
	return "Unknown RK"
}

var cpuPartNames = map[string]string{
	"0xd03": "A53", "0xd04": "A35", "0xd05": "A55", "0xd07": "A57",
	"0xd08": "A72", "0xd09": "A73", "0xd0a": "A75", "0xd0b": "A76",
	"0xd0d": "A77", "0xd40": "N1", "0xd41": "A78", "0xd44": "X1",
	"0xd46": "A510", "0xd47": "A710", "0xd48": "X2", "0xd4d": "A715",
}

func getCPUArchitecture() string {
	data, err := os.ReadFile("/proc/cpuinfo")
	if err == nil {
		var arch string
		parts := make(map[string]bool)
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "CPU architecture:") {
				if idx := strings.Index(line, ":"); idx >= 0 {
					arch = strings.TrimSpace(line[idx+1:])
				}
			} else if strings.HasPrefix(line, "CPU part") {
				if idx := strings.Index(line, ":"); idx >= 0 {
					parts[strings.TrimSpace(line[idx+1:])] = true
				}
			}
		}
		if arch != "" {
			result := "ARMv" + arch
			var coreNames []string
			for part := range parts {
				if name, ok := cpuPartNames[part]; ok {
					coreNames = append(coreNames, name)
				}
			}
			if len(coreNames) > 0 {
				sort.Strings(coreNames)
				result += " (" + strings.Join(coreNames, "+") + ")"
			}
			return result
		}
	}
	if out, err := unameM(); err == nil {
		return out
	}
	return "Unknown"
}

func unameM() (string, error) {
	var uts unix.Utsname
	if err := unix.Uname(&uts); err != nil {
		return "", err
	}
	return cstr(uts.Machine[:]), nil
}

func cstr(b []byte) string {
	n := 0
	for n < len(b) && b[n] != 0 {
		n++
	}
	return string(b[:n])
}

func getRGAVersion() string {
	data, err := os.ReadFile("/sys/kernel/debug/rkrga/driver_version")
	if err != nil {
		return "Not Detected"
	}
	if idx := strings.Index(string(data), ":"); idx >= 0 {
		return strings.TrimSpace(string(data)[idx+1:])
	}
	return "Not Detected"
}

func getNPUDriverVersion() string {
	data, err := os.ReadFile("/sys/kernel/debug/rknpu/version")
	if err != nil {
		return "Not Detected"
	}
	if idx := strings.Index(string(data), ":"); idx >= 0 {
		return strings.TrimSpace(string(data)[idx+1:])
	}
	return "Not Detected"
}

var versionNumRe = regexp.MustCompile(`(\d+\.\d+\.\d+)`)

func extractVersionFromBinary(path, pattern string) string {
	f, err := elf.Open(path)
	if err != nil {
		return "Not Detected"
	}
	defer f.Close()

	for _, section := range f.Sections {
		if section.Name != ".rodata" && !strings.Contains(section.Name, "data") {
			continue
		}
		data, err := section.Data()
		if err != nil {
			continue
		}
		text := string(data)
		if pos := strings.Index(text, pattern); pos >= 0 {
			sub := text[pos:]
			if m := versionNumRe.FindString(sub); m != "" {
				return m
			}
		}
	}
	return "Not Detected"
}

func getLibrknnrtVersion() string {
	return extractVersionFromBinary("/usr/lib/librknnrt.so", "librknnrt version:")
}

func getLibrkllmrtVersion() string {
	return extractVersionFromBinary("/usr/lib/librkllmrt.so", "RKLLM SDK (version:")
}

func formatFloat(v float64, prec int) string {
	return strconv.FormatFloat(v, 'f', prec, 64)
}
