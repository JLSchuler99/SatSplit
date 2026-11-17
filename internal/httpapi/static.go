package httpapi

import (
	"embed"
	"io/fs"
)

//go:embed static/*
var staticFiles embed.FS

var staticFS = mustSub(staticFiles, "static")

func mustSub(fsys embed.FS, dir string) fs.FS {
	sub, err := fs.Sub(fsys, dir)
	if err != nil {
		panic(err)
	}
	return sub
}

func readStaticFile(name string) ([]byte, error) {
	return fs.ReadFile(staticFS, name)
}
