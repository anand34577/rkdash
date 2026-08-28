package main

import (
	"os"
	"strings"
	"time"
)

const maxHistory = 60

type AppState struct {
	prevNetRX, prevNetTX uint64
	prevDiskReadSectors  uint64
	prevDiskWriteSectors uint64
	prevTime             time.Time
	diskReadRate         float64
	diskWriteRate        float64
	netRxRate, netTxRate float64
	prevAdapterStats     map[string][2]uint64
	adapterRates         map[string][2]float64

	boardName, rkModel, cpuArch string
	npuVersion, rgaVersion      string
	rknnVersion, rkllmVersion   string

	hasGPU, hasNPU, hasRGA bool

	cpuFreqRanges    [][2]uint32
	networkAdapters  []string
	thermalZonePaths []thermalZonePath

	cpuGovernor     string
	tcpConnections  int
	lastStatsUpdate time.Time

	processSortMode ProcessSortMode

	prevCtxSwitches, prevInterrupts, prevSoftirqs uint64
	ctxSwitchesRate, interruptsRate, softirqsRate uint64
	prevCPUStatsTime                              time.Time

	cpuUserPct, cpuSystemPct, cpuIOWaitPct, cpuIdlePct float64
	prevCPUTime                                        CpuStats

	runningProcs, blockedProcs uint64

	gpuHistory, npuHistory []float32

	filterText string
	filterMode bool

	selectedPid    int32
	selectedName   string
	visiblePids    []int32
	confirmingKill bool
	killTargetName string

	paused        bool
	showHelp      bool
	statusMessage string
	statusExpiry  time.Time
}

func (a *AppState) setStatus(msg string) {
	a.statusMessage = msg
	a.statusExpiry = time.Now().Add(4 * time.Second)
}

func (a *AppState) currentStatus() string {
	if a.statusMessage != "" && time.Now().Before(a.statusExpiry) {
		return a.statusMessage
	}
	return ""
}

func NewAppState() *AppState {
	a := &AppState{
		prevTime:         time.Now(),
		prevAdapterStats: make(map[string][2]uint64),
		adapterRates:     make(map[string][2]float64),

		boardName:    getBoardName(),
		rkModel:      getRKModel(),
		cpuArch:      getCPUArchitecture(),
		npuVersion:   getNPUDriverVersion(),
		rgaVersion:   getRGAVersion(),
		rknnVersion:  getLibrknnrtVersion(),
		rkllmVersion: getLibrkllmrtVersion(),

		cpuFreqRanges:    getCPUFreqRanges(),
		networkAdapters:  getNetworkAdapters(),
		thermalZonePaths: getThermalZonePaths(),

		lastStatsUpdate:  time.Now(),
		processSortMode:  SortCpuDesc,
		prevCPUStatsTime: time.Now(),
	}

	_, a.hasGPU = getGPUUsage()
	a.hasNPU = len(getNPULoad()) > 0
	a.hasRGA = len(getRGALoad()) > 0

	return a
}

func (a *AppState) updateHistory() {
	if usage, ok := getGPUUsage(); ok {
		a.gpuHistory = append(a.gpuHistory, usage)
		if len(a.gpuHistory) > maxHistory {
			a.gpuHistory = a.gpuHistory[1:]
		}
	}

	if loads := getNPULoad(); len(loads) > 0 {
		var sum float32
		for _, l := range loads {
			sum += float32(l)
		}
		avg := sum / float32(len(loads))
		a.npuHistory = append(a.npuHistory, avg)
		if len(a.npuHistory) > maxHistory {
			a.npuHistory = a.npuHistory[1:]
		}
	}
}

