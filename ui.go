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
	colorTrack = tcell.NewHexColor(0x2a3140) // unfilled portion of every meter/graph
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

	// Each left-column panel contributes its height only when visible, so
	// toggling one off gives its rows back to the rest of the screen instead
	// of leaving a gap.
	vis := func(name string, h int) int {
		if app.cfg.Visible(name) {
			return h
		}
		return 0
	}
	memPanelHeight := vis("memory", 7)
	ioNeeded := vis("io", gridRows(len(buildIORows(app)))+2)
	tempNeeded := vis("temps", gridRows(len(buildTemperatureRows(app)))+2)
	powerNeeded := vis("power", gridRows(len(buildPowerRows()))+2)
	cpuNeeded := vis("cpu", coreRows+4+2)

	leftNeeded := cpuNeeded + memPanelHeight + ioNeeded + tempNeeded + powerNeeded
	rightNeeded := sumConstraintValues(rightPanelConstraints(app))

	// Both columns are sized to exactly what their panels need (no
	// stretch-to-fill Min panel absorbing slack into a near-empty box), so
	// the shorter column just leaves plain background below its last panel
	// — never an oversized bordered box with dead space inside it.
	topNeeded := maxInt(leftNeeded, rightNeeded)

	// Guarantee the Processes table below always gets a usable amount of
	// room, even on a board whose accelerator panels alone would otherwise
	// need more rows than the terminal has (e.g. RK3588's 13 VPU blocks) —
	// clamp the top area rather than let it crowd Processes out entirely.
	const minProcessRows = 8
	if maxTop := body.H - minProcessRows; topNeeded > maxTop {
		topNeeded = maxInt(maxTop, 0)
	}

	mainChunks := splitVertical(body, []Constraint{Length(topNeeded), Min(minProcessRows)})
	topChunks := splitHorizontal(mainChunks[0], []Constraint{Percent(50), Percent(50)})

	leftCol := splitVertical(topChunks[0], []Constraint{
		Length(cpuNeeded), Length(memPanelHeight), Length(ioNeeded),
		Length(tempNeeded), Length(powerNeeded),
	})
	if cpuNeeded > 0 {
		renderCPUPanel(s, leftCol[0], mon, app)
	}
	if memPanelHeight > 0 {
		renderMemoryPanel(s, leftCol[1], mem, app)
	}
	if ioNeeded > 0 {
		renderIOPanel(s, leftCol[2], app)
	}
	if tempNeeded > 0 {
		renderTemperaturePanel(s, leftCol[3], app)
	}
	if powerNeeded > 0 {
		renderPowerPanel(s, leftCol[4])
	}

	renderRightPanels(s, topChunks[1], app, mon)

	renderProcessPanel(s, mainChunks[1], mon, app)

	switch {
	case app.showHelp:
		renderHelpOverlay(s, Rect{0, 0, w, h})
	case app.showDetail && app.selectedPid != 0:
		renderDetailOverlay(s, Rect{0, 0, w, h}, app)
	}
}

