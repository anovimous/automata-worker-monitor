//go:build !webui

// Package web holds the built UI (web/dist) for the workers control plane. With the build tag webui it is embedded in
// the binary; without it Files is nil and the control plane serves its placeholder page, so that Go builds and tests
// need no Node.
package web

import "io/fs"

// Files returns nil: this binary was built without the UI.
func Files() fs.FS {
	return nil
}
