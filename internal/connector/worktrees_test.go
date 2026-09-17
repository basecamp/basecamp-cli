package connector

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/connector/admission"
	"github.com/basecamp/basecamp-cli/internal/connector/driver"
)

// worktreeHarness is a repository with a bare remote, a ledger, and
// Worktrees placing worktrees under a private root.
type worktreeHarness struct {
	t      *testing.T
	home   string
	repo   string
	remote string
	root   string
	ledger *Ledger
	wt     *Worktrees
}

func newWorktreeHarness(t *testing.T) *worktreeHarness {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := t.TempDir()
	h := &worktreeHarness{
		t:      t,
		home:   filepath.Join(dir, "home"),
		repo:   filepath.Join(dir, "repo"),
		remote: filepath.Join(dir, "remote.git"),
		root:   filepath.Join(dir, "state", "worktrees"),
		ledger: newTestLedger(t),
	}
	require.NoError(t, os.MkdirAll(h.home, 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(h.repo, "app"), 0o700))
	require.NoError(t, os.MkdirAll(filepath.Dir(h.root), 0o700))
	h.git(dir, "init", "-q", "--bare", "-b", "main", h.remote)
	h.git(h.repo, "init", "-q", "-b", "main")
	h.write(h.repo, "app/README", "hello\n")
	h.git(h.repo, "add", ".")
	h.git(h.repo, "commit", "-q", "-m", "init")
	h.git(h.repo, "remote", "add", "origin", h.remote)
	h.git(h.repo, "push", "-q", "origin", "main")
	h.wt = h.worktrees("")
	return h
}

func (h *worktreeHarness) worktrees(gitBinary string) *Worktrees {
	h.t.Helper()
	w, err := NewWorktrees(WorktreesOptions{
		Ledger: h.ledger,
		Root:   h.root,
		Git:    gitBinary,
		Lookup: h.lookup,
	})
	require.NoError(h.t, err)
	return w
}

func (h *worktreeHarness) lookup(k string) (string, bool) {
	switch k {
	case "HOME":
		return h.home, true
	case "PATH":
		return os.Getenv("PATH"), true
	}
	return "", false
}

func (h *worktreeHarness) git(dir string, args ...string) string {
	h.t.Helper()
	cmd := exec.CommandContext(context.Background(), "git", append([]string{"-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	cmd.Env = []string{"HOME=" + h.home, "PATH=" + os.Getenv("PATH"), "GIT_CONFIG_NOSYSTEM=1"}
	out, err := cmd.CombinedOutput()
	require.NoError(h.t, err, "git %v: %s", args, out)
	return strings.TrimSpace(string(out))
}

func (h *worktreeHarness) write(dir, name, content string) {
	h.t.Helper()
	require.NoError(h.t, os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o700))
	require.NoError(h.t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600))
}

// prepare makes a worktree for the route "app" and returns the working
// directory and its row.
func (h *worktreeHarness) prepare(eventID int64) (string, Worktree) {
	h.t.Helper()
	workDir, err := h.wt.Prepare(context.Background(), filepath.Join(h.repo, "app"), eventID)
	require.NoError(h.t, err)
	row := h.row(workDir)
	return workDir, row
}

func (h *worktreeHarness) row(workDir string) Worktree {
	h.t.Helper()
	rows, err := h.ledger.Worktrees(context.Background())
	require.NoError(h.t, err)
	for _, r := range rows {
		if r.WorkDir == workDir {
			return r
		}
	}
	h.t.Fatalf("no worktree row for %s", workDir)
	return Worktree{}
}

func (h *worktreeHarness) finish(workDir string) Worktree {
	h.t.Helper()
	require.NoError(h.t, h.wt.Finish(context.Background(), filepath.Join(h.repo, "app"), workDir))
	return h.row(workDir)
}

func (h *worktreeHarness) branchExists(branch string) bool {
	h.t.Helper()
	return h.git(h.repo, "for-each-ref", "refs/heads/"+branch) != ""
}

func TestPrepareMakesAWorktreeOnATaskBranchOutsideTheCheckout(t *testing.T) {
	h := newWorktreeHarness(t)
	workDir, row := h.prepare(17)

	assert.Equal(t, WorktreeLive, row.State)
	assert.Equal(t, filepath.Join(row.Path, "app"), workDir, "the route's place inside the repository")
	assert.True(t, strings.HasPrefix(row.Path, h.root+string(filepath.Separator)), "placed under the connector's root")
	assert.False(t, strings.HasPrefix(row.Path, h.repo), "never inside the checkout")
	assert.True(t, strings.HasPrefix(row.Branch, BranchPrefix+"17-"))
	assert.Equal(t, h.git(h.repo, "rev-parse", "HEAD"), row.BaseCommit)
	assert.FileExists(t, filepath.Join(workDir, "README"))
	assert.Empty(t, h.git(h.repo, "status", "--porcelain"), "the checkout sees nothing of it")

	info, err := os.Stat(filepath.Dir(row.Path))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), info.Mode().Perm())
}

// Invariant 1: a worktree with nothing to lose is removed, with its branch.
func TestAWorktreeWithNothingToLoseIsRemoved(t *testing.T) {
	h := newWorktreeHarness(t)
	workDir, _ := h.prepare(1)
	row := h.finish(workDir)
	assert.Equal(t, WorktreeRemoved, row.State)
	assert.Equal(t, RemovedByConnector, row.RemovedBy)
	assert.False(t, exists(row.Path))
	assert.False(t, h.branchExists(row.Branch))
}

// Invariant 1: uncommitted work survives the task's end and is listed as
// retained (the card's done-when).
func TestUncommittedWorkSurvivesTheTaskAndIsRetained(t *testing.T) {
	for name, change := range map[string]func(h *worktreeHarness, workDir string){
		"modified":  func(h *worktreeHarness, d string) { h.write(d, "README", "changed\n") },
		"untracked": func(h *worktreeHarness, d string) { h.write(d, "notes/new.txt", "draft\n") },
		"staged": func(h *worktreeHarness, d string) {
			h.write(d, "staged.txt", "x\n")
			h.git(d, "add", "staged.txt")
		},
		"deleted": func(h *worktreeHarness, d string) { require.NoError(h.t, os.Remove(filepath.Join(d, "README"))) },
		"ignored": func(h *worktreeHarness, d string) {
			exclude := h.git(d, "rev-parse", "--path-format=absolute", "--git-path", "info/exclude")
			require.NoError(h.t, os.MkdirAll(filepath.Dir(exclude), 0o700))
			require.NoError(h.t, os.WriteFile(exclude, []byte("*.local\n"), 0o600))
			h.write(d, "report.local", "results\n")
		},
		"skip-worktree": func(h *worktreeHarness, d string) {
			h.git(d, "update-index", "--skip-worktree", "README")
			h.write(d, "README", "hidden edit\n")
		},
		"assume-unchanged": func(h *worktreeHarness, d string) {
			h.git(d, "update-index", "--assume-unchanged", "README")
			h.write(d, "README", "hidden edit\n")
		},
		"merge in progress": func(h *worktreeHarness, d string) {
			marker := h.git(d, "rev-parse", "--path-format=absolute", "--git-path", "MERGE_HEAD")
			require.NoError(h.t, os.WriteFile(marker, []byte(h.git(d, "rev-parse", "HEAD")+"\n"), 0o600))
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := newWorktreeHarness(t)
			workDir, _ := h.prepare(2)
			change(h, workDir)
			row := h.finish(workDir)
			assert.Equal(t, WorktreeRetained, row.State)
			assert.Equal(t, RetainedDirty, row.RetainedReason)
			assert.True(t, exists(workDir))
			assert.True(t, h.branchExists(row.Branch))

			retained, err := h.wt.Retained(context.Background())
			require.NoError(t, err)
			require.Len(t, retained, 1)
			assert.Equal(t, row.Path, retained[0].Path)
		})
	}
}

