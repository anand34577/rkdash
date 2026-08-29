package main

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
)

var (
	colorInfo    = tcell.NewHexColor(0x6cc7d6)
	colorAccel   = tcell.NewHexColor(0x7ee787)
	colorIO      = tcell.NewHexColor(0xd9a441)
	colorProcess = tcell.NewHexColor(0xb389f0)

	colorGood  = tcell.NewHexColor(0x7ee787)
	colorWarn  = tcell.NewHexColor(0xf2c94c)
	colorCrit  = tcell.NewHexColor(0xff6b6b)
	colorCache = tcell.NewHexColor(0xf2c94c) // reclaimable page cache/buffers segment in the RAM bar — distinct from "used"

	colorText  = tcell.NewHexColor(0xe6e6e6)
	colorMuted = tcell.NewHexColor(0x8b96a5)
	colorBar   = tcell.NewHexColor(0x1c2333)
	colorSelBg = tcell.NewHexColor(0x3a2f5c)

	styleInfo     = tcell.StyleDefault.Foreground(colorInfo)
	styleAccel    = tcell.StyleDefault.Foreground(colorAccel)
	styleIO       = tcell.StyleDefault.Foreground(colorIO)
	styleProcess  = tcell.StyleDefault.Foreground(colorProcess)
	styleWhite    = tcell.StyleDefault.Foreground(colorText)
	styleGray     = tcell.StyleDefault.Foreground(colorMuted)
	styleBold     = tcell.StyleDefault.Foreground(colorText).Bold(true)
	styleBoldU    = tcell.StyleDefault.Foreground(colorText).Bold(true).Underline(true)
	styleDefault  = tcell.StyleDefault
	styleSelected = tcell.StyleDefault.Background(colorSelBg).Foreground(colorText).Bold(true)
	styleCrit     = tcell.StyleDefault.Foreground(colorCrit)
)

func severityStyle(value, warn, crit float64) tcell.Style {
	switch {
	case value >= crit:
		return tcell.StyleDefault.Foreground(colorCrit)
	case value >= warn:
		return tcell.StyleDefault.Foreground(colorWarn)
	default:
		return tcell.StyleDefault.Foreground(colorGood)
	}
}

func usageStyle(pct float32) tcell.Style  { return severityStyle(float64(pct), 60, 85) }
func tempStyle(celsius int32) tcell.Style { return severityStyle(float64(celsius), 60, 80) }

func drawUI(s tcell.Screen, mon *SystemMonitor, app *AppState) {
	w, h := s.Size()
	const footerH = 1

	renderHeaderBar(s, Rect{0, 0, w, 1}, app)
	renderFooterBar(s, Rect{0, h - footerH, w, footerH}, app)

	mem := getMemStats()
	renderKPIStrip(s, Rect{0, 1, w, 1}, mon, mem, app)

	body := Rect{0, 2, w, h - 2 - footerH}

	numCores := len(mon.CoreUsages())
	if numCores == 0 {
		numCores = 1
	}
	coreRows := (numCores + 1) / 2
	const memPanelHeight = 6
	const ioMinHeight = 6
	const tempMinHeight = 6

	cpuNeeded := coreRows + 4 + 2
	leftColMinNeeded := cpuNeeded + memPanelHeight + ioMinHeight + tempMinHeight
	rightNeeded := sumConstraintValues(rightPanelConstraints(app))
	topNeeded := maxInt(leftColMinNeeded, rightNeeded)

	mainChunks := splitVertical(body, []Constraint{Length(topNeeded), Min(10)})
	topChunks := splitHorizontal(mainChunks[0], []Constraint{Percent(50), Percent(50)})

	leftCol := splitVertical(topChunks[0], []Constraint{
		Length(cpuNeeded), Length(memPanelHeight), Min(ioMinHeight), Min(tempMinHeight),
	})
	renderCPUPanel(s, leftCol[0], mon, app)
	renderMemoryPanel(s, leftCol[1], mem)
	renderIOPanel(s, leftCol[2], app)
	renderTemperaturePanel(s, leftCol[3], app)

	renderRightPanels(s, topChunks[1], app, mon)

	renderProcessPanel(s, mainChunks[1], mon, app)

	if app.showHelp {
		renderHelpOverlay(s, Rect{0, 0, w, h})
	}
}

func rightPanelConstraints(app *AppState) []Constraint {
	cs := []Constraint{Length(8)}
	if app.hasGPU {
		cs = append(cs, Length(6))
	}
	if app.hasNPU {

		npuCoreRows := (maxInt(len(getNPULoad()), 1) + 1) / 2
		cs = append(cs, Length(npuCoreRows+4))
	}
	if app.hasRGA {

		cs = append(cs, Length(maxInt(len(getRGALoad()), 1)+2))
	}
	cs = append(cs, Min(7))
	return cs
}

