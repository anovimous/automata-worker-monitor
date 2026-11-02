//go:build webui

// Package web holds the built UI (web/dist) for the workers control plane. With the build tag webui it is embedded in
// the binary; without it Files is nil and the control plane serves its placeholder page, so that Go builds and tests
// need no Node.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// Files returns the built UI, rooted at its index.html.
func Files() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err) // the embed pattern above guarantees dist
	}
	return sub
}
