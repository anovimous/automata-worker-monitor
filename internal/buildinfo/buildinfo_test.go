package buildinfo

import "testing"

func TestDefaultsWhenNotSetAtBuildTime(t *testing.T) {
	if Version != "dev" || Commit != "unknown" {
		t.Errorf("Version, Commit = %q, %q, want dev, unknown", Version, Commit)
	}
}
