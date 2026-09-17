package connector

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
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

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
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

// Invariant 2: work written between the check and the removal stops git's
// removal, and the worktree is retained with the work in it.
func TestWorkWrittenAfterTheCheckStopsTheRemoval(t *testing.T) {
	h := newWorktreeHarness(t)
	workDir, _ := h.prepare(10)
	late := filepath.Join(workDir, "late.txt")
	h.wt = h.worktrees(fakeGit(t, `case "$*" in *"worktree remove"*) echo late > "`+late+`";; esac`))
	row := h.finish(workDir)
	assert.Equal(t, WorktreeRetained, row.State)
	content, err := os.ReadFile(late)
	require.NoError(t, err)
	assert.Equal(t, "late\n", string(content))
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
	assert.True(t, actions[forced.Path].BranchKept, "an unpushed commit's branch outlives a forced removal")
	assert.True(t, h.branchExists(forced.Branch))
	assert.False(t, exists(forced.Path))
	assert.Equal(t, WorktreeLive, h.row(liveDir).State)
	assert.True(t, exists(filepath.Join(liveDir, "wip.txt")))
}

// Invariant 5: a forced prune keeps a commit only a detached HEAD holds, on a
// branch of its own.
func TestAForcedPruneKeepsADetachedHeadsCommit(t *testing.T) {
	h := newWorktreeHarness(t)
	workDir, row := h.prepare(45)
	h.git(workDir, "checkout", "-q", "--detach")
	h.write(workDir, "c.txt", "c\n")
	h.git(workDir, "add", "c.txt")
	h.git(workDir, "commit", "-q", "-m", "detached")
	commit := h.git(workDir, "rev-parse", "HEAD")
	row = h.finish(workDir)
	require.Equal(t, RetainedUnpushed, row.RetainedReason)

	results, err := h.wt.Prune(context.Background(), []string{row.Path})
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, PruneForced, results[0].Action)
	require.NotEmpty(t, results[0].HeadBranch)
	assert.Equal(t, commit, h.git(h.repo, "rev-parse", "refs/heads/"+results[0].HeadBranch))
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