func sumConstraintValues(cs []Constraint) int {
	total := 0
	for _, c := range cs {
		total += c.Value
	}
	return total
}

func renderKPIStrip(s tcell.Screen, area Rect, mon *SystemMonitor, mem MemStats, app *AppState) {
	kpiBg := tcell.NewHexColor(0x141a26)
	bg := tcell.StyleDefault.Background(kpiBg)
	for x := area.X; x < area.X+area.W; x++ {
		s.SetContent(x, area.Y, ' ', nil, bg)
	}

	usages := mon.CoreUsages()
	var totalCPU float32
	for _, u := range usages {
		totalCPU += u
	}
	if len(usages) > 0 {
		totalCPU /= float32(len(usages))
	}
	ramPct := pct((mem.TotalKB-mem.AvailableKB)*1024, mem.TotalKB*1024)
	cacheKB := mem.CacheKB()
	if cacheKB > mem.TotalKB {
		cacheKB = mem.TotalKB
	}
	var cacheFrac float32
	if mem.TotalKB > 0 {
		cacheFrac = float32(cacheKB) / float32(mem.TotalKB)
	}

	type gauge struct {
		label     string
		value     float32
		style     tcell.Style
		cacheFrac float32
	}
	gauges := []gauge{
		{"CPU", totalCPU, usageStyle(totalCPU), 0},
		{"MEM", float32(ramPct), severityStyle(float64(ramPct), 70, 90), cacheFrac},
	}
	if usage, ok := getGPUUsage(); ok {
		gauges = append(gauges, gauge{"GPU", usage, usageStyle(usage), 0})
	}
	if app.hasNPU {
		var avg float32
		if loads := getNPULoad(); len(loads) > 0 {
			var sum float32
			for _, l := range loads {
				sum += float32(l)
			}
			avg = sum / float32(len(loads))
		}
		gauges = append(gauges, gauge{"NPU", avg, usageStyle(avg), 0})
	}

	one, _, _ := getLoadAverage()
	numCPUs := len(usages)
	if numCPUs == 0 {
		numCPUs = 1
	}
	loadFg, _, _ := severityStyle(one/float64(numCPUs)*100, 70, 100).Decompose()
	loadText := fmt.Sprintf("LOAD %.2f ", one)

	fixedWidth := len([]rune(loadText))
	for _, g := range gauges {
		fixedWidth += len(g.label) + 1 + 7
	}
	barWidth := 10
	if len(gauges) > 0 {
		if avail := area.W - fixedWidth; avail > 0 {
			barWidth = avail / len(gauges)
		}
		if barWidth < 6 {
			barWidth = 6
		}
	}

	var spans []Span
	for _, g := range gauges {
		spans = append(spans, Span{Text: g.label + " ", Style: bg.Foreground(colorMuted)})
		if g.cacheFrac > 0 {
			spans = append(spans, segmentedBar(barWidth,
				[]float32{g.value / 100.0, g.cacheFrac},
				[]tcell.Style{g.style.Background(kpiBg), tcell.StyleDefault.Background(kpiBg).Foreground(colorCache)})...)
		} else {
			spans = append(spans, Span{Text: bar(barWidth, g.value/100.0), Style: g.style.Background(kpiBg)})
		}
		spans = append(spans, Span{Text: fmt.Sprintf(" %3.0f%%  ", g.value), Style: g.style.Background(kpiBg).Bold(true)})
	}
	spans = append(spans,
		Span{Text: "LOAD ", Style: bg.Foreground(colorMuted)},
		Span{Text: fmt.Sprintf("%.2f", one), Style: bg.Foreground(loadFg).Bold(true)},
	)

	drawText(s, area.X, area.Y, spans, area.W)
}

