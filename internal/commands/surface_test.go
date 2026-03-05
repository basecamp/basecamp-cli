package commands_test

import (
	"errors"
	"flag"
	"os"
	"strings"
	"testing"

	"github.com/basecamp/cli/surface"
)

var updateSurface = flag.Bool("update-surface", false, "Update .surface baseline file")

func TestSurfaceSnapshot(t *testing.T) {
	root := buildRootWithAllCommands()

	current := surface.SnapshotString(root)

	baselinePath := "../../.surface"

	baseline, err := os.ReadFile(baselinePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) && *updateSurface {
			if err := os.WriteFile(baselinePath, []byte(current), 0o644); err != nil {
				t.Fatalf("writing .surface: %v", err)
			}
			t.Log("Created .surface baseline")
			return
		}
		t.Fatalf("reading .surface baseline (run with -update-surface to generate): %v", err)
	}

	baselineLines := strings.Split(strings.TrimSpace(string(baseline)), "\n")
	currentLines := strings.Split(strings.TrimSpace(current), "\n")

	baselineSet := make(map[string]bool, len(baselineLines))
	for _, line := range baselineLines {
		baselineSet[line] = true
	}
	currentSet := make(map[string]bool, len(currentLines))
	for _, line := range currentLines {
		currentSet[line] = true
	}

	// Removals: in baseline but not in current (breaking change)
	var removals []string
	for _, line := range baselineLines {
		if !currentSet[line] {
			removals = append(removals, line)
		}
	}

	// Additions: in current but not in baseline (new surface)
	var additions []string
	for _, line := range currentLines {
		if !baselineSet[line] {
			additions = append(additions, line)
		}
	}

	if len(removals) > 0 {
		t.Errorf("CLI surface removals detected (compatibility break):\n  - %s",
			strings.Join(removals, "\n  - "))
	}

	if len(additions) > 0 {
		if *updateSurface {
			t.Logf("Accepted %d new surface entries:\n  + %s",
				len(additions), strings.Join(additions, "\n  + "))
		} else {
			t.Errorf("CLI surface additions detected (run with -update-surface to accept):\n  + %s",
				strings.Join(additions, "\n  + "))
		}
	}

	// Write updated baseline only when -update-surface is set and no removals were found
	if *updateSurface && len(removals) == 0 && len(additions) > 0 {
		if err := os.WriteFile(baselinePath, []byte(current), 0o644); err != nil {
			t.Fatalf("writing .surface: %v", err)
		}
	}
}
