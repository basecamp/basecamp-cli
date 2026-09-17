package connector

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/basecamp/basecamp-cli/internal/connector/driver"
	"github.com/basecamp/basecamp-cli/internal/connector/setup"
)

// Worktrees is --worktrees: each task works in a git worktree of its own,
// branched from the route's HEAD, so tasks on one repository run side by
// side. A worktree is removed when its task ends only if nothing in it could
// be lost; otherwise it is retained, recorded in the ledger with the reason,
// for `basecamp connect worktrees prune`.
//
// # Invariants
//
// Each is held by a test in worktrees_test.go.
//
//  1. No work is ever deleted by the connector. A worktree is removed only
//     when nothing on its disk is anything but a file git tracks, unchanged
//     (no modified, untracked or ignored file, no directory git has no file
//     in, nothing inside a submodule's empty directory, no index entry hiding
//     an edit), no operation is in progress, it is not locked, and every
//     commit it reaches — HEAD, its task branch, their reflogs, per-worktree
//     refs — is the base it was made from or is held by a remote branch or by
//     a local branch that is not another task's. Any error while deciding
//     that retains it. What git keeps for a worktree whose directory is gone
//     (its record under .git/worktrees, with any submodule git directories
//     and reflog in it) is git's to prune, never the connector's.
//  2. Git refuses too. The removal itself is `git worktree remove` without
//     --force, so a modified or untracked file written between the check and
//     the removal still stops it, and a task branch is deleted only by
//     compare-and-delete against the commit that was verified. Two things git
//     does not refuse in that window: an ignored file written into the
//     worktree, and a HEAD moved onto a commit nothing else holds. Removal
//     runs only after the task's process group is confirmed gone, so what is
//     left is a process that escaped the group or a person working in a kept
//     worktree while pruning it, and the window is the one git call.
//  3. The ledger first. A worktree is recorded creating before `git worktree
//     add` runs, and removing before `git worktree remove` does, so a crash
//     at any point leaves a row that says where a directory may be; the
//     connector's next start reconciles every such row under the same rules.
//  4. One remover at a time. Every check-and-remove, the connector's and
//     prune's, holds the worktrees lock, so a prune and a finishing task never
//     remove one worktree twice, and prune touches only retained worktrees.
//  5. Prune refuses work. A retained worktree still holding work is removed
//     only when the operator names it with --force, and even then its branch
//     is kept unless its commits are held elsewhere, and the commit HEAD is
//     on is kept on a branch of its own when nothing else holds it; a HEAD it
//     cannot read, or one holding a submodule's own content, is not forced.
//     What a force does discard is a commit only
//     the worktree's own reflog, a per-worktree ref, or the reflog of a task
//     branch deleted because its tip was held elsewhere still reaches.
//  6. Nothing the repository, its configuration or a worker's files name runs:
//     no git command looks inside a submodule's directory (the disk is judged
//     before git is asked anything that could recurse, and status is told to
//     ignore submodules; the non-forced removal's own check is the one-call
//     window invariant 2 names), and git runs with
//     hooks, the fsmonitor and every content filter its configuration defines
//     for the directory it runs in disabled (the new worktree's own, for its
//     checkout), and a fixed environment.
//
// Placement goes through Options.Path, one function, because under the
// sandbox launcher (step 26) the working directory comes from broker-owned
// scopes instead.
type Worktrees struct {
	ledger *Ledger
	root   string
	git    string
	env    []string
	path   func(root, repository, name string) string
	log    *slog.Logger
	now    func() time.Time

	// Off leaves new tasks in their route; see WorktreesOptions.Off.
	off bool

	mu       sync.Mutex
	failures map[string]prepareFailure
}

var _ WaitingWorkspaces = (*Worktrees)(nil)

// prepareFailure is a route that could not take a worktree, and when to try
// it again.
type prepareFailure struct {
	count int
	until time.Time
}

// Prepare's backoff after a failure: doubling from the first, capped.
const (
	PrepareBackoff    = time.Minute
	PrepareBackoffMax = 30 * time.Minute
)

// ErrPrepareBackoff is a Prepare on a route whose last worktree failed too
// recently to try again.
var ErrPrepareBackoff = errors.New("the last worktree on this route failed; waiting before trying again")

// WorktreesOptions configures Worktrees.
type WorktreesOptions struct {
	Ledger *Ledger
	// Root is the owner-only directory worktrees are placed under: the
	// connector state directory's worktrees/.
	Root string
	// Git is the git binary; "git" on PATH when empty.
	Git string
	// Lookup reads the connector's environment for git's; os.LookupEnv when
	// nil.
	Lookup func(string) (string, bool)
	// Path places a task's worktree; DefaultWorktreePath when nil.
	Path   func(root, repository, name string) string
	Logger *slog.Logger
	// Off gives new tasks no worktree: they work in the route itself. The
	// worktrees made while it was on are still settled and recovered, so
	// switching worktrees off never strands one.
	Off bool
}

var (
	_ PerTaskWorkspaces    = (*Worktrees)(nil)
	_ RecoveringWorkspaces = (*Worktrees)(nil)
)

// BranchPrefix names every task branch, so a task branch is never evidence
// that another task's commits are safe.
const BranchPrefix = "basecamp-connect/"

