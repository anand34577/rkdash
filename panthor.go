package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Panthor exposes accumulated client job time in DRM fdinfo, rather than the
// vendor Mali debugfs interface. Concurrent jobs can overlap, so the sum is an
// activity estimate, capped at 100%, not an exact hardware busy-time counter.
type panthorSampler struct {
	mu        sync.Mutex
	previous  map[string]uint64
	sampledAt time.Time
	usage     float32
	valid     bool
}

var panthorGPU panthorSampler

// enablePanthorTimestamps preserves other profiling bits. The setting remains
// enabled for other monitors until reboot; no boot configuration is changed.
func enablePanthorTimestamps(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	mask, err := strconv.ParseUint(strings.TrimSpace(string(data)), 0, 32)
	if err != nil {
		return false
	}
	const timestamps = 2
	if mask&timestamps != 0 {
		return true
	}
	return os.WriteFile(path, []byte(strconv.FormatUint(mask|timestamps, 10)), 0644) == nil
}

// parsePanthorFDInfo requires an actual timestamp field: missing counters must
// never be interpreted as an idle GPU. Client IDs identify shared DRM handles.
func parsePanthorFDInfo(data string) (string, uint64, bool) {
	fields := make(map[string]string)
	for _, line := range strings.Split(data, "\n") {
		key, value, ok := strings.Cut(line, ":")
		if ok {
			fields[key] = strings.TrimSpace(value)
		}
	}
	if fields["drm-driver"] != "panthor" || fields["drm-client-id"] == "" {
		return "", 0, false
	}
	engine := strings.Fields(fields["drm-engine-panthor"])
	if len(engine) != 2 || engine[1] != "ns" {
		return "", 0, false
	}
	ns, err := strconv.ParseUint(engine[0], 10, 64)
	if err != nil {
		return "", 0, false
	}
	return fields["drm-pdev"] + ":" + fields["drm-client-id"], ns, true
}

func readPanthorClients(procRoot string) map[string]uint64 {
	clients := make(map[string]uint64)
	processes, _ := os.ReadDir(procRoot)
	for _, process := range processes {
		if _, err := strconv.ParseUint(process.Name(), 10, 32); err != nil {
			continue
		}
		dir := filepath.Join(procRoot, process.Name(), "fdinfo")
		fds, err := os.ReadDir(dir)
		if err != nil {
			continue
		} // Processes may exit or be inaccessible.
		for _, fd := range fds {
			// Do not cache proc fds: their process/descriptor can disappear or be reused.
			data, err := os.ReadFile(filepath.Join(dir, fd.Name()))
			if err != nil {
				continue
			}
			id, ns, ok := parsePanthorFDInfo(string(data))
			if ok {
				// A duplicated fd may be read later and have a newer counter.
				if old, exists := clients[id]; !exists || ns > old {
					clients[id] = ns
				}
			}
		}
	}
	return clients
}

func (s *panthorSampler) sample(now time.Time, clients map[string]uint64) (float32, bool) {
	elapsed := now.Sub(s.sampledAt)
	var busy float64
	comparable := false
	for id, ns := range clients {
		if old, ok := s.previous[id]; ok && ns >= old {
			busy += float64(ns - old)
			comparable = true
		}
	}
	s.previous = clients
	s.sampledAt = now
	s.valid = comparable && elapsed > 0
	s.usage = 0
	if s.valid {
		s.usage = float32(busy / float64(elapsed.Nanoseconds()) * 100)
		if s.usage > 100 {
			s.usage = 100
		}
	}
	return s.usage, s.valid
}

func getPanthorGPUUsage() (float32, bool) {
	s := &panthorGPU
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if !s.sampledAt.IsZero() && now.Sub(s.sampledAt) < 500*time.Millisecond {
		return s.usage, s.valid
	}
	paths, _ := filepath.Glob("/sys/bus/platform/drivers/panthor/*/profiling")
	if len(paths) == 0 {
		return 0, false
	}
	for _, path := range paths {
		enablePanthorTimestamps(path)
	}
	return s.sample(time.Now(), readPanthorClients("/proc"))
}

// primePanthorSnapshot gives one-shot JSON mode an interval to measure too.
func primePanthorSnapshot() {
	paths, _ := filepath.Glob("/sys/bus/platform/drivers/panthor/*/profiling")
	if len(paths) == 0 {
		return
	}
	if _, ok := getPanthorGPUUsage(); !ok {
		time.Sleep(550 * time.Millisecond)
	}
}
