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
	// Git's record of it, as the connector stores it once the worktree is
	// made: what is left of an orphan.
	w.AdminDir = filepath.Join(repo, ".git", "worktrees", "7-abcdef")
	require.NoError(t, os.MkdirAll(w.AdminDir, 0o700))
	require.NoError(t, ledger.WorktreeAdminDir(context.Background(), id, w.AdminDir))
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

// A listing says what each kept worktree takes up, and says nothing about the
// size of one that is gone.
func TestConnectWorktreesSayWhatTheyTakeUp(t *testing.T) {
	app, out, w := worktreesCmdEnv(t)
	require.NoError(t, os.MkdirAll(w.Path, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(w.Path, "notes.txt"), bytes.Repeat([]byte("x"), 1234), 0o600))
	require.NoError(t, runWorktreesCmd(t, app, "list"))
	assert.Contains(t, out.String(), `"size_bytes": 1234`)

	out.Reset()
	require.NoError(t, os.RemoveAll(w.Path))
	require.NoError(t, runWorktreesCmd(t, app, "prune"))
	assert.Contains(t, out.String(), `"reason": "orphaned"`)
	assert.NotContains(t, out.String(), `"size_bytes"`, "a worktree that is gone has no size")
}

// An orphaned worktree is listed with git's record of it, which is what is
// left to deal with.
func TestConnectWorktreesShowTheRecordOfAnOrphan(t *testing.T) {
	app, out, w := worktreesCmdEnv(t)
	require.NoError(t, runWorktreesCmd(t, app, "list"))
	assert.Contains(t, out.String(), `"record": "`+w.AdminDir+`"`)
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

// A worktree whose directory is gone is reported as orphaned, with git's
// record of it, and a plain prune deletes none of what it left; the operator
// naming its path is what clears it.
func TestConnectWorktreesPruneLeavesAnOrphanAloneUntilItIsNamed(t *testing.T) {
	app, out, w := worktreesCmdEnv(t)
	require.NoError(t, runWorktreesCmd(t, app, "prune"))
	assert.Contains(t, out.String(), `"reason": "orphaned"`)
	assert.Contains(t, out.String(), `"record"`, "what is left of it")
	assert.NotContains(t, out.String(), `"force_refused"`, "nothing was forced")
	out.Reset()
	require.NoError(t, runWorktreesCmd(t, app, "list"))
	assert.Contains(t, out.String(), w.Path, "still listed for the operator")

	out.Reset()
	require.NoError(t, runWorktreesCmd(t, app, "prune", "--force", w.Path))
	assert.Contains(t, out.String(), `"action": "forced"`)
	out.Reset()
	require.NoError(t, runWorktreesCmd(t, app, "list"))
	assert.NotContains(t, out.String(), w.Path)
}