// rightPanelConstraints sizes every right-column panel to exactly what it
// will render — no panel is padded past its content, so a short column
// leaves plain background below it instead of one panel stretching into a
// mostly-empty box.
func rightPanelConstraints(app *AppState) []Constraint {
	// A hidden panel contributes Length(0) rather than being dropped, so the
	// index each panel renders into stays fixed regardless of what's toggled.
	show := func(name string, present bool, h int) Constraint {
		if present && app.cfg.Visible(name) {
			return Length(h)
		}
		return Length(0)
	}
	return []Constraint{
		show("sys", true, 8),
		show("gpu", app.hasGPU, gpuPanelHeight(app)),
		show("npu", app.hasNPU, npuPanelHeight()),
		show("rga", app.hasRGA, gridRows(len(getRGALoad()))+2),
		show("vpu", app.hasVPU, gridRows(len(getVPULoad()))+2),
		show("stats", true, 7),
	}
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
			spans = append(spans, gradientBar(barWidth, g.value/100.0, bg)...)
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
		{Text: " " + displayVersion(), Style: bg.Foreground(colorMuted)},
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
		key(" [a]"), label("ccel"),
	)
	if app.accelOnly {
		line = append(line, Span{Text: "*", Style: bg.Foreground(colorAccel).Bold(true)})
	}
	line = append(line,
		div,
		key(" [↵]"), label("Detail"),
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
		line("Accel:     a  show only NPU/VPU/RGA/GPU users   b  badge column"),
		line("Navigate:  Up/Down, PgUp/PgDn   (list scrolls to follow)"),
		line("Mouse:     click a row to select, wheel to scroll"),
		line("Detail:    Enter  open the selected process's pane"),
		line("Kill:      x  then y to confirm SIGTERM to the selected process"),
		line("Panels:    1..9,0 toggle " + strings.Join(panelOrder[:minInt(10, len(panelOrder))], " ")),
		line("Save:      S  write the current layout/sort to the config file"),
		line("Pause:     Space     freeze all data refreshes"),
		line("Quit:      q / Q"),
		line(""),
		{{Text: "Press any key to close", Style: overlayMuted}},
	}

	boxW := 72
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

	totalLine := []Span{
		{Text: "Total CPU: "},
		{Text: fmt.Sprintf("%5.1f%% ", totalUsage), Style: usageStyle(totalUsage).Bold(true)},
	}
	totalLine = append(totalLine, graphSpans(app.cpuHistory, 100, inner.W-18, styleDefault)...)
	writeLine(totalLine)

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
			spans := []Span{{Text: fmt.Sprintf("CPU %-2d ", i)}}
			spans = append(spans, gradientBar(barWidth, usage/100.0, styleDefault)...)
			return append(spans, Span{Text: fmt.Sprintf(" %3.0f%% %4d MHz", usage, freq)})
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

func renderMemoryPanel(s tcell.Screen, area Rect, mem MemStats, app *AppState) {
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

	memLine := []Span{
		{Text: "Total: "}, {Text: humanBytes(total), Style: styleWhite},
		{Text: "  Free: "}, {Text: humanBytes(available), Style: styleWhite},
		{Text: "  Used: "}, {Text: humanBytes(used), Style: styleWhite},
		{Text: "  Cache: "}, {Text: humanBytes(cache), Style: tcell.StyleDefault.Foreground(colorCache)},
	}
	if freq, ok := getDMCFrequency(); ok {
		memLine = append(memLine, Span{Text: fmt.Sprintf("  DMC: %d MHz", freq), Style: styleWhite})
	}

	lines := [][]Span{
		memLine,
		ramBar,
		append(append([]Span{{Text: "Swap "}}, gradientBar(barWidth, float32(swapPercent)/100.0, styleDefault)...),
			Span{Text: fmt.Sprintf(" %3d%% | %s / %s", swapPercent, humanBytes(swapUsed), humanBytes(swapTotal))}),
	}

	zramLine := append([]Span{{Text: "ZRAM "}}, gradientBar(barWidth, float32(zramPercent)/100.0, styleDefault)...)
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
	lines = append(lines, append([]Span{{Text: "Hist ", Style: styleGray}},
		graphSpans(app.memHistory, 100, area.W-8, styleDefault)...))

	drawParagraph(s, area, "Memory", lines, styleInfo)
}