// Invariant 1: a commit only this worktree holds keeps it; one a remote or
// the main line holds does not; another task's branch is no evidence.
func TestCommitsAreKeptUntilHeldElsewhere(t *testing.T) {
	commit := func(h *worktreeHarness, d, name string) string {
		h.write(d, name, name+"\n")
		h.git(d, "add", name)
		h.git(d, "commit", "-q", "-m", name)
		return h.git(d, "rev-parse", "HEAD")
	}
	t.Run("unpushed", func(t *testing.T) {
		h := newWorktreeHarness(t)
		workDir, _ := h.prepare(3)
		commit(h, workDir, "work.txt")
		row := h.finish(workDir)
		assert.Equal(t, RetainedUnpushed, row.RetainedReason)
		assert.True(t, exists(workDir))
	})
	t.Run("pushed", func(t *testing.T) {
		h := newWorktreeHarness(t)
		workDir, row := h.prepare(4)
		commit(h, workDir, "work.txt")
		h.git(workDir, "push", "-q", "origin", row.Branch)
		row = h.finish(workDir)
		assert.Equal(t, WorktreeRemoved, row.State)
		assert.False(t, h.branchExists(row.Branch))
	})
	t.Run("merged", func(t *testing.T) {
		h := newWorktreeHarness(t)
		workDir, row := h.prepare(5)
		commit(h, workDir, "work.txt")
		h.git(h.repo, "merge", "-q", "--ff-only", row.Branch)
		row = h.finish(workDir)
		assert.Equal(t, WorktreeRemoved, row.State)
	})
	t.Run("held only by another task's branch", func(t *testing.T) {
		h := newWorktreeHarness(t)
		workDir, _ := h.prepare(6)
		sha := commit(h, workDir, "work.txt")
		h.git(h.repo, "branch", BranchPrefix+"99-other", sha)
		row := h.finish(workDir)
		assert.Equal(t, RetainedUnpushed, row.RetainedReason)
	})
	t.Run("detached away from an unpushed branch", func(t *testing.T) {
		h := newWorktreeHarness(t)
		workDir, row := h.prepare(7)
		commit(h, workDir, "work.txt")
		h.git(workDir, "checkout", "-q", "--detach", row.BaseCommit)
		row = h.finish(workDir)
		assert.Equal(t, RetainedUnpushed, row.RetainedReason, "the task branch's commits count, wherever HEAD is")
	})
}

func TestALockedWorktreeIsRetained(t *testing.T) {
	h := newWorktreeHarness(t)
	workDir, row := h.prepare(8)
	h.git(h.repo, "worktree", "lock", row.Path)
	row = h.finish(workDir)
	assert.Equal(t, RetainedLocked, row.RetainedReason)
	assert.True(t, exists(workDir))
}

// fakeGit is a git that runs the real one, except where told to fail or to
// do something first.
func fakeGit(t *testing.T, script string) string {
	t.Helper()
	gitPath, err := exec.LookPath("git")
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "git")
	body := "#!/bin/sh\nREAL=" + gitPath + "\n" + script + "\nexec \"$REAL\" \"$@\"\n"
	require.NoError(t, os.WriteFile(path, []byte(body), 0o700))
	return path
}

// Invariant 1: a check that fails keeps the worktree.
func TestAFailedCheckRetains(t *testing.T) {
	h := newWorktreeHarness(t)
	workDir, _ := h.prepare(9)
	h.wt = h.worktrees(fakeGit(t, `for a in "$@"; do [ "$a" = status ] && exit 128; done`))
	row := h.finish(workDir)
	assert.Equal(t, WorktreeRetained, row.State)
	assert.Equal(t, RetainedUnverified, row.RetainedReason)
	assert.True(t, exists(workDir))
}

// Invariant 2: the branch is deleted only while it still points at the commit
// that was verified.
func TestABranchThatMovedIsNotDeleted(t *testing.T) {
	h := newWorktreeHarness(t)
	workDir, row := h.prepare(11)
	// Between the check and the branch's deletion someone commits onto the
	// task branch from elsewhere.
	other := filepath.Join(t.TempDir(), "other")
	h.git(h.repo, "worktree", "add", "-q", "--detach", other, row.BaseCommit)
	h.write(other, "moved.txt", "x\n")
	h.git(other, "add", "moved.txt")
	h.git(other, "commit", "-q", "-m", "moved")
	moved := h.git(other, "rev-parse", "HEAD")
	h.wt = h.worktrees(fakeGit(t, `case "$*" in *"update-ref -d"*) "$REAL" -C "`+h.repo+`" update-ref refs/heads/`+row.Branch+` `+moved+`;; esac`))
	row = h.finish(workDir)
	assert.Equal(t, WorktreeRemoved, row.State)
	assert.Equal(t, moved, h.git(h.repo, "rev-parse", "refs/heads/"+row.Branch))
}

// Invariant 6: the repository's hooks do not run.
func TestTheRepositorysHooksDoNotRun(t *testing.T) {
	h := newWorktreeHarness(t)
	marker := filepath.Join(t.TempDir(), "hook-ran")
	hook := filepath.Join(h.repo, ".git", "hooks", "post-checkout")
	require.NoError(t, os.WriteFile(hook, []byte("#!/bin/sh\ntouch "+marker+"\n"), 0o700))
	h.prepare(12)
	assert.False(t, exists(marker))
}

// Invariant 1, on the disk itself: a file in a submodule's directory, which
// git neither reports nor refuses to remove, is work.
func TestWorkInASubmodulesDirectoryIsRetained(t *testing.T) {
	h := newWorktreeHarness(t)
	sub := filepath.Join(t.TempDir(), "sub")
	require.NoError(t, os.MkdirAll(sub, 0o700))
	h.git(sub, "init", "-q", "-b", "main")
	h.write(sub, "lib.txt", "lib\n")
	h.git(sub, "add", ".")
	h.git(sub, "commit", "-q", "-m", "sub")
	h.git(h.repo, "-c", "protocol.file.allow=always", "submodule", "add", "-q", sub, "app/vendor")
	h.git(h.repo, "commit", "-q", "-m", "submodule")

	workDir, _ := h.prepare(14)
	h.write(workDir, "vendor/notes.txt", "notes\n")
	row := h.finish(workDir)
	assert.Equal(t, RetainedDirty, row.RetainedReason)
	assert.True(t, exists(filepath.Join(workDir, "vendor", "notes.txt")))
}

