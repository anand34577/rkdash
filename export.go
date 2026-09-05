package main

import (
	"encoding/csv"
	"encoding/json"
	"os"
	"sort"
	"strconv"
	"time"
)

// Snapshot is the whole-board reading emitted by --json and one row of --csv.
// It reuses the MCP snapshot builders rather than re-reading /proc separately.
type Snapshot struct {
	Timestamp string               `json:"timestamp"`
	System    SystemSnapshot       `json:"system"`
	Hardware  HardwareSnapshot     `json:"hardware"`
	VPU       []namedLoad          `json:"vpu,omitempty"`
	RGA       []namedLoad          `json:"rga,omitempty"`
	Throttle  []throttleState      `json:"throttle,omitempty"`
	Cooling   []coolingDevice      `json:"cooling,omitempty"`
	DiskIO    map[string][2]uint64 `json:"disk_io_sectors,omitempty"`
	Processes []ProcessInfo        `json:"processes,omitempty"`
}

func buildSnapshot(mon *SystemMonitor, topN int) Snapshot {
	s := Snapshot{
		Timestamp: time.Now().Format(time.RFC3339),
		System:    buildSystemSnapshot(mon),
		Hardware:  buildHardwareSnapshot(),
		VPU:       getVPULoad(),
		RGA:       getRGALoad(),
		Throttle:  getThrottleStates(),
		Cooling:   getCoolingDevices(),
		DiskIO:    getDiskIOPerDevice(),
	}
	if topN > 0 {
		mon.RefreshProcessBasic(getMemStats().TotalKB)
		s.Processes = mon.TopProcesses(SortCpuDesc, topN)
	}
	return s
}

// runJSONSnapshot prints one snapshot and exits — for scripting and for
// benchmarking an RKNN model from a shell loop.
func runJSONSnapshot(topN int) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(buildSnapshot(NewSystemMonitor(), topN))
}

// csvRecorder appends one row per sample to a file while the TUI runs, so a
// long benchmark can be plotted afterwards. Columns are fixed at open time
// from whatever accelerators the board reports, since a CSV can't grow columns
// halfway through.
type csvRecorder struct {
	f       *os.File
	w       *csv.Writer
	vpuKeys []string
	rgaKeys []string
	tempKey []string
}

func newCSVRecorder(path string, app *AppState) (*csvRecorder, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	r := &csvRecorder{f: f, w: csv.NewWriter(f)}

	for _, l := range getVPULoad() {
		r.vpuKeys = append(r.vpuKeys, l.Name)
	}
	for _, l := range getRGALoad() {
		r.rgaKeys = append(r.rgaKeys, l.Name)
	}
	for _, t := range getThermalCached(app.thermalZonePaths) {
		r.tempKey = append(r.tempKey, t.Name)
	}
	sort.Strings(r.vpuKeys)
	sort.Strings(r.rgaKeys)

	header := []string{"timestamp", "cpu_pct", "mem_pct", "gpu_pct", "npu_pct",
		"load1", "net_rx_bps", "net_tx_bps", "disk_read_bps", "disk_write_bps", "throttled"}
	for _, k := range r.tempKey {
		header = append(header, "temp_"+k)
	}
	for _, k := range r.vpuKeys {
		header = append(header, "vpu_"+k)
	}
	for _, k := range r.rgaKeys {
		header = append(header, "rga_"+k)
	}
	if err := r.w.Write(header); err != nil {
		f.Close()
		return nil, err
	}
	r.w.Flush()
	return r, nil
}

func (r *csvRecorder) Sample(mon *SystemMonitor, app *AppState) {
	if r == nil {
		return
	}
	f2 := func(v float64) string { return strconv.FormatFloat(v, 'f', 2, 64) }

	var cpu float32
	usages := mon.CoreUsages()
	for _, u := range usages {
		cpu += u
	}
	if len(usages) > 0 {
		cpu /= float32(len(usages))
	}

	mem := getMemStats()
	memPct := float64(pct((mem.TotalKB-mem.AvailableKB)*1024, mem.TotalKB*1024))

	gpu := 0.0
	if v, ok := getGPUUsage(); ok {
		gpu = float64(v)
	}
	npu := 0.0
	if loads := getNPULoad(); len(loads) > 0 {
		var sum int
		for _, l := range loads {
			sum += int(l)
		}
		npu = float64(sum) / float64(len(loads))
	}
	one, _, _ := getLoadAverage()

	throttled := "false"
	for _, t := range getThrottleStates() {
		if t.Throttled {
			throttled = "true"
			break
		}
	}

	row := []string{
		time.Now().Format(time.RFC3339),
		f2(float64(cpu)), f2(memPct), f2(gpu), f2(npu), f2(one),
		f2(app.netRxRate), f2(app.netTxRate), f2(app.diskReadRate), f2(app.diskWriteRate),
		throttled,
	}

	temps := map[string]int32{}
	for _, t := range getThermalCached(app.thermalZonePaths) {
		temps[t.Name] = t.Temp
	}
	for _, k := range r.tempKey {
		row = append(row, strconv.Itoa(int(temps[k])))
	}

	loadByName := func(loads []namedLoad) map[string]float32 {
		m := map[string]float32{}
		for _, l := range loads {
			m[l.Name] = l.Load
		}
		return m
	}
	vpu := loadByName(getVPULoad())
	for _, k := range r.vpuKeys {
		row = append(row, f2(float64(vpu[k])))
	}
	rga := loadByName(getRGALoad())
	for _, k := range r.rgaKeys {
		row = append(row, f2(float64(rga[k])))
	}

	_ = r.w.Write(row)
	// Flushed every sample: a benchmark run that gets Ctrl-C'd should still
	// leave a complete file behind.
	r.w.Flush()
}

func (r *csvRecorder) Close() {
	if r == nil {
		return
	}
	r.w.Flush()
	r.f.Close()
}