func renderHeaderBar(s tcell.Screen, area Rect, app *AppState) {
	bg := tcell.StyleDefault.Background(colorBar).Foreground(colorText)
	for x := area.X; x < area.X+area.W; x++ {
		s.SetContent(x, area.Y, ' ', nil, bg)
	}

	div := Span{Text: " │ ", Style: bg.Foreground(tcell.NewHexColor(0x3a4a63))}

	left := []Span{
		{Text: " rkdash", Style: bg.Foreground(colorInfo).Bold(true)},
		div,
		{Text: app.boardName, Style: bg.Foreground(colorText)},
		{Text: " (" + app.rkModel + ")", Style: bg.Foreground(colorAccel).Bold(true)},
	}
	if status := app.currentStatus(); status != "" {
		left = append(left, div, Span{Text: status, Style: bg.Foreground(colorWarn).Bold(true)})
	}
	drawText(s, area.X, area.Y, left, area.W)

	hostname := readTrimmed("/proc/sys/kernel/hostname", "")
	var rightSpans []Span
	if app.paused {
		rightSpans = append(rightSpans, Span{Text: "PAUSED", Style: bg.Foreground(colorWarn).Bold(true)}, div)
	}
	if hostname != "" {
		rightSpans = append(rightSpans, Span{Text: hostname, Style: bg.Foreground(colorMuted)}, div)
	}
	rightSpans = append(rightSpans, Span{Text: time.Now().Format("2006-01-02 15:04:05") + " ", Style: bg.Foreground(colorMuted)})

	width := 0
	for _, sp := range rightSpans {
		width += len([]rune(sp.Text))
	}
	x := area.X + area.W - width
	if x > area.X {
		drawText(s, x, area.Y, rightSpans, area.W)
	}
}

func renderFooterBar(s tcell.Screen, area Rect, app *AppState) {
	bg := tcell.StyleDefault.Background(colorBar).Foreground(colorText)
	for x := area.X; x < area.X+area.W; x++ {
		s.SetContent(x, area.Y, ' ', nil, bg)
	}

	if app.confirmingKill {
		prompt := []Span{
			{Text: " Kill PID ", Style: bg.Foreground(colorCrit)},
			{Text: fmt.Sprintf("%d", app.selectedPid), Style: bg.Foreground(colorCrit).Bold(true)},
			{Text: " (" + app.killTargetName + ")? ", Style: bg.Foreground(colorCrit)},
			{Text: "[y]", Style: bg.Foreground(colorCrit).Bold(true)},
			{Text: "es / any other key to cancel", Style: bg.Foreground(colorCrit)},
		}
		drawText(s, area.X, area.Y, prompt, area.W)
		return
	}

	sortName := map[ProcessSortMode]string{
		SortCpuDesc: "CPU↓", SortCpuAsc: "CPU↑",
		SortMemoryDesc: "Mem↓", SortMemoryAsc: "Mem↑",
		SortPidAsc: "PID↑", SortPidDesc: "PID↓",
		SortNameAsc: "Name↑", SortNameDesc: "Name↓",
	}[app.processSortMode]

	key := func(k string) Span { return Span{Text: k, Style: bg.Foreground(colorInfo).Bold(true)} }
	label := func(l string) Span { return Span{Text: l, Style: bg.Foreground(colorMuted)} }
	div := Span{Text: "│", Style: bg.Foreground(tcell.NewHexColor(0x3a4a63))}

	line := []Span{
		key("[C]"), label("PU "),
		key("[M]"), label("em "),
		key("[P]"), label("ID "),
		key("[N]"), label("ame:"),
		{Text: sortName + " ", Style: bg.Foreground(colorWarn).Bold(true)},
		div,
		key(" [/]"), label("Filter"),
	}
	if app.filterMode {
		line = append(line, label(": "), Span{Text: app.filterText, Style: bg.Foreground(colorWarn)}, Span{Text: "_", Style: bg.Foreground(colorWarn)})
	} else if app.filterText != "" {
		line = append(line, label(": "), Span{Text: app.filterText, Style: bg.Foreground(colorGood)})
	}
	line = append(line,
		div,
		key(" [↑↓]"), label("Nav"),
		div,
		key(" [x]"), label("Kill"),
		div,
		key(" [Space]"), label("Pause"),
		div,
		key(" [?]"), label("Help"),
		div,
		key(" [Q]"), label("uit "),
	)
	drawText(s, area.X, area.Y, line, area.W)
}

func renderHelpOverlay(s tcell.Screen, full Rect) {
	overlayBg := tcell.NewHexColor(0x11151c)
	overlayText := tcell.StyleDefault.Foreground(colorText).Background(overlayBg)
	overlayMuted := tcell.StyleDefault.Foreground(colorMuted).Background(overlayBg)

	line := func(s string) []Span { return []Span{{Text: s, Style: overlayText}} }
	lines := [][]Span{
		line("Sort:      c/C  m/M  p/P  n/N   (press again to reverse)"),
		line("Filter:    /  type to filter, Enter to confirm, Esc to clear"),
		line("Navigate:  Up/Down   select a process"),
		line("Kill:      x  then y to confirm SIGTERM to the selected process"),
		line("Pause:     Space     freeze all data refreshes"),
		line("Quit:      q / Q"),
		line(""),
		{{Text: "Press any key to close", Style: overlayMuted}},
	}

	boxW := 62
	boxH := len(lines) + 2
	if boxW > full.W-4 {
		boxW = full.W - 4
	}
	if boxH > full.H-4 {
		boxH = full.H - 4
	}
	area := Rect{
		X: full.X + (full.W-boxW)/2,
		Y: full.Y + (full.H-boxH)/2,
		W: boxW,
		H: boxH,
	}

	bg := tcell.StyleDefault.Background(overlayBg)
	for y := area.Y; y < area.Y+area.H; y++ {
		for x := area.X; x < area.X+area.W; x++ {
			s.SetContent(x, y, ' ', nil, bg)
		}
	}
	drawParagraph(s, area, "Keybindings", lines, tcell.StyleDefault.Foreground(colorProcess).Background(overlayBg))
}

