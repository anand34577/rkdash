package main

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// Long enough to fill a wide panel's history strip; graphSpans only draws the
// trailing samples that fit.
const maxHistory = 240

// pushHist appends v to a fixed-length history ring.
func pushHist(h []float32, v float32) []float32 {
	h = append(h, v)
	if len(h) > maxHistory {
		h = h[len(h)-maxHistory:]
	}
	return h
}

// histMax is the running peak of a history, used to auto-scale traces of
// unbounded quantities (byte rates). Never returns 0, so callers can divide.
func histMax(h []float32) float32 {
	max := float32(0)
	for _, v := range h {
		if v > max {
			max = v
		}
	}
	if max <= 0 {
		return 1
	}
	return max
}

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

	hasGPU, hasNPU, hasRGA, hasVPU bool

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

	gpuHistory       []float32
	cpuHistory       []float32
	memHistory       []float32
	netRxHistory     []float32
	netTxHistory     []float32
	diskReadHistory  []float32
	diskWriteHistory []float32

	// procScroll is the first row of the Processes table that's drawn, kept in
	// range of the selection so arrowing past the bottom scrolls instead of
	// moving the cursor off-screen.
	procScroll int
	// procRowPids maps drawn table rows to PIDs (0 = thread row) starting at
	// screen row procRowY, so a mouse click can select what was clicked.
	procRowPids []int32
	procRowY    int

	filterText string
	filterMode bool

	selectedPid    int32
	selectedName   string
	visiblePids    []int32
	confirmingKill bool
	killTargetName string

	paused        bool
	showHelp      bool
	showDetail    bool
	statusMessage string
	statusExpiry  time.Time

	cfg      *Config
	recorder *csvRecorder

	// accelOnly filters the process list to processes holding an accelerator
	// device open.
	accelOnly bool

	// accelHistory traces each accelerator block by name ("npu:0", "vpu:rkvenc_0",
	// "rga:rga2_0"), so the panels that are the reason to use rkdash over btop
	// get the same history strips CPU and memory have.
	accelHistory map[string][]float32

	// Per-process traces, collected only while a process is selected — reset on
	// change so the graph never mixes two processes' samples.
	detailPid        int32
	detailCPUHistory []float32
	detailMemHistory []float32

	prevDiskPerDevice map[string][2]uint64
	diskRates         map[string][2]float64

	// lastProcSample is the most recent process listing, kept so the per-process
	// history can be sampled without a second /proc walk.
	lastProcSample []ProcessInfo
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

func NewAppState(cfg *Config) *AppState {
	a := &AppState{
		cfg:               cfg,
		accelOnly:         cfg.AccelOnly,
		accelHistory:      make(map[string][]float32),
		prevDiskPerDevice: make(map[string][2]uint64),
		diskRates:         make(map[string][2]float64),

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
		processSortMode:  cfg.SortMode(),
		prevCPUStatsTime: time.Now(),
	}

	// Every supported SoC (RK3566/3576/3588) has a Mali GPU, so always show
	// the panel rather than silently omitting it when neither the debugfs
	// utilization node nor the devfreq node is wired up on this kernel.
	a.hasGPU = true
	a.hasNPU = len(getNPULoad()) > 0
	a.hasRGA = len(getRGALoad()) > 0
	a.hasVPU = len(getVPULoad()) > 0

	return a
}

func (a *AppState) updateHistory(mon *SystemMonitor) {
	if usage, ok := getGPUUsage(); ok {
		a.gpuHistory = pushHist(a.gpuHistory, usage)
	}

	usages := mon.CoreUsages()
	var total float32
	for _, u := range usages {
		total += u
	}
	if len(usages) > 0 {
		total /= float32(len(usages))
	}
	a.cpuHistory = pushHist(a.cpuHistory, total)

	if mem := getMemStats(); mem.TotalKB > 0 {
		a.memHistory = pushHist(a.memHistory, float32(mem.TotalKB-mem.AvailableKB)/float32(mem.TotalKB)*100)
	}

	for i, l := range getNPULoad() {
		a.pushAccel(fmt.Sprintf("npu:%d", i), float32(l))
	}
	for _, l := range getVPULoad() {
		a.pushAccel("vpu:"+l.Name, l.Load)
	}
	for _, l := range getRGALoad() {
		a.pushAccel("rga:"+l.Name, l.Load)
	}

	a.updateDetailHistory()
}

func (a *AppState) pushAccel(key string, v float32) {
	a.accelHistory[key] = pushHist(a.accelHistory[key], v)
}

// updateDetailHistory tracks the selected process only. Switching selection
// clears the trace rather than continuing it, so the graph in the detail pane
// always belongs to one process.
func (a *AppState) updateDetailHistory() {
	if a.selectedPid != a.detailPid {
		a.detailPid = a.selectedPid
		a.detailCPUHistory = nil
		a.detailMemHistory = nil
	}
	if a.selectedPid == 0 {
		return
	}
	for _, p := range a.lastProcSample {
		if p.Pid == a.selectedPid && !p.IsThread {
			a.detailCPUHistory = pushHist(a.detailCPUHistory, p.Cpu)
			a.detailMemHistory = pushHist(a.detailMemHistory, p.Mem)
			return
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

	// Per-device rates alongside the aggregate: on these boards eMMC vs SD vs
	// NVMe is exactly the split that matters, and the sum hides it.
	perDevice := getDiskIOPerDevice()
	for name, rw := range perDevice {
		if prev, ok := a.prevDiskPerDevice[name]; ok && interval > 0 {
			a.diskRates[name] = [2]float64{
				satSub(rw[0], prev[0]) * diskSectorBytes / interval,
				satSub(rw[1], prev[1]) * diskSectorBytes / interval,
			}
		}
	}
	a.prevDiskPerDevice = perDevice

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

	a.netRxHistory = pushHist(a.netRxHistory, float32(a.netRxRate))
	a.netTxHistory = pushHist(a.netTxHistory, float32(a.netTxRate))
	a.diskReadHistory = pushHist(a.diskReadHistory, float32(a.diskReadRate))
	a.diskWriteHistory = pushHist(a.diskWriteHistory, float32(a.diskWriteRate))

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
