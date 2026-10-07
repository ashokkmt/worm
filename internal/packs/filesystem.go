package packs

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// RejectSymlinks keeps managed pack state anchored beneath its configured root.
func RejectSymlinks(root, target string) error {
	root, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	target, err = filepath.Abs(target)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return errors.New("managed path escapes its root")
	}
	current := root
	if info, e := os.Lstat(current); e == nil && info.Mode()&os.ModeSymlink != 0 {
		return errors.New("managed root cannot be a symlink")
	}
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		if part == "." || part == "" {
			continue
		}
		current = filepath.Join(current, filepath.FromSlash(part))
		info, e := os.Lstat(current)
		if os.IsNotExist(e) {
			break
		}
		if e != nil {
			return e
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("managed path cannot contain symlinks")
		}
	}
	return nil
}

// AtomicWriteFile writes a file under root without following managed symlinks.
func AtomicWriteFile(root, target string, data []byte, mode os.FileMode) error {
	if err := RejectSymlinks(root, target); err != nil {
		return err
	}
	return atomicWrite(target, data, mode)
}