func renderCPUPanel(s tcell.Screen, area Rect, mon *SystemMonitor, app *AppState) {
	usages := mon.CoreUsages()
	freqs := getCPUFrequencies()

	var totalUsage float32
	for _, u := range usages {
		totalUsage += u
	}
	if len(usages) > 0 {
		totalUsage /= float32(len(usages))
	}

	inner := drawBox(s, area, "CPU", styleInfo)
	if inner.H == 0 {
		return
	}
	y := inner.Y

	writeLine := func(spans []Span) {
		if y >= inner.Y+inner.H {
			return
		}
		drawText(s, inner.X, y, spans, inner.W)
		y++
	}

	writeLine([]Span{
		{Text: "Total CPU: "},
		{Text: fmt.Sprintf("%.1f%%", totalUsage), Style: usageStyle(totalUsage).Bold(true)},
	})

	if len(usages) > 0 {
		half := (len(usages) + 1) / 2
		colW := inner.W / 2
		barWidth := computeBarWidth(colW, 23, 8, 40)

		coreLine := func(i int) []Span {
			usage := usages[i]
			freq := uint32(0)
			if i < len(freqs) {
				freq = freqs[i]
			}
			return []Span{
				{Text: fmt.Sprintf("CPU %-2d ", i)},
				{Text: bar(barWidth, usage/100.0), Style: usageStyle(usage)},
				{Text: fmt.Sprintf(" %3.0f%% %4d MHz", usage, freq)},
			}
		}

		for row := 0; row < half; row++ {
			if y >= inner.Y+inner.H {
				break
			}
			drawText(s, inner.X, y, coreLine(row), colW)
			if right := row + half; right < len(usages) {
				drawText(s, inner.X+colW+1, y, coreLine(right), inner.W-colW-1)
			}
			y++
		}
	}

	writeLine(plain(fmt.Sprintf(
		"User %.0f%%  Sys %.0f%%  IOWait %.0f%%  Idle %.0f%%",
		app.cpuUserPct, app.cpuSystemPct, app.cpuIOWaitPct, app.cpuIdlePct)))

	if len(app.cpuFreqRanges) > 0 {
		var parts []string
		for _, r := range app.cpuFreqRanges {
			parts = append(parts, fmt.Sprintf("%d-%d", r[0], r[1]))
		}
		writeLine(plain("Freq: " + strings.Join(parts, ", ") + " MHz"))
	}

	writeLine(plain(fmt.Sprintf(
		"Run %d  Blk %d  Ctx %s/s  IRQ %s/s  SoftIRQ %s/s",
		app.runningProcs, app.blockedProcs,
		formatNumber(app.ctxSwitchesRate), formatNumber(app.interruptsRate), formatNumber(app.softirqsRate))))
}

