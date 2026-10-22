//go:build windows

package output

import (
	"golang.org/x/sys/windows"
)

func replaceFile(oldPath, newPath string) error {
	oldName, err := windows.UTF16PtrFromString(oldPath)
	if err != nil {
		return err
	}
	newName, err := windows.UTF16PtrFromString(newPath)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(oldName, newName, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}

func syncDirectory(path string) error {
	// shortcut: Windows has no documented directory flush equivalent; replaceFile uses WRITE_THROUGH for rename metadata.
	return nil
}