// Invariant 1: a commit only the worktree's reflog still reaches is work.
func TestACommitOnlyTheReflogReachesIsRetained(t *testing.T) {
	h := newWorktreeHarness(t)
	workDir, row := h.prepare(15)
	h.git(workDir, "checkout", "-q", "--detach")
	h.write(workDir, "c.txt", "c\n")
	h.git(workDir, "add", "c.txt")
	h.git(workDir, "commit", "-q", "-m", "moved away from")
	h.git(workDir, "checkout", "-q", row.Branch)
	row = h.finish(workDir)
	assert.Equal(t, RetainedUnpushed, row.RetainedReason)
}

// Invariant 6: a filter whose name git's -c could not carry, or that only
// the task branch's configuration defines, does not run either.
func TestFiltersOutOfReachOfAScanStillDoNotRun(t *testing.T) {
	for name, configure := range map[string]func(h *worktreeHarness, marker string){
		"name with =": func(h *worktreeHarness, marker string) {
			h.write(h.repo, ".gitattributes", "*.txt filter=a=b\n")
			h.git(h.repo, "config", "filter.a=b.smudge", "touch "+marker+"; cat")
		},
		"defined on the task branch": func(h *worktreeHarness, marker string) {
			h.write(h.repo, ".gitattributes", "*.txt filter=probe\n")
			include := filepath.Join(h.home, "branch-filter.gitconfig")
			h.write(h.home, "branch-filter.gitconfig", "[filter \"probe\"]\n\tsmudge = touch "+marker+"; cat\n")
			h.git(h.repo, "config", "includeIf.onbranch:"+BranchPrefix+"**.path", include)
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := newWorktreeHarness(t)
			marker := filepath.Join(t.TempDir(), "ran")
			configure(h, marker)
			h.write(h.repo, "app/data.txt", "data\n")
			h.git(h.repo, "add", ".")
			h.git(h.repo, "commit", "-q", "-m", "attributes")
			h.prepare(16)
			assert.False(t, exists(marker), "no filter ran")
		})
	}
}

// Invariant 6, at removal: git worktree remove reads the worktree's files
// under the task branch's own configuration, and a filter defined there does
// not run either.
func TestAFilterOnTheTaskBranchDoesNotRunAtRemoval(t *testing.T) {
	h := newWorktreeHarness(t)
	marker := filepath.Join(t.TempDir(), "ran")
	h.write(h.repo, ".gitattributes", "*.txt filter=probe\n")
	h.write(h.repo, "app/data.txt", "data\n")
	h.git(h.repo, "add", ".")
	h.git(h.repo, "commit", "-q", "-m", "attributes")
	workDir, _ := h.prepare(96)
	h.write(h.home, "branch-filter.gitconfig", "[filter \"probe\"]\n\tclean = touch "+marker+"; cat\n\tsmudge = touch "+marker+"; cat\n")
	h.git(h.repo, "config", "includeIf.onbranch:"+BranchPrefix+"**.path", filepath.Join(h.home, "branch-filter.gitconfig"))
	// A racy index entry makes status read the file through its clean filter.
	require.NoError(t, os.Chtimes(filepath.Join(workDir, "data.txt"), time.Now().Add(time.Hour), time.Now().Add(time.Hour)))

	row := h.finish(workDir)
	assert.False(t, exists(marker), "no filter ran")
	assert.Equal(t, WorktreeRemoved, row.State)
}

// A filter git lfs marks required does not break the checkout once blanked.
func TestARequiredFilterDoesNotBreakTheCheckout(t *testing.T) {
	h := newWorktreeHarness(t)
	h.write(h.repo, ".gitattributes", "*.bin filter=lfsish\n")
	h.write(h.repo, "app/blob.bin", "blob\n")
	h.git(h.repo, "add", ".")
	h.git(h.repo, "commit", "-q", "-m", "blob")
	h.git(h.repo, "config", "filter.lfsish.smudge", "cat")
	h.git(h.repo, "config", "filter.lfsish.clean", "cat")
	h.git(h.repo, "config", "filter.lfsish.required", "true")

	workDir, row := h.prepare(97)
	assert.Equal(t, WorktreeLive, row.State)
	assert.FileExists(t, filepath.Join(workDir, "blob.bin"))
	assert.Equal(t, WorktreeRemoved, h.finish(workDir).State)
}

// submoduleHarness is a worktree harness whose repository has a submodule at
// app/vendor, and the submodule's source.
func submoduleHarness(t *testing.T) (*worktreeHarness, string) {
	t.Helper()
	h := newWorktreeHarness(t)
	sub := filepath.Join(t.TempDir(), "sub")
	require.NoError(t, os.MkdirAll(sub, 0o700))
	h.git(sub, "init", "-q", "-b", "main")
	h.write(sub, "lib.txt", "lib\n")
	h.git(sub, "add", ".")
	h.git(sub, "commit", "-q", "-m", "sub")
	h.git(h.repo, "-c", "protocol.file.allow=always", "submodule", "add", "-q", sub, "app/vendor")
	h.git(h.repo, "commit", "-q", "-m", "submodule")
	return h, sub
}

// Invariant 6 in a submodule's directory: a git directory and configuration a
// worker planted there never make the connector's git run a filter, because
// git is never asked to look inside it.
func TestAFilterPlantedInASubmoduleDoesNotRun(t *testing.T) {
	h, sub := submoduleHarness(t)
	workDir, _ := h.prepare(98)
	marker := filepath.Join(t.TempDir(), "ran")
	vendor := filepath.Join(workDir, "vendor")
	clone := filepath.Join(t.TempDir(), "clone")
	h.git(filepath.Dir(clone), "clone", "-q", sub, clone)
	require.NoError(t, os.Rename(filepath.Join(clone, ".git"), filepath.Join(vendor, ".planted")))
	require.NoError(t, os.Rename(filepath.Join(clone, "lib.txt"), filepath.Join(vendor, "lib.txt")))
	h.write(vendor, ".git", "gitdir: .planted\n")
	h.write(vendor, ".planted/info/attributes", "lib.txt filter=probe\n")
	h.git(vendor, "config", "filter.probe.clean", "touch "+marker+"; cat")
	require.NoError(t, os.Chtimes(filepath.Join(vendor, "lib.txt"), time.Now().Add(time.Hour), time.Now().Add(time.Hour)))

	row := h.finish(workDir)
	assert.False(t, exists(marker), "no filter ran")
	assert.Equal(t, RetainedDirty, row.RetainedReason)
}