// NewWorktrees builds Worktrees.
func NewWorktrees(opts WorktreesOptions) (*Worktrees, error) {
	if opts.Ledger == nil || opts.Root == "" || !filepath.IsAbs(opts.Root) {
		return nil, errors.New("connector: worktrees need the ledger and an absolute root")
	}
	if opts.Git == "" {
		opts.Git = "git"
	}
	if opts.Lookup == nil {
		opts.Lookup = os.LookupEnv
	}
	if opts.Path == nil {
		opts.Path = DefaultWorktreePath
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	env := driver.BuildEnv(driver.BaseEnv, opts.Lookup, map[string]string{
		// Never ask anyone anything, never take an optional lock a person's
		// own git in the checkout would then wait on.
		"GIT_TERMINAL_PROMPT": "0",
		"GIT_OPTIONAL_LOCKS":  "0",
		"LC_ALL":              "C",
	})
	return &Worktrees{
		ledger: opts.Ledger, root: opts.Root, git: opts.Git, env: env, path: opts.Path, log: opts.Logger,
		now: time.Now, off: opts.Off, failures: map[string]prepareFailure{},
	}, nil
}

// DefaultWorktreePath places a worktree under the connector's state
// directory, one directory per repository: never inside the checkout, where a
// task working in the route itself could edit another task's retained work,
// and `git add -A` in the checkout would pick it up.
func DefaultWorktreePath(root, repository, name string) string {
	sum := sha256.Sum256([]byte(repository))
	return filepath.Join(root, safeName(filepath.Base(repository))+"-"+hex.EncodeToString(sum[:4]), name)
}

var unsafeNameRunes = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func safeName(s string) string {
	s = unsafeNameRunes.ReplaceAllString(s, "-")
	s = strings.Trim(s, ".-")
	if len(s) > 40 {
		s = s[:40]
	}
	if s == "" {
		return "repo"
	}
	return s
}

// PerTaskDirs implements PerTaskWorkspaces.
func (w *Worktrees) PerTaskDirs() bool { return !w.off }

// Prepare implements Workspaces: a new worktree on a new task branch at the
// route's HEAD, and the route's place inside it.
//
// A failure holds the route, not the event: what stops a worktree (a route
// that is not a repository, one with no commit, a full disk) stops every
// event on it. The route waits PrepareBackoff, doubling up to
// PrepareBackoffMax, and RoutesWaiting tells the dispatcher to leave its
// records out, so they neither fill the disk and the ledger nor the window
// other routes' records are started from.
func (w *Worktrees) Prepare(ctx context.Context, route string, originatingEventID int64) (string, error) {
	if w.off {
		return route, nil
	}
	w.mu.Lock()
	failure, failed := w.failures[route]
	w.mu.Unlock()
	if failed && w.now().Before(failure.until) {
		return "", fmt.Errorf("connector: event %d on %s: %w", originatingEventID, route, ErrPrepareBackoff)
	}
	workDir, err := w.prepare(ctx, route, originatingEventID)
	w.mu.Lock()
	defer w.mu.Unlock()
	if err != nil {
		failure.count++
		delay := PrepareBackoff << min(failure.count-1, 10)
		failure.until = w.now().Add(min(delay, PrepareBackoffMax))
		w.failures[route] = failure
		return "", err
	}
	delete(w.failures, route)
	return workDir, nil
}

// RoutesWaiting implements WaitingWorkspaces: the routes still in a Prepare
// backoff.
func (w *Worktrees) RoutesWaiting() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	now := w.now()
	var out []string
	for route, f := range w.failures {
		if now.Before(f.until) {
			out = append(out, route)
		}
	}
	return out
}

func (w *Worktrees) prepare(ctx context.Context, route string, originatingEventID int64) (string, error) {
	if !filepath.IsAbs(route) {
		return "", fmt.Errorf("connector: route %q is not absolute", route)
	}
	top, err := w.gitOut(ctx, route, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("connector: route %s is not in a git repository: %w", route, err)
	}
	repository := filepath.Clean(top)
	rel, err := filepath.Rel(realPath(repository), realPath(route))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("connector: route %s is not inside its repository", route)
	}
	base, err := w.gitOut(ctx, repository, "rev-parse", "--verify", "--end-of-options", "HEAD^{commit}")
	if err != nil {
		return "", fmt.Errorf("connector: route %s has no commit to branch from: %w", route, err)
	}
	suffix := make([]byte, 3)
	if _, err := rand.Read(suffix); err != nil {
		return "", err
	}
	name := strconv.FormatInt(originatingEventID, 10) + "-" + hex.EncodeToString(suffix)
	path := w.path(w.root, repository, name)
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("connector: worktree path %q is not absolute", path)
	}
	workDir := filepath.Join(path, rel)
	record := Worktree{
		Path: path, WorkDir: workDir, Route: route, Repository: repository,
		Branch: BranchPrefix + name, BaseCommit: base, OriginatingEventID: originatingEventID,
		State: WorktreeCreating,
	}
	id, err := w.ledger.BeginWorktree(ctx, record)
	if err != nil {
		return "", err
	}
	record.ID = id

	err = w.add(ctx, &record)
	if err == nil {
		record.AdminDir, err = w.gitOut(ctx, record.Path, "rev-parse", "--absolute-git-dir")
	}
	if err == nil {
		err = w.ledger.WorktreeAdminDir(ctx, id, record.AdminDir)
	}
	if err == nil {
		err = w.ledger.MoveWorktree(ctx, id, WorktreeLive, WorktreeCreating)
	}
	if err != nil {
		// Whatever git left is judged like any finished worktree; a lock not
		// had leaves the row for the next start.
		settleCtx := context.WithoutCancel(ctx)
		if unlock, lockErr := w.lock(settleCtx); lockErr == nil {
			w.discardUnpopulated(settleCtx, record)
			w.settle(settleCtx, record, RemovedByConnector)
			unlock()
		}
		return "", fmt.Errorf("connector: create a worktree for event %d: %w", originatingEventID, err)
	}
	return workDir, nil
}