func renderRightPanels(s tcell.Screen, area Rect, app *AppState, mon *SystemMonitor) {
	// Positions match rightPanelConstraints exactly; a hidden panel gets a
	// zero-height rect and drawBox declines to draw it.
	chunks := splitVertical(area, rightPanelConstraints(app))
	renderSystemPanel(s, chunks[0], app)
	renderGPUPanel(s, chunks[1], app)
	renderNPUPanel(s, chunks[2], app)
	renderRGAPanel(s, chunks[3], app)
	renderVPUPanel(s, chunks[4], app)
	renderStatsPanel(s, chunks[5], app)
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
	usage, usageOK := getGPUUsage()
	freqStr := " freq N/A"
	if freq, ok := getGPUFrequency(); ok {
		freqStr = fmt.Sprintf(" %d MHz", freq)
	}

	barWidth := computeBarWidth(area.W-2, 25, 10, 50)
	var gpuLine []Span
	if usageOK {
		gpuLine = append([]Span{{Text: "Mali0 "}}, gradientBar(barWidth, usage/100.0, styleDefault)...)
		gpuLine = append(gpuLine, Span{Text: fmt.Sprintf(" %5.2f%%%s", usage, freqStr)})
	} else {
		// ponytail: Mali devfreq utilization node isn't wired up on this
		// kernel build (common on RK3566 BSP kernels) — show clock only.
		gpuLine = []Span{
			{Text: "Mali0 "},
			{Text: "utilization N/A", Style: styleGray},
			{Text: freqStr},
		}
	}
	lines := [][]Span{gpuLine}
	if len(app.gpuHistory) > 0 {
		lines = append(lines, append([]Span{{Text: "Hist  ", Style: styleGray}},
			graphSpans(app.gpuHistory, 100, area.W-9, styleDefault)...))
	}
	drawParagraph(s, area, "GPU", lines, styleAccel)
}

// gpuPanelHeight is the exact box height renderGPUPanel needs: border plus
// its one status line, plus a history line once there's history to show.
func gpuPanelHeight(app *AppState) int {
	h := 3
	if len(app.gpuHistory) > 0 {
		h++
	}
	return h
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

	half := gridRows(len(loads))
	colW := inner.W / 2
	barWidth := computeBarWidth(colW, 12, 8, 30)

	coreLine := func(i int) []Span {
		suffix := ""
		if i == 0 {
			suffix = freqStr
		}
		spans := []Span{{Text: fmt.Sprintf("Core %d ", i)}}
		spans = append(spans, gradientBar(barWidth, float32(loads[i])/100.0, styleDefault)...)
		spans = append(spans, Span{Text: fmt.Sprintf(" %3d%%%s", loads[i], suffix)})
		if h := app.accelHistory[fmt.Sprintf("npu:%d", i)]; len(h) > 0 {
			if gw := colW - barWidth - 14 - len(suffix); gw >= 4 {
				spans = append(spans, Span{Text: " "})
				spans = append(spans, graphSpans(h, 100, gw, styleDefault)...)
			}
		}
		return spans
	}

	y := inner.Y
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
}

// npuPanelHeight is the exact box height renderNPUPanel needs — border plus
// its core rows, nothing more — so the layout never reserves blank space
// for it.
func npuPanelHeight() int { return gridRows(len(getNPULoad())) + 2 }

// gridCols picks how many items renderLoadGrid/renderLabelGrid pack per
// row: 3 once a panel has enough entries that 2 columns would still run
// tall (RK3588 reports 13 VPU blocks; RK3577's I/O panel lists ~14 rows
// across disks/interfaces), 2 otherwise.
func gridCols(n int) int {
	if n > 8 {
		return 3
	}
	return 2
}

// gridRows is the row count a gridCols(n)-column grid needs for n items —
// right-column panel constraints size against this so the box height
// always matches what actually gets drawn.
func gridRows(n int) int {
	n = maxInt(n, 1)
	cols := gridCols(n)
	return (n + cols - 1) / cols
}