// Invariant 5 with a submodule: its commits live in git directories a forced
// removal would delete, so a worktree holding any is not forced.
func TestAForcedPruneKeepsASubmodulesCommits(t *testing.T) {
	h, _ := submoduleHarness(t)
	workDir, _ := h.prepare(99)
	h.git(workDir, "-c", "protocol.file.allow=always", "submodule", "update", "-q", "--init")
	vendor := filepath.Join(workDir, "vendor")
	h.write(vendor, "more.txt", "more\n")
	h.git(vendor, "add", ".")
	h.git(vendor, "commit", "-q", "-m", "only copy")
	subGitDir := h.git(vendor, "rev-parse", "--absolute-git-dir")
	row := h.finish(workDir)

	results, err := h.wt.Prune(context.Background(), []string{row.Path})
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, PruneKept, results[0].Action)
	assert.True(t, results[0].ForceRefused)
	assert.DirExists(t, subGitDir)
}

// A checkout that never happened leaves nothing kept: an empty worktree is
// not work, and keeping it as dirty at every retry would fill the disk.
func TestAnUnpopulatedWorktreeIsNotKept(t *testing.T) {
	h := newWorktreeHarness(t)
	h.wt = h.worktrees(fakeGit(t, `case "$*" in *"reset --quiet --hard"*) exit 128;; esac`))
	_, err := h.wt.Prepare(context.Background(), filepath.Join(h.repo, "app"), 100)
	require.Error(t, err)
	rows, err := h.ledger.Worktrees(context.Background())
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, WorktreeRemoved, rows[0].State)
	assert.False(t, exists(rows[0].Path))
	assert.False(t, h.branchExists(rows[0].Branch))
}

// A worktree whose directory was deleted is recorded missing, and the
// repository's own record of it is left for git: it can hold a submodule's
// only commits, a reflog, or a lock for a directory that is only away.
func TestAMissingWorktreesRepositoryRecordIsLeftAlone(t *testing.T) {
	h, _ := submoduleHarness(t)
	workDir, row := h.prepare(103)
	h.git(workDir, "-c", "protocol.file.allow=always", "submodule", "update", "-q", "--init")
	vendor := filepath.Join(workDir, "vendor")
	h.write(vendor, "more.txt", "more\n")
	h.git(vendor, "add", ".")
	h.git(vendor, "commit", "-q", "-m", "only copy")
	subGitDir := h.git(vendor, "rev-parse", "--absolute-git-dir")
	require.NoError(t, os.RemoveAll(row.Path))

	row = h.finish(workDir)
	assert.Equal(t, RemovedMissing, row.RemovedBy)
	assert.DirExists(t, row.AdminDir)
	assert.DirExists(t, subGitDir, "the submodule's only commits survive")
}

// A worktree someone moved is kept, not forgotten: its files are still
// somewhere, and the connector cannot judge them where it cannot find them.
func TestAMovedWorktreeIsKept(t *testing.T) {
	h := newWorktreeHarness(t)
	workDir, row := h.prepare(90)
	moved := filepath.Join(t.TempDir(), "moved")
	h.git(h.repo, "worktree", "move", row.Path, moved)
	require.False(t, exists(workDir))

	row = h.finish(workDir)
	assert.Equal(t, WorktreeRetained, row.State)
	assert.Equal(t, RetainedMoved, row.RetainedReason)
	assert.True(t, h.branchExists(row.Branch), "the branch the moved worktree has checked out")
	assert.FileExists(t, filepath.Join(moved, "app", "README"))
}

// A worktree moved with a detached HEAD is kept too: the repository's own
// record of it, not its branch, is what says where it is.
func TestAMovedWorktreeWithNoBranchIsKept(t *testing.T) {
	h := newWorktreeHarness(t)
	workDir, row := h.prepare(92)
	h.git(workDir, "checkout", "-q", "--detach")
	h.git(h.repo, "branch", "-q", "-D", row.Branch)
	moved := filepath.Join(t.TempDir(), "moved")
	h.git(h.repo, "worktree", "move", row.Path, moved)

	row = h.finish(workDir)
	assert.Equal(t, WorktreeRetained, row.State)
	assert.FileExists(t, filepath.Join(moved, "app", "README"))
}

// A worktree moved and then deleted is gone, not kept forever.
func TestAMovedWorktreeThatIsThenDeletedIsGone(t *testing.T) {
	h := newWorktreeHarness(t)
	workDir, row := h.prepare(91)
	moved := filepath.Join(t.TempDir(), "moved")
	h.git(h.repo, "worktree", "move", row.Path, moved)
	require.NoError(t, os.RemoveAll(moved))

	row = h.finish(workDir)
	assert.Equal(t, WorktreeRemoved, row.State)
	assert.Equal(t, RemovedMissing, row.RemovedBy)
}

// Invariant 1: a task branch the connector did not create is never deleted,
// however that worktree ends.
func TestABranchTheConnectorDidNotMakeIsNotDeleted(t *testing.T) {
	h := newWorktreeHarness(t)
	ctx := context.Background()
	base := h.git(h.repo, "rev-parse", "HEAD")
	branch := BranchPrefix + "80-taken"
	h.git(h.repo, "branch", branch, base)

	record := Worktree{
		Path: filepath.Join(h.root, "repo", "80-taken"), WorkDir: filepath.Join(h.root, "repo", "80-taken"),
		Route: filepath.Join(h.repo, "app"), Repository: h.repo, Branch: branch, BaseCommit: base,
		OriginatingEventID: 80, State: WorktreeCreating,
	}
	id, err := h.ledger.BeginWorktree(ctx, record)
	require.NoError(t, err)
	record.ID = id
	require.Error(t, h.wt.add(ctx, &record), "the branch is already someone's")

	unlock, err := h.wt.lock(ctx)
	require.NoError(t, err)
	settled := h.wt.settle(ctx, record)
	unlock()
	assert.Equal(t, WorktreeRemoved, settled.State)
	assert.True(t, h.branchExists(branch), "someone else's branch survives")
	assert.False(t, h.row(record.WorkDir).BranchCreated)
}

// A worktree that cannot be made is not attempted again at every dispatch
// tick: each failure leaves a row and maybe a partial checkout.
func TestAFailedPrepareBacksOff(t *testing.T) {
	h := newWorktreeHarness(t)
	h.wt = h.worktrees(fakeGit(t, `case "$*" in *"worktree add"*) exit 128;; esac`))
	clock := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	h.wt.now = func() time.Time { return clock }
	route := filepath.Join(h.repo, "app")
	ctx := context.Background()

	_, err := h.wt.Prepare(ctx, route, 70)
	require.Error(t, err)
	_, err = h.wt.Prepare(ctx, route, 70)
	require.ErrorIs(t, err, ErrPrepareBackoff)
	rows, err := h.ledger.Worktrees(ctx)
	require.NoError(t, err)
	assert.Len(t, rows, 1, "the second call made nothing")

	clock = clock.Add(PrepareBackoff)
	_, err = h.wt.Prepare(ctx, route, 70)
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrPrepareBackoff)
	clock = clock.Add(PrepareBackoff)
	_, err = h.wt.Prepare(ctx, route, 70)
	require.ErrorIs(t, err, ErrPrepareBackoff, "the wait doubles")

	_, err = h.wt.Prepare(ctx, route, 71)
	require.ErrorIs(t, err, ErrPrepareBackoff, "the route waits, whichever event asks")
	assert.Equal(t, []string{route}, h.wt.RoutesWaiting())

	clock = clock.Add(PrepareBackoffMax)
	assert.Empty(t, h.wt.RoutesWaiting(), "a route whose wait is over is not held")
}

