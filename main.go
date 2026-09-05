package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
)

// appVersion is overridable at build time via -ldflags "-X main.appVersion=x.y.z".
var appVersion = "1.1.0-dev"

// displayVersion normalizes appVersion to a single "v" prefix regardless of
// whether the build already supplied one (e.g. from `git describe`, which
// returns "v1.0.0-..." since the repo's tags are already "v"-prefixed).
func displayVersion() string {
	return "v" + strings.TrimPrefix(appVersion, "v")
}

type RefreshConfig struct {
	CPUMemory   time.Duration
	NetworkDisk time.Duration
	Processes   time.Duration
}

func refreshConfigFrom(c *Config) RefreshConfig {
	return RefreshConfig{
		CPUMemory:   time.Duration(c.RefreshCPUMs) * time.Millisecond,
		NetworkDisk: time.Duration(c.RefreshIOMs) * time.Millisecond,
		Processes:   time.Duration(c.RefreshProcMs) * time.Millisecond,
	}
}

func main() {
	var (
		mcpMode    = flag.Bool("mcp", false, "run as an MCP server over stdio")
		jsonMode   = flag.Bool("json", false, "print one JSON snapshot and exit")
		jsonProcs  = flag.Int("json-procs", 0, "include this many top processes in --json output")
		csvPath    = flag.String("csv", "", "append a sample row to this CSV file on every refresh")
		configFlag = flag.String("config", "", "config file path (default $XDG_CONFIG_HOME/rkdash/rkdash.conf)")
	)
	flag.Parse()

	// Both non-TUI modes are checked before the root/tcell setup so they work
	// from a script without a terminal.
	if *mcpMode || mcpFlag(os.Args[1:]) {
		if err := runMCPServer(); err != nil {
			fmt.Fprintln(os.Stderr, "MCP server error:", err)
			os.Exit(1)
		}
		return
	}
	if *jsonMode {
		if err := runJSONSnapshot(*jsonProcs); err != nil {
			fmt.Fprintln(os.Stderr, "snapshot error:", err)
			os.Exit(1)
		}
		return
	}

	if os.Geteuid() != 0 {
		fmt.Fprintln(os.Stderr, "Root permissions required. Use: sudo rkdash")
		os.Exit(1)
	}

	cfg := LoadConfig(*configFlag)
	useTruecolor = truecolorEnabled(cfg.Truecolor)

	screen, err := tcell.NewScreen()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Failed to initialize terminal:", err)
		os.Exit(1)
	}
	if err := screen.Init(); err != nil {
		fmt.Fprintln(os.Stderr, "Failed to initialize terminal:", err)
		os.Exit(1)
	}
	defer screen.Fini()
	screen.EnableMouse()
	screen.SetStyle(styleDefault)

	mon := NewSystemMonitor()
	app := NewAppState(cfg)

	if *csvPath != "" {
		rec, err := newCSVRecorder(*csvPath, app)
		if err != nil {
			screen.Fini()
			fmt.Fprintln(os.Stderr, "Cannot open CSV file:", err)
			os.Exit(1)
		}
		defer rec.Close()
		app.recorder = rec
		app.setStatus("Recording to " + *csvPath)
	}

	if err := runApp(screen, mon, app); err != nil {
		screen.Fini()
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}

func runApp(screen tcell.Screen, mon *SystemMonitor, app *AppState) error {
	config := refreshConfigFrom(app.cfg)

	lastUpdate := time.Now()
	lastNetworkUpdate := time.Now()
	lastRender := time.Now()

	mon.RefreshProcessBasic(getMemStats().TotalKB)
	lastProcessUpdate := time.Now()

	events := make(chan tcell.Event, 16)
	go func() {
		for {
			ev := screen.PollEvent()
			if ev == nil {
				close(events)
				return
			}
			events <- ev
		}
	}()

	draw := func() {
		screen.Clear()
		drawUI(screen, mon, app)
		screen.Show()
	}
	draw()

	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case ev, ok := <-events:
			if !ok {
				return nil
			}
			switch e := ev.(type) {
			case *tcell.EventResize:
				screen.Sync()
				draw()
			case *tcell.EventKey:
				if quit := handleKey(app, e); quit {
					return nil
				}
				draw()
			case *tcell.EventMouse:
				if handleMouse(app, e) {
					draw()
				}
			}

		case <-ticker.C:
			shouldRender := false

			if !app.paused {
				if time.Since(lastUpdate) >= config.CPUMemory {
					mon.RefreshCPU()
					app.updateCPUStats()
					app.updateStats()

					app.updateHistory(mon)
					app.recorder.Sample(mon, app)

					lastUpdate = time.Now()
					shouldRender = true
				}

				if time.Since(lastNetworkUpdate) >= config.NetworkDisk {
					app.updateIORates(getNetworkCounters())
					lastNetworkUpdate = time.Now()
				}

				if time.Since(lastProcessUpdate) >= config.Processes {
					mon.RefreshProcessBasic(getMemStats().TotalKB)
					lastProcessUpdate = time.Now()
				}
			}

			if shouldRender || time.Since(lastRender) >= time.Second {
				draw()
				lastRender = time.Now()
			}
		}
	}
}

