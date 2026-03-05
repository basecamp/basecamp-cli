package commands_test

import (
	"flag"
	"os"
	"testing"

	"github.com/basecamp/cli/surface"
)

var updateSurface = flag.Bool("update-surface", false, "Update .surface baseline file")

func TestSurfaceSnapshot(t *testing.T) {
	root := buildRootWithAllCommands()

	current := surface.SnapshotString(root)

	baselinePath := "../../.surface"

	if *updateSurface {
		if err := os.WriteFile(baselinePath, []byte(current), 0o644); err != nil {
			t.Fatalf("writing .surface: %v", err)
		}
		t.Log("Updated .surface baseline")
		return
	}

	baseline, err := os.ReadFile(baselinePath)
	if err != nil {
		t.Fatalf("reading .surface baseline (run with -update-surface to generate): %v", err)
	}

	if string(baseline) != current {
		t.Errorf("CLI surface has changed. Run: go test ./internal/commands/ -run TestSurfaceSnapshot -update-surface")
		t.Logf("Expected length: %d, got: %d", len(baseline), len(current))
	}
}