// A route that cannot take a worktree never fills the window the dispatcher
// starts records from: a healthy route's record still starts.
func TestAFailingWorktreeRouteDoesNotStarveTheOthers(t *testing.T) {
	h := newWorktreeHarness(t)
	broken := filepath.Join(t.TempDir(), "not-a-repository")
	require.NoError(t, os.MkdirAll(broken, 0o700))
	healthy := filepath.Join(h.repo, "app")
	const brokenBucket = 777
	fake := newFakeDriver()
	d := newDispatchHarness(t, fake, func(o *DispatcherOptions) {
		o.Ledger = h.ledger
		o.Workspaces = h.wt
	})
	d.ledger = h.ledger
	d.mu.Lock()
	d.routes = map[int64]admission.Route{adapterBucketID: {Path: healthy}, brokenBucket: {Path: broken}}
	d.mu.Unlock()
	admit := func(id, bucket int64, route string) {
		event := testEvent(id)
		event.BucketID = bucket
		_, err := h.ledger.RecordSeen(context.Background(), event, LanePoll)
		require.NoError(t, err)
		v := admittedVerdict(id, 0, "recording:"+strconv.FormatInt(id, 10))
		v.BucketID, v.Route = bucket, route
		_, err = h.ledger.Admission().Commit(context.Background(), v)
		require.NoError(t, err)
	}
	for id := int64(100); id < 110; id++ {
		admit(id, brokenBucket, broken)
	}
	admit(200, adapterBucketID, healthy)
	d.run(t)
	select {
	case s := <-fake.made:
		assert.True(t, strings.HasPrefix(s.cfg.Cwd, h.root), "the healthy route's record started in its worktree")
	case <-time.After(10 * time.Second):
		t.Fatal("a route that cannot take a worktree starved a healthy one")
	}
}

// With worktrees off, a new task works in its route, and a worktree made
// while they were on is still recovered.
func TestWorktreesOffStillRecoversWhatWasMade(t *testing.T) {
	h := newWorktreeHarness(t)
	ctx := context.Background()
	workDir, _ := h.prepare(72)
	h.write(workDir, "wip.txt", "wip\n")

	off, err := NewWorktrees(WorktreesOptions{Ledger: h.ledger, Root: h.root, Lookup: h.lookup, Off: true})
	require.NoError(t, err)
	assert.False(t, off.PerTaskDirs())
	route := filepath.Join(h.repo, "app")
	dir, err := off.Prepare(ctx, route, 73)
	require.NoError(t, err)
	assert.Equal(t, route, dir)
	require.NoError(t, off.Finish(ctx, route, route))

	require.NoError(t, off.Recover(ctx))
	assert.Equal(t, RetainedDirty, h.row(workDir).RetainedReason)
}

// Invariant 6: a content filter the repository's configuration defines does
// not run when the connector checks out or inspects a worktree.
func TestConfiguredContentFiltersDoNotRun(t *testing.T) {
	h := newWorktreeHarness(t)
	markers := t.TempDir()
	h.write(h.repo, ".gitattributes", "*.txt filter=probe\n")
	h.write(h.repo, "app/data.txt", "data\n")
	h.git(h.repo, "add", ".")
	h.git(h.repo, "commit", "-q", "-m", "attributes")
	h.git(h.repo, "config", "filter.probe.smudge", "touch "+filepath.Join(markers, "smudge")+"; cat")
	h.git(h.repo, "config", "filter.probe.clean", "touch "+filepath.Join(markers, "clean")+"; cat")

	workDir, _ := h.prepare(13)
	h.write(workDir, "data.txt", "changed\n")
	row := h.finish(workDir)
	assert.Equal(t, RetainedDirty, row.RetainedReason)
	entries, err := os.ReadDir(markers)
	require.NoError(t, err)
	assert.Empty(t, entries, "no filter ran")
}

// Invariant 3: every row a crash can leave is settled on the next start under
// the same rules, and a worktree a live task works in is not touched.
func TestRecoverSettlesWhatACrashLeft(t *testing.T) {
	h := newWorktreeHarness(t)
	ctx := context.Background()

	// Crashed before git ran: a row and nothing on disk.
	never := Worktree{Path: filepath.Join(h.root, "x", "20-aaaaaa"), WorkDir: filepath.Join(h.root, "x", "20-aaaaaa", "app"), Route: filepath.Join(h.repo, "app"),
		Repository: h.repo, Branch: BranchPrefix + "20-aaaaaa", BaseCommit: h.git(h.repo, "rev-parse", "HEAD"), OriginatingEventID: 20}
	neverID, err := h.ledger.BeginWorktree(ctx, never)
	require.NoError(t, err)

	// Crashed between git and live, with work in it.
	dirtyDir, dirty := h.prepare(21)
	h.write(dirtyDir, "wip.txt", "wip\n")
	// Crashed mid-removal of a clean one.
	cleanDir, clean := h.prepare(22)
	require.NoError(t, h.ledger.MoveWorktree(ctx, clean.ID, WorktreeRemoving, WorktreeLive))
	// A live task still works in this one.
	liveDir, _ := h.prepare(23)
	admitOn(t, h.ledger, 23, "recording:23")
	_, err = h.ledger.LaunchTask(ctx, LaunchSpec{EventID: 23, Route: testRoute, WorkDir: liveDir, Driver: "fake"})
	require.NoError(t, err)

	require.NoError(t, h.wt.Recover(ctx))

	rows, err := h.ledger.Worktrees(ctx)
	require.NoError(t, err)
	byID := map[int64]Worktree{}
	for _, r := range rows {
		byID[r.ID] = r
	}
	assert.Equal(t, RemovedNeverCreated, byID[neverID].RemovedBy)
	assert.Equal(t, RetainedDirty, byID[dirty.ID].RetainedReason)
	assert.True(t, exists(filepath.Join(dirtyDir, "wip.txt")))
	assert.Equal(t, WorktreeRemoved, byID[clean.ID].State)
	assert.False(t, exists(cleanDir))
	assert.Equal(t, WorktreeLive, h.row(liveDir).State)
}

