//go:build frontend

package main

import (
	"embed"
	"io/fs"
)

// The built interface. Build the frontend first (npm run build in gui/frontend).
//
//go:embed all:frontend/dist
var distFS embed.FS

func assetsFS() fs.FS {
	sub, err := fs.Sub(distFS, "frontend/dist")
	if err != nil {
		panic(err) // the path is a constant that exists in the embed
	}
	return sub
}
