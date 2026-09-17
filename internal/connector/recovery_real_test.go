//go:build unix

package connector

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRecoveryAgainstRealAgents runs the kill points the connector itself can
// reach against the real agent binaries, with the real `basecamp mcp` built
// from this tree as the workers' MCP server. The server holds a token that
// reaches no Basecamp, so the workers' basecamp_connect calls are real and
// their Basecamp calls fail; nothing is posted anywhere. A model is called, so
// it is opt-in:
//
//	make build
//	BASECAMP_RECOVERY_REAL_AGENTS=1 BASECAMP_RECOVERY_BASECAMP=$PWD/bin/basecamp \
//	  go test ./internal/connector/ -run TestRecoveryAgainstRealAgents -v
//
// What a model does with its turn is not scripted, so the assertions are the
// guarantees that hold whatever it does.
func TestRecoveryAgainstRealAgents(t *testing.T) {
	if os.Getenv(harnessRealEnv) == "" {
		t.Skip("opt in with " + harnessRealEnv + "=1 and " + harnessRealBasecampEnv + "=<basecamp built from this tree>: it runs the real agents, which call their models")
	}
	basecampBinary := os.Getenv(harnessRealBasecampEnv)
	require.NotEmpty(t, basecampBinary, harnessRealBasecampEnv+" names the basecamp binary built from this tree")
	rows := []struct {
		name string
		kill string
		// lost says the attempt is lost: the kill left it live.
		lost bool
	}{
		{name: "no crash"},
		{name: "attempt launching", kill: "line:dispatch:launching", lost: true},
		{name: "attempt running", kill: "line:dispatch:running", lost: true},
		{name: "after get_dispatch", kill: "get-dispatch", lost: true},
	}
	ran := false
	for _, d := range harnessDrivers {
		if !d.Real {
			continue
		}
		ran = true
		t.Run(d.Name, func(t *testing.T) {
			for _, row := range rows {
				t.Run(row.name, func(t *testing.T) {
					h := newHarness(t, d, harnessScenario{})
					stateDir := h.state
					config := filepath.Join(h.dir, "config", "basecamp")
					require.NoError(t, os.MkdirAll(config, 0o700))
					require.NoError(t, os.WriteFile(filepath.Join(config, "config.json"),
						[]byte(`{"profiles":{"agent":{"base_url":"http://127.0.0.1:9","account_id":"`+harnessAccount+`"}}}`), 0o600))
					// These are appended after os.Environ(), and the last
					// duplicate wins in exec, so a real BASECAMP_TOKEN in the
					// operator's environment is overridden by the fake one
					// rather than reaching the worker's MCP server. That
					// server's profile points at a closed port, so no request
					// it makes can leave the machine either.
					env := []string{
						harnessRealBasecampEnv + "=" + basecampBinary,
						"XDG_CONFIG_HOME=" + filepath.Join(h.dir, "config"),
						"BASECAMP_TOKEN=test-token-not-real",
						"BASECAMP_NO_KEYRING=1",
					}
					h.publish(feedEntry{Event: todoEvent(101, 5001)})
					h.run(harnessRun{StateDir: stateDir, Env: env, Kill: row.kill, Killed: row.kill != ""})

					l := h.ledgerAt(stateDir)
					var pids []int
					for _, a := range harnessAttempts(t, l) {
						var pid int
						require.NoError(t, l.db.QueryRowContext(context.Background(), `SELECT COALESCE(pid, 0) FROM attempts WHERE id = ?`, a.ID).Scan(&pid))
						if pid > 0 {
							pids = append(pids, pid)
						}
					}
					for range 2 {
						h.run(harnessRun{StateDir: stateDir, Env: env})
					}

					attempts := harnessAttempts(t, l)
					require.Len(t, attempts, 1, "no second attempt, whatever the worker did")
					assert.Equal(t, StateCompleted, stateOf(t, l, 101))
					outcome := outcomeOf(t, l, 101)
					assert.True(t, slices.Contains([]string{string(OutcomeUnknown), string(OutcomeSucceeded), string(OutcomeFailed)}, outcome), "outcome %q", outcome)
					if row.lost {
						assert.Equal(t, string(StopLost), attempts[0].StopReason)
					}
					if row.kill == "line:dispatch:launching" || row.kill == "line:dispatch:running" {
						assert.Equal(t, string(OutcomeUnknown), outcome, "never prompted, still unknown: a process may have existed")
					}
					assert.LessOrEqual(t, len(h.notices(101)), 1, "at most one completion notice")
					for _, pid := range pids {
						assert.True(t, processGone(pid), "the worker the crash left is gone, pid %d", pid)
					}
					t.Logf("%s: outcome %s, stop %s, notices %d", row.name, outcome, attempts[0].StopReason, len(h.notices(101)))
				})
			}
		})
	}
	require.True(t, ran, "no real agent registered")
}
