# rkdash

A terminal-based system monitor for Rockchip single-board computers (RK3566, RK3576, RK3588 and variants), built with Go and [tcell](https://github.com/gdamore/tcell). It's an `htop`/`btop`-style dashboard that additionally surfaces Rockchip-specific hardware: GPU, NPU, and RGA utilization, per-cluster CPU frequency ranges, thermal zones, and driver/SDK version info.

This is a Go port of [rktop](https://github.com/ajokela/rktop) by Alex Jokela, originally written in Rust with [Ratatui](https://ratatui.rs/). Panel layout, keybindings, and data sources mirror the original; this port reimplements them in Go with tcell instead of Ratatui/crossterm.

![rkdash screenshot](screenshots/info.png)

## Features

- **CPU** — per-core usage, aggregate user/system/iowait/idle breakdown, frequency ranges per cluster, governor, context switch/interrupt/softirq rates, running/blocked process counts, load average
- **Memory** — total/used/available RAM, swap, and ZRAM compression stats
- **GPU / NPU / RGA** — Mali GPU utilization and frequency, RKNPU per-core load and frequency, RGA per-scheduler load, with sparkline history for GPU/NPU
- **Disk & Network I/O** — aggregate read/write throughput across physical disks, per-adapter RX/TX rates with IP addresses
- **Temperatures** — all thermal zones plus GPU temperature via hwmon
- **Board info** — board model, detected Rockchip SoC, CPU architecture/core mix, NPU/RGA driver versions, librknnrt/librkllmrt SDK versions
- **Process table** — sortable by CPU, memory, PID, or name; live filter by name/user; SIGTERM a selected process with a confirmation prompt
- **Live controls** — pause/resume data refresh, mouse support, resize-aware layout

## Requirements

- A Rockchip-based Linux board (RK3566/RK3576/RK3588 families); some panels degrade gracefully (e.g. "Not Detected") on other hardware
- Root privileges at runtime (reads root-only debugfs paths such as `/sys/kernel/debug/mali0` and `/sys/kernel/debug/rknpu`)
- Go 1.26+ to build

## Building

### From the target board (or any Linux/arm64 host)

```sh
go build -o rkdash .
```

### Cross-compiling from Windows

Two equivalent helper scripts are provided; both cross-compile for `linux/arm64` and place the binary in `dist/`:

```bat
build-linux-arm64.bat
```

```powershell
./build-linux-arm64.ps1
```

Copy the resulting binary to the board and run it:

```sh
chmod +x rkdash-linux-arm64
sudo ./rkdash-linux-arm64
```

## Usage

```sh
sudo rkdash
```

| Key         | Action                                             |
|-------------|-----------------------------------------------------|
| `↑` / `↓`   | Move process selection                              |
| `/`         | Filter processes by name/user (Enter confirms, Esc clears) |
| `c` / `C`   | Sort by CPU (toggles ascending/descending)          |
| `m` / `M`   | Sort by memory (toggles ascending/descending)       |
| `p` / `P`   | Sort by PID (toggles ascending/descending)          |
| `n` / `N`   | Sort by name (toggles ascending/descending)         |
| `x` / `X`   | Kill selected process (press `y`/`Y` to confirm SIGTERM) |
| `Space`     | Pause/resume data refresh                           |
| `?`         | Toggle help overlay                                 |
| `q` / `Q`   | Quit                                                |

## Project layout

| File               | Responsibility                                                        |
|--------------------|------------------------------------------------------------------------|
| `main.go`          | Entry point, event loop, refresh scheduling, key handling             |
| `app.go`           | `AppState` — UI/runtime state and derived stats updated each tick     |
| `sysmon.go`        | Core system stats: CPU jiffies, memory, ZRAM, disk I/O, network, load |
| `hardware.go`      | Rockchip/board-specific hardware: GPU/NPU/RGA, thermal, board identity|
| `procs.go`         | Process/thread enumeration, CPU%/mem% computation, sorting            |
| `proccontrol.go`   | Process termination (SIGTERM)                                         |
| `filecache.go`     | Cached file descriptors for frequently-polled sysfs/debugfs paths     |
| `canvas.go`        | Small terminal-layout/rendering primitives (rects, spans, splitting)  |
| `ui.go`            | Panel rendering: header, CPU/memory/accelerator/I/O panels, process table, help overlay |

## Refresh intervals

Configured in `main.go` via `RefreshConfig`:

- CPU/memory: 1s
- Network/disk I/O: 2s
- Process table: 2s

The terminal itself is polled every 250ms for input and resize responsiveness independent of these data refresh intervals.

## Developer tooling

`tools/stripcomments` is a small Go-AST-based utility used to strip all comments from the top-level `*.go` files in this repository without altering behavior — it parses each file with `go/parser`, drops all comment nodes, and re-emits the source with `go/format`, which guarantees the result still parses and compiles.

```sh
# Preview which files would change
go run ./tools/stripcomments -dir .

# Apply the changes
go run ./tools/stripcomments -dir . -write
```

It only touches `*.go` files directly inside the given `-dir` (non-recursive) and skips `_test.go` files.

## License

BSD 3-Clause, inherited from the upstream [rktop](https://github.com/ajokela/rktop) project (Copyright (c) 2025, Alex Jokela and contributors). See [LICENSE](LICENSE).
