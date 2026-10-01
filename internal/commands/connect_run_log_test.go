package commands

import (
	"bytes"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
)

func startLine(only []int64) string {
	var out bytes.Buffer
	slog.New(slog.NewTextHandler(&out, nil)).Info("connector: running",
		connectRunningAttrs("agent", "181900405", 42, false, 3, only, "/state")...)
	return out.String()
}

// The start line says how many projects the agent serves, not how many a
// --project flag named: with no flag, the old count read as "serving nothing".
func TestTheStartLineSaysHowManyProjectsTheAgentServes(t *testing.T) {
	line := startLine(nil)
	assert.Contains(t, line, "served_projects=3")
	assert.NotContains(t, line, "only_projects")
	assert.NotContains(t, line, "projects=0")
}

// A run narrowed with --project says which projects it is limited to.
func TestANarrowedRunSaysWhichProjectsItListensTo(t *testing.T) {
	line := startLine([]int64{1042979247})
	assert.Contains(t, line, "served_projects=3")
	assert.Contains(t, line, "only_projects=[1042979247]")
}
