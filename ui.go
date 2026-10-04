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
	colorBorder = tcell.NewHexColor(0x3a4a63) // panel frames: dim, so data carries the colour
	colorKey    = tcell.NewHexColor(0x2a3550) // keycap pill behind footer shortcuts

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

// Terminals smaller than this can't show anything useful; say so instead of
// drawing a broken layout.
const minTermW, minTermH = 40, 10

// wideLayoutW is the width at which the top area splits into two columns;
// below it every panel stacks in one column.
const wideLayoutW = 100

// panel is one top-area box: its exact height, a priority (lower = kept first
// when the terminal is too short) and how to draw it.
type panel struct {
	h, minH, prio int
	draw    func(Rect)
}

// A panel with minH < h can shrink to minH (its content is ordered most- to
// least-important, so the box just clips the tail) before it is dropped.
//
// fitPanels drops the lowest-priority panels until the rest fit in budget rows,
// keeping on-screen order. The top-priority panel is always kept, clamped to the
// budget, so a short terminal degrades to "fewer panels", never "crushed boxes".
func fitPanels(ps []panel, budget int) []panel {
	order := make([]int, len(ps))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return ps[order[a]].prio < ps[order[b]].prio })
	keep := make([]bool, len(ps))
	used := 0
	for n, i := range order {
		switch {
		case used+ps[i].h <= budget:
			keep[i] = true
			used += ps[i].h
		case ps[i].minH > 0 && used+ps[i].minH <= budget:
			keep[i] = true
			ps[i].h = ps[i].minH
			used += ps[i].h
		case n == 0 && budget >= 3:
			keep[i] = true
			ps[i].h = budget
			used = budget
		}
	}
	var out []panel
	for i, p := range ps {
		if keep[i] {
			out = append(out, p)
		}
	}
	return out
}

func panelsHeight(ps []panel) int {
	n := 0
	for _, p := range ps {
		n += p.h
	}
	return n
}

func drawColumn(r Rect, ps []panel) {
	cs := make([]Constraint, len(ps))
	for i, p := range ps {
		cs[i] = Length(p.h)
	}
	for i, rect := range splitVertical(r, cs) {
		ps[i].draw(rect)
	}
}

