package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/config"
	"github.com/basecamp/basecamp-cli/internal/connector"
	"github.com/basecamp/basecamp-cli/internal/connector/admission"
	"github.com/basecamp/basecamp-cli/internal/connector/setup"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// worktreesCmdEnv is a set-up connector profile with a ledger holding one
// retained worktree whose directory the operator already removed.
func worktreesCmdEnv(t *testing.T) (*appctx.App, *bytes.Buffer, connector.Worktree) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("USERPROFILE", root)

	file := setup.New("agent")
	file.AccountID = "2914079"
	file.Agent = setup.Agent{PersonID: 52007412, Kind: setup.KindAgent}
	file.Trust.OperatorID = 26909558
	repo := filepath.Join(root, "repo")
	require.NoError(t, os.MkdirAll(repo, 0o700))
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"commit", "-q", "--allow-empty", "-m", "init"}} {
		cmd := exec.CommandContext(context.Background(), "git", append([]string{"-c", "user.name=T", "-c", "user.email=t@example.invalid"}, args...)...)
		cmd.Dir = repo
		cmd.Env = []string{"HOME=" + root, "PATH=" + os.Getenv("PATH")}
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
	}
	file.Projects[48699913] = admission.Route{Path: repo}
	path, err := setup.Path(config.GlobalConfigDir(), "agent")
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	data, err := json.Marshal(file)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, data, 0o600))

	stateDir, err := connectStateDir(file, false)
	require.NoError(t, err)
	ledger, err := connector.OpenLedger(filepath.Join(stateDir, connector.LedgerFile))
	require.NoError(t, err)
	defer func() { _ = ledger.Close() }()
	w := connector.Worktree{
		Path: filepath.Join(stateDir, "worktrees", "app-00000000", "7-abcdef"), Route: repo, Repository: repo,
		Branch: connector.BranchPrefix + "7-abcdef", BaseCommit: "0123456789abcdef0123456789abcdef01234567", OriginatingEventID: 7,
	}
	w.WorkDir = w.Path
	id, err := ledger.BeginWorktree(context.Background(), w)
	require.NoError(t, err)
	require.NoError(t, ledger.RetainWorktree(context.Background(), id, connector.RetainedDirty, connector.WorktreeCreating))

	cfg := config.Default()
	cfg.ActiveProfile = "agent"
	var out bytes.Buffer
	app := &appctx.App{Config: cfg, Output: output.New(output.Options{Format: output.FormatJSON, Writer: &out})}
	return app, &out, w
}

func runWorktreesCmd(t *testing.T, app *appctx.App, args ...string) error {
	t.Helper()
	cmd := NewConnectCmd()
	cmd.SetArgs(append([]string{"worktrees"}, args...))
	cmd.SetContext(appctx.WithApp(context.Background(), app))
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	return cmd.Execute()
}

func TestConnectWorktreesListShowsTheKeptOnes(t *testing.T) {
	app, out, w := worktreesCmdEnv(t)
	require.NoError(t, runWorktreesCmd(t, app, "list"))
	assert.Contains(t, out.String(), w.Path)
	assert.Contains(t, out.String(), `"reason": "dirty"`)
}

func TestConnectWorktreesPruneRefusesWhatItCannotName(t *testing.T) {
	app, _, _ := worktreesCmdEnv(t)
	err := runWorktreesCmd(t, app, "prune", "--force", "relative/path")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "absolute path")

	err = runWorktreesCmd(t, app, "prune", "--force", "/not/a/kept/worktree")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Nothing was pruned")
}

func TestConnectWorktreesPruneRecordsOnesTheOperatorRemoved(t *testing.T) {
	app, out, w := worktreesCmdEnv(t)
	require.NoError(t, runWorktreesCmd(t, app, "prune"))
	assert.Contains(t, out.String(), `"action": "missing"`)
	out.Reset()
	require.NoError(t, runWorktreesCmd(t, app, "list"))
	assert.NotContains(t, out.String(), w.Path)
}