func renderMemoryPanel(s tcell.Screen, area Rect, mem MemStats) {
	total := mem.TotalKB * 1024
	used := (mem.TotalKB - mem.AvailableKB) * 1024
	available := mem.AvailableKB * 1024
	swapTotal := mem.SwapTotalKB * 1024
	swapUsed := (mem.SwapTotalKB - mem.SwapFreeKB) * 1024

	// Cache/buffers are reclaimable, so they're excluded from "used" already
	// (used = total - available, and available counts most of the cache as
	// reclaimable). Show that cache as its own segment appended after used,
	// clamped so it can't overrun the bar if total is stale/zero.
	cacheKB := mem.CacheKB()
	if cacheKB > mem.TotalKB {
		cacheKB = mem.TotalKB
	}
	cache := cacheKB * 1024

	ramPercent := pct(used, total)
	swapPercent := pct(swapUsed, swapTotal)

	zram, hasZram := getZramInfo()
	zramPercent := uint64(0)
	if hasZram && zram.Limit > 0 {
		zramPercent = pct(zram.Used, zram.Limit)
	}

	barWidth := computeBarWidth(area.W-2, 35, 10, 50)

	usedFrac, cacheFrac := float32(0), float32(0)
	if total > 0 {
		usedFrac = float32(used) / float32(total)
		cacheFrac = float32(cache) / float32(total)
	}
	ramBar := []Span{{Text: "RAM  "}}
	ramBar = append(ramBar, segmentedBar(barWidth,
		[]float32{usedFrac, cacheFrac},
		[]tcell.Style{severityStyle(float64(ramPercent), 70, 90), tcell.StyleDefault.Foreground(colorCache)})...)
	combinedUsed := used + cache
	combinedPercent := pct(combinedUsed, total)
	ramBar = append(ramBar, Span{Text: fmt.Sprintf(" %3d%% | %s / %s",
		combinedPercent, humanBytes(combinedUsed), humanBytes(total))})

	lines := [][]Span{
		{
			{Text: "Total: "}, {Text: humanBytes(total), Style: styleWhite},
			{Text: "  Free: "}, {Text: humanBytes(available), Style: styleWhite},
			{Text: "  Used: "}, {Text: humanBytes(used), Style: styleWhite},
			{Text: "  Cache: "}, {Text: humanBytes(cache), Style: tcell.StyleDefault.Foreground(colorCache)},
		},
		ramBar,
		{
			{Text: "Swap "},
			{Text: bar(barWidth, float32(swapPercent)/100.0), Style: severityStyle(float64(swapPercent), 40, 75)},
			{Text: fmt.Sprintf(" %3d%% | %s / %s", swapPercent, humanBytes(swapUsed), humanBytes(swapTotal))},
		},
	}

	zramLine := []Span{
		{Text: "ZRAM "},
		{Text: bar(barWidth, float32(zramPercent)/100.0), Style: severityStyle(float64(zramPercent), 70, 90)},
	}
	if hasZram {
		ratio := zram.CompressionRatio()
		ratioStr := "N/A"
		if ratio > 0 {
			ratioStr = fmt.Sprintf("%.1f", ratio)
		}
		zramLine = append(zramLine, Span{Text: fmt.Sprintf(" %3d%% | %s / %s (%sx)",
			zramPercent, humanBytes(zram.Used), humanBytes(zram.Limit), ratioStr)})
	} else {
		zramLine = append(zramLine, Span{Text: " N/A"})
	}
	lines = append(lines, zramLine)

	drawParagraph(s, area, "Memory", lines, styleInfo)
}

func renderRightPanels(s tcell.Screen, area Rect, app *AppState, mon *SystemMonitor) {
	chunks := splitVertical(area, rightPanelConstraints(app))
	idx := 0
	renderSystemPanel(s, chunks[idx], app)
	idx++
	if app.hasGPU {
		renderGPUPanel(s, chunks[idx], app)
		idx++
	}
	if app.hasNPU {
		renderNPUPanel(s, chunks[idx], app)
		idx++
	}
	if app.hasRGA {
		renderRGAPanel(s, chunks[idx])
		idx++
	}
	renderStatsPanel(s, chunks[idx], app)
}

func renderSystemPanel(s tcell.Screen, area Rect, app *AppState) {
	hostname := readTrimmed("/proc/sys/kernel/hostname", "Unknown")
	kernel := readTrimmed("/proc/sys/kernel/osrelease", "Unknown")

	rowData := [][2]string{
		{"Board: " + app.boardName, "Host: " + hostname},
		{"SoC: " + app.rkModel, "Kernel: " + kernel},
		{"NPU Driver:    " + app.npuVersion, "Arch: " + app.cpuArch},
		{"RGA Driver:    " + app.rgaVersion, ""},
		{"RKNN Runtime:  " + app.rknnVersion, ""},
		{"RKLLM Runtime: " + app.rkllmVersion, ""},
	}

	inner := drawBox(s, area, "SYS", styleInfo)
	colW := inner.W / 2
	for i, row := range rowData {
		if i >= inner.H {
			break
		}
		drawText(s, inner.X, inner.Y+i, plain(row[0]), colW)
		drawText(s, inner.X+colW+1, inner.Y+i, plain(row[1]), colW-1)
	}
}

func renderGPUPanel(s tcell.Screen, area Rect, app *AppState) {
	usage, ok := getGPUUsage()
	if !ok {
		return
	}
	freqStr := ""
	if freq, ok := getGPUFrequency(); ok {
		freqStr = fmt.Sprintf(" %d MHz", freq)
	}

	barWidth := computeBarWidth(area.W-2, 25, 10, 50)
	lines := [][]Span{
		{
			{Text: "Mali0 "},
			{Text: bar(barWidth, usage/100.0), Style: usageStyle(usage)},
			{Text: fmt.Sprintf(" %5.2f%%%s", usage, freqStr)},
		},
	}
	if len(app.gpuHistory) > 0 {
		lines = append(lines, []Span{
			{Text: "History: "},
			{Text: renderSparkline(app.gpuHistory, 100.0), Style: styleAccel},
		})
	}
	drawParagraph(s, area, "GPU", lines, styleAccel)
}

