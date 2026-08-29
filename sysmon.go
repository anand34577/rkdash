package main

import (
	"os"
	"strconv"
	"strings"
	"time"
)

const clkTck = 100.0

type CpuStats struct {
	ContextSwitches uint64
	Interrupts      uint64
	Softirqs        uint64
	User, Nice      uint64
	System, Idle    uint64
	IOWait          uint64
	IRQ, SoftIRQ    uint64
	RunningProcs    uint64
	BlockedProcs    uint64
}

func getCPUStats() CpuStats {
	var s CpuStats
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return s
	}
	for _, line := range strings.Split(string(data), "\n") {
		switch {
		case strings.HasPrefix(line, "cpu "):
			f := strings.Fields(line)
			if len(f) >= 8 {
				s.User, _ = strconv.ParseUint(f[1], 10, 64)
				s.Nice, _ = strconv.ParseUint(f[2], 10, 64)
				s.System, _ = strconv.ParseUint(f[3], 10, 64)
				s.Idle, _ = strconv.ParseUint(f[4], 10, 64)
				s.IOWait, _ = strconv.ParseUint(f[5], 10, 64)
				s.IRQ, _ = strconv.ParseUint(f[6], 10, 64)
				s.SoftIRQ, _ = strconv.ParseUint(f[7], 10, 64)
			}
		case strings.HasPrefix(line, "ctxt "):
			f := strings.Fields(line)
			if len(f) >= 2 {
				s.ContextSwitches, _ = strconv.ParseUint(f[1], 10, 64)
			}
		case strings.HasPrefix(line, "intr "):
			f := strings.Fields(line)
			if len(f) >= 2 {
				s.Interrupts, _ = strconv.ParseUint(f[1], 10, 64)
			}
		case strings.HasPrefix(line, "softirq "):
			f := strings.Fields(line)
			if len(f) >= 2 {
				s.Softirqs, _ = strconv.ParseUint(f[1], 10, 64)
			}
		case strings.HasPrefix(line, "procs_running "):
			f := strings.Fields(line)
			if len(f) >= 2 {
				s.RunningProcs, _ = strconv.ParseUint(f[1], 10, 64)
			}
		case strings.HasPrefix(line, "procs_blocked "):
			f := strings.Fields(line)
			if len(f) >= 2 {
				s.BlockedProcs, _ = strconv.ParseUint(f[1], 10, 64)
			}
		}
	}
	return s
}

type coreJiffies struct{ total, idle uint64 }

type SystemMonitor struct {
	numCPUs      int
	prevCores    []coreJiffies
	coreUsage    []float32
	prevCoreTime time.Time

	prevProcTicks map[int32]uint64
	prevProcTime  time.Time
	basicProcs    []basicProc

	prevThreadTicks map[int32]uint64
	prevThreadTime  time.Time

	userCache map[uint32]string
}

func NewSystemMonitor() *SystemMonitor {
	m := &SystemMonitor{
		prevProcTicks: make(map[int32]uint64),
		userCache:     make(map[uint32]string),
	}
	m.RefreshCPU()
	m.prevProcTime = time.Now()
	return m
}

func (m *SystemMonitor) RefreshCPU() {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return
	}
	var cores []coreJiffies
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "cpu") || len(line) < 4 || line[3] == ' ' {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 8 {
			continue
		}
		var total, idle uint64
		for i := 1; i < len(f) && i <= 10; i++ {
			v, err := strconv.ParseUint(f[i], 10, 64)
			if err != nil {
				continue
			}
			total += v
			if i == 4 || i == 5 {
				idle += v
			}
		}
		cores = append(cores, coreJiffies{total: total, idle: idle})
	}

	if m.prevCores != nil && len(m.prevCores) == len(cores) {
		usage := make([]float32, len(cores))
		for i, c := range cores {
			prev := m.prevCores[i]
			totalDelta := c.total - prev.total
			idleDelta := c.idle - prev.idle
			if totalDelta > 0 {
				usage[i] = float32(totalDelta-idleDelta) / float32(totalDelta) * 100.0
			}
		}
		m.coreUsage = usage
	} else {
		m.coreUsage = make([]float32, len(cores))
	}
	m.prevCores = cores
	m.numCPUs = len(cores)
}

func (m *SystemMonitor) CoreUsages() []float32 { return m.coreUsage }

type MemStats struct {
	TotalKB, FreeKB, AvailableKB uint64
	SwapTotalKB, SwapFreeKB      uint64
	CachedKB, BuffersKB          uint64
}

// CacheKB returns page cache + buffers: memory the kernel is holding for
// speedup but will reclaim under pressure, so it shouldn't read as "used".
func (m MemStats) CacheKB() uint64 { return m.CachedKB + m.BuffersKB }

