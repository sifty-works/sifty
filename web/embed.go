package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var distFS embed.FS

func StaticFS() fs.FS {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		return nil
	}

	if _, err := sub.Open("index.html"); err != nil {
		return nil
	}
	return sub
}