// Invariant 4: a check-and-remove waits for the lock another remover holds.
func TestRemovalsTakeTheWorktreesLock(t *testing.T) {
	h := newWorktreeHarness(t)
	workDir, _ := h.prepare(30)
	unlock, err := h.wt.lock(context.Background())
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	err = h.wt.Finish(ctx, filepath.Join(h.repo, "app"), workDir)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Equal(t, WorktreeLive, h.row(workDir).State)
	unlock()
	assert.Equal(t, WorktreeRemoved, h.finish(workDir).State)
}

// Invariant 5: prune removes what the operator dealt with, keeps what still
// holds work, forces only what the operator names, and never reaches a
// worktree that is not retained.
func TestPruneRemovesOnlyWhatTheOperatorDealtWith(t *testing.T) {
	h := newWorktreeHarness(t)
	ctx := context.Background()

	dealtDir, dealt := h.prepare(40)
	h.write(dealtDir, "done.txt", "x\n")
	h.finish(dealtDir)
	h.git(dealtDir, "add", "done.txt")
	h.git(dealtDir, "commit", "-q", "-m", "done")
	h.git(dealtDir, "push", "-q", "origin", dealt.Branch)

	keptDir, _ := h.prepare(41)
	h.write(keptDir, "wip.txt", "wip\n")
	h.finish(keptDir)

	goneDir, gone := h.prepare(42)
	h.write(goneDir, "wip.txt", "wip\n")
	h.finish(goneDir)
	require.NoError(t, os.RemoveAll(gone.Path))

	forcedDir, forced := h.prepare(43)
	h.write(forcedDir, "c.txt", "c\n")
	h.git(forcedDir, "add", "c.txt")
	h.git(forcedDir, "commit", "-q", "-m", "c")
	h.write(forcedDir, "wip.txt", "wip\n")
	h.finish(forcedDir)

	liveDir, live := h.prepare(44)
	h.write(liveDir, "wip.txt", "wip\n")

	_, err := h.wt.Prune(ctx, []string{live.Path})
	require.ErrorIs(t, err, ErrNotRetained, "a live worktree is never prune's")
	assert.True(t, exists(filepath.Join(liveDir, "wip.txt")))
	assert.True(t, exists(filepath.Join(forcedDir, "wip.txt")), "a refused prune removes nothing")

	results, err := h.wt.Prune(ctx, []string{forced.Path})
	require.NoError(t, err)
	actions := map[string]PruneResult{}
	for _, r := range results {
		actions[r.Worktree.Path] = r
	}
	require.Len(t, actions, 4)
	assert.Equal(t, PruneRemoved, actions[dealt.Path].Action)
	assert.False(t, exists(dealt.Path))
	assert.Equal(t, PruneKept, actions[h.row(keptDir).Path].Action)
	assert.Equal(t, RetainedDirty, actions[h.row(keptDir).Path].Reason)
	assert.True(t, exists(filepath.Join(keptDir, "wip.txt")))
	assert.Equal(t, PruneMissing, actions[gone.Path].Action)
	assert.Equal(t, PruneForced, actions[forced.Path].Action)
	assert.NotEmpty(t, actions[forced.Path].RetainedRefs, "an unpushed commit is kept under a ref")
	assert.True(t, h.branchExists(forced.Branch))
	assert.False(t, exists(forced.Path))
	assert.Equal(t, WorktreeLive, h.row(liveDir).State)
	assert.True(t, exists(filepath.Join(liveDir, "wip.txt")))
}

// Invariant 5: a forced prune keeps a commit only a detached HEAD holds, on a
// branch of its own.
func TestAForcedPruneKeepsADetachedHeadsCommit(t *testing.T) {
	h := newWorktreeHarness(t)
	workDir, _ := h.prepare(45)
	h.git(workDir, "checkout", "-q", "--detach")
	h.write(workDir, "c.txt", "c\n")
	h.git(workDir, "add", "c.txt")
	h.git(workDir, "commit", "-q", "-m", "detached")
	commit := h.git(workDir, "rev-parse", "HEAD")
	row := h.finish(workDir)
	require.Equal(t, RetainedUnpushed, row.RetainedReason)

	results, err := h.wt.Prune(context.Background(), []string{row.Path})
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, PruneForced, results[0].Action)
	require.NotEmpty(t, results[0].RetainedRefs)
	assert.Contains(t, h.git(h.repo, "for-each-ref", "--format=%(objectname)", RetainedRefPrefix), commit)
	assert.False(t, exists(row.Path))
}

// The card's done-when, through the dispatcher: a worker leaves uncommitted
// work, its task ends, and the worktree is retained and listed.
func TestADispatchedTasksUncommittedWorkIsRetained(t *testing.T) {
	h := newWorktreeHarness(t)
	route := filepath.Join(h.repo, "app")
	fake := newFakeDriver()
	fake.turn = func(s *fakeSession, _ int, _ string) (driver.PromptResult, error) {
		require.NoError(t, os.WriteFile(filepath.Join(s.cfg.Cwd, "answer.txt"), []byte("work\n"), 0o600))
		return driver.PromptResult{Stop: driver.TurnEndTurn}, nil
	}
	d := newDispatchHarness(t, fake, func(o *DispatcherOptions) {
		o.Ledger = h.ledger
		o.Workspaces = h.wt
	})
	d.ledger = h.ledger
	d.routes = map[int64]admission.Route{adapterBucketID: {Path: route}}
	for _, id := range []int64{50, 51} {
		seenRecord(t, h.ledger, id)
		v := admittedVerdict(id, 0, "recording:"+string(rune('a'+id-50)))
		v.Route = route
		_, err := h.ledger.Admission().Commit(context.Background(), v)
		require.NoError(t, err)
	}
	stop := d.run(t)
	a, b := <-fake.made, <-fake.made
	assert.NotEqual(t, a.cfg.Cwd, b.cfg.Cwd, "two tasks on one route, each in its own worktree")
	d.attemptsEnded(t, 2)
	stop()

	retained, err := h.wt.Retained(context.Background())
	require.NoError(t, err)
	require.Len(t, retained, 2)
	for _, r := range retained {
		assert.Equal(t, RetainedDirty, r.RetainedReason)
		assert.NotZero(t, r.TaskID)
		content, err := os.ReadFile(filepath.Join(r.WorkDir, "answer.txt"))
		require.NoError(t, err)
		assert.Equal(t, "work\n", string(content))
	}
}

// Worktree states move along their edges only: nothing goes back to live,
// and nothing leaves removed.
func TestWorktreeStatesMoveAlongTheirEdgesOnly(t *testing.T) {
	h := newWorktreeHarness(t)
	ctx := context.Background()
	_, row := h.prepare(60)
	require.NoError(t, h.ledger.RetainWorktree(ctx, row.ID, RetainedDirty, WorktreeLive))
	_, err := h.ledger.db.ExecContext(ctx, `UPDATE worktrees SET state = 'live' WHERE id = ?`, row.ID)
	require.Error(t, err)
	require.NoError(t, h.ledger.RemovedWorktree(ctx, row.ID, RemovedMissing, WorktreeRetained))
	_, err = h.ledger.db.ExecContext(ctx, `UPDATE worktrees SET state = 'retained', removed_by = '', retained_reason = 'dirty' WHERE id = ?`, row.ID)
	require.Error(t, err)
	require.ErrorIs(t, h.ledger.MoveWorktree(ctx, row.ID, WorktreeRemoving, WorktreeRetained), ErrWorktreeState)
}