// renderLoadGrid draws named load bars several-per-row (like the CPU/NPU
// core grids) instead of one-per-line, so boards with many accelerator
// blocks (RK3588's 13 VPU blocks, RK3576's dual RGA schedulers, ...) don't
// blow up panel height and starve the rest of the screen of vertical space.
// sessions, if non-nil, annotates each bar with the owning PID looked up by
// the load name stripped of its "_N" instance suffix.
func renderLoadGrid(s tcell.Screen, area Rect, title string, style tcell.Style, loads []namedLoad, sessions map[string][]int32, hist map[string][]float32, histPrefix string) {
	if len(loads) == 0 {
		return
	}
	inner := drawBox(s, area, title, style)
	if inner.H == 0 {
		return
	}

	cols := gridCols(len(loads))
	rows := gridRows(len(loads))
	colW := inner.W / cols
	nameWidth := 8
	for _, l := range loads {
		if n := len(l.Name); n > nameWidth {
			nameWidth = n
		}
	}
	barWidth := computeBarWidth(colW, nameWidth+14, 6, 30)

	itemLine := func(l namedLoad, w int) []Span {
		spans := []Span{{Text: fmt.Sprintf("%-*s ", nameWidth, l.Name)}}
		spans = append(spans, gradientBar(barWidth, l.Load/100.0, styleDefault)...)
		spans = append(spans, Span{Text: fmt.Sprintf(" %5.1f%%", l.Load)})
		// Trailing history trace in whatever width is left — the accelerator
		// panels are the reason to run rkdash, so they get the same treatment
		// CPU and memory got.
		if h := hist[histPrefix+l.Name]; len(h) > 0 {
			if gw := w - nameWidth - barWidth - 9; gw >= 4 {
				spans = append(spans, Span{Text: " "})
				spans = append(spans, graphSpans(h, 100, gw, styleDefault)...)
			}
		}
		if sessions != nil {
			base := l.Name
			if i := strings.LastIndex(base, "_"); i > 0 {
				base = base[:i]
			}
			if pids := sessions[base]; len(pids) > 0 {
				spans = append(spans, Span{Text: fmt.Sprintf(" p%d", pids[0]), Style: styleGray})
			}
		}
		return spans
	}

	for row := 0; row < rows; row++ {
		y := inner.Y + row
		if y >= inner.Y+inner.H {
			break
		}
		for col := 0; col < cols; col++ {
			idx := col*rows + row
			if idx >= len(loads) {
				break
			}
			x := inner.X + col*colW
			w := colW - 1
			if col == cols-1 {
				w = inner.X + inner.W - x
			}
			drawText(s, x, y, itemLine(loads[idx], w), w)
		}
	}
}

func renderRGAPanel(s tcell.Screen, area Rect, app *AppState) {
	renderLoadGrid(s, area, "RGA", styleAccel, getRGALoad(), nil, app.accelHistory, "rga:")
}

