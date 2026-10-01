package commands

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/config"
	"github.com/basecamp/basecamp-cli/internal/connector/admission"
	"github.com/basecamp/basecamp-cli/internal/connector/setup"
)

// policyFile is a connect.json on disk where setup and the connector both
// expect one: under the profile's own directory, owner-only, with the
// directories above it owner-only too. Nothing here fakes the lock or the
// read — the tests below take the real flock on the real file.
func policyFile(t *testing.T, projects map[int64]admission.Project) (string, setup.File) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path, err := setup.Path(config.GlobalConfigDir(), "agent")
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))

	file := setup.New("agent")
	file.AccountID = "2914079"
	file.Agent = setup.Agent{PersonID: 52007412, Kind: setup.KindAgent}
	file.Trust.OperatorID = 26909558
	file.Projects = projects
	writePolicyFile(t, path, file)
	return path, file
}

func writePolicyFile(t *testing.T, path string, file setup.File) {
	t.Helper()
	require.NoError(t, file.Validate())
	data, err := json.Marshal(file)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, data, 0o600))
}

func servingNothing(file setup.File) setup.File {
	out := file
	out.Projects = map[int64]admission.Project{}
	return out
}

// connectServedTTL is the width of the one disagreement the lock does not
// close: admission decides against a cached reading, dispatch against a
// locked one, so a project unserved within the TTL can still be admitted —
// and is then refused at the launch and named by the stranded report.
// Raising this constant widens that, and a bound only a comment knows is a
// description rather than a guarantee.
func TestConnectServedTTLStaysSmall(t *testing.T) {
	assert.Positive(t, connectServedTTL, "a reading reused for no time at all reads connect.json once per event")
	assert.LessOrEqual(t, connectServedTTL, 2*time.Second,
		"connectServedTTL is how stale admission's view of the served set may be; raising it is a decision, not an edit")
}

// And the constant is the bound because the cache is real: a reading inside
// the TTL is reused, so the number is what an admission decision can be
// behind the file. Asserting the constant without this would pin a number
// nothing obeys.
func TestAReadingIsReusedWithinItsTTL(t *testing.T) {
	path, file := policyFile(t, map[int64]admission.Project{48929974: {}})
	clock := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	served := newConnectServed(path, file, slog.New(slog.DiscardHandler))
	served.now = func() time.Time { return clock }

	first, err := served.Current()
	require.NoError(t, err)
	require.Contains(t, first, int64(48929974))

	writePolicyFile(t, path, servingNothing(file))
	clock = clock.Add(connectServedTTL - time.Nanosecond)
	within, err := served.Current()
	require.NoError(t, err)
	assert.Contains(t, within, int64(48929974), "inside the TTL the reading is reused, which is what the TTL bounds")

	clock = clock.Add(time.Nanosecond)
	after, err := served.Current()
	require.NoError(t, err)
	assert.Empty(t, after, "and at the TTL it is read again")
}