// A path the connector cannot even look at is not proof that work is gone.
func TestAnUnreadablePathCountsAsThere(t *testing.T) {
	dir := t.TempDir()
	closed := filepath.Join(dir, "closed")
	require.NoError(t, os.Mkdir(closed, 0o700))
	inside := filepath.Join(closed, "worktree")
	require.NoError(t, os.Mkdir(inside, 0o700))
	require.NoError(t, os.Chmod(closed, 0o000))
	t.Cleanup(func() { _ = os.Chmod(closed, 0o700) })
	if _, err := os.Lstat(inside); err == nil {
		t.Skip("this user can read through a closed directory")
	}

	assert.True(t, exists(inside), "unreadable is not absent")
	assert.False(t, exists(filepath.Join(dir, "never")), "absent is absent")
}

// A moved worktree is found through the repository's record of it however
// that record spells the path.
func TestAMovedWorktreeIsFoundWithRelativePaths(t *testing.T) {
	h := newWorktreeHarness(t)
	h.git(h.repo, "config", "worktree.useRelativePaths", "true")
	workDir, row := h.prepare(95)
	moved := filepath.Join(t.TempDir(), "moved")
	h.git(h.repo, "worktree", "move", row.Path, moved)

	row = h.finish(workDir)
	assert.Equal(t, RetainedMoved, row.RetainedReason)
	assert.True(t, h.branchExists(row.Branch))
	assert.FileExists(t, filepath.Join(moved, "app", "README"))
}

// A moved worktree is not forced away either: there is nothing at the path to
// judge, and prune says so instead of trying.
func TestAMovedWorktreeIsNotForced(t *testing.T) {
	h := newWorktreeHarness(t)
	workDir, row := h.prepare(93)
	moved := filepath.Join(t.TempDir(), "moved")
	h.git(h.repo, "worktree", "move", row.Path, moved)
	row = h.finish(workDir)
	require.Equal(t, RetainedMoved, row.RetainedReason)

	results, err := h.wt.Prune(context.Background(), []string{row.Path})
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, PruneKept, results[0].Action)
	assert.Equal(t, RetainedMoved, results[0].Reason)
	assert.False(t, results[0].ForceRefused, "prune says where it is, it does not try and fail")
	assert.FileExists(t, filepath.Join(moved, "app", "README"))
}

// A branch the connector made for a worktree that then failed to appear is
// its own to clean up.
func TestAFailedAddLeavesNoBranchBehind(t *testing.T) {
	h := newWorktreeHarness(t)
	h.wt = h.worktrees(fakeGit(t, `case "$*" in *"worktree add"*) exit 128;; esac`))
	_, err := h.wt.Prepare(context.Background(), filepath.Join(h.repo, "app"), 94)
	require.Error(t, err)
	rows, err := h.ledger.Worktrees(context.Background())
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.True(t, rows[0].BranchCreated)
	assert.False(t, h.branchExists(rows[0].Branch), "the branch it made goes with it")
}

// A removal the ledger could not record is still reported as a removal, and
// Finish says it was not recorded, rather than anyone being told it was kept.
func TestARemovalTheLedgerCouldNotRecordIsNotReportedKept(t *testing.T) {
	h := newWorktreeHarness(t)
	ctx := context.Background()
	workDir, _ := h.prepare(104)
	_, err := h.ledger.db.ExecContext(ctx, `CREATE TRIGGER refuse_removed BEFORE UPDATE OF state ON worktrees
WHEN NEW.state = 'removed' BEGIN SELECT RAISE(ABORT, 'test: the ledger refuses'); END`)
	require.NoError(t, err)

	err = h.wt.Finish(ctx, filepath.Join(h.repo, "app"), workDir)
	require.Error(t, err)
	row := h.row(workDir)
	assert.Equal(t, WorktreeRemoving, row.State)
	assert.False(t, exists(row.Path))

}

// A missing worktree whose record in the repository still reaches a commit
// nothing else holds is kept, so the operator hears of it before git's own
// prune takes it; the connector deletes nothing either way.
func TestAMissingWorktreeWhoseRecordHoldsACommitIsKept(t *testing.T) {
	h := newWorktreeHarness(t)
	workDir, row := h.prepare(105)
	h.git(workDir, "checkout", "-q", "--detach")
	h.write(workDir, "c.txt", "c\n")
	h.git(workDir, "add", "c.txt")
	h.git(workDir, "commit", "-q", "-m", "reflog only")
	h.git(workDir, "checkout", "-q", row.Branch)
	require.NoError(t, os.RemoveAll(row.Path))

	row = h.finish(workDir)
	assert.Equal(t, WorktreeRetained, row.State)
	assert.DirExists(t, row.AdminDir)
}

// A forced removal the ledger could not record is still reported forced.
func TestAForcedRemovalTheLedgerCouldNotRecordIsReportedForced(t *testing.T) {
	h := newWorktreeHarness(t)
	ctx := context.Background()
	workDir, _ := h.prepare(106)
	h.write(workDir, "wip.txt", "wip\n")
	row := h.finish(workDir)
	require.Equal(t, RetainedDirty, row.RetainedReason)
	_, err := h.ledger.db.ExecContext(ctx, `CREATE TRIGGER refuse_removed BEFORE UPDATE OF state ON worktrees
WHEN NEW.state = 'removed' BEGIN SELECT RAISE(ABORT, 'test: the ledger refuses'); END`)
	require.NoError(t, err)

	results, err := h.wt.Prune(ctx, []string{row.Path})
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, PruneForced, results[0].Action)
	assert.False(t, results[0].ForceRefused)
	assert.False(t, exists(row.Path))
}