func handleKey(app *AppState, ev *tcell.EventKey) bool {

	if app.showHelp {
		app.showHelp = false
		return false
	}

	// The detail pane stays open while you arrow between processes; only Enter,
	// Esc and q close it, and x still kills the process it's describing.
	if app.showDetail {
		switch ev.Key() {
		case tcell.KeyEnter, tcell.KeyEscape:
			app.showDetail = false
			return false
		case tcell.KeyUp:
			moveSelection(app, -1)
			return false
		case tcell.KeyDown:
			moveSelection(app, 1)
			return false
		}
		switch ev.Rune() {
		case 'q', 'Q':
			app.showDetail = false
		case 'x', 'X':
			if app.selectedPid != 0 {
				app.confirmingKill = true
				app.killTargetName = app.selectedName
			}
		}
		return false
	}

	if app.confirmingKill {
		app.confirmingKill = false
		if ev.Rune() == 'y' || ev.Rune() == 'Y' {
			pid, name := app.selectedPid, app.killTargetName
			if err := killProcess(pid); err != nil {
				app.setStatus(fmt.Sprintf("Failed to kill %d (%s): %v", pid, name, err))
			} else {
				app.setStatus(fmt.Sprintf("Sent SIGTERM to %d (%s)", pid, name))
			}
		}
		return false
	}

	if app.filterMode {
		switch ev.Key() {
		case tcell.KeyRune:
			app.filterText += string(ev.Rune())
		case tcell.KeyBackspace, tcell.KeyBackspace2:
			if len(app.filterText) > 0 {
				runes := []rune(app.filterText)
				app.filterText = string(runes[:len(runes)-1])
			}
		case tcell.KeyEscape:
			app.filterMode = false
			app.filterText = ""
		case tcell.KeyEnter:
			app.filterMode = false
		}
		return false
	}

	switch ev.Key() {
	case tcell.KeyEscape:
		app.filterText = ""
		return false
	case tcell.KeyUp:
		moveSelection(app, -1)
		return false
	case tcell.KeyDown:
		moveSelection(app, 1)
		return false
	case tcell.KeyEnter:
		if app.selectedPid != 0 {
			app.showDetail = true
		}
		return false
	case tcell.KeyPgUp:
		moveSelection(app, -10)
		return false
	case tcell.KeyPgDn:
		moveSelection(app, 10)
		return false
	}

	// Digits 1..9,0 toggle panels in panelOrder; anything past the tenth panel
	// is reachable only by editing the config, which is fine — the keyboard
	// runs out before the panel list does.
	if r := ev.Rune(); r >= '0' && r <= '9' {
		idx := int(r - '1')
		if r == '0' {
			idx = 9
		}
		if idx >= 0 && idx < len(panelOrder) {
			app.cfg.Toggle(panelOrder[idx])
			app.setStatus("Toggled panel: " + panelOrder[idx])
		}
		return false
	}

	switch ev.Rune() {
	case 'q', 'Q':
		return true
	case 'a', 'A':
		app.accelOnly = !app.accelOnly
		app.procScroll = 0
		if app.accelOnly {
			app.setStatus("Showing only NPU/VPU/RGA/GPU users")
		} else {
			app.setStatus("Showing all processes")
		}
	case 'b', 'B':
		app.cfg.Badges = !app.cfg.Badges
	case 'S':
		app.cfg.Sort = sortModeName(app.processSortMode)
		app.cfg.AccelOnly = app.accelOnly
		if err := app.cfg.Save(); err != nil {
			app.setStatus("Save failed: " + err.Error())
		} else {
			app.setStatus("Saved config to " + app.cfg.path)
		}
	case '/':
		app.filterMode = true
		app.filterText = ""
	case '?':
		app.showHelp = true
	case ' ':
		app.paused = !app.paused
	case 'x', 'X':
		if app.selectedPid != 0 {
			app.confirmingKill = true
			app.killTargetName = app.selectedName
		}
	case 'c', 'C':
		if app.processSortMode == SortCpuDesc {
			app.processSortMode = SortCpuAsc
		} else {
			app.processSortMode = SortCpuDesc
		}
	case 'm', 'M':
		if app.processSortMode == SortMemoryDesc {
			app.processSortMode = SortMemoryAsc
		} else {
			app.processSortMode = SortMemoryDesc
		}
	case 'p', 'P':
		if app.processSortMode == SortPidAsc {
			app.processSortMode = SortPidDesc
		} else {
			app.processSortMode = SortPidAsc
		}
	case 'n', 'N':
		if app.processSortMode == SortNameAsc {
			app.processSortMode = SortNameDesc
		} else {
			app.processSortMode = SortNameAsc
		}
	}
	return false
}

// handleMouse gives the already-enabled mouse something to do: wheel scrolls
// the process viewport, a click on a process row selects it. Reports whether
// anything changed and a redraw is warranted.
func handleMouse(app *AppState, ev *tcell.EventMouse) bool {
	switch ev.Buttons() {
	// Wheel moves the selection rather than the viewport directly — the
	// viewport already follows the selection, so scrolling them independently
	// would just fight the clamp in renderProcessPanel.
	case tcell.WheelUp:
		moveSelection(app, -3)
		return true
	case tcell.WheelDown:
		moveSelection(app, 3)
		return true
	case tcell.Button1:
		_, y := ev.Position()
		row := y - app.procRowY
		if row < 0 || row >= len(app.procRowPids) {
			return false
		}
		if pid := app.procRowPids[row]; pid != 0 {
			app.selectedPid = pid
			return true
		}
	}
	return false
}

func moveSelection(app *AppState, delta int) {
	if len(app.visiblePids) == 0 {
		return
	}
	idx := 0
	for i, pid := range app.visiblePids {
		if pid == app.selectedPid {
			idx = i
			break
		}
	}
	idx += delta
	if idx < 0 {
		idx = 0
	}
	if idx >= len(app.visiblePids) {
		idx = len(app.visiblePids) - 1
	}
	app.selectedPid = app.visiblePids[idx]
}
