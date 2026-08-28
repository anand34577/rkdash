package main

import (
	"bytes"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
)

type fileCache struct {
	mu    sync.Mutex
	files map[string]*os.File
}

var globalFileCache = &fileCache{files: make(map[string]*os.File)}

func readCachedFile(path string) (string, error) {
	globalFileCache.mu.Lock()
	defer globalFileCache.mu.Unlock()

	f, ok := globalFileCache.files[path]
	if !ok {
		var err error
		f, err = os.Open(path)
		if err != nil {
			return "", err
		}
		globalFileCache.files[path] = f
	}

	if _, err := f.Seek(0, 0); err != nil {
		return "", err
	}

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, f); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func readCachedU32(path string) (uint32, bool) {
	s, err := readCachedFile(path)
	if err != nil {
		return 0, false
	}
	v, err := strconv.ParseUint(strings.TrimSpace(s), 10, 32)
	if err != nil {
		return 0, false
	}
	return uint32(v), true
}

func readCachedI32(path string) (int32, bool) {
	s, err := readCachedFile(path)
	if err != nil {
		return 0, false
	}
	v, err := strconv.ParseInt(strings.TrimSpace(s), 10, 32)
	if err != nil {
		return 0, false
	}
	return int32(v), true
}
