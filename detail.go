package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/gdamore/tcell/v2"
)

// ProcDetail is the extra per-process data the detail pane shows — everything
// here costs a syscall or two, so it's only read for the one selected PID
// rather than for every row in the table.
type ProcDetail struct {
	Pid      int32
	Name     string
	Cmdline  string
	Exe      string
	Cwd      string
	State    string
	PPid     int32
	Threads  int32
	FDCount  int
	RSSBytes uint64
	VSZBytes uint64

	ReadBytes  uint64
	WriteBytes uint64

	// Accel lists the accelerator badges (NPU/VPU/RGA/GPU) this process holds
	// open — the reason to run rkdash instead of htop.
	Accel []string
}

// procStateNames expands the single-letter state from /proc/<pid>/stat, which
// is otherwise unreadable to anyone who hasn't memorised proc(5).
var procStateNames = map[string]string{
	"R": "running", "S": "sleeping", "D": "disk wait (uninterruptible)",
	"Z": "zombie", "T": "stopped", "t": "tracing stop", "X": "dead", "I": "idle",
}

func readProcDetail(pid int32, accel map[int32][]string) (ProcDetail, bool) {
	base := "/proc/" + strconv.Itoa(int(pid))
	statData, err := os.ReadFile(base + "/stat")
	if err != nil {
		return ProcDetail{}, false
	}

	d := ProcDetail{Pid: pid, Name: extractComm(string(statData)), Accel: accel[pid]}

	// Fields after the comm's closing paren, per proc(5): [0]=state, [1]=ppid,
	// [17]=num_threads, [20]=vsize, [21]=rss (in pages).
	if idx := strings.LastIndex(string(statData), ")"); idx >= 0 {
		f := strings.Fields(string(statData)[idx+1:])
		if len(f) > 0 {
			d.State = f[0]
			if long, ok := procStateNames[f[0]]; ok {
				d.State = f[0] + " (" + long + ")"
			}
		}
		if len(f) > 1 {
			if v, err := strconv.ParseInt(f[1], 10, 32); err == nil {
				d.PPid = int32(v)
			}
		}
		if len(f) > 17 {
			if v, err := strconv.ParseInt(f[17], 10, 32); err == nil {
				d.Threads = int32(v)
			}
		}
		if len(f) > 21 {
			if v, err := strconv.ParseUint(f[20], 10, 64); err == nil {
				d.VSZBytes = v
			}
			if v, err := strconv.ParseUint(f[21], 10, 64); err == nil {
				d.RSSBytes = v * uint64(os.Getpagesize())
			}
		}
	}

	// cmdline is NUL-separated; the trailing NUL would otherwise render as a
	// stray glyph at the end of the line.
	if raw, err := os.ReadFile(base + "/cmdline"); err == nil {
		d.Cmdline = strings.TrimSpace(strings.ReplaceAll(strings.TrimRight(string(raw), "\x00"), "\x00", " "))
	}
	if d.Cmdline == "" {
		d.Cmdline = "[" + d.Name + "]" // kernel thread: no argv
	}
	d.Exe, _ = os.Readlink(base + "/exe")
	d.Cwd, _ = os.Readlink(base + "/cwd")
	if fds, err := os.ReadDir(base + "/fd"); err == nil {
		d.FDCount = len(fds)
	}

	if io, err := os.ReadFile(base + "/io"); err == nil {
		for _, line := range strings.Split(string(io), "\n") {
			f := strings.Fields(line)
			if len(f) != 2 {
				continue
			}
			v, err := strconv.ParseUint(f[1], 10, 64)
			if err != nil {
				continue
			}
			switch f[0] {
			case "read_bytes:":
				d.ReadBytes = v
			case "write_bytes:":
				d.WriteBytes = v
			}
		}
	}

	return d, true
}

// renderDetailOverlay draws the per-process pane opened with Enter. Centred
// modal rather than a split panel: it's transient, and stealing a column from
// the layout for it would shrink every other panel permanently.
func renderDetailOverlay(s tcell.Screen, full Rect, app *AppState) {
	d, ok := readProcDetail(app.selectedPid, getAccelUsers())
	if !ok {
		return
	}

	overlayBg := tcell.NewHexColor(0x11151c)
	text := tcell.StyleDefault.Foreground(colorText).Background(overlayBg)
	muted := tcell.StyleDefault.Foreground(colorMuted).Background(overlayBg)
	accent := tcell.StyleDefault.Foreground(colorAccel).Background(overlayBg).Bold(true)

	boxW := minInt(96, full.W-4)
	boxH := minInt(20, full.H-4)
	area := Rect{
		X: full.X + (full.W-boxW)/2,
		Y: full.Y + (full.H-boxH)/2,
		W: boxW,
		H: boxH,
	}
	for y := area.Y; y < area.Y+area.H; y++ {
		for x := area.X; x < area.X+area.W; x++ {
			s.SetContent(x, y, ' ', nil, tcell.StyleDefault.Background(overlayBg))
		}
	}

	field := func(label, value string) []Span {
		return []Span{
			{Text: fmt.Sprintf("%-10s ", label), Style: muted},
			{Text: value, Style: text},
		}
	}

	lines := [][]Span{
		field("PID", fmt.Sprintf("%d   PPID %d   Threads %d   FDs %d", d.Pid, d.PPid, d.Threads, d.FDCount)),
		field("State", d.State),
		field("Command", d.Cmdline),
	}
	if d.Exe != "" {
		lines = append(lines, field("Exe", d.Exe))
	}
	if d.Cwd != "" {
		lines = append(lines, field("Cwd", d.Cwd))
	}
	lines = append(lines,
		field("Memory", fmt.Sprintf("RSS %s   VSZ %s", humanBytes(d.RSSBytes), humanBytes(d.VSZBytes))),
		field("Disk I/O", fmt.Sprintf("read %s   written %s", humanBytes(d.ReadBytes), humanBytes(d.WriteBytes))),
	)

	if len(d.Accel) > 0 {
		lines = append(lines, []Span{
			{Text: fmt.Sprintf("%-10s ", "Accel"), Style: muted},
			{Text: strings.Join(d.Accel, " "), Style: accent},
		})
	} else {
		lines = append(lines, field("Accel", "none held"))
	}

	// Per-process history is only collected while its row is selected, so an
	// empty trace here means "just opened", not "idle".
	graphW := boxW - 14
	lines = append(lines,
		[]Span{},
		append([]Span{{Text: fmt.Sprintf("%-10s ", "CPU %"), Style: muted}},
			graphSpans(app.detailCPUHistory, 100, graphW, tcell.StyleDefault.Background(overlayBg))...),
		append([]Span{{Text: fmt.Sprintf("%-10s ", "Mem %"), Style: muted}},
			graphSpans(app.detailMemHistory, histMax(app.detailMemHistory), graphW, tcell.StyleDefault.Background(overlayBg))...),
		[]Span{},
		[]Span{{Text: "Enter/Esc to close   x to kill this process", Style: muted}},
	)

	drawParagraph(s, area, "Process "+d.Name, lines, tcell.StyleDefault.Foreground(colorProcess).Background(overlayBg))
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