func (w *Worktrees) add(ctx context.Context, r *Worktree) error {
	if err := os.MkdirAll(w.root, 0o700); err != nil {
		return err
	}
	if err := setup.EnsurePrivateDir(filepath.Dir(r.Path)); err != nil {
		return err
	}
	// The branch is created before the worktree and only if it does not
	// exist, so the row's branch is this task's and deleting it later can
	// never delete a branch someone else made (invariant 1).
	if _, err := w.gitOut(ctx, r.Repository, "update-ref", "--end-of-options", "refs/heads/"+r.Branch, r.BaseCommit, ""); err != nil {
		return err
	}
	if err := w.ledger.WorktreeBranchCreated(ctx, r.ID); err != nil {
		return err
	}
	// The caller settles this record if anything below fails, and only a
	// record that says the branch is ours lets it be deleted again.
	r.BranchCreated = true
	// The checkout runs in the new worktree, so the filters blanked are the
	// ones its own configuration defines (an include on its branch among
	// them), not the checkout's the route is in.
	if _, err := w.gitOut(ctx, r.Repository, "worktree", "add", "--no-checkout", "--end-of-options", r.Path, r.Branch); err != nil {
		return err
	}
	_, err := w.gitOut(ctx, r.Path, "reset", "--quiet", "--hard", "--end-of-options", r.BaseCommit)
	return err
}

// Finish implements Workspaces: the worktree a task worked in is removed if
// nothing in it could be lost, and retained otherwise. A directory that is not
// one of this connector's worktrees is left alone.
func (w *Worktrees) Finish(ctx context.Context, _ string, workDir string) error {
	record, ok, err := w.ledger.WorktreeByWorkDir(ctx, workDir)
	if err != nil || !ok {
		return err
	}
	if record.State != WorktreeCreating && record.State != WorktreeLive {
		return nil
	}
	unlock, err := w.lock(ctx)
	if err != nil {
		// Kept, and reconciled on the next start.
		return err
	}
	defer unlock()
	if after := w.settle(ctx, record, RemovedByConnector); after.State == WorktreeRemoving {
		return fmt.Errorf("connector: worktree %s was removed but not recorded; the next start records it", record.Path)
	}
	return nil
}

// Recover implements RecoveringWorkspaces: every worktree a crash left
// creating, live or removing with no live task in it is settled under the
// same rules as a finished task's. It runs in the connector that holds the
// instance lock, before anything is dispatched.
func (w *Worktrees) Recover(ctx context.Context) error {
	unlock, err := w.lock(ctx)
	if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
		// A prune holding the lock does not keep the connector from starting:
		// what Recover would settle is no task's, nothing is dispatched into
		// it, and the next start settles it.
		w.log.Warn("connector: worktrees are locked by another process; recovery left for the next start")
		return nil
	}
	if err != nil {
		return err
	}
	defer unlock()
	records, err := w.ledger.UnfinishedWorktrees(ctx)
	if err != nil {
		return err
	}
	for _, r := range records {
		w.settle(ctx, r, RemovedByConnector)
	}
	return nil
}

// Retained lists the worktrees kept for the operator.
func (w *Worktrees) Retained(ctx context.Context) ([]Worktree, error) {
	return w.ledger.RetainedWorktrees(ctx)
}

// PruneAction is what prune did with one retained worktree.
type PruneAction string

const (
	PruneRemoved PruneAction = "removed"
	PruneForced  PruneAction = "forced"
	PruneMissing PruneAction = "missing"
	PruneKept    PruneAction = "kept"
)

// PruneResult is one retained worktree after prune.
type PruneResult struct {
	Worktree Worktree
	Action   PruneAction
	// Reason is why a kept worktree was kept.
	Reason RetainedReason
	// BranchKept is a forced removal's branch, kept because its commits are
	// held nowhere else.
	BranchKept bool
	// ForceRefused is a --force that could not go through: the worktree's
	// state could not be established well enough to remove it safely.
	ForceRefused bool
	// HeadBranch is a branch a forced removal made for a detached HEAD whose
	// commit nothing else held.
	HeadBranch string
}

// ErrNotRetained is a --force naming a path that is no retained worktree.
var ErrNotRetained = errors.New("not a retained worktree")

