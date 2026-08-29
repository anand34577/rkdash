package main

import (
	"context"
	"fmt"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// runMCPServer exposes rkdash's system/hardware readers as an MCP server over
// stdio, so an MCP client (an agent, an IDE) can query board telemetry
// without driving the TUI. Entered via `rkdash --mcp`.
func runMCPServer() error {
	mon := NewSystemMonitor()

	server := mcp.NewServer(&mcp.Implementation{Name: "rkdash", Version: "1.0.0"}, nil)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "system_snapshot",
		Description: "CPU, memory, swap, ZRAM, load average, and uptime for the board right now.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, SystemSnapshot, error) {
		return nil, buildSystemSnapshot(mon), nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "hardware_info",
		Description: "Board identity plus GPU/NPU/RGA utilization, frequency, and thermal zone readings.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, HardwareSnapshot, error) {
		return nil, buildHardwareSnapshot(), nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "top_processes",
		Description: "Top processes sorted by CPU or memory usage.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args TopProcessesArgs) (*mcp.CallToolResult, []ProcessInfo, error) {
		count := args.Count
		if count <= 0 || count > 200 {
			count = 20
		}
		sortMode := SortCpuDesc
		if args.SortBy == "memory" {
			sortMode = SortMemoryDesc
		}
		mon.RefreshProcessBasic(getMemStats().TotalKB)
		return nil, mon.TopProcesses(sortMode, count), nil
	})

	return server.Run(context.Background(), &mcp.StdioTransport{})
}

type TopProcessesArgs struct {
	Count  int    `json:"count,omitempty" jsonschema:"max number of processes to return (default 20, max 200)"`
	SortBy string `json:"sort_by,omitempty" jsonschema:"'cpu' (default) or 'memory'"`
}

type SystemSnapshot struct {
	CPUPercent    float32   `json:"cpu_percent"`
	CorePercents  []float32 `json:"core_percents"`
	LoadAvg1      float64   `json:"load_avg_1m"`
	LoadAvg5      float64   `json:"load_avg_5m"`
	LoadAvg15     float64   `json:"load_avg_15m"`
	UptimeSeconds uint64    `json:"uptime_seconds"`

	MemTotalBytes     uint64 `json:"mem_total_bytes"`
	MemUsedBytes      uint64 `json:"mem_used_bytes"`
	MemCacheBytes     uint64 `json:"mem_cache_bytes"`
	MemAvailableBytes uint64 `json:"mem_available_bytes"`
	MemUsedPercent    uint64 `json:"mem_used_percent"`

	SwapTotalBytes  uint64 `json:"swap_total_bytes"`
	SwapUsedBytes   uint64 `json:"swap_used_bytes"`
	ZramUsedPercent uint64 `json:"zram_used_percent,omitempty"`
}

// buildSystemSnapshot takes two CPU samples spaced 200ms apart so
// per-core/aggregate usage reflects a real delta instead of the all-zero
// reading a single /proc/stat read would produce.
func buildSystemSnapshot(mon *SystemMonitor) SystemSnapshot {
	mon.RefreshCPU()
	time.Sleep(200 * time.Millisecond)
	mon.RefreshCPU()

	cores := mon.CoreUsages()
	var total float32
	for _, c := range cores {
		total += c
	}
	var cpuPercent float32
	if len(cores) > 0 {
		cpuPercent = total / float32(len(cores))
	}

	mem := getMemStats()
	memTotal := mem.TotalKB * 1024
	memAvailable := mem.AvailableKB * 1024
	memUsed := memTotal - memAvailable
	cache := mem.CacheKB() * 1024
	if cache > memUsed {
		cache = memUsed
	}

	one, five, fifteen := getLoadAverage()

	snap := SystemSnapshot{
		CPUPercent:        cpuPercent,
		CorePercents:      cores,
		LoadAvg1:          one,
		LoadAvg5:          five,
		LoadAvg15:         fifteen,
		UptimeSeconds:     getUptimeSeconds(),
		MemTotalBytes:     memTotal,
		MemUsedBytes:      memUsed,
		MemCacheBytes:     cache,
		MemAvailableBytes: memAvailable,
		MemUsedPercent:    pct(memUsed, memTotal),
		SwapTotalBytes:    mem.SwapTotalKB * 1024,
		SwapUsedBytes:     (mem.SwapTotalKB - mem.SwapFreeKB) * 1024,
	}
	if zram, ok := getZramInfo(); ok && zram.Limit > 0 {
		snap.ZramUsedPercent = pct(zram.Used, zram.Limit)
	}
	return snap
}

type HardwareSnapshot struct {
	BoardName       string   `json:"board_name"`
	SoC             string   `json:"soc"`
	Architecture    string   `json:"architecture"`
	GPUUsagePercent *float32 `json:"gpu_usage_percent,omitempty"`
	GPUFreqHz       *uint32  `json:"gpu_freq_hz,omitempty"`
	GPUTempCelsius  *int32   `json:"gpu_temp_celsius,omitempty"`
	NPUFreqHz       *uint32  `json:"npu_freq_hz,omitempty"`
	NPUCoreLoad     []uint8  `json:"npu_core_load_percent,omitempty"`
	ThermalZones    []string `json:"thermal_zones,omitempty"`
}

func buildHardwareSnapshot() HardwareSnapshot {
	hw := HardwareSnapshot{
		BoardName:    getBoardName(),
		SoC:          getRKModel(),
		Architecture: getCPUArchitecture(),
		NPUCoreLoad:  getNPULoad(),
	}
	if v, ok := getGPUUsage(); ok {
		hw.GPUUsagePercent = &v
	}
	if v, ok := getGPUFrequency(); ok {
		hw.GPUFreqHz = &v
	}
	if v, ok := getGPUTemperature(); ok {
		hw.GPUTempCelsius = &v
	}
	if v, ok := getNPUFrequency(); ok {
		hw.NPUFreqHz = &v
	}
	for _, z := range getThermalCached(getThermalZonePaths()) {
		hw.ThermalZones = append(hw.ThermalZones, fmt.Sprintf("%s: %d°C", z.Name, z.Temp))
	}
	return hw
}

// mcpFlag reports whether argv requests MCP server mode, so it can be checked
// before the root-permission/tcell TUI setup in main().
func mcpFlag(argv []string) bool {
	for _, a := range argv {
		if a == "--mcp" || a == "-mcp" {
			return true
		}
	}
	return false
}