func renderNPUPanel(s tcell.Screen, area Rect, app *AppState) {
	loads := getNPULoad()
	if len(loads) == 0 {
		return
	}
	freqStr := ""
	if freq, ok := getNPUFrequency(); ok {
		freqStr = fmt.Sprintf(" %d MHz", freq)
	}

	inner := drawBox(s, area, "NPU", styleAccel)
	if inner.H == 0 {
		return
	}
	y := inner.Y

	half := (len(loads) + 1) / 2
	colW := inner.W / 2
	barWidth := computeBarWidth(colW, 12, 8, 30)

	coreLine := func(i int) []Span {
		suffix := ""
		if i == 0 {
			suffix = freqStr
		}
		return []Span{
			{Text: fmt.Sprintf("Core %d ", i)},
			{Text: bar(barWidth, float32(loads[i])/100.0), Style: usageStyle(float32(loads[i]))},
			{Text: fmt.Sprintf(" %3d%%%s", loads[i], suffix)},
		}
	}

	for row := 0; row < half; row++ {
		if y >= inner.Y+inner.H {
			break
		}
		drawText(s, inner.X, y, coreLine(row), colW)
		if right := row + half; right < len(loads) {
			drawText(s, inner.X+colW+1, y, coreLine(right), inner.W-colW-1)
		}
		y++
	}

	if len(app.npuHistory) > 0 && y+1 < inner.Y+inner.H {
		y++
		drawText(s, inner.X, y, []Span{
			{Text: "History: "},
			{Text: renderSparkline(app.npuHistory, 100.0), Style: styleAccel},
		}, inner.W)
	}
}

func renderRGAPanel(s tcell.Screen, area Rect) {
	loads := getRGALoad()
	if len(loads) == 0 {
		return
	}
	barWidth := computeBarWidth(area.W-2, 20, 10, 40)
	var lines [][]Span
	for _, l := range loads {
		lines = append(lines, []Span{
			{Text: fmt.Sprintf("%-6s ", l.Name)},
			{Text: bar(barWidth, l.Load/100.0), Style: usageStyle(l.Load)},
			{Text: fmt.Sprintf(" %5.1f%%", l.Load)},
		})
	}
	drawParagraph(s, area, "RGA", lines, styleAccel)
}

func renderStatsPanel(s tcell.Screen, area Rect, app *AppState) {
	uptimeSecs := getUptimeSeconds()
	days := uptimeSecs / 86400
	hours := (uptimeSecs % 86400) / 3600
	mins := (uptimeSecs % 3600) / 60
	var uptimeStr string
	switch {
	case days > 0:
		uptimeStr = fmt.Sprintf("%dd %dh %dm", days, hours, mins)
	case hours > 0:
		uptimeStr = fmt.Sprintf("%dh %dm", hours, mins)
	default:
		uptimeStr = fmt.Sprintf("%dm", mins)
	}

	one, five, fifteen := getLoadAverage()

	totalProcesses := 0
	if entries, err := os.ReadDir("/proc"); err == nil {
		for _, e := range entries {
			if _, err := parsePid(e.Name()); err == nil {
				totalProcesses++
			}
		}
	}

	numCPUs := len(getCPUFrequencies())
	if numCPUs == 0 {
		numCPUs = 1
	}
	loadPct := one / float64(numCPUs) * 100

	lines := [][]Span{
		plain(fmt.Sprintf("Uptime:     %s", uptimeStr)),
		{
			{Text: "Load Avg:   "},
			{Text: fmt.Sprintf("%.2f", one), Style: severityStyle(loadPct, 70, 100)},
			{Text: fmt.Sprintf(" %.2f %.2f", five, fifteen)},
		},
		plain(fmt.Sprintf("Governor:   %s", app.cpuGovernor)),
		plain(fmt.Sprintf("Processes:  %d", totalProcesses)),
		plain(fmt.Sprintf("TCP Conns:  %d", app.tcpConnections)),
	}

	drawParagraph(s, area, "Stats", lines, styleInfo)
}

