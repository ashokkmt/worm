package web

import (
	"embed"
	"io/fs"
)

//go:embed dist/*
var distFS embed.FS

// Dist returns an fs.FS rooted at the "dist" directory.
func Dist() fs.FS {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		panic(err)
	}
	return sub
}
