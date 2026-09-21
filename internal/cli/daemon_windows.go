//go:build windows

package cli

import (
	"os"
	"os/exec"
	"syscall"
)

func IsPIDAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	const WAIT_TIMEOUT = 258

	h, err := syscall.OpenProcess(syscall.PROCESS_QUERY_INFORMATION|syscall.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(h)

	s, err := syscall.WaitForSingleObject(h, 0)
	if err != nil {
		return false
	}
	return s == WAIT_TIMEOUT
}

func setDaemonSysProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP,
	}
}

func stopProcess(proc *os.Process) error {
	return proc.Kill()
}
