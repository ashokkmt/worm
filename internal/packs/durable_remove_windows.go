//go:build windows

package packs

import (
	"golang.org/x/sys/windows"
	"os"
	"path/filepath"
)

func durableRemove(path string) error {
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	stage := path + ".worm-delete"
	if _, err := os.Lstat(stage); err == nil {
		f, e := os.CreateTemp(filepath.Dir(path), ".worm-delete-*")
		if e != nil {
			return e
		}
		stage = f.Name()
		if e = f.Close(); e != nil {
			return e
		}
		if e = os.Remove(stage); e != nil {
			return e
		}
	}
	from, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(stage)
	if err != nil {
		return err
	}
	if err = windows.MoveFileEx(from, to, windows.MOVEFILE_WRITE_THROUGH); err != nil {
		return err
	}
	return os.Remove(stage)
}
