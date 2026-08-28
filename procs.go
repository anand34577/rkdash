package main

import (
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

type ProcessSortMode int

const (
	SortCpuAsc ProcessSortMode = iota
	SortCpuDesc
	SortMemoryAsc
	SortMemoryDesc
	SortPidAsc
	SortPidDesc
	SortNameAsc
	SortNameDesc
)

type ProcessInfo struct {
	Pid           int32
	Name          string
	User          string
	Cpu           float32
	Mem           float32
	Nice          int32
	Runtime       uint64
	CpuCore       int32
	IsThread      bool
	ThreadGroupID int32
	State         byte
	NumThreads    int32
}

type procStatFields struct {
	state        byte
	utime, stime uint64
	nice         int32
	numThreads   int32
	processor    int32
	starttime    uint64
	ok           bool
}

func parseProcStat(content string) procStatFields {
	idx := strings.LastIndex(content, ")")
	if idx < 0 {
		return procStatFields{}
	}
	rest := strings.Fields(content[idx+1:])
	if len(rest) < 37 {
		return procStatFields{}
	}
	utime, _ := strconv.ParseUint(rest[11], 10, 64)
	stime, _ := strconv.ParseUint(rest[12], 10, 64)
	nice, _ := strconv.ParseInt(rest[16], 10, 32)
	numThreads, _ := strconv.ParseInt(rest[17], 10, 32)
	starttime, _ := strconv.ParseUint(rest[19], 10, 64)
	processor, _ := strconv.ParseInt(rest[36], 10, 32)
	return procStatFields{
		state:      rest[0][0],
		utime:      utime,
		stime:      stime,
		nice:       int32(nice),
		numThreads: int32(numThreads),
		processor:  int32(processor),
		starttime:  starttime,
		ok:         true,
	}
}

func extractComm(statContent string) string {
	lp := strings.Index(statContent, "(")
	rp := strings.LastIndex(statContent, ")")
	if lp < 0 || rp <= lp {
		return "?"
	}
	return statContent[lp+1 : rp]
}

type procStatusInfo struct {
	uid     uint32
	vmRSSKB uint64
}

func parseProcStatus(content string) procStatusInfo {
	var info procStatusInfo
	for _, line := range strings.Split(content, "\n") {
		switch {
		case strings.HasPrefix(line, "Uid:"):
			f := strings.Fields(line)
			if len(f) >= 2 {
				v, _ := strconv.ParseUint(f[1], 10, 32)
				info.uid = uint32(v)
			}
		case strings.HasPrefix(line, "VmRSS:"):
			f := strings.Fields(line)
			if len(f) >= 2 {
				v, _ := strconv.ParseUint(f[1], 10, 64)
				info.vmRSSKB = v
			}
		}
	}
	return info
}

func (m *SystemMonitor) resolveUser(uid uint32) string {
	if name, ok := m.userCache[uid]; ok {
		return name
	}
	if data, err := os.ReadFile("/etc/passwd"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			parts := strings.Split(line, ":")
			if len(parts) >= 3 {
				if lineUID, err := strconv.ParseUint(parts[2], 10, 32); err == nil && uint32(lineUID) == uid {
					m.userCache[uid] = parts[0]
					return parts[0]
				}
			}
		}
	}
	name := strconv.FormatUint(uint64(uid), 10)
	m.userCache[uid] = name
	return name
}

type basicProc struct {
	pid        int32
	name       string
	uid        uint32
	cpu, mem   float32
	nice       int32
	processor  int32
	numThreads int32
	state      byte
	runtime    uint64
}

func (m *SystemMonitor) RefreshProcessBasic(totalMemKB uint64) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return
	}

	now := time.Now()
	elapsed := now.Sub(m.prevProcTime).Seconds()
	if elapsed <= 0 {
		elapsed = 1
	}
	uptimeNow := getUptimeSeconds()

	newTicks := make(map[int32]uint64, len(m.prevProcTicks))
	basics := make([]basicProc, 0, len(entries))

	for _, e := range entries {
		pid64, err := strconv.ParseInt(e.Name(), 10, 32)
		if err != nil {
			continue
		}
		pid := int32(pid64)

		statData, err := os.ReadFile("/proc/" + e.Name() + "/stat")
		if err != nil {
			continue
		}
		stat := parseProcStat(string(statData))
		if !stat.ok {
			continue
		}

		statusData, err := os.ReadFile("/proc/" + e.Name() + "/status")
		if err != nil {
			continue
		}
		status := parseProcStatus(string(statusData))

		totalTicks := stat.utime + stat.stime
		newTicks[pid] = totalTicks

		var cpuPct float32
		if prevTicks, had := m.prevProcTicks[pid]; had && totalTicks >= prevTicks {
			deltaTicks := float64(totalTicks - prevTicks)
			cpuPct = float32(deltaTicks / (elapsed * clkTck) * 100.0)
		}

		var memPct float32
		if totalMemKB > 0 {
			memPct = float32(float64(status.vmRSSKB) / float64(totalMemKB) * 100.0)
		}

		startSecs := stat.starttime / uint64(clkTck)
		var runtimeSecs uint64
		if uptimeNow > startSecs {
			runtimeSecs = uptimeNow - startSecs
		}

		basics = append(basics, basicProc{
			pid:        pid,
			name:       extractComm(string(statData)),
			uid:        status.uid,
			cpu:        cpuPct,
			mem:        memPct,
			nice:       stat.nice,
			processor:  stat.processor,
			numThreads: stat.numThreads,
			state:      stat.state,
			runtime:    runtimeSecs,
		})
	}

	m.prevProcTicks = newTicks
	m.prevProcTime = now
	m.basicProcs = basics
}