func renderVPUPanel(s tcell.Screen, area Rect, app *AppState) {
	renderLoadGrid(s, area, "VPU", styleAccel, getVPULoad(), getMPPSessions(), app.accelHistory, "vpu:")
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

// labelValue is a name/value pair for the dense label:value grid panels
// (I/O, Temperatures) — mirrors namedLoad's role for the load-bar grids.
type labelValue struct {
	label, value string
	valueStyle   tcell.Style
	// graph, when set, appends a history trace after the value using whatever
	// column width is left over; graphMax scales it.
	graph    []float32
	graphMax float32
}

// renderLabelGrid draws label:value rows two per line instead of one, so
// panels with many rows (several network interfaces, a full sensor list)
// don't need as much vertical space, freeing rows for the rest of the UI.
func renderLabelGrid(s tcell.Screen, area Rect, title string, style tcell.Style, rows []labelValue) {
	if len(rows) == 0 {
		return
	}
	inner := drawBox(s, area, title, style)
	if inner.H == 0 {
		return
	}

	cols := gridCols(len(rows))
	gridH := gridRows(len(rows))
	colW := inner.W / cols
	labelWidth := 8
	for _, r := range rows {
		if n := len([]rune(r.label)); n > labelWidth {
			labelWidth = n
		}
	}
	if max := colW - 8; labelWidth > max && max >= 6 {
		labelWidth = max
	}

	itemLine := func(r labelValue, w int) []Span {
		spans := []Span{
			{Text: fmt.Sprintf("%-*s ", labelWidth, r.label), Style: styleGray},
			{Text: r.value, Style: r.valueStyle},
		}
		if len(r.graph) > 0 {
			if gw := w - labelWidth - 2 - len([]rune(r.value)); gw >= 4 {
				spans = append(spans, Span{Text: " "})
				spans = append(spans, graphSpans(r.graph, r.graphMax, gw, styleDefault)...)
			}
		}
		return spans
	}

	for row := 0; row < gridH; row++ {
		y := inner.Y + row
		if y >= inner.Y+inner.H {
			break
		}
		for col := 0; col < cols; col++ {
			idx := col*gridH + row
			if idx >= len(rows) {
				break
			}
			x := inner.X + col*colW
			w := colW - 1
			if col == cols-1 {
				w = inner.X + inner.W - x
			}
			drawText(s, x, y, itemLine(rows[idx], w), w)
		}
	}
}

func buildIORows(app *AppState) []labelValue {
	var rows []labelValue
	plainRow := func(label, value string) labelValue { return labelValue{label: label, value: value, valueStyle: styleInfo} }
	// Rates are unbounded, so each trace scales to its own running peak — the
	// shape of the traffic is the useful signal, not an absolute scale.
	rateRow := func(label string, rate float64, hist []float32) labelValue {
		return labelValue{label: label, value: humanBytesF64(rate) + "/s", valueStyle: styleInfo, graph: hist, graphMax: histMax(hist)}
	}
	rows = append(rows,
		rateRow("Disk Read", app.diskReadRate, app.diskReadHistory),
		rateRow("Disk Write", app.diskWriteRate, app.diskWriteHistory),
		rateRow("Net RX (Tot)", app.netRxRate, app.netRxHistory),
		rateRow("Net TX (Tot)", app.netTxRate, app.netTxHistory),
	)

	// Per-device rates below the aggregate: eMMC vs SD vs NVMe is the split
	// that actually matters on an SBC, and the total hides which one is busy.
	var devices []string
	for name := range app.diskRates {
		devices = append(devices, name)
	}
	sort.Strings(devices)
	for _, name := range devices {
		rt := app.diskRates[name]
		rows = append(rows, labelValue{
			label:      name,
			value:      fmt.Sprintf("r %s  w %s", humanBytesF64(rt[0])+"/s", humanBytesF64(rt[1])+"/s"),
			valueStyle: styleInfo,
		})
	}

	if used, total, ok := getDiskTotal(); ok {
		percent := pct(used, total)
		rows = append(rows, labelValue{label: "Disk Space", value: fmt.Sprintf("%s / %s (%d%%)", humanBytes(used), humanBytes(total), percent), valueStyle: severityStyle(float64(percent), 80, 95)})
	}

	var names []string
	for name := range app.adapterRates {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if ip, ok := getInterfaceIPv4(name); ok {
			rows = append(rows, labelValue{label: name + " IP", value: ip, valueStyle: styleWhite})
		}
		rt := app.adapterRates[name]
		rows = append(rows, plainRow(name+" RX", humanBytesF64(rt[0])+"/s"))
		rows = append(rows, plainRow(name+" TX", humanBytesF64(rt[1])+"/s"))
	}
	return rows
}

func renderIOPanel(s tcell.Screen, area Rect, app *AppState) {
	renderLabelGrid(s, area, "I/O", styleIO, buildIORows(app))
}

func buildTemperatureRows(app *AppState) []labelValue {
	var rows []labelValue
	for _, t := range getThermalCached(app.thermalZonePaths) {
		rows = append(rows, labelValue{label: t.Name, value: fmt.Sprintf("%d°C", t.Temp), valueStyle: tempStyle(t.Temp)})
	}
	if gpuTemp, ok := getGPUTemperature(); ok {
		rows = append(rows, labelValue{label: "GPU", value: fmt.Sprintf("%d°C", gpuTemp), valueStyle: tempStyle(gpuTemp)})
	}
	return rows
}

func renderTemperaturePanel(s tcell.Screen, area Rect, app *AppState) {
	renderLabelGrid(s, area, "Temperatures", styleIO, buildTemperatureRows(app))
}

// buildPowerRows surfaces thermal throttling and active cooling. On these
// boards "why did it get slow" is nearly always a cpufreq cap the kernel
// applied silently, and nothing in the UI used to show it.
func buildPowerRows() []labelValue {
	var rows []labelValue

	for _, t := range getThrottleStates() {
		value := fmt.Sprintf("%d/%d MHz  %s", t.CurMHz, t.CurMaxMHz, t.GovernorName)
		style := styleWhite
		if t.Throttled {
			value = fmt.Sprintf("%d MHz  CAPPED %d%% (max %d)", t.CurMHz, t.ThrottlePct, t.HWMaxMHz)
			style = tcell.StyleDefault.Foreground(colorCrit).Bold(true)
		}
		rows = append(rows, labelValue{label: "policy" + t.Policy, value: value, valueStyle: style})
	}

	for _, c := range getCoolingDevices() {
		style := styleGray
		if c.Active() {
			style = tcell.StyleDefault.Foreground(colorWarn).Bold(true)
		}
		rows = append(rows, labelValue{
			label:      c.Type,
			value:      fmt.Sprintf("state %d/%d", c.Cur, c.Max),
			valueStyle: style,
		})
	}

	// Fan RPM and rail power live in hwmon; they belong with throttling rather
	// than buried at the bottom of the temperature list.
	for _, hw := range getHwmonSensors() {
		rows = append(rows, labelValue{label: hw.Name, value: hw.Value, valueStyle: styleWhite})
	}
	return rows
}

func renderPowerPanel(s tcell.Screen, area Rect) {
	renderLabelGrid(s, area, "Power / Throttling", styleIO, buildPowerRows())
}

func renderProcessPanel(s tcell.Screen, area Rect, mon *SystemMonitor, app *AppState) {
	availableRows := area.H - 3
	if availableRows < 0 {
		availableRows = 0
	}

	// Fetch enough to fill the viewport plus whatever the user has scrolled
	// past — TopProcesses walks /proc/<pid>/task per entry, so an unbounded
	// count would be expensive on these boards.
	processCount := availableRows*3 + app.procScroll
	if processCount < 20 {
		processCount = 20
	}

	// With the accelerator filter on, take the whole list before narrowing —
	// an NPU job is often nowhere near the top by CPU, so cutting to the top N
	// first would hide the exact process the filter exists to find.
	// ponytail: 512 covers every board this runs on; a machine with more
	// processes than that would silently drop the tail. Push the filter into
	// TopProcesses if that ever happens.
	if app.accelOnly {
		processCount = 512
	}
	allProcs := mon.TopProcesses(app.processSortMode, processCount)
	app.lastProcSample = allProcs

	// The fd walk behind getAccelUsers isn't free, so it only runs when
	// something on screen needs it. It caches internally, so asking on every
	// frame is fine.
	accel := map[int32][]string{}
	if app.accelOnly || app.cfg.Badges {
		accel = getAccelUsers()
	}

	filterLower := strings.ToLower(app.filterText)
	var filtered []ProcessInfo
	for _, p := range allProcs {
		if app.accelOnly {
			// Threads inherit their group's accelerator handles: fds are
			// per-process, so match the thread group rather than the tid.
			key := p.Pid
			if p.IsThread {
				key = p.ThreadGroupID
			}
			if len(accel[key]) == 0 {
				continue
			}
		}
		if filterLower != "" &&
			!strings.Contains(strings.ToLower(p.Name), filterLower) &&
			!strings.Contains(strings.ToLower(p.User), filterLower) {
			continue
		}
		filtered = append(filtered, p)
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
	var rowPids []int32 // parallel to rows; 0 for thread rows
	var visiblePids []int32
	selRow := -1
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
			// Track where the selection landed so the viewport can follow it,
			// and capture the name here rather than only when it happens to be
			// on screen — [x] used to kill against a stale name otherwise.
			selRow = len(rows)
			app.selectedName = p.Name
		}
		rowPids = append(rowPids, p.Pid)
		cell := func(text string) Span { return Span{Text: text, Style: rowStyle} }
		cpuCell := cell(fmt.Sprintf("%.1f", p.Cpu))
		memCell := cell(fmt.Sprintf("%.1f", p.Mem))
		if !selected {
			cpuCell.Style = usageStyle(p.Cpu)
			memCell.Style = usageStyle(p.Mem)
		}

		// The accelerator badge is the column that makes this a Rockchip tool
		// rather than another htop: it says which processes are actually on the
		// NPU/VPU/RGA right now.
		badge := cell(strings.Join(accel[p.Pid], ","))
		if !selected && len(accel[p.Pid]) > 0 {
			badge.Style = styleAccel.Bold(true)
		}

		rows = append(rows, []Span{
			cell(fmt.Sprintf("%d", p.Pid)),
			cell(p.User),
			cell(string(p.State)),
			cell(fmt.Sprintf("%3d", p.Nice)),
			cell(fmt.Sprintf("%d", p.CpuCore)),
			cell(fmt.Sprintf("%d", p.NumThreads)),
			cell(runtimeStr(p.Runtime)),
			badge,
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
				tCell(""),
				tCell(t.Name + " [thread]"),
				tCell(fmt.Sprintf("%.1f", t.Cpu)),
				tCell(fmt.Sprintf("%.1f", t.Mem)),
			})
			rowPids = append(rowPids, 0)
		}
	}
	app.visiblePids = visiblePids

	// Scroll the viewport to keep the selected row on screen, then window the
	// rows — drawTable used to just stop at the box edge, leaving a selection
	// below the fold invisible while [x] still targeted it.
	app.procScroll = clampScroll(app.procScroll, selRow, len(rows), availableRows)
	if app.procScroll < len(rows) {
		rows = rows[app.procScroll:]
		rowPids = rowPids[app.procScroll:]
	}
	// First body row's screen y, so a mouse click maps back to a PID.
	app.procRowY = area.Y + 2
	app.procRowPids = rowPids

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
		{Text: "Accel", Style: styleBold},
		{Text: nameText, Style: nameStyle},
		{Text: cpuText, Style: cpuStyle},
		{Text: memText, Style: memStyle},
	}

	// Accel column collapses to zero when badges are off, so the name column
	// gets those cells back rather than the table carrying a blank strip.
	accelW := 0
	if app.cfg.Badges || app.accelOnly {
		accelW = 11
	}
	fixedColsWidth := 9 + 9 + 1 + 3 + 2 + 3 + 9 + accelW + 6 + 6
	const numGaps = 10
	nameWidth := maxInt(area.W-fixedColsWidth-numGaps, 10)
	colWidths := []int{9, 9, 1, 3, 2, 3, 9, accelW, nameWidth, 6, 6}

	drawTable(s, area, "Processes", header, rows, colWidths, styleProcess, styleBold)
}

// clampScroll returns the viewport offset that keeps row selRow (-1 for no
// selection) visible in a viewport of `height` rows over `total` rows, without
// scrolling past the end.
func clampScroll(scroll, selRow, total, height int) int {
	if height < 1 {
		height = 1
	}
	if selRow >= 0 {
		if selRow < scroll {
			scroll = selRow
		}
		if selRow >= scroll+height {
			scroll = selRow - height + 1
		}
	}
	if maxScroll := total - height; scroll > maxScroll {
		scroll = maxScroll
	}
	if scroll < 0 {
		scroll = 0
	}
	return scroll
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
