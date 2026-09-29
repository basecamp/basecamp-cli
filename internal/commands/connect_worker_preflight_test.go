//go:build unix

package commands

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/connector"
	"github.com/basecamp/basecamp-cli/internal/connector/driver"
	"github.com/basecamp/basecamp-cli/internal/connector/setup"
)

// Doctor starts the worker connect.json names as the connector would: the
// claude on this PATH, with a session's environment. A launcher whose Claude
// Code is gone is a failed row that says so, not a pass for finding a file
// called claude.
func TestDoctorStartsTheWorkerTheConnectorWouldStart(t *testing.T) {
	bin := t.TempDir()
	launcher := filepath.Join(bin, "claude")
	require.NoError(t, os.WriteFile(launcher, []byte("#!/bin/sh\nexec \"$HOME/.local/share/mise/installs/claude/latest/claude\" \"$@\"\n"), 0o700))
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	t.Setenv("HOME", t.TempDir())

	checks := workerChecks(context.Background(), setup.New("agent"))
	require.Len(t, checks, 1, "nothing more is asked of a worker that did not start")
	assert.Equal(t, "Claude Code starts", checks[0].Name)
	assert.Equal(t, setup.StatusFail, checks[0].Status)
	assert.Contains(t, checks[0].Message, "The claude on your PATH is a launcher that couldn't find Claude Code (")
	// The shell's own words vary (bash: "No such file or directory", dash:
	// "not found"); the missing path is what every shell names.
	assert.Contains(t, checks[0].Message, "/.local/share/mise/installs/claude/latest/claude")
	assert.Equal(t, "Reinstall Claude Code, or fix the launcher at "+launcher+".", checks[0].Hint)
}

// Each preflight check is a row of its own, and a warning stays a warning.
func TestDoctorShowsEachWorkerCheck(t *testing.T) {
	workerAnswers(t, driver.Preflight{Product: "Codex", Version: "0.157.1", Checks: []driver.PreflightCheck{
		{Name: driver.PreflightStarts, Status: driver.PreflightPass, Message: "Codex 0.157.1 starts (/usr/local/bin/codex)"},
		{Name: driver.PreflightFlags, Status: driver.PreflightFail, Message: "Codex 0.157.1 is too old for the connector — update it", Hint: "It doesn't know --ignore-rules. Update Codex."},
		{Name: driver.PreflightLogin, Status: driver.PreflightWarn, Message: "Codex may be logged out on this computer — run `codex login`"},
	}})
	file := setup.New("agent")
	file.Worker = setup.WorkerCodex

	checks := workerChecks(context.Background(), file)
	require.Len(t, checks, 3)
	assert.Equal(t, []string{"Codex starts", "Codex options", "Codex login"}, []string{checks[0].Name, checks[1].Name, checks[2].Name})
	assert.Equal(t, []string{setup.StatusPass, setup.StatusFail, setup.StatusWarn}, []string{checks[0].Status, checks[1].Status, checks[2].Status})
	assert.Equal(t, "It doesn't know --ignore-rules. Update Codex.", checks[1].Hint)
}

// A connector that stopped taking work says so first, with why and the fix.
func TestConnectStatusLeadsWithNotTakingWork(t *testing.T) {
	why := "Claude Code couldn't start twice in a row — Claude Code is logged out on this computer — run `claude` and log in"
	report := connectStatusReport{Profile: "agent", Status: connector.Status{Connection: &connector.ConnectionStatus{
		State: connector.ConnectionNotTakingWork, PID: 42, ChangedAt: time.Now(), Detail: why,
	}}}
	var out bytes.Buffer
	renderConnectStatus(&out, report)
	lines := strings.Split(out.String(), "\n")
	require.Greater(t, len(lines), 2)
	assert.Equal(t, "  Not taking work: "+why+". Fix it, then run `basecamp connect setup` or restart the connector.", lines[2])
	assert.True(t, strings.HasPrefix(connectStatusSummary(report), "not taking work"))

	report.Status.Connection.State, report.Status.Connection.Detail = connector.ConnectionRunning, ""
	out.Reset()
	renderConnectStatus(&out, report)
	assert.NotContains(t, out.String(), "Not taking work")
}
