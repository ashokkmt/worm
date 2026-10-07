//go:build !windows

package packs

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

type DirectoryLock struct{ file *os.File }

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
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("parser packs directory is already in use: %w", err)
	}
	return &DirectoryLock{file: f}, nil
}

func (l *DirectoryLock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	_ = syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	return l.file.Close()
}