func renderIOPanel(s tcell.Screen, area Rect, app *AppState) {
	type row struct {
		label, value string
		valueStyle   tcell.Style
	}
	var rows []row
	plainRow := func(label, value string) row { return row{label, value, styleInfo} }
	rows = append(rows,
		plainRow("Disk Read", humanBytesF64(app.diskReadRate)+"/s"),
		plainRow("Disk Write", humanBytesF64(app.diskWriteRate)+"/s"),
		plainRow("Net RX (Tot)", humanBytesF64(app.netRxRate)+"/s"),
		plainRow("Net TX (Tot)", humanBytesF64(app.netTxRate)+"/s"),
	)

	if used, total, ok := getDiskTotal(); ok {
		percent := pct(used, total)
		rows = append(rows, row{"Disk Space", fmt.Sprintf("%s / %s (%d%%)", humanBytes(used), humanBytes(total), percent), severityStyle(float64(percent), 80, 95)})
	}

	var names []string
	for name := range app.adapterRates {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if ip, ok := getInterfaceIPv4(name); ok {
			rows = append(rows, row{name + " IP", ip, styleWhite})
		}
		rt := app.adapterRates[name]
		rows = append(rows, plainRow(name+" RX", humanBytesF64(rt[0])+"/s"))
		rows = append(rows, plainRow(name+" TX", humanBytesF64(rt[1])+"/s"))
	}

	inner := drawBox(s, area, "I/O", styleIO)
	colW := inner.W / 2
	for i, r := range rows {
		if i >= inner.H {
			break
		}
		drawText(s, inner.X, inner.Y+i, []Span{{Text: r.label, Style: styleGray}}, colW)
		drawText(s, inner.X+colW+1, inner.Y+i, []Span{{Text: r.value, Style: r.valueStyle}}, colW-1)
	}
}

func renderTemperaturePanel(s tcell.Screen, area Rect, app *AppState) {
	type row struct {
		name, value string
		valueStyle  tcell.Style
	}
	var rows []row

	for _, t := range getThermalCached(app.thermalZonePaths) {
		rows = append(rows, row{t.Name, fmt.Sprintf("%d°C", t.Temp), tempStyle(t.Temp)})
	}
	if gpuTemp, ok := getGPUTemperature(); ok {
		rows = append(rows, row{"GPU", fmt.Sprintf("%d°C", gpuTemp), tempStyle(gpuTemp)})
	}
	for _, hw := range getHwmonSensors() {
		rows = append(rows, row{hw.Name, hw.Value, styleWhite})
	}

	inner := drawBox(s, area, "Temperatures", styleIO)
	colW := inner.W * 6 / 10
	for i, r := range rows {
		if i >= inner.H {
			break
		}
		drawText(s, inner.X, inner.Y+i, []Span{{Text: r.name, Style: styleGray}}, colW)
		drawText(s, inner.X+colW+1, inner.Y+i, []Span{{Text: r.value, Style: r.valueStyle}}, inner.W-colW-1)
	}
}

