//go:build windows

package packs

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

type DirectoryLock struct {
	file       *os.File
	overlapped windows.Overlapped
}

func AcquireDirectoryLock(dir string) (*DirectoryLock, error) {
	if err := RejectSymlinks(dir, filepath.Join(dir, ".marketplace", "owner.lock")); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(dir, ".marketplace"), 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, ".marketplace", "owner.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	l := &DirectoryLock{file: f}
	flags := uint32(windows.LOCKFILE_EXCLUSIVE_LOCK | windows.LOCKFILE_FAIL_IMMEDIATELY)
	if err = windows.LockFileEx(windows.Handle(f.Fd()), flags, 0, 1, 0, &l.overlapped); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("parser packs directory is already in use: %w", err)
	}
	return l, nil
}

func (l *DirectoryLock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	_ = windows.UnlockFileEx(windows.Handle(l.file.Fd()), 0, 1, 0, &l.overlapped)
	return l.file.Close()
}