func (a *AppState) updateCPUStats() {
	stats := getCPUStats()
	elapsed := time.Since(a.prevCPUStatsTime).Seconds()
	if elapsed < 1.0 {
		return
	}

	a.ctxSwitchesRate = uint64(float64(stats.ContextSwitches-a.prevCtxSwitches) / elapsed)
	a.interruptsRate = uint64(float64(stats.Interrupts-a.prevInterrupts) / elapsed)
	a.softirqsRate = uint64(float64(stats.Softirqs-a.prevSoftirqs) / elapsed)

	userDelta := stats.User - a.prevCPUTime.User
	niceDelta := stats.Nice - a.prevCPUTime.Nice
	systemDelta := stats.System - a.prevCPUTime.System
	idleDelta := stats.Idle - a.prevCPUTime.Idle
	iowaitDelta := stats.IOWait - a.prevCPUTime.IOWait
	irqDelta := stats.IRQ - a.prevCPUTime.IRQ
	softirqDelta := stats.SoftIRQ - a.prevCPUTime.SoftIRQ

	totalDelta := userDelta + niceDelta + systemDelta + idleDelta + iowaitDelta + irqDelta + softirqDelta

	if totalDelta > 0 {
		a.cpuUserPct = float64(userDelta+niceDelta) / float64(totalDelta) * 100.0
		a.cpuSystemPct = float64(systemDelta+irqDelta+softirqDelta) / float64(totalDelta) * 100.0
		a.cpuIOWaitPct = float64(iowaitDelta) / float64(totalDelta) * 100.0
		a.cpuIdlePct = float64(idleDelta) / float64(totalDelta) * 100.0
	}

	a.runningProcs = stats.RunningProcs
	a.blockedProcs = stats.BlockedProcs

	a.prevCtxSwitches = stats.ContextSwitches
	a.prevInterrupts = stats.Interrupts
	a.prevSoftirqs = stats.Softirqs
	a.prevCPUTime = stats
	a.prevCPUStatsTime = time.Now()
}

func (a *AppState) updateStats() {
	if time.Since(a.lastStatsUpdate) < 5*time.Second {
		return
	}

	governor, err := os.ReadFile("/sys/devices/system/cpu/cpu0/cpufreq/scaling_governor")
	if err != nil {
		a.cpuGovernor = "N/A"
	} else {
		a.cpuGovernor = strings.TrimSpace(string(governor))
	}

	count := 0
	if content, err := os.ReadFile("/proc/net/tcp"); err == nil {
		lines := strings.Split(string(content), "\n")
		for _, line := range lines[1:] {
			if !strings.Contains(line, " 01 ") {
				continue
			}
			parts := strings.Fields(line)
			if len(parts) > 1 {
				localAddr := parts[1]
				if !strings.HasPrefix(localAddr, "0100007F") && !strings.HasPrefix(localAddr, "00000000") {
					count++
				}
			}
		}
	}
	if content, err := os.ReadFile("/proc/net/tcp6"); err == nil {
		lines := strings.Split(string(content), "\n")
		for _, line := range lines[1:] {
			if !strings.Contains(line, " 01 ") {
				continue
			}
			parts := strings.Fields(line)
			if len(parts) > 1 {
				localAddr := parts[1]
				if !strings.HasPrefix(localAddr, "00000000000000000000000001000000") &&
					!strings.HasPrefix(localAddr, "00000000000000000000000000000000") {
					count++
				}
			}
		}
	}

	a.tcpConnections = count
	a.lastStatsUpdate = time.Now()
}

func (a *AppState) updateIORates(netCounters map[string][2]uint64) {
	now := time.Now()
	interval := now.Sub(a.prevTime).Seconds()

	readSectors, writeSectors := getDiskIOCounters()
	if interval > 0 {
		a.diskReadRate = satSub(readSectors, a.prevDiskReadSectors) * diskSectorBytes / interval
		a.diskWriteRate = satSub(writeSectors, a.prevDiskWriteSectors) * diskSectorBytes / interval
	}
	a.prevDiskReadSectors = readSectors
	a.prevDiskWriteSectors = writeSectors

	var totalRX, totalTX uint64
	newAdapterStats := make(map[string][2]uint64)

	for name, rxtx := range netCounters {
		if len(a.networkAdapters) > 0 && !containsStr(a.networkAdapters, name) {
			continue
		}
		rx, tx := rxtx[0], rxtx[1]
		totalRX += rx
		totalTX += tx

		if interval > 0 {
			if prev, ok := a.prevAdapterStats[name]; ok {
				rxRate := satSub(rx, prev[0]) / interval
				txRate := satSub(tx, prev[1]) / interval
				a.adapterRates[name] = [2]float64{rxRate, txRate}
			}
		}
		newAdapterStats[name] = rxtx
	}

	if interval > 0 {
		a.netRxRate = satSub(totalRX, a.prevNetRX) / interval
		a.netTxRate = satSub(totalTX, a.prevNetTX) / interval
	}

	a.prevNetRX = totalRX
	a.prevNetTX = totalTX
	a.prevAdapterStats = newAdapterStats
	a.prevTime = now
}

func satSub(a, b uint64) float64 {
	if a < b {
		return 0
	}
	return float64(a - b)
}

func containsStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
