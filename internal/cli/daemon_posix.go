//go:build !windows

package cli

import (
	"os"
	"os/exec"
	"syscall"
	"time"
)

func IsPIDAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = proc.Signal(syscall.Signal(0))
	return err == nil
}

func setDaemonSysProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
	}
}

func stopProcess(proc *os.Process) error {
	_ = proc.Signal(syscall.SIGTERM)

	// Wait up to 5 seconds
	for i := 0; i < 50; i++ {
		time.Sleep(100 * time.Millisecond)
		if !IsPIDAlive(proc.Pid) {
			return nil
		}
	}

	// Force kill if still alive
	return proc.Signal(syscall.SIGKILL)
}