// Prune removes the retained worktrees the operator has dealt with: those now
// clean with every commit held elsewhere, and those whose directory is gone.
// A worktree still holding work is kept unless its path is in force, and a
// path in force that is no retained worktree refuses the whole prune before
// anything is removed.
func (w *Worktrees) Prune(ctx context.Context, force []string) ([]PruneResult, error) {
	unlock, err := w.lock(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()
	// A removal a crash interrupted holds the lock no longer: it is retained
	// work until judged again.
	records, err := w.ledger.Worktrees(ctx, WorktreeRetained, WorktreeRemoving)
	if err != nil {
		return nil, err
	}
	forced := map[string]bool{}
	for _, p := range force {
		clean := filepath.Clean(p)
		if !slices.ContainsFunc(records, func(r Worktree) bool { return r.Path == clean }) {
			return nil, fmt.Errorf("connector: %s: %w", p, ErrNotRetained)
		}
		forced[clean] = true
	}
	var out []PruneResult
	for _, r := range records {
		if r.State == WorktreeRemoving {
			// Only a remover holding this lock writes removing, and none does.
			if err := w.ledger.RetainWorktree(ctx, r.ID, RetainedUnverified, WorktreeRemoving); err != nil {
				return out, err
			}
			r.State, r.RetainedReason = WorktreeRetained, RetainedUnverified
		}
		out = append(out, w.pruneOne(ctx, r, forced[r.Path]))
	}
	return out, nil
}

func (w *Worktrees) pruneOne(ctx context.Context, r Worktree, force bool) PruneResult {
	var result PruneResult
	after := w.settle(ctx, r, RemovedByPrune)
	result.Worktree = after
	switch {
	case after.State == WorktreeRemoved && after.RemovedBy == RemovedMissing:
		result.Action = PruneMissing
	case after.State == WorktreeRemoved, after.State == WorktreeRemoving && !exists(after.Path):
		// Removing and gone is removed that the ledger could not record yet.
		result.Action = PruneRemoved
	case force && after.RetainedReason == RetainedMoved:
		// There is nothing here to force: the directory is somewhere else.
		result.Action, result.Reason = PruneKept, after.RetainedReason
	case force && after.RetainedReason != RetainedLocked:
		result = w.forceRemove(ctx, after)
	default:
		result.Action, result.Reason = PruneKept, after.RetainedReason
	}
	return result
}

// forceRemove removes a retained worktree the operator named, keeping its
// branch unless its commits are held elsewhere.
func (w *Worktrees) forceRemove(ctx context.Context, r Worktree) PruneResult {
	kept := PruneResult{Worktree: r, Action: PruneKept, Reason: r.RetainedReason, ForceRefused: true}
	// A submodule's commits live in git directories a forced removal deletes
	// and no anchor here covers: a worktree with any is not forced.
	if held, err := w.submoduleContent(ctx, r); err != nil || held {
		w.log.Warn("connector: forced worktree removal refused: it holds submodule content; kept", "path", r.Path)
		return kept
	}
	headBranch, err := w.anchorHead(ctx, r)
	if err != nil {
		// A HEAD that cannot be read or kept is not forced away.
		w.log.Warn("connector: forced worktree removal refused; kept", "path", r.Path, "error", err)
		return kept
	}
	if err := w.ledger.MoveWorktree(ctx, r.ID, WorktreeRemoving, WorktreeRetained); err != nil {
		return kept
	}
	if _, err := w.gitOut(ctx, r.Repository, "worktree", "remove", "--force", "--end-of-options", r.Path); err != nil {
		w.log.Warn("connector: forced worktree removal failed; kept", "path", r.Path, "error", err)
		_ = w.ledger.RetainWorktree(ctx, r.ID, RetainedUnverified, WorktreeRemoving)
		kept.Reason = RetainedUnverified
		return kept
	}
	branchKept := !w.deleteBranchIfHeld(ctx, r)
	if branchKept && headBranch != "" {
		// The task branch kept the commit anyway: the anchor is redundant.
		if tip, err := w.branchTip(ctx, r); err == nil && tip != "" {
			if anchor, err := w.gitOut(ctx, r.Repository, "rev-parse", "--verify", "--end-of-options", "refs/heads/"+headBranch); err == nil && anchor == tip {
				if _, err := w.gitOut(ctx, r.Repository, "update-ref", "-d", "refs/heads/"+headBranch, anchor); err == nil {
					headBranch = ""
				}
			}
		}
	}
	if err := w.ledger.RemovedWorktree(ctx, r.ID, RemovedByPruneForced, WorktreeRemoving); err != nil {
		return kept
	}
	r.State, r.RemovedBy = WorktreeRemoved, RemovedByPruneForced
	return PruneResult{Worktree: r, Action: PruneForced, BranchKept: branchKept, HeadBranch: headBranch}
}

// discardUnpopulated removes a worktree whose checkout never happened: its
// directory holds nothing but git's .git file, so there is nothing in it to
// lose, and settling it as it is would keep an empty checkout as dirty (every
// file a staged deletion) at each retry.
func (w *Worktrees) discardUnpopulated(ctx context.Context, r Worktree) {
	entries, err := os.ReadDir(r.Path)
	if err != nil || len(entries) != 1 || entries[0].Name() != ".git" || entries[0].IsDir() {
		return
	}
	if _, err := w.gitOut(ctx, r.Repository, "worktree", "remove", "--force", "--end-of-options", r.Path); err != nil {
		w.log.Debug("connector: an unpopulated worktree stays for settling", "path", r.Path, "error", err)
	}
}

// submoduleContent reports whether a worktree holds anything of a submodule's
// own: a submodule directory that is not empty, or git directories under the
// worktree's modules/.
func (w *Worktrees) submoduleContent(ctx context.Context, r Worktree) (bool, error) {
	modules, err := w.gitOut(ctx, r.Path, "rev-parse", "--path-format=absolute", "--git-path", "modules")
	if err != nil {
		return false, err
	}
	switch entries, err := os.ReadDir(modules); {
	case err == nil && len(entries) > 0:
		return true, nil
	case err != nil && !errors.Is(err, os.ErrNotExist):
		return false, err
	}
	out, err := w.gitRaw(ctx, r.Path, "ls-files", "--stage", "-z")
	if err != nil {
		return false, err
	}
	for entry := range strings.SplitSeq(string(out), "\x00") {
		meta, path, ok := strings.Cut(entry, "\t")
		if !ok || !strings.HasPrefix(meta, "160000 ") {
			continue
		}
		switch entries, err := os.ReadDir(filepath.Join(r.Path, filepath.FromSlash(path))); {
		case err == nil && len(entries) > 0:
			return true, nil
		case err != nil && !errors.Is(err, os.ErrNotExist):
			return false, err
		}
	}
	return false, nil
}

// anchorHead makes sure the commit a worktree's HEAD is on survives its
// removal: a HEAD on the task branch, at a held commit, needs nothing; a
// detached HEAD whose commit nothing holds gets a branch of its own, created
// only if absent. It returns that branch, or "".
func (w *Worktrees) anchorHead(ctx context.Context, r Worktree) (string, error) {
	head, err := w.gitOut(ctx, r.Path, "rev-parse", "--verify", "--end-of-options", "HEAD^{commit}")
	if err != nil {
		return "", err
	}
	held, err := w.held(ctx, r, head)
	if err != nil || held {
		return "", err
	}
	// Anchored even when HEAD is the task branch's own tip: another process
	// can move that branch between this check and the removal. The commit is
	// in the name, so an anchor a failed force left is the anchor this one
	// wants, not a branch in the way.
	branch := r.Branch + "-head-" + head[:min(12, len(head))]
	if _, err := w.gitOut(ctx, r.Repository, "update-ref", "--end-of-options", "refs/heads/"+branch, head, ""); err != nil {
		at, atErr := w.gitOut(ctx, r.Repository, "rev-parse", "--verify", "--end-of-options", "refs/heads/"+branch)
		if atErr != nil || at != head {
			return "", err
		}
	}
	return branch, nil
}

// settle judges one worktree and removes or retains it (invariants 1 to 3).
// The caller holds the lock. It returns the row as it now stands.
func (w *Worktrees) settle(ctx context.Context, r Worktree, by RemovedBy) Worktree {
	from := []WorktreeState{r.State}
	if _, err := os.Lstat(r.Path); errors.Is(err, os.ErrNotExist) && !w.movedElsewhere(ctx, r) {
		// Nothing on disk. The repository's own record of the worktree
		// (<repo>/.git/worktrees/<name>) is left for git: it may hold a
		// submodule's git directory, a reflog, or a lock someone set for a
		// directory that is only away, and `git worktree prune` is the
		// operator's to run. A branch git made stays unless it still points at
		// the base, which holds nothing of the task's.
		w.deleteBranchAt(ctx, r, r.BaseCommit)
		gone := RemovedMissing
		if r.State == WorktreeCreating {
			gone = RemovedNeverCreated
		}
		if err := w.ledger.RemovedWorktree(ctx, r.ID, gone, from...); err != nil {
			w.log.Warn("connector: recording a worktree gone", "path", r.Path, "error", err)
			return r
		}
		r.State, r.RemovedBy = WorktreeRemoved, gone
		return r
	}

	if _, err := os.Lstat(r.Path); errors.Is(err, os.ErrNotExist) {
		// Moved out from under the connector: its files are still someone's.
		return w.retain(ctx, r, RetainedMoved, from)
	}
	reason, tip := w.inspect(ctx, r)
	if reason != "" {
		return w.retain(ctx, r, reason, from)
	}
	if err := w.ledger.MoveWorktree(ctx, r.ID, WorktreeRemoving, from...); err != nil {
		w.log.Warn("connector: claiming a worktree for removal", "path", r.Path, "error", err)
		return r
	}
	r.State = WorktreeRemoving
	// Removal runs git status inside the worktree, where the task branch's
	// own configuration applies: its filters are blanked as well.
	if _, err := w.gitIn(ctx, r.Repository, []string{r.Path}, "worktree", "remove", "--end-of-options", r.Path); err != nil {
		// Git's own refusal (a file written since the check) or a failure:
		// either way the worktree is kept.
		return w.retain(ctx, r, RetainedUnverified, []WorktreeState{WorktreeRemoving})
	}
	w.deleteBranchAt(ctx, r, tip)
	if err := w.ledger.RemovedWorktree(ctx, r.ID, by, WorktreeRemoving); err != nil {
		// The directory is gone; the row still says removing, and the next
		// settle records it missing. Nobody is told it was kept.
		w.log.Warn("connector: a worktree was removed but the ledger could not record it", "path", r.Path, "error", err)
		return r
	}
	r.State, r.RemovedBy = WorktreeRemoved, by
	return r
}

// movedElsewhere reports whether the repository still has a worktree on this
// row's branch somewhere else: someone moved it, and its files are work the
// connector neither judges nor forgets, so the row is kept.
func (w *Worktrees) movedElsewhere(ctx context.Context, r Worktree) bool {
	// The repository's record of this worktree names where it is now,
	// whatever it has checked out and whatever its branch is called.
	if r.AdminDir != "" {
		switch at, err := os.ReadFile(filepath.Join(r.AdminDir, "gitdir")); {
		case err == nil:
			// The record is the worktree's .git file, absolute or — with
			// worktree.useRelativePaths — relative to the admin directory.
			path := strings.TrimSpace(string(at))
			if !filepath.IsAbs(path) {
				path = filepath.Join(r.AdminDir, path)
			}
			path = filepath.Dir(path)
			return !samePath(path, r.Path) && exists(path)
		case !errors.Is(err, os.ErrNotExist):
			// The record cannot be read: assume it is still somewhere.
			return true
		}
		return false
	}
	// A row from before the admin directory was recorded: its branch is the
	// only handle left.
	out, err := w.gitRaw(ctx, r.Repository, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return true
	}
	var current string
	for field := range strings.SplitSeq(string(out), "\x00") {
		switch {
		case strings.HasPrefix(field, "worktree "):
			current = strings.TrimPrefix(field, "worktree ")
		case field == "branch refs/heads/"+r.Branch:
			if !samePath(current, r.Path) && exists(current) {
				return true
			}
		}
	}
	return false
}

// exists reports whether a path is anything but proven absent: a path that
// cannot be read counts as there, because an error is not evidence that work
// is gone.
func exists(path string) bool {
	_, err := os.Lstat(path)
	return !errors.Is(err, os.ErrNotExist)
}

func (w *Worktrees) retain(ctx context.Context, r Worktree, reason RetainedReason, from []WorktreeState) Worktree {
	if err := w.ledger.RetainWorktree(ctx, r.ID, reason, from...); err != nil {
		w.log.Warn("connector: recording a worktree retained", "path", r.Path, "error", err)
		return r
	}
	w.log.Info("connector: worktree retained", "path", r.Path, "branch", r.Branch, "reason", string(reason))
	r.State, r.RetainedReason = WorktreeRetained, reason
	return r
}

// inspect decides whether a worktree holds anything that could be lost. It
// returns the reason to keep it, or "" and the task branch's verified tip
// ("" when the branch is gone).
func (w *Worktrees) inspect(ctx context.Context, r Worktree) (RetainedReason, string) {
	top, err := w.gitOut(ctx, r.Path, "rev-parse", "--show-toplevel")
	if err != nil || !samePath(top, r.Path) {
		// Not a worktree of its own any more (a stray directory, a broken
		// link to the repository): nothing here can be judged.
		return RetainedUnverified, ""
	}
	locked, err := w.locked(ctx, r)
	switch {
	case err != nil:
		return RetainedUnverified, ""
	case locked:
		return RetainedLocked, ""
	}
	for _, marker := range []string{"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "BISECT_LOG", "rebase-merge", "rebase-apply", "sequencer"} {
		p, err := w.gitOut(ctx, r.Path, "rev-parse", "--path-format=absolute", "--git-path", marker)
		if err != nil {
			return RetainedUnverified, ""
		}
		if _, err := os.Lstat(p); err == nil {
			return RetainedDirty, ""
		} else if !errors.Is(err, os.ErrNotExist) {
			return RetainedUnverified, ""
		}
	}
	// Everything on disk first. Git does not report every file it would
	// delete with the worktree (a file inside a submodule's never-initialized
	// directory, for one), so the rule is on the disk itself: whatever is not
	// a file git tracks is work. It comes before any git command that could
	// recurse: a submodule directory holding anything at all — a git directory
	// and configuration a worker planted among it — is work, and git is never
	// asked to look inside it.
	switch untracked, err := w.untrackedOnDisk(ctx, r); {
	case err != nil:
		return RetainedUnverified, ""
	case untracked:
		return RetainedDirty, ""
	}
	// What git tracks, and what differs from it. Every submodule directory is
	// empty by now, so there is nothing to recurse into, and git is told not
	// to.
	status, err := w.gitRaw(ctx, r.Path, "status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignored=traditional", "--ignore-submodules=all")
	if err != nil {
		return RetainedUnverified, ""
	}
	if len(status) > 0 {
		return RetainedDirty, ""
	}
	// An index entry marked skip-worktree or assume-unchanged hides its edits
	// from status.
	entries, err := w.gitRaw(ctx, r.Path, "ls-files", "-v", "-z")
	if err != nil {
		return RetainedUnverified, ""
	}
	for entry := range strings.SplitSeq(string(entries), "\x00") {
		if entry == "" {
			continue
		}
		if tag := entry[0]; tag == 'S' || (tag >= 'a' && tag <= 'z') {
			return RetainedDirty, ""
		}
	}

	head, err := w.gitOut(ctx, r.Path, "rev-parse", "--verify", "--end-of-options", "HEAD^{commit}")
	if err != nil {
		return RetainedUnverified, ""
	}
	tip, err := w.branchTip(ctx, r)
	if err != nil {
		return RetainedUnverified, ""
	}
	// Every commit the worktree or its branch reaches, and that its removal
	// would forget: HEAD, the branch, what their reflogs remember (a commit
	// the worker made and then moved away from), and per-worktree refs.
	tips := []string{head}
	if tip != "" {
		tips = append(tips, tip)
	}
	lists := [][]string{
		{r.Path, "reflog", "show", "--format=%H", "HEAD", "--"},
		{r.Path, "for-each-ref", "--format=%(objectname)", "refs/worktree/"},
	}
	if tip != "" {
		lists = append(lists, []string{r.Repository, "reflog", "show", "--format=%H", "refs/heads/" + r.Branch, "--"})
	}
	for _, list := range lists {
		out, err := w.gitOut(ctx, list[0], list[1:]...)
		if err != nil {
			return RetainedUnverified, ""
		}
		tips = append(tips, strings.Fields(out)...)
	}
	slices.Sort(tips)
	tips = slices.Compact(tips)
	for _, commit := range tips {
		held, err := w.held(ctx, r, commit)
		if err != nil {
			return RetainedUnverified, ""
		}
		if !held {
			return RetainedUnpushed, ""
		}
	}
	return "", tip
}

// untrackedOnDisk reports whether the worktree holds anything on disk that is
// not a file git tracks: an untracked or ignored file, a directory git has no
// file in, or anything inside a submodule's directory, which the checkout
// left empty. Symlinks are not followed.
func (w *Worktrees) untrackedOnDisk(ctx context.Context, r Worktree) (bool, error) {
	out, err := w.gitRaw(ctx, r.Path, "ls-files", "--stage", "-z")
	if err != nil {
		return false, err
	}
	files, gitlinks, dirs := map[string]bool{}, map[string]bool{}, map[string]bool{".": true}
	for entry := range strings.SplitSeq(string(out), "\x00") {
		meta, path, ok := strings.Cut(entry, "\t")
		if !ok {
			continue
		}
		if strings.HasPrefix(meta, "160000 ") {
			gitlinks[path] = true
		} else {
			files[path] = true
		}
		for dir := filepath.Dir(filepath.FromSlash(path)); dir != "."; dir = filepath.Dir(dir) {
			dirs[filepath.ToSlash(dir)] = true
		}
	}
	found := errors.New("untracked")
	// Bounded: a tree too big to read in time is not proven clean, and a task
	// that left one must not hold the connector's shutdown.
	deadline := time.Now().Add(WalkLimit)
	err = filepath.WalkDir(r.Path, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Now().After(deadline) {
			return errors.New("connector: the worktree could not be read in time")
		}
		rel, err := filepath.Rel(r.Path, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		switch {
		case rel == ".git" && !d.IsDir():
			// The worktree's link to its repository.
			return nil
		case gitlinks[rel]:
			if !d.IsDir() {
				return found
			}
			entries, err := os.ReadDir(path)
			if err != nil {
				return err
			}
			if len(entries) > 0 {
				return found
			}
			return filepath.SkipDir
		case d.IsDir():
			if !dirs[rel] {
				return found
			}
			return nil
		case !files[rel]:
			return found
		}
		return nil
	})
	if errors.Is(err, found) {
		return true, nil
	}
	return false, err
}

// held reports whether a commit is safe to lose from this worktree: it is the
// base the worktree was made from, or a remote branch or a local branch that
// is not a task branch contains it.
func (w *Worktrees) held(ctx context.Context, r Worktree, commit string) (bool, error) {
	if commit == r.BaseCommit {
		return true, nil
	}
	refs, err := w.gitOut(ctx, r.Repository, "for-each-ref", "--format=%(refname)", "--contains", commit, "refs/remotes", "refs/heads")
	if err != nil {
		return false, err
	}
	for ref := range strings.SplitSeq(refs, "\n") {
		switch {
		case ref == "", strings.HasPrefix(ref, "refs/heads/"+BranchPrefix):
		case strings.HasPrefix(ref, "refs/remotes/"), strings.HasPrefix(ref, "refs/heads/"):
			return true, nil
		}
	}
	return false, nil
}

func (w *Worktrees) branchTip(ctx context.Context, r Worktree) (string, error) {
	out, err := w.gitRaw(ctx, r.Repository, "for-each-ref", "--format=%(objectname)", "refs/heads/"+r.Branch)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func (w *Worktrees) locked(ctx context.Context, r Worktree) (bool, error) {
	out, err := w.gitRaw(ctx, r.Repository, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return false, err
	}
	var current string
	for field := range strings.SplitSeq(string(out), "\x00") {
		switch {
		case strings.HasPrefix(field, "worktree "):
			current = strings.TrimPrefix(field, "worktree ")
		case field == "locked" || strings.HasPrefix(field, "locked "):
			if samePath(current, r.Path) {
				return true, nil
			}
		}
	}
	return false, nil
}

// deleteBranchAt deletes the task branch only while it still points at
// commit, which was verified held (invariant 2), and only when this row made
// it.
func (w *Worktrees) deleteBranchAt(ctx context.Context, r Worktree, commit string) {
	if commit == "" || !r.BranchCreated || !strings.HasPrefix(r.Branch, BranchPrefix) {
		return
	}
	if _, err := w.gitOut(ctx, r.Repository, "update-ref", "-d", "refs/heads/"+r.Branch, commit); err != nil {
		w.log.Debug("connector: task branch kept", "branch", r.Branch, "error", err)
	}
}

// deleteBranchIfHeld deletes a forced removal's branch only when every commit
// on it is held elsewhere. It reports whether the branch is gone.
func (w *Worktrees) deleteBranchIfHeld(ctx context.Context, r Worktree) bool {
	tip, err := w.branchTip(ctx, r)
	if err != nil {
		return false
	}
	if tip == "" {
		return true
	}
	held, err := w.held(ctx, r, tip)
	if err != nil || !held {
		return false
	}
	w.deleteBranchAt(ctx, r, tip)
	tip, err = w.branchTip(ctx, r)
	return err == nil && tip == ""
}

// WalkLimit bounds how long reading a worktree's files may take before it is
// kept as unverified.
const WalkLimit = 2 * time.Minute

// LockWait bounds how long a settling worktree waits for another remover's
// lock. Longer than a removal takes, short enough that a stuck prune cannot
// hold a task's end, and so the connector's shutdown, open: the row is
// reconciled on the next start instead.
const LockWait = 2 * time.Minute

// lock takes the worktrees lock (invariant 4), waiting up to LockWait for
// another holder.
func (w *Worktrees) lock(ctx context.Context) (func(), error) {
	ctx, cancel := context.WithTimeout(ctx, LockWait)
	defer cancel()
	if err := os.MkdirAll(w.root, 0o700); err != nil {
		return nil, err
	}
	if err := setup.EnsurePrivateDir(w.root); err != nil {
		return nil, err
	}
	path := filepath.Join(w.root, ".lock")
	for {
		unlock, err := setup.TryLockPrivate(path)
		switch {
		case err == nil:
			return func() { _ = unlock() }, nil
		case !errors.Is(err, setup.ErrLockHeld):
			return nil, fmt.Errorf("connector: worktrees lock: %w", err)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func (w *Worktrees) gitOut(ctx context.Context, dir string, args ...string) (string, error) {
	out, err := w.gitRaw(ctx, dir, args...)
	return strings.TrimSpace(string(out)), err
}

// gitRaw runs git in dir with hooks, the fsmonitor and every configured
// content filter disabled, and a fixed environment (invariant 6).
func (w *Worktrees) gitRaw(ctx context.Context, dir string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	guard, err := w.filterOverrides(ctx, dir)
	if err != nil {
		return nil, err
	}
	return w.run(ctx, guard, append([]string{"-C", dir}, args...), args[0])
}

// gitIn runs git in dir with the filters of dir and of every one of also
// blanked: for a command that reads another worktree's files.
func (w *Worktrees) gitIn(ctx context.Context, dir string, also []string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	guard, err := w.filterOverrides(ctx, dir)
	if err != nil {
		return "", err
	}
	for _, other := range also {
		more, err := w.filterOverrides(ctx, other)
		if err != nil {
			return "", err
		}
		guard = append(guard, more[len(safeGit):]...)
	}
	out, err := w.run(ctx, guard, append([]string{"-C", dir}, args...), args[0])
	return strings.TrimSpace(string(out)), err
}

// safeGit is the configuration every git call runs with.
var safeGit = [][2]string{{"core.hooksPath", "/dev/null"}, {"core.fsmonitor", "false"}}

// filterOverrides blanks every content filter git's configuration defines
// for dir. A checkout runs a path's smudge, clean or process filter, which is
// a command from configuration a worker in the checkout could have edited;
// an empty command is no filter. Reading the configuration runs nothing.
//
// The overrides travel as GIT_CONFIG_KEY_n/GIT_CONFIG_VALUE_n, not `-c`,
// which splits at the first "=" and would miss a driver whose name has one.
func (w *Worktrees) filterOverrides(ctx context.Context, dir string) ([][2]string, error) {
	out, err := w.run(ctx, safeGit, []string{"-C", dir, "config", "--name-only", "--get-regexp", `^filter\.`}, "config")
	var exitErr *exec.ExitError
	if err != nil && (!errors.As(err, &exitErr) || exitErr.ExitCode() != 1) {
		// Exit 1 is "no such keys"; anything else leaves filters unknown.
		return nil, err
	}
	guard := slices.Clone(safeGit)
	seen := map[string]bool{}
	for key := range strings.SplitSeq(string(out), "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSpace(key), "filter.")
		if !ok {
			continue
		}
		i := strings.LastIndexByte(rest, '.')
		if i <= 0 || seen[rest[:i]] {
			continue
		}
		name := rest[:i]
		seen[name] = true
		for _, cmd := range []string{"clean", "smudge", "process"} {
			guard = append(guard, [2]string{"filter." + name + "." + cmd, ""})
		}
		// A blanked filter that is also required makes git die mid-checkout
		// (git lfs install sets required for its own).
		guard = append(guard, [2]string{"filter." + name + ".required", "false"})
	}
	return guard, nil
}

func (w *Worktrees) run(ctx context.Context, config [][2]string, args []string, what string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, w.git, args...) //nolint:gosec // G204: git with the connector's own arguments
	env := slices.Clone(w.env)
	env = append(env, "GIT_CONFIG_COUNT="+strconv.Itoa(len(config)))
	for i, kv := range config {
		env = append(env, "GIT_CONFIG_KEY_"+strconv.Itoa(i)+"="+kv[0], "GIT_CONFIG_VALUE_"+strconv.Itoa(i)+"="+kv[1])
	}
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return stdout.Bytes(), fmt.Errorf("git %s: %w: %s", what, err, driver.Redact(msg))
	}
	return stdout.Bytes(), nil
}

func realPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

func samePath(a, b string) bool {
	return a != "" && b != "" && realPath(a) == realPath(b)
}
