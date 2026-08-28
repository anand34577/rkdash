package main

import (
	"fmt"
	"os"
	"syscall"
)

func killProcess(pid int32) error {
	if pid <= 1 {
		return fmt.Errorf("refusing to signal pid %d", pid)
	}
	if int(pid) == os.Getpid() {
		return fmt.Errorf("refusing to kill rkdash itself")
	}
	if err := syscall.Kill(int(pid), syscall.SIGTERM); err != nil {
		return err
	}
	return nil
}