func drawUI(s tcell.Screen, mon *SystemMonitor, app *AppState) {
	w, h := s.Size()
	if w < minTermW || h < minTermH {
		msg := fmt.Sprintf("rkdash: terminal too small (%dx%d, need %dx%d)", w, h, minTermW, minTermH)
		drawText(s, maxInt((w-len([]rune(msg)))/2, 0), h/2, plain(msg), w)
		return
	}
	const footerH = 1

	renderHeaderBar(s, Rect{0, 0, w, 1}, app)
	renderFooterBar(s, Rect{0, h - footerH, w, footerH}, app)

	mem := getMemStats()
	renderKPIStrip(s, Rect{0, 1, w, 1}, mon, mem, app)

	body := Rect{0, 2, w, h - 2 - footerH}

	wide := w >= wideLayoutW
	colW := w
	if wide {
		colW = w / 2
	}
	inW := colW - 2 // inner width of a top-area panel

	numCores := len(mon.CoreUsages())
	if numCores == 0 {
		numCores = 1
	}
	// One core per line once two columns of bars would be clipped.
	cpuCols := 2
	if inW < 58 {
		cpuCols = 1
	}
	coreRows := (numCores + cpuCols - 1) / cpuCols
	// Total + cores + User/Sys + Run lines, plus the Freq line when present.
	cpuH := coreRows + 3 + 2
	if len(app.cpuFreqRanges) > 0 {
		cpuH++
	}

	// A hidden or absent panel is simply not in the list, so toggling one off
	// gives its rows back instead of leaving a gap.
	mk := func(name string, present bool, prio, h, minH int, draw func(Rect)) []panel {
		if !present || !app.cfg.Visible(name) {
			return nil
		}
		return []panel{{h: h, minH: minH, prio: prio, draw: draw}}
	}
	cat := func(groups ...[]panel) (out []panel) {
		for _, g := range groups {
			out = append(out, g...)
		}
		return
	}

	// prio is [wide, stacked]: the accelerators are the reason to run rkdash, so
	// they outrank host chrome (SYS, Stats) when rows run out.
	pr := func(wideP, stackP int) int {
		if wide {
			return wideP
		}
		return stackP
	}
	cpu := mk("cpu", true, pr(0, 0), cpuH, coreRows+3, func(r Rect) { renderCPUPanel(s, r, mon, app, cpuCols) })
	memory := mk("memory", true, pr(1, 1), 7, 4, func(r Rect) { renderMemoryPanel(s, r, mem, app) })
	io := mk("io", true, pr(2, 6), gridRows(len(buildIORows(app)), inW)+2, 0, func(r Rect) { renderIOPanel(s, r, app) })
	temps := mk("temps", true, pr(3, 7), gridRows(len(buildTemperatureRows(app)), inW)+2, 0, func(r Rect) { renderTemperaturePanel(s, r, app) })
	power := mk("power", true, pr(4, 8), gridRows(len(buildPowerRows()), inW)+2, 0, func(r Rect) { renderPowerPanel(s, r) })
	sys := mk("sys", true, pr(4, 9), sysPanelHeight(inW), 0, func(r Rect) { renderSystemPanel(s, r, app) })
	gpu := mk("gpu", app.hasGPU, pr(1, 3), gpuPanelHeight(app), 0, func(r Rect) { renderGPUPanel(s, r, app) })
	npu := mk("npu", app.hasNPU && len(getNPULoad()) > 0, pr(0, 2), npuPanelHeight(inW), 0, func(r Rect) { renderNPUPanel(s, r, app, inW) })
	rga := mk("rga", app.hasRGA && len(getRGALoad()) > 0, pr(2, 4), gridRows(len(getRGALoad()), inW)+2, 0, func(r Rect) { renderRGAPanel(s, r, app) })
	vpu := mk("vpu", app.hasVPU && len(getVPULoad()) > 0, pr(3, 5), gridRows(len(getVPULoad()), inW)+2, 0, func(r Rect) { renderVPUPanel(s, r, app) })
	stats := mk("stats", true, pr(5, 10), 7, 0, func(r Rect) { renderStatsPanel(s, r, app) })

	// Always leave the Processes table a usable slice of the screen, even on a
	// board whose accelerator panels alone would fill the terminal (RK3588's 13
	// VPU blocks).
	minProcessRows := maxInt(6, minInt(10, body.H/3))
	budget := maxInt(body.H-minProcessRows, 0)

	var top Rect
	if wide {
		left := fitPanels(cat(cpu, memory, io, temps, power), budget)
		right := fitPanels(cat(sys, gpu, npu, rga, vpu, stats), budget)
		topH := maxInt(panelsHeight(left), panelsHeight(right))
		chunks := splitVertical(body, []Constraint{Length(topH), Min(minProcessRows)})
		top = chunks[0]
		cols := splitHorizontal(top, []Constraint{Percent(50), Percent(50)})
		drawColumn(cols[0], left)
		drawColumn(cols[1], right)
		renderProcessPanel(s, chunks[1], mon, app)
	} else {
		all := fitPanels(cat(cpu, memory, npu, gpu, rga, vpu, io, temps, power, sys, stats), budget)
		chunks := splitVertical(body, []Constraint{Length(panelsHeight(all)), Min(minProcessRows)})
		drawColumn(chunks[0], all)
		renderProcessPanel(s, chunks[1], mon, app)
	}

	switch {
	case app.showHelp:
		renderHelpOverlay(s, Rect{0, 0, w, h})
	case app.showDetail && app.selectedPid != 0:
		renderDetailOverlay(s, Rect{0, 0, w, h}, app)
	}
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

	showLoad := area.W >= 64
	if !showLoad {
		loadText = ""
	}
	fixedWidth := len([]rune(loadText))
	for _, g := range gauges {
		fixedWidth += len(g.label) + 1 + 7
	}
	barWidth := 10
	if len(gauges) > 0 {
		if avail := area.W - fixedWidth; avail > 0 {
			barWidth = avail / len(gauges)
		}
		if barWidth < 4 {
			barWidth = 4
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
	if showLoad {
		spans = append(spans,
			Span{Text: "LOAD ", Style: bg.Foreground(colorMuted)},
			Span{Text: fmt.Sprintf("%.2f", one), Style: bg.Foreground(loadFg).Bold(true)},
		)
	}

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
	clock := Span{Text: time.Now().Format("2006-01-02 15:04:05") + " ", Style: bg.Foreground(colorMuted)}
	paused := Span{Text: "PAUSED", Style: bg.Foreground(colorWarn).Bold(true)}
	host := Span{Text: hostname, Style: bg.Foreground(colorMuted)}

	// Drop the least important items until the right side fits beside the
	// left: hostname first, then the date, finally everything but PAUSED.
	leftW := spansWidth(left)
	var candidates [][]Span
	if hostname != "" {
		candidates = append(candidates, []Span{host, div, clock})
	}
	short := Span{Text: time.Now().Format("15:04:05") + " ", Style: clock.Style}
	candidates = append(candidates, []Span{clock}, []Span{short})
	var rightSpans []Span
	for _, c := range candidates {
		if app.paused {
			c = append([]Span{paused, div}, c...)
		}
		if leftW+spansWidth(c) < area.W {
			rightSpans = c
			break
		}
	}
	if rightSpans == nil && app.paused {
		rightSpans = []Span{paused, {Text: " ", Style: bg}}
	}
	if rightSpans != nil {
		drawText(s, area.X+area.W-spansWidth(rightSpans), area.Y, rightSpans, area.W)
	}
}

func spansWidth(sp []Span) int {
	n := 0
	for _, x := range sp {
		n += len([]rune(x.Text))
	}
	return n
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

	// Each hint is a keycap plus a label; prio decides which survive when the
	// terminal is too narrow for all of them (lowest dropped first).
	type hint struct {
		key, label string
		labelStyle tcell.Style
		prio       int
	}
	muted := bg.Foreground(colorMuted)
	hints := []hint{
		{"c m p n", "Sort " + sortName, bg.Foreground(colorWarn).Bold(true), 6},
		{"/", "Filter", muted, 5},
		{"a", "Accel", muted, 3},
		{"↵", "Detail", muted, 4},
		{"x", "Kill", muted, 3},
		{"␣", "Pause", muted, 2},
		{"?", "Help", muted, 8},
		{"q", "Quit", muted, 7},
	}
	if app.filterMode || app.filterText != "" {
		hints[1].label = "Filter " + app.filterText
		hints[1].labelStyle = bg.Foreground(colorGood)
		if app.filterMode {
			hints[1].label += "_"
			hints[1].labelStyle = bg.Foreground(colorWarn)
			hints[1].prio = 9
		}
	}
	if app.accelOnly {
		hints[2].label = "Accel*"
		hints[2].labelStyle = bg.Foreground(colorAccel).Bold(true)
	}

	hintW := func(h hint) int { return len([]rune(h.key)) + 2 + 1 + len([]rune(h.label)) + 2 }
	keep := make([]bool, len(hints))
	for i := range keep {
		keep[i] = true
	}
	total := func() int {
		n := 1
		for i, h := range hints {
			if keep[i] {
				n += hintW(h)
			}
		}
		return n
	}
	for total() > area.W {
		drop := -1
		for i, h := range hints {
			if keep[i] && (drop < 0 || h.prio < hints[drop].prio) {
				drop = i
			}
		}
		if drop < 0 {
			break
		}
		keep[drop] = false
	}

	line := []Span{{Text: " ", Style: bg}}
	for i, h := range hints {
		if !keep[i] {
			continue
		}
		line = append(line,
			Span{Text: " " + h.key + " ", Style: bg.Background(colorKey).Foreground(colorInfo).Bold(true)},
			Span{Text: " " + h.label + "  ", Style: h.labelStyle},
		)
	}
	drawText(s, area.X, area.Y, line, area.W)
}

func renderHelpOverlay(s tcell.Screen, full Rect) {
	overlayBg := tcell.NewHexColor(0x11151c)
	overlayText := tcell.StyleDefault.Foreground(colorText).Background(overlayBg)
	overlayMuted := tcell.StyleDefault.Foreground(colorMuted).Background(overlayBg)

	key := tcell.StyleDefault.Foreground(colorInfo).Bold(true).Background(overlayBg)
	row := func(k, desc string) []Span {
		return []Span{{Text: fmt.Sprintf("  %-10s", k), Style: key}, {Text: desc, Style: overlayText}}
	}
	lines := [][]Span{
		row("c m p n", "sort by CPU / Mem / PID / Name (press again to reverse)"),
		row("/", "filter by name or user; Enter confirms, Esc clears"),
		row("a  b", "accelerator-only filter / accelerator badge column"),
		row("↑ ↓ PgUp", "move selection; the list scrolls to follow"),
		row("click", "select a row; mouse wheel scrolls"),
		row("Enter", "open the selected process's detail pane"),
		row("x", "SIGTERM the selected process (y to confirm)"),
		row("1..9 0", "toggle panels: "+strings.Join(panelOrder[:minInt(10, len(panelOrder))], " ")),
		row("S", "save layout and sort to the config file"),
		row("Space", "freeze all data refreshes"),
		row("q", "quit"),
		nil,
		{{Text: "  Press any key to close", Style: overlayMuted}},
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

func renderCPUPanel(s tcell.Screen, area Rect, mon *SystemMonitor, app *AppState, cols int) {
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
	panelBadge(s, area, fmt.Sprintf("%.0f%%", totalUsage), usageStyle(totalUsage))
	y := inner.Y

	writeLine := func(spans []Span) {
		if y >= inner.Y+inner.H {
			return
		}
		drawText(s, inner.X, y, spans, inner.W)
		y++
	}

	totalLine := []Span{
		{Text: "Total ", Style: styleGray},
		{Text: fmt.Sprintf("%5.1f%% ", totalUsage), Style: usageStyle(totalUsage).Bold(true)},
	}
	totalLine = append(totalLine, graphSpans(app.cpuHistory, 100, inner.W-18, styleDefault)...)
	writeLine(totalLine)

	if len(usages) > 0 {
		half := (len(usages) + cols - 1) / cols
		colW := inner.W / cols
		barWidth := computeBarWidth(colW, 23, 6, 40)

		coreLine := func(i int) []Span {
			usage := usages[i]
			freq := uint32(0)
			if i < len(freqs) {
				freq = freqs[i]
			}
			spans := []Span{{Text: fmt.Sprintf("CPU %-2d ", i), Style: styleGray}}
			spans = append(spans, gradientBar(barWidth, usage/100.0, styleDefault)...)
			return append(spans, Span{Text: fmt.Sprintf(" %3.0f%% %4d MHz", usage, freq)})
		}

		for row := 0; row < half; row++ {
			if y >= inner.Y+inner.H {
				break
			}
			for c := 0; c < cols; c++ {
				if i := c*half + row; i < len(usages) {
					x := inner.X + c*colW
					drawText(s, x, y, coreLine(i), minInt(colW-1, inner.X+inner.W-x))
				}
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
	ramBar := []Span{{Text: "RAM  ", Style: styleGray}}
	ramBar = append(ramBar, segmentedBar(barWidth,
		[]float32{usedFrac, cacheFrac},
		[]tcell.Style{severityStyle(float64(ramPercent), 70, 90), tcell.StyleDefault.Foreground(colorCache)})...)
	combinedUsed := used + cache
	combinedPercent := pct(combinedUsed, total)
	ramBar = append(ramBar, Span{Text: fmt.Sprintf(" %3d%%  %s / %s",
		combinedPercent, humanBytes(combinedUsed), humanBytes(total))})

	memLine := []Span{
		{Text: "Total ", Style: styleGray}, {Text: humanBytes(total), Style: styleWhite},
		{Text: "  Free ", Style: styleGray}, {Text: humanBytes(available), Style: styleWhite},
		{Text: "  Used ", Style: styleGray}, {Text: humanBytes(used), Style: styleWhite},
		{Text: "  Cache ", Style: styleGray}, {Text: humanBytes(cache), Style: tcell.StyleDefault.Foreground(colorCache)},
	}
	if freq, ok := getDMCFrequency(); ok {
		memLine = append(memLine, Span{Text: fmt.Sprintf("  DMC %d MHz", freq), Style: styleWhite})
	}

	lines := [][]Span{
		memLine,
		ramBar,
		append(append([]Span{{Text: "Swap ", Style: styleGray}}, gradientBar(barWidth, float32(swapPercent)/100.0, styleDefault)...),
			Span{Text: fmt.Sprintf(" %3d%%  %s / %s", swapPercent, humanBytes(swapUsed), humanBytes(swapTotal))}),
	}

	zramLine := append([]Span{{Text: "ZRAM ", Style: styleGray}}, gradientBar(barWidth, float32(zramPercent)/100.0, styleDefault)...)
	if hasZram {
		ratio := zram.CompressionRatio()
		ratioStr := "N/A"
		if ratio > 0 {
			ratioStr = fmt.Sprintf("%.1f", ratio)
		}
		zramLine = append(zramLine, Span{Text: fmt.Sprintf(" %3d%%  %s / %s (%sx)",
			zramPercent, humanBytes(zram.Used), humanBytes(zram.Limit), ratioStr)})
	} else {
		zramLine = append(zramLine, Span{Text: " N/A"})
	}
	lines = append(lines, zramLine)
	lines = append(lines, append([]Span{{Text: "Hist ", Style: styleGray}},
		graphSpans(app.memHistory, 100, area.W-8, styleDefault)...))

	drawParagraph(s, area, "Memory", lines, styleInfo)
	panelBadge(s, area, fmt.Sprintf("%d%%", combinedPercent), severityStyle(float64(combinedPercent), 70, 90))
}

// sysItems is the SYS panel's content as label/value pairs, so it can flow into
// one or two columns depending on width.
func sysItems(app *AppState) [][2]string {
	return [][2]string{
		{"Board", app.boardName},
		{"SoC", app.rkModel},
		{"Host", readTrimmed("/proc/sys/kernel/hostname", "Unknown")},
		{"Kernel", readTrimmed("/proc/sys/kernel/osrelease", "Unknown")},
		{"Arch", app.cpuArch},
		{"NPU Driver", app.npuVersion},
		{"RGA Driver", app.rgaVersion},
		{"RKNN", app.rknnVersion},
		{"RKLLM", app.rkllmVersion},
	}
}

const sysItemCount = 9

func sysPanelHeight(innerW int) int {
	cols := 2
	if innerW < 60 {
		cols = 1
	}
	return (sysItemCount+cols-1)/cols + 2
}

func renderSystemPanel(s tcell.Screen, area Rect, app *AppState) {
	inner := drawBox(s, area, "System", styleInfo)
	if inner.H == 0 {
		return
	}
	cols := 2
	if inner.W < 60 {
		cols = 1
	}
	items := sysItems(app)
	rows := (len(items) + cols - 1) / cols
	colW := inner.W / cols
	for i, it := range items {
		c, r := i/rows, i%rows
		if r >= inner.H {
			continue
		}
		x := inner.X + c*colW
		drawText(s, x, inner.Y+r, []Span{
			{Text: fmt.Sprintf("%-11s ", it[0]), Style: styleGray},
			{Text: it[1], Style: styleWhite},
		}, minInt(colW-1, inner.X+inner.W-x))
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
		gpuLine = append([]Span{{Text: "Mali0 ", Style: styleGray}}, gradientBar(barWidth, usage/100.0, styleDefault)...)
		gpuLine = append(gpuLine, Span{Text: fmt.Sprintf(" %5.2f%%%s", usage, freqStr)})
	} else {
		// ponytail: Mali devfreq utilization node isn't wired up on this
		// kernel build (common on RK3566 BSP kernels) — show clock only.
		gpuLine = []Span{
			{Text: "Mali0 ", Style: styleGray},
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

func renderNPUPanel(s tcell.Screen, area Rect, app *AppState, _ int) {
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

	cols := gridCols(len(loads), inner.W)
	half := gridRows(len(loads), inner.W)
	colW := inner.W / cols
	barWidth := computeBarWidth(colW, 12, 8, 30)

	coreLine := func(i int) []Span {
		suffix := ""
		if i == 0 {
			suffix = freqStr
		}
		spans := []Span{{Text: fmt.Sprintf("Core %d ", i), Style: styleGray}}
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
		for c := 0; c < cols; c++ {
			if i := c*half + row; i < len(loads) {
				x := inner.X + c*colW
				drawText(s, x, y, coreLine(i), minInt(colW-1, inner.X+inner.W-x))
			}
		}
		y++
	}
}

// npuPanelHeight is the exact box height renderNPUPanel needs — border plus
// its core rows, nothing more — so the layout never reserves blank space
// for it.
func npuPanelHeight(innerW int) int { return gridRows(len(getNPULoad()), innerW) + 2 }

// gridCols picks how many items renderLoadGrid/renderLabelGrid pack per row
// in a panel innerW cells wide: 3 once there are enough entries that 2 columns
// would still run tall (RK3588 reports 13 VPU blocks), 2 otherwise — but never
// so many that a column drops under ~34 cells and starts clipping its values.
func gridCols(n, innerW int) int {
	cols := 2
	if n > 8 {
		cols = 3
	}
	for cols > 1 && innerW/cols < 34 {
		cols--
	}
	return cols
}

// gridRows is the row count a gridCols-column grid needs for n items — panel
// heights are sized against this so the box always matches what gets drawn.
func gridRows(n, innerW int) int {
	n = maxInt(n, 1)
	cols := gridCols(n, innerW)
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

	cols := gridCols(len(loads), inner.W)
	rows := gridRows(len(loads), inner.W)
	colW := inner.W / cols
	nameWidth := 8
	for _, l := range loads {
		if n := len(l.Name); n > nameWidth {
			nameWidth = n
		}
	}
	barWidth := computeBarWidth(colW, nameWidth+14, 6, 30)

	itemLine := func(l namedLoad, w int) []Span {
		spans := []Span{{Text: fmt.Sprintf("%-*s ", nameWidth, l.Name), Style: styleGray}}
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
		kv("Uptime", plain(uptimeStr)),
		kv("Load avg", []Span{
			{Text: fmt.Sprintf("%.2f", one), Style: severityStyle(loadPct, 70, 100).Bold(true)},
			{Text: fmt.Sprintf("  %.2f  %.2f", five, fifteen)},
		}),
		kv("Governor", plain(app.cpuGovernor)),
		kv("Processes", plain(fmt.Sprintf("%d", totalProcesses))),
		kv("TCP conns", plain(fmt.Sprintf("%d", app.tcpConnections))),
	}

	drawParagraph(s, area, "Stats", lines, styleInfo)
}

// kv is a muted, fixed-width label followed by its value spans, so stacked
// lines align on the value column.
func kv(label string, value []Span) []Span {
	return append([]Span{{Text: fmt.Sprintf("%-10s ", label), Style: styleGray}}, value...)
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

	cols := gridCols(len(rows), inner.W)
	gridH := gridRows(len(rows), inner.W)
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

	// Columns drop out as the terminal narrows (least important first) so the
	// Name column always keeps a readable share. Index 8 (Name) absorbs the
	// leftover width. minW is the terminal width a column needs to appear.
	accelOn := app.cfg.Badges || app.accelOnly
	cols := []struct{ w, minW int }{
		{9, 0}, {9, 72}, {1, 96}, {3, 104}, {2, 104}, {3, 90}, {9, 82}, {11, 60}, {0, 0}, {6, 0}, {6, 0},
	}
	var keep []int
	used := 0
	for i, c := range cols {
		if i == 7 && !accelOn || i != 8 && area.W < c.minW {
			continue
		}
		keep = append(keep, i)
		used += c.w + 1
	}
	nameWidth := maxInt(area.W-2-used, 6)
	widths := make([]int, 0, len(keep))
	pick := func(cells []Span) []Span {
		out := make([]Span, 0, len(keep))
		for _, i := range keep {
			if i < len(cells) {
				out = append(out, cells[i])
			}
		}
		return out
	}
	for _, i := range keep {
		if i == 8 {
			widths = append(widths, nameWidth)
		} else {
			widths = append(widths, cols[i].w)
		}
	}
	for i := range rows {
		rows[i] = pick(rows[i])
	}

	drawTable(s, area, "Processes", pick(header), rows, widths, styleProcess, styleBold)
	panelBadge(s, area, fmt.Sprintf("%d shown", len(visiblePids)), styleProcess)
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
