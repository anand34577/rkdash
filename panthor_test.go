package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPanthorFDInfo(t *testing.T) {
	base := "drm-driver:\tpanthor\ndrm-client-id:\t7\n"
	for _, tc := range []struct {
		data  string
		valid bool
		ns    uint64
	}{
		{base + "drm-engine-panthor:\t123456 ns\n", true, 123456},
		{base + "drm-engine-panthor:\t0 ns\n", true, 0},
		{base, false, 0},
		{base + "drm-engine-panthor:\t-1 ns\n", false, 0},
		{base + "drm-engine-panthor:\t42 cycles\n", false, 0},
		{"drm-driver: other\ndrm-client-id: 7\ndrm-engine-panthor: 42 ns\n", false, 0},
	} {
		id, ns, ok := parsePanthorFDInfo(tc.data)
		if ok != tc.valid || ns != tc.ns || (ok && id != ":7") {
			t.Errorf("parse %q: %q %d %v", tc.data, id, ns, ok)
		}
	}
}

func TestPanthorSharedClients(t *testing.T) {
	root := t.TempDir()
	for _, tc := range []struct{ path, data string }{
		{"10/fdinfo/3", "drm-driver: panthor\ndrm-client-id: 7\ndrm-engine-panthor: 100 ns\n"},
		{"11/fdinfo/4", "drm-driver: panthor\ndrm-client-id: 7\ndrm-engine-panthor: 120 ns\n"},
		{"11/fdinfo/5", "drm-driver: panthor\ndrm-client-id: 8\ndrm-engine-panthor: 20 ns\n"},
		{"self/fdinfo/3", "drm-driver: panthor\ndrm-client-id: 9\ndrm-engine-panthor: 999 ns\n"},
	} {
		path := filepath.Join(root, tc.path)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(tc.data), 0644); err != nil {
			t.Fatal(err)
		}
	}
	got := readPanthorClients(root)
	if len(got) != 2 || got[":7"] != 120 || got[":8"] != 20 {
		t.Fatalf("clients: %v", got)
	}
}

func TestPanthorCounterDeltas(t *testing.T) {
	var s panthorSampler
	now := time.Now()
	if _, ok := s.sample(now, map[string]uint64{"a": 100}); ok {
		t.Fatal("first sample must warm up")
	}
	usage, ok := s.sample(now.Add(time.Second), map[string]uint64{"a": 250000100, "new": 9999999999})
	if !ok || usage != 25 {
		t.Fatalf("new clients must not add historical work: %v %v", usage, ok)
	}
	usage, ok = s.sample(now.Add(2*time.Second), map[string]uint64{"a": 250000100})
	if !ok || usage != 0 {
		t.Fatalf("idle/removed clients: %v %v", usage, ok)
	}
	if _, ok = s.sample(now.Add(3*time.Second), map[string]uint64{"a": 1}); ok {
		t.Fatal("reset counter must rebaseline")
	}
	usage, ok = s.sample(now.Add(4*time.Second), map[string]uint64{"a": 2000000001})
	if !ok || usage != 100 {
		t.Fatalf("overlapping jobs must clamp: %v %v", usage, ok)
	}
	if _, ok = s.sample(now.Add(5*time.Second), nil); ok {
		t.Fatal("missing counters must not mean idle")
	}
}

func TestEnablePanthorTimestamps(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiling")
	for _, tc := range []struct {
		initial, want string
		ok            bool
	}{
		{"0\n", "2", true}, {"1\n", "3", true}, {"3", "3", true}, {"bad", "bad", false},
	} {
		if err := os.WriteFile(path, []byte(tc.initial), 0644); err != nil {
			t.Fatal(err)
		}
		if ok := enablePanthorTimestamps(path); ok != tc.ok {
			t.Fatalf("enable %q: %v", tc.initial, ok)
		}
		got, _ := os.ReadFile(path)
		if string(got) != tc.want {
			t.Fatalf("enable %q: got %q, want %q", tc.initial, got, tc.want)
		}
	}
}
