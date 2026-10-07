//go:build !windows

package packs

import (
	"os"
	"path/filepath"
)

func atomicReplace(src, dst string) error {
	if err := os.Rename(src, dst); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(dst))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
