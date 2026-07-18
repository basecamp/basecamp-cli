package commands_test

import (
	"bytes"
	"errors"
	"flag"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/basecamp/cli/surface"
)

var updateSurface = flag.Bool("update-surface", false, "Update .surface baseline file")

// shouldWriteBaseline reports whether -update-surface should rewrite .surface:
// only in update mode, only when no unacknowledged removals remain, and only
// when the baseline actually differs from the freshly generated surface. The
// last condition matters for removal-only changes — removals acknowledged in
// .surface-breaking leave zero additions, so gating on additions alone would
// skip the write and leave stale removed lines in .surface — while still
// leaving an unchanged surface untouched.
func shouldWriteBaseline(update bool, unacknowledgedRemovals int, baseline, current []byte) bool {
	return update && unacknowledgedRemovals == 0 && !bytes.Equal(baseline, current)
}

func TestSurfaceSnapshot(t *testing.T) {
	root := buildRootWithAllCommands()

	// Trailing newline so -update-surface writes a POSIX-clean file; the
	// comparisons below TrimSpace, so it does not affect drift detection.
	current := surface.SnapshotString(root) + "\n"

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

	// Load acknowledged breaking changes from .surface-breaking.
	// Only entries that are actually absent from the current surface count
	// as acknowledged removals — entries still present in the current surface
	// are ignored so they remain protected against accidental future removal.
	breakingPath := "../../.surface-breaking"
	acknowledged := make(map[string]bool)
	data, readErr := os.ReadFile(breakingPath)
	if readErr != nil {
		if !errors.Is(readErr, os.ErrNotExist) {
			t.Fatalf("reading .surface-breaking: %v", readErr)
		}
	} else {
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			if line != "" && !currentSet[line] {
				acknowledged[line] = true
			}
		}
	}

	// Removals: in baseline but not in current (breaking change)
	var removals []string
	for _, line := range baselineLines {
		if !currentSet[line] && !acknowledged[line] {
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

	if shouldWriteBaseline(*updateSurface, len(removals), baseline, []byte(current)) {
		if err := os.WriteFile(baselinePath, []byte(current), 0o644); err != nil {
			t.Fatalf("writing .surface: %v", err)
		}
	}
}

func TestShouldWriteBaseline(t *testing.T) {
	base := []byte("A\nB\nC\n")

	// Removal-only: current dropped B, acknowledged (0 unacknowledged removals),
	// no additions — must still write so the removed line leaves .surface.
	assert.True(t, shouldWriteBaseline(true, 0, base, []byte("A\nC\n")))

	// Addition: current gained D — writes.
	assert.True(t, shouldWriteBaseline(true, 0, base, []byte("A\nB\nC\nD\n")))

	// No change — must not write (avoids needless churn).
	assert.False(t, shouldWriteBaseline(true, 0, base, base))

	// Unacknowledged removal present — must not write (drift is a failure).
	assert.False(t, shouldWriteBaseline(true, 1, base, []byte("A\nC\n")))

	// Not in update mode — never writes.
	assert.False(t, shouldWriteBaseline(false, 0, base, []byte("A\nC\n")))
}