func (m *SystemMonitor) TopProcesses(sortMode ProcessSortMode, count int) []ProcessInfo {
	basics := make([]basicProc, len(m.basicProcs))
	copy(basics, m.basicProcs)
	sortBasics(basics, sortMode)
	if len(basics) > count {
		basics = basics[:count]
	}

	now := time.Now()
	elapsed := now.Sub(m.prevThreadTime).Seconds()
	if elapsed <= 0 {
		elapsed = 1
	}
	if m.prevThreadTicks == nil {
		m.prevThreadTicks = make(map[int32]uint64)
	}
	newThreadTicks := make(map[int32]uint64)
	uptimeNow := getUptimeSeconds()

	result := make([]ProcessInfo, 0, len(basics))
	for _, b := range basics {
		result = append(result, ProcessInfo{
			Pid:           b.pid,
			Name:          b.name,
			User:          m.resolveUser(b.uid),
			Cpu:           b.cpu,
			Mem:           b.mem,
			Nice:          b.nice,
			Runtime:       b.runtime,
			CpuCore:       b.processor,
			IsThread:      false,
			ThreadGroupID: b.pid,
			State:         b.state,
			NumThreads:    b.numThreads,
		})

		if b.numThreads <= 1 {
			continue
		}
		taskEntries, err := os.ReadDir("/proc/" + strconv.Itoa(int(b.pid)) + "/task")
		if err != nil {
			continue
		}
		for _, te := range taskEntries {
			tid64, err := strconv.ParseInt(te.Name(), 10, 32)
			if err != nil {
				continue
			}
			tid := int32(tid64)
			if tid == b.pid {
				continue
			}
			statData, err := os.ReadFile("/proc/" + strconv.Itoa(int(b.pid)) + "/task/" + te.Name() + "/stat")
			if err != nil {
				continue
			}
			stat := parseProcStat(string(statData))
			if !stat.ok {
				continue
			}

			totalTicks := stat.utime + stat.stime
			newThreadTicks[tid] = totalTicks
			var cpuPct float32
			if prevTicks, had := m.prevThreadTicks[tid]; had && totalTicks >= prevTicks {
				cpuPct = float32(float64(totalTicks-prevTicks) / (elapsed * clkTck) * 100.0)
			}

			startSecs := stat.starttime / uint64(clkTck)
			var runtimeSecs uint64
			if uptimeNow > startSecs {
				runtimeSecs = uptimeNow - startSecs
			}

			result = append(result, ProcessInfo{
				Pid:           tid,
				Name:          extractComm(string(statData)),
				User:          "",
				Cpu:           cpuPct,
				Mem:           b.mem,
				Nice:          stat.nice,
				Runtime:       runtimeSecs,
				CpuCore:       stat.processor,
				IsThread:      true,
				ThreadGroupID: b.pid,
				State:         stat.state,
				NumThreads:    1,
			})
		}
	}

	m.prevThreadTicks = newThreadTicks
	m.prevThreadTime = now

	return result
}

func sortBasics(procs []basicProc, mode ProcessSortMode) {
	switch mode {
	case SortCpuDesc:
		sort.SliceStable(procs, func(i, j int) bool { return procs[i].cpu > procs[j].cpu })
	case SortCpuAsc:
		sort.SliceStable(procs, func(i, j int) bool { return procs[i].cpu < procs[j].cpu })
	case SortMemoryDesc:
		sort.SliceStable(procs, func(i, j int) bool { return procs[i].mem > procs[j].mem })
	case SortMemoryAsc:
		sort.SliceStable(procs, func(i, j int) bool { return procs[i].mem < procs[j].mem })
	case SortPidAsc:
		sort.SliceStable(procs, func(i, j int) bool { return procs[i].pid < procs[j].pid })
	case SortPidDesc:
		sort.SliceStable(procs, func(i, j int) bool { return procs[i].pid > procs[j].pid })
	case SortNameAsc:
		sort.SliceStable(procs, func(i, j int) bool {
			return strings.ToLower(procs[i].name) < strings.ToLower(procs[j].name)
		})
	case SortNameDesc:
		sort.SliceStable(procs, func(i, j int) bool {
			return strings.ToLower(procs[i].name) > strings.ToLower(procs[j].name)
		})
	}
}
