//go:build !webui

package web

import "testing"

func TestFilesWithoutTheUI(t *testing.T) {
	if Files() != nil {
		t.Error("Files() != nil without the build tag webui")
	}
}