// The worktree rule ("One worktree, one removal"), case by case: what counts
// as work, what happens to it, and that the check still holds when something
// tries to land work between the check and the removal.
func TestTheWorktreeRule(t *testing.T) {
	commit := func(h *worktreeHarness, dir, name string) string {
		h.write(dir, name, name+"\n")
		h.git(dir, "add", name)
		h.git(dir, "commit", "-q", "-m", name)
		return h.git(dir, "rev-parse", "HEAD")
	}
	type rowT struct {
		name string
		// work makes the worktree's state; it returns a commit that must
		// survive, if any.
		work func(h *worktreeHarness, dir string, row Worktree) string
		// frozen runs after the removal has frozen the worktree.
		frozen func(t *testing.T, h *worktreeHarness, dir string, row Worktree)
		force  bool
		// want is the row's state after; reason when retained.
		want   WorktreeState
		reason RetainedReason
	}
	rows := []rowT{
		{name: "clean", want: WorktreeRemoved},
		{name: "modified file", work: func(h *worktreeHarness, d string, _ Worktree) string { h.write(d, "README", "x\n"); return "" }, want: WorktreeRetained, reason: RetainedDirty},
		{name: "untracked file", work: func(h *worktreeHarness, d string, _ Worktree) string { h.write(d, "new.txt", "x\n"); return "" }, want: WorktreeRetained, reason: RetainedDirty},
		{name: "ignored file", work: func(h *worktreeHarness, d string, _ Worktree) string {
			exclude := h.git(d, "rev-parse", "--path-format=absolute", "--git-path", "info/exclude")
			require.NoError(h.t, os.MkdirAll(filepath.Dir(exclude), 0o700))
			require.NoError(h.t, os.WriteFile(exclude, []byte("*.local\n"), 0o600))
			h.write(d, "notes.local", "x\n")
			return ""
		}, want: WorktreeRetained, reason: RetainedDirty},
		{name: "unpushed commit", work: func(h *worktreeHarness, d string, _ Worktree) string { return commit(h, d, "c.txt") }, want: WorktreeRetained, reason: RetainedUnpushed},
		{name: "commit only the reflog reaches", work: func(h *worktreeHarness, d string, row Worktree) string {
			h.git(d, "checkout", "-q", "--detach")
			sha := commit(h, d, "c.txt")
			h.git(d, "checkout", "-q", row.Branch)
			return sha
		}, want: WorktreeRetained, reason: RetainedUnpushed},
		{name: "commit a per-worktree ref holds", work: func(h *worktreeHarness, d string, row Worktree) string {
			sha := commit(h, d, "c.txt")
			h.git(d, "update-ref", "refs/worktree/keep", sha)
			h.git(d, "reset", "-q", "--hard", row.BaseCommit)
			h.git(d, "reflog", "expire", "--expire=now", "--all")
			return sha
		}, want: WorktreeRetained, reason: RetainedUnpushed},
		{name: "stash", work: func(h *worktreeHarness, d string, _ Worktree) string {
			h.write(d, "README", "stashed\n")
			h.git(d, "stash", "-q")
			return h.git(d, "rev-parse", "refs/stash")
		}, want: WorktreeRemoved},
		{name: "locked", work: func(h *worktreeHarness, _ string, row Worktree) string {
			h.git(h.repo, "worktree", "lock", row.Path)
			return ""
		}, want: WorktreeRetained, reason: RetainedLocked},
		{name: "a commit tried between the check and the removal", frozen: func(t *testing.T, h *worktreeHarness, dir string, row Worktree) {
			for _, at := range []string{filepath.Join(row.Path, "app"), filepath.Join(dir, "app")} {
				cmd := exec.CommandContext(context.Background(), "git", "-c", "user.name=T", "-c", "user.email=t@example.invalid", "commit", "-q", "--allow-empty", "-m", "late")
				cmd.Dir = at
				cmd.Env = []string{"HOME=" + h.home, "PATH=" + os.Getenv("PATH")}
				assert.Error(t, cmd.Run(), "no commit lands in a frozen worktree (%s)", at)
			}
		}, want: WorktreeRemoved},
		{name: "a file written by path between the check and the removal", frozen: func(t *testing.T, _ *worktreeHarness, _ string, row Worktree) {
			assert.Error(t, os.WriteFile(filepath.Join(row.Path, "app", "late.txt"), []byte("x"), 0o600), "the path does not reach a frozen worktree")
		}, want: WorktreeRemoved},
		{name: "forced unpushed commit", work: func(h *worktreeHarness, d string, _ Worktree) string { return commit(h, d, "c.txt") }, force: true, want: WorktreeRemoved},
		{name: "forced commit only the reflog reaches", work: func(h *worktreeHarness, d string, row Worktree) string {
			h.git(d, "checkout", "-q", "--detach")
			sha := commit(h, d, "c.txt")
			h.git(d, "checkout", "-q", row.Branch)
			h.write(d, "wip.txt", "wip\n")
			return sha
		}, force: true, want: WorktreeRemoved},
	}
	for _, tc := range rows {
		t.Run(tc.name, func(t *testing.T) {
			h := newWorktreeHarness(t)
			ctx := context.Background()
			workDir, row := h.prepare(300)
			var keep string
			if tc.work != nil {
				keep = tc.work(h, workDir, row)
			}
			if tc.frozen != nil {
				h.wt.whileFrozen = func(dir string) error { tc.frozen(t, h, dir, row); return nil }
			}
			var after Worktree
			if tc.force {
				// A force is prune's: the worktree is retained first.
				h.wt.whileFrozen = nil
				require.Equal(t, WorktreeRetained, h.finish(workDir).State)
				results, err := h.wt.Prune(ctx, []string{row.Path})
				require.NoError(t, err)
				require.Len(t, results, 1)
				after = h.row(workDir)
			} else {
				after = h.finish(workDir)
			}
			assert.Equal(t, tc.want, after.State)
			if tc.reason != "" {
				assert.Equal(t, tc.reason, after.RetainedReason)
			}
			if tc.want == WorktreeRetained {
				assert.DirExists(t, workDir, "a kept worktree is where it was")
				assert.NoDirExists(t, frozenName(row.Path))
			} else {
				assert.NoDirExists(t, row.Path)
				assert.NoDirExists(t, frozenName(row.Path))
				assert.NoDirExists(t, frozenName(row.AdminDir))
			}
			if keep != "" {
				assert.NoError(t, exec.CommandContext(ctx, "git", "-C", h.repo, "cat-file", "-e", keep+"^{commit}").Run())
				if tc.want == WorktreeRemoved {
					refs := h.git(h.repo, "for-each-ref", "--contains", keep, "--format=%(refname)")
					assert.NotEmpty(t, refs, "the commit is still reachable from a ref")
				}
			}
		})
	}
}

// A crash while a worktree is frozen leaves a removing row and frozen names;
// the next start restores them and judges again.
func TestACrashWhileFrozenIsRestoredOnTheNextStart(t *testing.T) {
	h := newWorktreeHarness(t)
	ctx := context.Background()
	workDir, row := h.prepare(301)
	h.write(workDir, "wip.txt", "wip\n")
	h.wt.whileFrozen = func(string) error { return errors.New("crash") }
	require.Error(t, h.wt.Finish(ctx, filepath.Join(h.repo, "app"), workDir))
	require.DirExists(t, frozenName(row.Path))
	require.Equal(t, WorktreeRemoving, h.row(workDir).State)

	h.wt.whileFrozen = nil
	require.NoError(t, h.wt.Recover(ctx))
	after := h.row(workDir)
	assert.Equal(t, WorktreeRetained, after.State)
	assert.Equal(t, RetainedDirty, after.RetainedReason)
	assert.FileExists(t, filepath.Join(workDir, "wip.txt"))
	assert.DirExists(t, row.AdminDir)
	assert.NoDirExists(t, frozenName(row.Path))
}
