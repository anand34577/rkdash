package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// panelOrder is the fixed list of toggleable panels, in the order the number
// keys 1..9,0 address them. Keep it stable — the digit a user learns for a
// panel shouldn't move when a new panel is added, so append, never insert.
var panelOrder = []string{"cpu", "memory", "io", "temps", "power", "sys", "gpu", "npu", "rga", "vpu", "stats"}

// Config is rkdash's persisted settings. It's a flat key=value file rather
// than TOML/YAML so there's no dependency and no schema to keep in sync.
type Config struct {
	RefreshCPUMs  int
	RefreshIOMs   int
	RefreshProcMs int

	// Hidden holds panel names the user has toggled off.
	Hidden map[string]bool

	Sort string // initial process sort: cpu, mem, pid, name

	// Truecolor is "auto" (trust COLORTERM), "on", or "off". On a terminal
	// without 24-bit colour the gradient collapses to a muddy single hue, so
	// "off" swaps in the 8-colour palette instead.
	Truecolor string

	// AccelOnly starts the process list filtered to accelerator users.
	AccelOnly bool

	// Badges shows an NPU/VPU/RGA/GPU column in the process table. It costs a
	// walk of every process's fds, so it can be turned off on a busy board.
	Badges bool

	path string // where this was loaded from, for Save
}

func defaultConfig() *Config {
	return &Config{
		RefreshCPUMs:  1000,
		RefreshIOMs:   2000,
		RefreshProcMs: 2000,
		Hidden:        map[string]bool{},
		Sort:          "cpu",
		Truecolor:     "auto",
		Badges:        true,
	}
}

// configPath is $RKDASH_CONFIG, else $XDG_CONFIG_HOME/rkdash/rkdash.conf, else
// $HOME/.config/rkdash/rkdash.conf. Note rkdash runs under sudo, so $HOME is
// usually root's — set RKDASH_CONFIG (or --config) to keep it elsewhere.
func configPath() string {
	if p := os.Getenv("RKDASH_CONFIG"); p != "" {
		return p
	}
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		base = filepath.Join(os.Getenv("HOME"), ".config")
	}
	return filepath.Join(base, "rkdash", "rkdash.conf")
}

// LoadConfig reads path (empty = the default location). A missing file is not
// an error — it just means defaults.
func LoadConfig(path string) *Config {
	c := defaultConfig()
	if path == "" {
		path = configPath()
	}
	c.path = path

	data, err := os.ReadFile(path)
	if err != nil {
		return c
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		eq := strings.Index(line, "=")
		if eq < 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])
		atoi := func(dst *int) {
			if n, err := strconv.Atoi(val); err == nil && n > 0 {
				*dst = n
			}
		}
		switch key {
		case "refresh_cpu_ms":
			atoi(&c.RefreshCPUMs)
		case "refresh_io_ms":
			atoi(&c.RefreshIOMs)
		case "refresh_proc_ms":
			atoi(&c.RefreshProcMs)
		case "hidden":
			for _, name := range strings.Split(val, ",") {
				if name = strings.TrimSpace(name); name != "" {
					c.Hidden[name] = true
				}
			}
		case "sort":
			c.Sort = val
		case "truecolor":
			c.Truecolor = val
		case "accel_only":
			c.AccelOnly = truthy(val)
		case "badges":
			c.Badges = truthy(val)
		}
	}
	return c
}

// Save writes the config back, creating the directory if needed. Called from
// the TUI so panel toggles survive a restart — otherwise the number keys are
// a per-session novelty rather than a setting.
func (c *Config) Save() error {
	if c.path == "" {
		c.path = configPath()
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0755); err != nil {
		return err
	}

	var hidden []string
	for name := range c.Hidden {
		hidden = append(hidden, name)
	}
	sort.Strings(hidden)

	body := fmt.Sprintf(`# rkdash configuration
# Panel names: %s

refresh_cpu_ms  = %d
refresh_io_ms   = %d
refresh_proc_ms = %d

# Panels toggled off (keys 1..9,0 in the order above).
hidden = %s

# Initial process sort: cpu, mem, pid, name
sort = %s

# 24-bit colour: auto (trust $COLORTERM), on, off
truecolor = %s

# Start filtered to processes holding an NPU/VPU/RGA/GPU device
accel_only = %t

# Show the NPU/VPU/RGA/GPU badge column (costs an fd walk per refresh)
badges = %t
`,
		strings.Join(panelOrder, ", "),
		c.RefreshCPUMs, c.RefreshIOMs, c.RefreshProcMs,
		strings.Join(hidden, ","),
		c.Sort, c.Truecolor, c.AccelOnly, c.Badges)

	return os.WriteFile(c.path, []byte(body), 0644)
}

func truthy(v string) bool { return v == "true" || v == "yes" || v == "1" }

func (c *Config) Visible(panel string) bool { return !c.Hidden[panel] }

func (c *Config) Toggle(panel string) {
	if c.Hidden[panel] {
		delete(c.Hidden, panel)
	} else {
		c.Hidden[panel] = true
	}
}

func (c *Config) SortMode() ProcessSortMode {
	switch c.Sort {
	case "mem", "memory":
		return SortMemoryDesc
	case "pid":
		return SortPidAsc
	case "name":
		return SortNameAsc
	default:
		return SortCpuDesc
	}
}

// sortModeName is SortMode's inverse, so Save round-trips the user's current
// sort rather than resetting it to the configured default.
func sortModeName(m ProcessSortMode) string {
	switch m {
	case SortMemoryAsc, SortMemoryDesc:
		return "mem"
	case SortPidAsc, SortPidDesc:
		return "pid"
	case SortNameAsc, SortNameDesc:
		return "name"
	default:
		return "cpu"
	}
}

// truecolorEnabled decides whether to emit 24-bit gradients. tcell will happily
// accept hex colours on an 8-colour TERM and quantise them all to the same
// hue — which is why a gradient can render as one flat block of green.
func truecolorEnabled(mode string) bool {
	switch mode {
	case "on":
		return true
	case "off":
		return false
	}
	switch os.Getenv("COLORTERM") {
	case "truecolor", "24bit":
		return true
	}
	return strings.Contains(os.Getenv("TERM"), "256color")
}
