package main

import (
	"fmt"
	"os"
	"time"

	"github.com/gdamore/tcell/v2"
)

type RefreshConfig struct {
	CPUMemory   time.Duration
	NetworkDisk time.Duration
	Processes   time.Duration
}

func defaultRefreshConfig() RefreshConfig {
	return RefreshConfig{
		CPUMemory:   1 * time.Second,
		NetworkDisk: 2 * time.Second,
		Processes:   2 * time.Second,
	}
}

func main() {
	if os.Geteuid() != 0 {
		fmt.Fprintln(os.Stderr, "Root permissions required. Use: sudo rkdash")
		os.Exit(1)
	}

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
	app := NewAppState()

	if err := runApp(screen, mon, app); err != nil {
		screen.Fini()
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}

func runApp(screen tcell.Screen, mon *SystemMonitor, app *AppState) error {
	config := defaultRefreshConfig()

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
			}

		case <-ticker.C:
			shouldRender := false

			if !app.paused {
				if time.Since(lastUpdate) >= config.CPUMemory {
					mon.RefreshCPU()
					app.updateCPUStats()
					app.updateStats()

					app.updateHistory()

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
	}

	switch ev.Rune() {
	case 'q', 'Q':
		return true
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
