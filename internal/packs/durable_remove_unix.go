//go:build !windows

package packs

import (
	"os"
	"path/filepath"
)

func durableRemove(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}
