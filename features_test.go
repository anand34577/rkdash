package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfigRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rkdash.conf")

	c := defaultConfig()
	c.path = path
	c.RefreshCPUMs = 500
	c.RefreshProcMs = 3000
	c.Hidden["vpu"] = true
	c.Hidden["rga"] = true
	c.Sort = "mem"
	c.Truecolor = "off"
	c.AccelOnly = true
	c.Badges = false
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}

	got := LoadConfig(path)
	if got.RefreshCPUMs != 500 || got.RefreshProcMs != 3000 {
		t.Errorf("refresh rates lost: %+v", got)
	}
	if !got.Hidden["vpu"] || !got.Hidden["rga"] || got.Visible("vpu") {
		t.Errorf("hidden panels lost: %v", got.Hidden)
	}
	if got.Visible("cpu") != true {
		t.Error("a panel that was never hidden should stay visible")
	}
	if got.Sort != "mem" || got.SortMode() != SortMemoryDesc {
		t.Errorf("sort lost: %q -> %v", got.Sort, got.SortMode())
	}
	if got.Truecolor != "off" || !got.AccelOnly || got.Badges {
		t.Errorf("flags lost: %+v", got)
	}
}

func TestLoadConfigMissingFileIsDefaults(t *testing.T) {
	c := LoadConfig(filepath.Join(t.TempDir(), "nope.conf"))
	if c.RefreshCPUMs != 1000 || !c.Badges || c.Sort != "cpu" {
		t.Errorf("missing file should give defaults, got %+v", c)
	}
}

func TestConfigIgnoresJunk(t *testing.T) {
	// A hand-edited config shouldn't be able to brick startup.
	path := filepath.Join(t.TempDir(), "junk.conf")
	os.WriteFile(path, []byte("# comment\n\nnot a pair\nrefresh_cpu_ms = banana\nrefresh_io_ms = -5\nsort=pid\n"), 0644)
	c := LoadConfig(path)
	if c.RefreshCPUMs != 1000 {
		t.Errorf("non-numeric value should be ignored, got %d", c.RefreshCPUMs)
	}
	if c.RefreshIOMs != 2000 {
		t.Errorf("negative refresh should be ignored, got %d", c.RefreshIOMs)
	}
	if c.SortMode() != SortPidAsc {
		t.Error("valid line after junk lines should still apply")
	}
}

func TestSortModeNameRoundTrip(t *testing.T) {
	for _, m := range []ProcessSortMode{SortCpuDesc, SortMemoryDesc, SortPidAsc, SortNameAsc} {
		c := defaultConfig()
		c.Sort = sortModeName(m)
		if c.SortMode() != m {
			t.Errorf("%v round-tripped to %v via %q", m, c.SortMode(), c.Sort)
		}
	}
	// Reverse modes collapse to the same name; that's intended (the config
	// stores which column, not which direction).
	if sortModeName(SortCpuAsc) != "cpu" {
		t.Error("ascending CPU should still name the cpu column")
	}
}

func TestToggle(t *testing.T) {
	c := defaultConfig()
	if !c.Visible("gpu") {
		t.Fatal("panels start visible")
	}
	c.Toggle("gpu")
	if c.Visible("gpu") {
		t.Fatal("toggle should hide")
	}
	c.Toggle("gpu")
	if !c.Visible("gpu") {
		t.Fatal("toggle should restore")
	}
}

func TestPanelOrderCoversNumberKeys(t *testing.T) {
	// The help text and the digit handler both index panelOrder; a duplicate
	// or an empty name would silently make one key a no-op.
	seen := map[string]bool{}
	for _, p := range panelOrder {
		if p == "" || seen[p] {
			t.Errorf("bad panel name %q", p)
		}
		seen[p] = true
	}
	if len(panelOrder) < 10 {
		t.Errorf("only %d panels but 10 digit keys are advertised", len(panelOrder))
	}
}

func TestTruecolorEnabled(t *testing.T) {
	t.Setenv("COLORTERM", "")
	t.Setenv("TERM", "xterm")
	if truecolorEnabled("on") != true || truecolorEnabled("off") != false {
		t.Error("explicit on/off must override detection")
	}
	if truecolorEnabled("auto") {
		t.Error("plain xterm is not truecolor")
	}
	t.Setenv("COLORTERM", "truecolor")
	if !truecolorEnabled("auto") {
		t.Error("COLORTERM=truecolor should enable")
	}
	t.Setenv("COLORTERM", "")
	t.Setenv("TERM", "xterm-256color")
	if !truecolorEnabled("auto") {
		t.Error("256color should still get the gradient")
	}
}

func TestGradientColorFallback(t *testing.T) {
	useTruecolor = false
	defer func() { useTruecolor = true }()
	// The fallback must still be three visually distinct bands, otherwise it's
	// no better than the flat-green degradation it exists to avoid.
	low, mid, high := gradientColor(0.1), gradientColor(0.7), gradientColor(0.95)
	if low == mid || mid == high || low == high {
		t.Errorf("fallback bands collapsed: %v %v %v", low, mid, high)
	}
}

func TestAccelBadge(t *testing.T) {
	cases := map[string]string{
		"/dev/rknpu":          "NPU",
		"/dev/mpp_service":    "VPU",
		"/dev/rga":            "RGA",
		"/dev/mali0":          "GPU",
		"/dev/dri/renderD128": "GPU",
		"/dev/video11":        "V4L",
	}
	for target, want := range cases {
		got, ok := accelBadge(target)
		if !ok || got != want {
			t.Errorf("%s -> %q,%v; want %q", target, got, ok, want)
		}
	}
	for _, target := range []string{"/dev/null", "socket:[12345]", "/home/user/model.rknn", "pipe:[99]"} {
		if _, ok := accelBadge(target); ok {
			t.Errorf("%s should not be an accelerator", target)
		}
	}
}

func TestReadProcDetailOnSelf(t *testing.T) {
	// Runs against this test process, so it exercises the real /proc/<pid>/stat
	// field offsets rather than a fixture that could drift from the kernel.
	d, ok := readProcDetail(int32(os.Getpid()), nil)
	if !ok {
		t.Fatal("could not read our own process")
	}
	if d.Name == "" || d.Cmdline == "" {
		t.Errorf("empty identity: %+v", d)
	}
	if d.Threads < 1 {
		t.Errorf("thread count = %d, want >= 1", d.Threads)
	}
	if d.RSSBytes == 0 {
		t.Error("a running process has non-zero RSS")
	}
	if d.PPid == 0 {
		t.Error("a test binary always has a parent")
	}
	if d.FDCount == 0 {
		t.Error("a running process has open fds")
	}
	if _, ok := readProcDetail(-1, nil); ok {
		t.Error("a nonexistent pid should report failure, not a zero struct")
	}
}