func renderProcessPanel(s tcell.Screen, area Rect, mon *SystemMonitor, app *AppState) {
	availableRows := area.H - 3
	if availableRows < 0 {
		availableRows = 0
	}

	processCount := availableRows * 3
	if processCount < 20 {
		processCount = 20
	}

	allProcs := mon.TopProcesses(app.processSortMode, processCount)

	filterLower := strings.ToLower(app.filterText)
	var filtered []ProcessInfo
	if filterLower == "" {
		filtered = allProcs
	} else {
		for _, p := range allProcs {
			if strings.Contains(strings.ToLower(p.Name), filterLower) || strings.Contains(strings.ToLower(p.User), filterLower) {
				filtered = append(filtered, p)
			}
		}
	}

	stillVisible := false
	for _, p := range filtered {
		if !p.IsThread && p.Pid == app.selectedPid {
			stillVisible = true
			break
		}
	}
	if !stillVisible {
		app.selectedPid = 0
		for _, p := range filtered {
			if !p.IsThread {
				app.selectedPid = p.Pid
				break
			}
		}
	}

	var rows [][]Span
	var visiblePids []int32
	seen := make(map[int32]bool)
	for _, p := range filtered {
		if p.IsThread || seen[p.Pid] {
			continue
		}
		seen[p.Pid] = true
		visiblePids = append(visiblePids, p.Pid)

		rowStyle := tcell.StyleDefault
		selected := p.Pid == app.selectedPid
		if selected {
			rowStyle = styleSelected
			app.selectedName = p.Name
		}
		cell := func(text string) Span { return Span{Text: text, Style: rowStyle} }
		cpuCell := cell(fmt.Sprintf("%.1f", p.Cpu))
		memCell := cell(fmt.Sprintf("%.1f", p.Mem))
		if !selected {
			cpuCell.Style = usageStyle(p.Cpu)
			memCell.Style = usageStyle(p.Mem)
		}

		rows = append(rows, []Span{
			cell(fmt.Sprintf("%d", p.Pid)),
			cell(p.User),
			cell(string(p.State)),
			cell(fmt.Sprintf("%3d", p.Nice)),
			cell(fmt.Sprintf("%d", p.CpuCore)),
			cell(fmt.Sprintf("%d", p.NumThreads)),
			cell(runtimeStr(p.Runtime)),
			cell(p.Name),
			cpuCell,
			memCell,
		})

		var threads []ProcessInfo
		for _, t := range filtered {
			if t.IsThread && t.ThreadGroupID == p.Pid {
				threads = append(threads, t)
			}
		}
		for i, t := range threads {
			prefix := " ├─"
			if i == len(threads)-1 {
				prefix = " └─"
			}
			tCell := func(text string) Span { return Span{Text: text, Style: rowStyle} }
			rows = append(rows, []Span{
				tCell(fmt.Sprintf("%s%d", prefix, t.Pid)),
				tCell(""),
				tCell(string(t.State)),
				tCell(fmt.Sprintf("%3d", t.Nice)),
				tCell(fmt.Sprintf("%d", t.CpuCore)),
				tCell(""),
				tCell(runtimeStr(t.Runtime)),
				tCell(t.Name + " [thread]"),
				tCell(fmt.Sprintf("%.1f", t.Cpu)),
				tCell(fmt.Sprintf("%.1f", t.Mem)),
			})
		}
	}
	app.visiblePids = visiblePids

	pidText, pidStyle := sortHeader(app.processSortMode, SortPidAsc, SortPidDesc, "PID")
	nameText, nameStyle := sortHeader(app.processSortMode, SortNameAsc, SortNameDesc, "Name")
	cpuText, cpuStyle := sortHeader(app.processSortMode, SortCpuAsc, SortCpuDesc, "CPU%")
	memText, memStyle := sortHeader(app.processSortMode, SortMemoryAsc, SortMemoryDesc, "Mem%")

	header := []Span{
		{Text: pidText, Style: pidStyle},
		{Text: "User", Style: styleBold},
		{Text: "S", Style: styleBold},
		{Text: "NI", Style: styleBold},
		{Text: "C", Style: styleBold},
		{Text: "THR", Style: styleBold},
		{Text: "Time", Style: styleBold},
		{Text: nameText, Style: nameStyle},
		{Text: cpuText, Style: cpuStyle},
		{Text: memText, Style: memStyle},
	}

	const fixedColsWidth = 9 + 9 + 1 + 3 + 2 + 3 + 9 + 6 + 6
	const numGaps = 9
	nameWidth := maxInt(area.W-fixedColsWidth-numGaps, 10)
	colWidths := []int{9, 9, 1, 3, 2, 3, 9, nameWidth, 6, 6}

	drawTable(s, area, "Processes", header, rows, colWidths, styleProcess, styleBold)
}

func sortHeader(mode, ascMode, descMode ProcessSortMode, label string) (string, tcell.Style) {
	switch mode {
	case ascMode:
		return label + "↑", styleBoldU
	case descMode:
		return label + "↓", styleBoldU
	default:
		return label, styleBold
	}
}

func pct(part, whole uint64) uint64 {
	if whole == 0 {
		return 0
	}
	return part * 100 / whole
}

func humanBytes(v uint64) string { return humanBytesF64(float64(v)) }

func humanBytesF64(v float64) string {
	units := []string{"B", "KB", "MB", "GB", "TB"}
	i := 0
	for v >= 1024.0 && i < len(units)-1 {
		v /= 1024.0
		i++
	}
	return fmt.Sprintf("%.1f %s", v, units[i])
}

func formatNumber(n uint64) string {
	switch {
	case n < 1000:
		return fmt.Sprintf("%d", n)
	case n < 1_000_000:
		return fmt.Sprintf("%.1fK", float64(n)/1000.0)
	case n < 1_000_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000.0)
	default:
		return fmt.Sprintf("%.1fG", float64(n)/1_000_000_000.0)
	}
}

func runtimeStr(secs uint64) string {
	hours := secs / 3600
	mins := (secs % 3600) / 60
	s := secs % 60
	if hours > 0 {
		return fmt.Sprintf("%d:%02d:%02d", hours, mins, s)
	}
	return fmt.Sprintf("%d:%02d", mins, s)
}

func readTrimmed(path, fallback string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return fallback
	}
	return strings.TrimSpace(string(data))
}

func parsePid(name string) (int, error) {
	n := 0
	for _, c := range name {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("not numeric")
		}
		n = n*10 + int(c-'0')
	}
	if n == 0 {
		return 0, fmt.Errorf("empty")
	}
	return n, nil
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