func getMemStats() MemStats {
	var s MemStats
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return s
	}
	get := func(line string) uint64 {
		f := strings.Fields(line)
		if len(f) < 2 {
			return 0
		}
		v, _ := strconv.ParseUint(f[1], 10, 64)
		return v
	}
	for _, line := range strings.Split(string(data), "\n") {
		switch {
		case strings.HasPrefix(line, "MemTotal:"):
			s.TotalKB = get(line)
		case strings.HasPrefix(line, "MemFree:"):
			s.FreeKB = get(line)
		case strings.HasPrefix(line, "MemAvailable:"):
			s.AvailableKB = get(line)
		case strings.HasPrefix(line, "SwapTotal:"):
			s.SwapTotalKB = get(line)
		case strings.HasPrefix(line, "SwapFree:"):
			s.SwapFreeKB = get(line)
		case strings.HasPrefix(line, "Buffers:"):
			s.BuffersKB = get(line)
		case strings.HasPrefix(line, "Cached:"):
			s.CachedKB = get(line)
		}
	}
	return s
}

type ZramInfo struct {
	OrigDataSize, ComprDataSize, Used, Limit uint64
}

func (z ZramInfo) CompressionRatio() float64 {
	if z.ComprDataSize > 0 {
		return float64(z.OrigDataSize) / float64(z.ComprDataSize)
	}
	return 0
}

func getZramInfo() (ZramInfo, bool) {
	data, err := os.ReadFile("/sys/block/zram0/mm_stat")
	if err != nil {
		return ZramInfo{}, false
	}
	f := strings.Fields(string(data))
	if len(f) < 4 {
		return ZramInfo{}, false
	}
	orig, e1 := strconv.ParseUint(f[0], 10, 64)
	compr, e2 := strconv.ParseUint(f[1], 10, 64)
	used, e3 := strconv.ParseUint(f[2], 10, 64)
	limit, e4 := strconv.ParseUint(f[3], 10, 64)
	if e1 != nil || e2 != nil || e3 != nil || e4 != nil {
		return ZramInfo{}, false
	}
	return ZramInfo{orig, compr, used, limit}, true
}

const diskSectorBytes = 512

func isWholeDiskDevice(name string) bool {
	switch {
	case strings.HasPrefix(name, "loop"),
		strings.HasPrefix(name, "ram"),
		strings.HasPrefix(name, "dm-"),
		strings.HasPrefix(name, "md"),
		strings.HasPrefix(name, "zram"),
		strings.HasPrefix(name, "sr"):
		return false
	case strings.HasPrefix(name, "mmcblk"):

		return !strings.ContainsAny(name[len("mmcblk"):], "pb")
	case strings.HasPrefix(name, "nvme"):

		return !strings.Contains(name, "p")
	case len(name) >= 3 && (strings.HasPrefix(name, "sd") || strings.HasPrefix(name, "hd") || strings.HasPrefix(name, "vd")):

		last := name[len(name)-1]
		return last < '0' || last > '9'
	default:
		return true
	}
}

func getDiskIOCounters() (readSectors, writeSectors uint64) {
	data, err := os.ReadFile("/proc/diskstats")
	if err != nil {
		return 0, 0
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
		if v, err := strconv.ParseUint(f[5], 10, 64); err == nil {
			readSectors += v
		}
		if v, err := strconv.ParseUint(f[9], 10, 64); err == nil {
			writeSectors += v
		}
	}
	return readSectors, writeSectors
}

func getNetworkCounters() map[string][2]uint64 {
	result := make(map[string][2]uint64)
	data, err := os.ReadFile("/proc/net/dev")
	if err != nil {
		return result
	}
	lines := strings.Split(string(data), "\n")
	for _, line := range lines[2:] {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		colon := strings.Index(line, ":")
		if colon < 0 {
			continue
		}
		name := strings.TrimSpace(line[:colon])
		f := strings.Fields(line[colon+1:])
		if len(f) < 9 {
			continue
		}
		rx, _ := strconv.ParseUint(f[0], 10, 64)
		tx, _ := strconv.ParseUint(f[8], 10, 64)
		result[name] = [2]uint64{rx, tx}
	}
	return result
}

func getLoadAverage() (one, five, fifteen float64) {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0, 0, 0
	}
	f := strings.Fields(string(data))
	if len(f) < 3 {
		return 0, 0, 0
	}
	one, _ = strconv.ParseFloat(f[0], 64)
	five, _ = strconv.ParseFloat(f[1], 64)
	fifteen, _ = strconv.ParseFloat(f[2], 64)
	return
}

func getUptimeSeconds() uint64 {
	data, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0
	}
	f := strings.Fields(string(data))
	if len(f) < 1 {
		return 0
	}
	v, _ := strconv.ParseFloat(f[0], 64)
	return uint64(v)
}
