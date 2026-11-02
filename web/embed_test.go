//go:build webui

package web

import (
	"io/fs"
	"testing"
)

func TestFilesHaveTheUI(t *testing.T) {
	b, err := fs.ReadFile(Files(), "index.html")
	if err != nil || len(b) == 0 {
		t.Fatalf("index.html = %d bytes, %v", len(b), err)
	}
}
