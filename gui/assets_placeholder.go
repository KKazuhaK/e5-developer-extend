//go:build !frontend

package main

import (
	"embed"
	"io/fs"
)

// Without the frontend tag the binary carries a one-page placeholder, so the
// Go code builds, vets and tests before the frontend has been built.
//
//go:embed frontend-placeholder
var placeholderFS embed.FS

func assetsFS() fs.FS {
	sub, err := fs.Sub(placeholderFS, "frontend-placeholder")
	if err != nil {
		panic(err) // the path is a constant that exists in the embed
	}
	return sub
}
