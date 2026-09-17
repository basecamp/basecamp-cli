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
// side. When the task ends its worktree is kept, recorded in the ledger and
// listed by `basecamp connect status` and `basecamp connect worktrees list`,
// until an operator discards it with `basecamp connect worktrees prune`.
//
// # One worktree, one removal
//
// WHEN. Only an operator's `worktrees prune` removes a worktree. The
// connector never removes one of its own accord: a task's end (Finish, which
// the dispatcher calls at its release point, after the task has ended and
// ConfirmGroupGone has confirmed the worker's process group gone) and a
// start's recovery (Recover, before anything is dispatched) only ever keep
// it, whatever is in it. Prune touches only worktrees no live task holds,
// holds the worktrees lock, and goes through removeWorktree. Nothing else in
// the connector deletes a worktree's directory or git's record of it
// (<repo>/.git/worktrees/<name>), and nothing runs `git worktree remove`.
//
// A worktree whose directory something outside the connector removed is a
// case of its own: what is left — git's record and the task branch — reaches
// whatever it reaches, and the connector neither judges that nor deletes any
// of it. The row is kept, said to be orphaned, and listed with the record, so
// an operator sees it; naming its path in a force is what deletes the branch,
// and git's own `worktree prune` is what clears the record. Nothing about
// reachability is decided on that path at all.
//
// WHAT is work. Anything on the disk that is not a tracked file, unchanged:
// a modified, staged, untracked or ignored file, a directory git has no file
// in, anything inside a submodule's directory, an index entry that hides an
// edit. An operation in progress (merge, rebase, cherry-pick, revert,
// bisect). A lock someone set. A submodule's git data. And every commit the
// worktree reaches — HEAD, the task branch, their reflogs, per-worktree refs
// (refs/worktree, refs/bisect, refs/rewritten) — that no ref the connector
// keeps holds, a kept ref being a remote branch, a local branch that is not a
// task's, or a ref a forced removal of this worktree kept it under. The commit
// the worktree was made from is one of those commits: the route's branch
// usually holds it, and a route reset since is not evidence that it does. A
// stash
// is in refs/stash, which belongs to the repository and is never touched.
//
// WHAT happens to work. The connector never discards it. A prune without an
// operator's force keeps the worktree, with its reason, and lists it. With
// the force, every commit the worktree reaches that nothing holds is first
// kept under refs/basecamp-connect/retained/<name>/<commit>;
// a worktree whose work cannot be kept that way (submodule git data, a HEAD
// that cannot be read) is not removed.
//
// HOW the check holds until the removal. removeWorktree freezes the worktree
// before it judges anything: it locks git's record of it (as `git worktree
// lock` does, so git's own prune leaves the frozen record alone), renames the
// record and then the directory aside, each an atomic rename. From then on no git command can
// move its HEAD or commit in it (its .git file names a record that is not
// there), and nothing that reaches it by path can write to it. The evidence
// is judged on the frozen copy, and the frozen copy is what is deleted — or
// both names are restored and the worktree retained. What the judgment leans
// on outside the frozen copy — the refs that hold the commits it reaches — is
// verified again where it was found, in the one ref transaction that ends the
// task branch, immediately before the frozen copy is deleted: a fetch, a
// reset or a deleted branch since makes git refuse the transaction, and the
// worktree is kept instead. A crash while frozen leaves a removing row, and
// the next start restores the names and judges again. The one writer outside the rule is a process that escaped the
// task's process group and holds a descriptor inside the directory.
//
// WHO forces. Only an operator, naming the worktree's path in `basecamp
// connect worktrees prune --force <path>`. A force is a decision about work,
// not a judgment of it: it is the one thing that goes ahead where the rule
// above would keep a worktree, and what it can find is kept under refs first.
//
// # Invariants
//
// Each is held by a test in worktrees_test.go.
//
//  1. The rule above, the first half of it being that a task's end and a
//     start's recovery keep every worktree they find.
//  2. The ledger first. A worktree is recorded creating before `git worktree
//     add` runs, and removing before it is frozen, so a crash at any point
//     leaves a row that says where a directory may be.
//  3. Nothing the repository, its configuration or a worker's files name
//     runs: git never looks inside a submodule's directory (the disk is judged
//     before git is asked anything that could recurse, and status ignores
//     submodules), and every git call runs with hooks, the fsmonitor,
//     signature verification and every content filter its configuration
//     defines disabled, with a fixed environment.
//  4. A task branch is deleted only if this connector created it, and only in
//     one ref transaction that deletes it at the commit judged held and
//     verifies that every ref holding a commit the worktree reaches is still
//     where the judgment found it.
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
	// red takes the connector's own paths and environment out of anything git
	// says (the shared rule in driver/redact.go).
	red *driver.Redactor
	// walkLimit is WalkLimit; a test seam.
	walkLimit time.Duration
	// whileFrozen runs once a removal has frozen a worktree, before it is
	// judged; a test seam. An error leaves it frozen, as a crash would.
	whileFrozen func(dir string) error
	now         func() time.Time

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
	// Redaction is what is taken out of anything git says: the driver
	// package's shared rule.
	Redaction driver.Redaction
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
		now: time.Now, off: opts.Off, walkLimit: WalkLimit,
		red: driver.NewRedactor(opts.Redaction.With(driver.Redaction{Env: env, Dirs: []string{opts.Root}})), failures: map[string]prepareFailure{},
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
			w.settle(settleCtx, record)
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

// Finish implements Workspaces: the worktree a task worked in is kept,
// whatever is in it, and recorded as kept so `worktrees list` shows it and a
// prune can judge it. Nothing here removes anything. A directory that is not
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
		// The worktree is kept either way, but it is said to be kept: a row
		// left live is a directory `worktrees list` does not show and no
		// prune touches until the next start settles it.
		return errors.Join(err, w.keepUnjudged(ctx, record))
	}
	defer unlock()
	if after := w.settle(ctx, record); after.State != WorktreeRetained && after.State != WorktreeRemoved {
		// The worktree is where it was; the ledger could not say so, and the
		// row is not one `worktrees list` shows or a prune touches. The next
		// start settles it.
		return fmt.Errorf("connector: worktree %s is kept, but the ledger could not record it; the next start does", record.Path)
	}
	return nil
}

// keepUnjudged retains a worktree the connector could not judge, so the
// operator sees it in `worktrees list` and a prune judges it later.
func (w *Worktrees) keepUnjudged(ctx context.Context, r Worktree) error {
	// Waiting for the lock is what used the caller's deadline up: the row is
	// still recorded, on a deadline of its own.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := w.ledger.RetainWorktree(ctx, r.ID, RetainedUnverified, r.State); err != nil {
		return fmt.Errorf("connector: worktree %s is kept, but the ledger could not record it; the next start does: %w", r.Path, err)
	}
	w.log.Info("connector: worktree retained", "path", r.Path, "branch", r.Branch, "reason", string(RetainedUnverified))
	return nil
}

// Recover implements RecoveringWorkspaces: every worktree a crash left
// creating, live or removing with no live task in it is kept and recorded as
// kept, as a finished task's is, after a removal the crash interrupted has
// its names restored. It removes nothing. It runs in the connector that holds
// the instance lock, before anything is dispatched.
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
	var unrecorded []string
	for _, r := range records {
		if after := w.settle(ctx, r); after.State != WorktreeRetained && after.State != WorktreeRemoved {
			unrecorded = append(unrecorded, r.Path)
		}
	}
	if len(unrecorded) > 0 {
		// Nothing was deleted — recovery deletes nothing — but the ledger
		// does not say where these worktrees are, so nothing lists them and
		// no prune touches them. Starting on that is starting blind.
		return fmt.Errorf("connector: %d worktree(s) could not be recorded: %s", len(unrecorded), strings.Join(unrecorded, ", "))
	}
	return nil
}

// Retained lists the worktrees kept for the operator: those retained, and
// those a removal left mid-flight, which hold work until a start or a prune
// judges them again.
func (w *Worktrees) Retained(ctx context.Context) ([]Worktree, error) {
	return w.ledger.Worktrees(ctx, WorktreeRetained, WorktreeRemoving)
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
	// ForceRefused is a --force that could not go through: the worktree's
	// work could not be kept by refs.
	ForceRefused bool
	// RetainedRefs are the refs a forced removal kept commits under.
	RetainedRefs []string
}

// RetainedRefPrefix names the refs a forced removal keeps commits under: the
// operator is told about each one, and nothing here deletes them.
const RetainedRefPrefix = "refs/basecamp-connect/retained/"

// RemovingRefPrefix names the refs a removal holds a worktree's commits under
// while it deletes it. They are the connector's own bookkeeping, let go when
// the removal is over, and never counted as holding a commit for anybody: one
// a crash left behind holds its commits without making the next judgment
// think someone else does.
const RemovingRefPrefix = "refs/basecamp-connect/removing/"

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
	out := make([]PruneResult, 0, len(records))
	for _, r := range records {
		out = append(out, w.pruneOne(ctx, r, forced[r.Path]))
	}
	return out, nil
}

func (w *Worktrees) pruneOne(ctx context.Context, r Worktree, force bool) PruneResult {
	var refs []string
	by := RemovedByPrune
	if force {
		by = RemovedByPruneForced
	}
	after := w.settleKeeping(ctx, r, by, force, &refs)
	result := PruneResult{Worktree: after, RetainedRefs: refs}
	gone := after.State == WorktreeRemoving && !exists(after.Path) && !exists(frozenName(after.Path))
	switch {
	case force && (after.State == WorktreeRemoved || gone):
		result.Action = PruneForced
	case after.State == WorktreeRemoved && after.RemovedBy == RemovedMissing:
		result.Action = PruneMissing
	case after.State == WorktreeRemoved || gone:
		// Removing and gone is a removal the ledger could not record yet.
		result.Action = PruneRemoved
	default:
		result.Action, result.Reason = PruneKept, after.RetainedReason
		// A moved worktree has nothing here to force; anything else kept
		// under a force is a force refused.
		result.ForceRefused = force && after.RetainedReason != RetainedMoved
	}
	return result
}

// settle is what the connector does with a worktree of its own accord, at the
// end of a task and at recovery: it keeps it. Nothing the connector does
// removes a worktree — only an operator's `worktrees prune` does — so this
// restores a removal a crash left frozen, reconciles a row whose directory is
// no longer there, and otherwise retains the row for the operator. The caller
// holds the lock. It returns the row as it now stands.
func (w *Worktrees) settle(ctx context.Context, r Worktree) Worktree {
	from := []WorktreeState{r.State}
	if restored, ok := w.unfreeze(r); !ok {
		w.log.Warn("connector: a frozen worktree could not be restored; kept", "path", r.Path)
		return w.retain(ctx, r, RetainedUnverified, from)
	} else if restored {
		w.log.Info("connector: restored a worktree a removal left frozen", "path", r.Path)
	}
	if !exists(r.Path) {
		return w.forget(ctx, r, from, nil)
	}
	return w.retain(ctx, r, RetainedFinished, from)
}

// forget reconciles a row whose worktree is not on disk: something outside
// the connector removed the directory. What is left is git's record of the
// worktree and the task branch, which reach whatever they reach. The
// connector does not judge that and does not delete any of it — that is the
// class of defect this stopped trying to get right — so the row is kept,
// said to be orphaned, and listed with its record and the refs in it. Only an
// operator's explicit discard (`worktrees prune --force <path>`) deletes the
// branch, having been told what goes.
func (w *Worktrees) forget(ctx context.Context, r Worktree, from []WorktreeState, how *removal) Worktree {
	if w.movedElsewhere(ctx, r) {
		// Moved out from under the connector: its files are someone's.
		return w.retain(ctx, r, RetainedMoved, from)
	}
	tip, err := w.branchTip(ctx, r)
	if err != nil {
		return w.retain(ctx, r, RetainedUnverified, from)
	}
	ours := tip != "" && r.BranchCreated && strings.HasPrefix(r.Branch, BranchPrefix)
	if !ours && !exists(r.AdminDir) {
		// Nothing of the connector's is left: no branch it made, no record.
		// There is nothing to decide and nothing to delete.
		return w.recordGone(ctx, r, from)
	}
	if how == nil || !how.force {
		return w.retain(ctx, r, RetainedOrphaned, from)
	}
	// The operator named this worktree: the branch it made goes, at the
	// commit it stands at, and git's record is left for `git worktree prune`.
	if ours {
		w.deleteBranch(ctx, r, tip)
	}
	return w.recordGone(ctx, r, from)
}

// recordGone records a row whose worktree is not on disk and has nothing left
// to decide.
func (w *Worktrees) recordGone(ctx context.Context, r Worktree, from []WorktreeState) Worktree {
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

func (w *Worktrees) settleKeeping(ctx context.Context, r Worktree, by RemovedBy, force bool, refs *[]string) Worktree {
	from := []WorktreeState{r.State}
	// A removal a crash interrupted: its names come back first, and it is
	// judged as it stands.
	if restored, ok := w.unfreeze(r); !ok {
		w.log.Warn("connector: a frozen worktree could not be restored; kept", "path", r.Path)
		return w.retain(ctx, r, RetainedUnverified, from)
	} else if restored {
		w.log.Info("connector: restored a worktree a removal left frozen", "path", r.Path)
	}

	if !exists(r.Path) {
		return w.forget(ctx, r, from, &removal{force: force})
	}
	return w.removeWorktree(ctx, r, by, removal{force: force}, refs)
}

// removal is how removeWorktree judges.
type removal struct {
	// force is an operator's explicit discard: unheld commits are kept under
	// refs and the worktree goes.
	force bool
}

// RemovingSuffix is what a removal adds to a worktree's name and to its
// record's while it judges them: a directory under it is a removal that is
// running, or one a crash left for the next start to restore.
const RemovingSuffix = ".removing"

// frozenName is where removeWorktree moves a name while it judges.
func frozenName(path string) string { return path + RemovingSuffix }

// removeWorktree is the one removal (the rule, in the type's doc). It claims
// the row, freezes the worktree, judges it frozen, and deletes the frozen copy
// or restores it and retains the row. The caller holds the lock and has seen
// the directory there.
func (w *Worktrees) removeWorktree(ctx context.Context, r Worktree, by RemovedBy, how removal, refs *[]string) Worktree {
	from := []WorktreeState{r.State}
	admin := r.AdminDir
	if admin == "" {
		// A row whose record's place was never stored: found, proven to be
		// this worktree's own record, and stored before anything is renamed.
		found, err := w.recordOf(ctx, r)
		if err != nil {
			return w.retain(ctx, r, RetainedUnverified, from)
		}
		if err := w.ledger.WorktreeAdminDir(ctx, r.ID, found); err != nil {
			return w.retain(ctx, r, RetainedUnverified, from)
		}
		admin, r.AdminDir = found, found
	}
	if r.State != WorktreeRemoving {
		if err := w.ledger.MoveWorktree(ctx, r.ID, WorktreeRemoving, from...); err != nil {
			w.log.Warn("connector: claiming a worktree for removal", "path", r.Path, "error", err)
			return r
		}
		r.State = WorktreeRemoving
	}
	removing := []WorktreeState{WorktreeRemoving}

	// Freeze: lock the record, rename it, then the directory. The lock is
	// git's own: a frozen record's gitdir names a directory that is not there,
	// and git's prune deletes such a record unless it is locked.
	switch taken, err := lockRecord(admin); {
	case err != nil:
		return w.retain(ctx, r, RetainedUnverified, removing)
	case !taken:
		return w.retain(ctx, r, RetainedLocked, removing)
	}
	v := view{dir: frozenName(r.Path), gitDir: frozenName(admin)}
	if err := os.Rename(admin, v.gitDir); err != nil {
		unlockRecord(admin)
		return w.retain(ctx, r, RetainedUnverified, removing)
	}
	if err := os.Rename(r.Path, v.dir); err != nil {
		if os.Rename(v.gitDir, admin) == nil {
			unlockRecord(admin)
		} else {
			w.log.Warn("connector: a worktree's record could not be restored; the next start restores it", "path", r.Path)
			return r
		}
		return w.retain(ctx, r, RetainedUnverified, removing)
	}
	if w.whileFrozen != nil {
		if err := w.whileFrozen(v.dir); err != nil {
			// A test standing in for a crash: names stay frozen.
			return r
		}
	}

	judged := w.judge(ctx, r, v, how)
	if judged.reason == "" && how.force && len(judged.unheld) > 0 {
		// A force keeps what nothing holds before anything else happens, and
		// those refs hold it from here on: the transaction below verifies
		// them with every other holder.
		kept, err := w.keepCommits(ctx, r, judged.unheld)
		if err != nil {
			judged.reason = RetainedUnverified
		} else {
			if refs != nil {
				*refs = append(*refs, kept...)
			}
			for i, ref := range kept {
				judged.holds = append(judged.holds, hold{ref: ref, oid: judged.unheld[i], commit: judged.unheld[i]})
			}
		}
	}
	if judged.reason != "" {
		if !w.restore(r, v, admin) {
			w.log.Warn("connector: a frozen worktree could not be restored; the next start restores it", "path", r.Path)
			return r
		}
		return w.retain(ctx, r, judged.reason, removing)
	}

	// Every commit the worktree reaches is held under a ref of the
	// connector's own before anything is deleted, and let go only once the
	// removal is over. Whatever else holds those commits — a remote branch a
	// fetch prunes, a branch someone deletes — may go while the removal runs:
	// it takes nothing with it. These refs hold nothing for anybody else (see
	// RemovingRefPrefix), so one left by a crash cannot pass for a holder.
	if _, err := w.anchor(ctx, r, judged.tips); err != nil {
		w.log.Warn("connector: a worktree's commits could not be held for its removal; kept", "path", r.Path, "error", err)
		if w.restore(r, v, admin) {
			return w.retain(ctx, r, RetainedUnverified, removing)
		}
		return r
	}
	// The branch goes first, in the transaction that proves the judgment
	// still stands: every ref the judgment leaned on is verified where it was
	// found, so a fetch, a reset or a branch deleted since makes git refuse
	// the whole thing and the worktree is kept instead. It is deleted only at
	// a commit judged held, so a crash between the two leaves nothing
	// unreachable, while the other order would leave a branch nothing later
	// settles.
	if !w.endBranch(ctx, r, judged) {
		// The removal does not happen: its anchors go, each only while what
		// the judgment found still holds its commit.
		w.dropAnchors(ctx, r, judged)
		if w.restore(r, v, admin) {
			return w.retain(ctx, r, RetainedUnverified, removing)
		}
		w.log.Warn("connector: a frozen worktree could not be restored; the next start restores it", "path", r.Path)
		return r
	}
	// Delete the frozen copy: the directory, then the record.
	if err := os.RemoveAll(v.dir); err != nil {
		w.log.Warn("connector: a frozen worktree could not be deleted; kept", "path", r.Path, "error", err)
		w.dropAnchors(ctx, r, judged)
		if w.restore(r, v, admin) {
			return w.retain(ctx, r, RetainedUnverified, removing)
		}
		return r
	}
	if err := os.RemoveAll(v.gitDir); err != nil {
		w.log.Warn("connector: a worktree's record could not be deleted", "path", r.Path, "error", err)
	}
	// The worktree is gone: the anchors of its held commits are let go, each
	// only while the ref the judgment found still holds its commit. One that
	// moved keeps its anchor, and the operator is told which.
	if left := w.dropAnchors(ctx, r, judged); len(left) > 0 && refs != nil {
		*refs = append(*refs, left...)
	}
	if err := w.ledger.RemovedWorktree(ctx, r.ID, by, removing...); err != nil {
		// The worktree is gone; the row still says removing, and the next
		// settle records it missing. Nobody is told it was kept.
		w.log.Warn("connector: a worktree was removed but the ledger could not record it", "path", r.Path, "error", err)
		return r
	}
	r.State, r.RemovedBy = WorktreeRemoved, by
	return r
}

// restore gives a frozen worktree its names back: the directory, then the
// record.
func (w *Worktrees) restore(r Worktree, v view, admin string) bool {
	if exists(v.dir) && os.Rename(v.dir, r.Path) != nil {
		return false
	}
	if exists(v.gitDir) && os.Rename(v.gitDir, admin) != nil {
		return false
	}
	unlockRecord(admin)
	return true
}

// recordLockReason marks a lock on git's record of a worktree as the
// connector's own, taken while a removal holds it frozen.
const recordLockReason = "basecamp-connect: removal in progress\n"

// lockRecord locks git's record of a worktree for the connector, the way
// `git worktree lock` does, if nobody holds a lock on it. taken is false for a
// lock someone else holds.
func lockRecord(admin string) (taken bool, err error) {
	f, err := os.OpenFile(filepath.Join(admin, "locked"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return ownRecordLock(filepath.Join(admin, "locked")), nil
	}
	if err != nil {
		return false, err
	}
	if _, err := f.WriteString(recordLockReason); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return false, err
	}
	return true, f.Close()
}

// ownRecordLock reports whether a record's lock file is the connector's.
func ownRecordLock(path string) bool {
	content, err := os.ReadFile(path)
	return err == nil && string(content) == recordLockReason
}

// unlockRecord removes the connector's own lock on a record, never another's.
func unlockRecord(admin string) {
	path := filepath.Join(admin, "locked")
	if ownRecordLock(path) {
		_ = os.Remove(path)
	}
}

// recordOf finds git's record of a worktree and proves it is this worktree's:
// under the repository's common directory's worktrees/, with a gitdir that
// names this worktree's .git.
func (w *Worktrees) recordOf(ctx context.Context, r Worktree) (string, error) {
	admin, err := w.gitOut(ctx, r.Path, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return "", err
	}
	common, err := w.gitOut(ctx, r.Repository, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", err
	}
	if !samePath(filepath.Dir(admin), filepath.Join(common, "worktrees")) {
		return "", fmt.Errorf("connector: %s is not a worktree record of %s", admin, r.Repository)
	}
	at, err := os.ReadFile(filepath.Join(admin, "gitdir"))
	if err != nil {
		return "", err
	}
	named := strings.TrimSpace(string(at))
	if !filepath.IsAbs(named) {
		named = filepath.Join(admin, named)
	}
	if !samePath(filepath.Dir(named), r.Path) {
		return "", fmt.Errorf("connector: %s is another worktree's record", admin)
	}
	return admin, nil
}

// unfreeze restores the names of a worktree a crash left frozen. It reports
// whether it restored anything, and false in ok when a frozen name is there
// but cannot be put back.
func (w *Worktrees) unfreeze(r Worktree) (restored, ok bool) {
	pairs := [][2]string{{frozenName(r.Path), r.Path}}
	if r.AdminDir != "" {
		pairs = append(pairs, [2]string{frozenName(r.AdminDir), r.AdminDir})
	}
	for _, p := range pairs {
		if !exists(p[0]) {
			continue
		}
		if exists(p[1]) || os.Rename(p[0], p[1]) != nil {
			return restored, false
		}
		restored = true
	}
	if r.AdminDir != "" {
		unlockRecord(r.AdminDir)
	}
	return restored, true
}

// view is how git is pointed at a worktree: its directory, and, for a frozen
// one, git's record of it by its frozen name.
type view struct {
	dir    string
	gitDir string
}

func (v view) args(args ...string) []string {
	if v.gitDir == "" {
		return append([]string{"-C", v.dir}, args...)
	}
	return append([]string{"-C", v.dir, "--git-dir", v.gitDir, "--work-tree", v.dir}, args...)
}

// hold is a ref the connector keeps, the commit it pointed at when a judgment
// leaned on it, and the commit of the worktree it was found to hold.
type hold struct{ ref, oid, commit string }

// judgment is what judge decided about a frozen worktree: the reason to keep
// it, or "" with the task branch's tip ("" when the branch is gone), the refs
// that hold every commit the removal would forget, and, for a force, the
// commits nothing holds.
type judgment struct {
	reason RetainedReason
	tip    string
	// tips is every commit the worktree reaches that removing it would
	// forget; unheld are the ones nothing else holds, which only a force
	// reaches; holds are the refs that hold the rest.
	tips   []string
	unheld []string
	holds  []hold
}

// pseudoRefs are the record's own refs outside refs/: what a reset, a fetch or
// an operation in progress left in <repo>/.git/worktrees/<name>, and what goes
// with the record when it is deleted.
var pseudoRefs = []string{"ORIG_HEAD", "FETCH_HEAD", "MERGE_HEAD", "REBASE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "AUTO_MERGE", "BISECT_EXPECTED_REV", "MERGE_AUTOSTASH"}

// autostashFiles are where a rebase keeps the commit it stashed away: not a
// ref, a file in the record naming one, and nothing else reaches it.
var autostashFiles = []string{filepath.Join("rebase-merge", "autostash"), filepath.Join("rebase-apply", "autostash")}

// judge decides whether a frozen worktree holds anything that could be lost.
func (w *Worktrees) judge(ctx context.Context, r Worktree, v view, how removal) judgment {
	gitPath := func(name string) string { return filepath.Join(v.gitDir, name) }
	switch _, err := os.Lstat(gitPath("locked")); {
	case err == nil && !ownRecordLock(gitPath("locked")):
		return judgment{reason: RetainedLocked}
	case err == nil:
	case !errors.Is(err, os.ErrNotExist):
		return judgment{reason: RetainedUnverified}
	}
	// A submodule's git data is never lost, and never forced away: no ref
	// here can keep it.
	switch entries, err := os.ReadDir(gitPath("modules")); {
	case err == nil && len(entries) > 0:
		return judgment{reason: RetainedDirty}
	case err != nil && !errors.Is(err, os.ErrNotExist):
		return judgment{reason: RetainedUnverified}
	}
	if !how.force {
		for _, marker := range []string{"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "BISECT_LOG", "rebase-merge", "rebase-apply", "sequencer"} {
			switch _, err := os.Lstat(gitPath(marker)); {
			case err == nil:
				return judgment{reason: RetainedDirty}
			case !errors.Is(err, os.ErrNotExist):
				return judgment{reason: RetainedUnverified}
			}
		}
	}
	// The disk before any git command that could recurse: whatever is not a
	// tracked file is work, and a submodule directory holding anything — a git
	// directory and configuration a worker planted among it — is work git is
	// never asked to look inside.
	untracked, gitlinkContent, err := w.untrackedOnDisk(ctx, v)
	switch {
	case err != nil:
		return judgment{reason: RetainedUnverified}
	case gitlinkContent:
		return judgment{reason: RetainedDirty}
	case untracked && !how.force:
		return judgment{reason: RetainedDirty}
	}
	// A checkout that never happened — a crash between `worktree add
	// --no-checkout` and the checkout — holds git's .git file and nothing
	// else, with an empty index. There is nothing in it to lose, though the
	// rule below would read that index against HEAD as every file deleted.
	bare, err := w.neverCheckedOut(ctx, v)
	if err != nil {
		return judgment{reason: RetainedUnverified}
	}
	if !how.force && !bare {
		status, err := w.gitRawIn(ctx, v, "status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignored=traditional", "--ignore-submodules=all")
		if err != nil {
			return judgment{reason: RetainedUnverified}
		}
		if len(status) > 0 {
			return judgment{reason: RetainedDirty}
		}
		// An index entry marked skip-worktree or assume-unchanged hides its
		// edits from status.
		entries, err := w.gitRawIn(ctx, v, "ls-files", "-v", "-z")
		if err != nil {
			return judgment{reason: RetainedUnverified}
		}
		for entry := range strings.SplitSeq(string(entries), "\x00") {
			if entry == "" {
				continue
			}
			if tag := entry[0]; tag == 'S' || (tag >= 'a' && tag <= 'z') {
				return judgment{reason: RetainedDirty}
			}
		}
	}

	tip, err := w.branchTip(ctx, r)
	if err != nil {
		return judgment{reason: RetainedUnverified}
	}
	// Every commit the worktree or its branch reaches, and that removing it
	// would forget: HEAD, the branch, their reflogs, per-worktree refs.
	var tips []string
	head, err := w.gitRawIn(ctx, v, "rev-parse", "--verify", "--end-of-options", "HEAD^{commit}")
	if err != nil {
		return judgment{reason: RetainedUnverified}
	}
	tips = append(tips, strings.TrimSpace(string(head)))
	if tip != "" {
		tips = append(tips, tip)
		out, err := w.gitOut(ctx, r.Repository, "reflog", "show", "--format=%H", "refs/heads/"+r.Branch, "--")
		if err != nil {
			return judgment{reason: RetainedUnverified}
		}
		tips = append(tips, strings.Fields(out)...)
	}
	for _, args := range [][]string{
		{"reflog", "show", "--format=%H", "HEAD", "--"},
		{"for-each-ref", "--format=%(objectname)", "refs/worktree/", "refs/bisect/", "refs/rewritten/"},
	} {
		out, err := w.gitRawIn(ctx, v, args...)
		if err != nil {
			return judgment{reason: RetainedUnverified}
		}
		tips = append(tips, strings.Fields(string(out))...)
	}
	// Those refs' own reflogs, when the repository keeps them: git logs ref
	// updates under refs/ only with core.logAllRefUpdates=always, and a
	// per-worktree ref's log lives in the record and goes with it.
	for _, dir := range []string{"refs/worktree", "refs/bisect", "refs/rewritten"} {
		logged, err := reflogDirTips(filepath.Join(v.gitDir, "logs", filepath.FromSlash(dir)))
		if err != nil {
			return judgment{reason: RetainedUnverified}
		}
		tips = append(tips, logged...)
	}
	//
	// The record's pseudo-refs are its too, and go with it: ORIG_HEAD is what
	// a reset left behind, and the rest are an operation's.
	for _, name := range pseudoRefs {
		out, err := w.gitRawIn(ctx, v, "rev-parse", "--verify", "--quiet", "--end-of-options", name+"^{commit}")
		var exitErr *exec.ExitError
		switch {
		case err == nil:
			tips = append(tips, strings.Fields(string(out))...)
		case errors.As(err, &exitErr) && exitErr.ExitCode() == 1:
			// Not there, or not a commit.
		default:
			return judgment{reason: RetainedUnverified}
		}
	}
	stashed, err := autostashTips(v.gitDir)
	if err != nil {
		return judgment{reason: RetainedUnverified}
	}
	tips = append(tips, stashed...)
	// A reflog that is not there is not a reflog that holds nothing: with
	// core.logAllRefUpdates off, or after an expire, what the worktree
	// reached is unreadable, and what cannot be read is not judged clean —
	// unless an operator names this worktree and forces it, which is a
	// decision about work, not a judgment. Every repository keeps reflogs by
	// default; one that does not would otherwise leave rows nothing could
	// ever clear.
	if !bare && !how.force {
		if _, err := os.Lstat(filepath.Join(v.gitDir, "logs", "HEAD")); err != nil {
			return judgment{reason: RetainedUnverified}
		}
	}
	slices.Sort(tips)
	decided := judgment{tip: tip, tips: slices.Compact(tips)}
	for _, commit := range decided.tips {
		// The ref that holds it, and where that ref stands: the removal
		// verifies each one again, in the transaction that ends the branch,
		// so a holder that moved in between stops the removal.
		ref, oid, err := w.holder(ctx, r, commit)
		switch {
		case err != nil:
			return judgment{reason: RetainedUnverified}
		case ref == "":
			if !how.force {
				return judgment{reason: RetainedUnpushed}
			}
			decided.unheld = append(decided.unheld, commit)
		default:
			decided.holds = append(decided.holds, hold{ref: ref, oid: oid, commit: commit})
		}
	}
	return decided
}

// dropAnchors lets go of the refs a removal held its commits under, each only
// while the ref the judgment found still holds that commit. It returns the
// anchors that stay, because what held their commits moved.
func (w *Worktrees) dropAnchors(ctx context.Context, r Worktree, judged judgment) []string {
	var left []string
	for _, h := range judged.holds {
		anchor := anchorRef(r, h.commit)
		stdin := "start\nverify " + h.ref + " " + h.oid + "\ndelete " + anchor + " " + h.commit + "\nprepare\ncommit\n"
		if err := w.gitStdin(ctx, r.Repository, stdin, "update-ref", "--stdin"); err != nil {
			w.log.Info("connector: a commit of a removed worktree is kept under a ref: what held it moved", "ref", anchor, "path", r.Path)
			left = append(left, anchor)
		}
	}
	return left
}

// retainedRef is where a commit of this worktree is kept for the operator.
func retainedRef(r Worktree, commit string) string {
	return RetainedRefPrefix + safeName(filepath.Base(r.Path)) + "/" + commit
}

// anchorRef is where a removal holds a commit of this worktree while it runs.
func anchorRef(r Worktree, commit string) string {
	return RemovingRefPrefix + safeName(filepath.Base(r.Path)) + "/" + commit
}

// keepCommits keeps each commit under refs/basecamp-connect/retained/<name>/
// <commit>, create-only; a ref already there at that commit is the same keep.
func (w *Worktrees) keepCommits(ctx context.Context, r Worktree, commits []string) ([]string, error) {
	return w.holdUnder(ctx, r, commits, retainedRef)
}

// anchor holds each commit under RemovingRefPrefix for as long as a removal
// runs.
func (w *Worktrees) anchor(ctx context.Context, r Worktree, commits []string) ([]string, error) {
	return w.holdUnder(ctx, r, commits, anchorRef)
}

func (w *Worktrees) holdUnder(ctx context.Context, r Worktree, commits []string, where func(Worktree, string) string) ([]string, error) {
	refs := make([]string, 0, len(commits))
	for _, commit := range commits {
		ref := where(r, commit)
		if _, err := w.gitOut(ctx, r.Repository, "update-ref", "--end-of-options", ref, commit, ""); err != nil {
			at, atErr := w.gitOut(ctx, r.Repository, "rev-parse", "--verify", "--end-of-options", ref)
			if atErr != nil || at != commit {
				return nil, err
			}
		}
		refs = append(refs, ref)
	}
	return refs, nil
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

// reflogDirTips is every commit the reflogs under one directory of a record
// name. A directory that is not there is a repository that logs nothing
// there, which names nothing.
func reflogDirTips(dir string) ([]string, error) {
	var tips []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		switch {
		case errors.Is(err, os.ErrNotExist):
			return nil
		case err != nil:
			return err
		case d.IsDir():
			return nil
		}
		logged, err := reflogFileTips(path)
		if err != nil {
			return err
		}
		tips = append(tips, logged...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return tips, nil
}

// reflogFileTips is every commit a reflog file names, read as git writes it:
// one line per entry, the commit before it and the commit after it first. A
// reflog that is not there names nothing; one that cannot be read is an error,
// never an empty answer.
func reflogFileTips(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var tips []string
	for line := range strings.SplitSeq(string(data), "\n") {
		fields := strings.Fields(line)
		// The two object names an entry starts with; the rest of the line is
		// who, when and why, which name nothing.
		for _, field := range fields[:min(2, len(fields))] {
			if !isObjectName(field) {
				continue
			}
			tips = append(tips, field)
		}
	}
	return tips, nil
}

// autostashTips is every commit an operation in progress stashed away in a
// record: git writes the object name to a file, and nothing else names it.
func autostashTips(gitDir string) ([]string, error) {
	var tips []string
	for _, name := range autostashFiles {
		data, err := os.ReadFile(filepath.Join(gitDir, name))
		switch {
		case errors.Is(err, os.ErrNotExist):
			continue
		case err != nil:
			return nil, err
		}
		if oid := strings.TrimSpace(string(data)); isObjectName(oid) {
			tips = append(tips, oid)
		}
	}
	return tips, nil
}

// isGitDir reports whether a directory is a repository's git data: git's own
// test is a HEAD, an objects directory and a refs directory.
func isGitDir(path string) bool {
	for _, name := range []string{"HEAD", "objects", "refs"} {
		if _, err := os.Lstat(filepath.Join(path, name)); err != nil {
			return false
		}
	}
	return true
}

// isObjectName reports whether a field is an object name and not the zero one.
func isObjectName(field string) bool {
	return len(field) >= 40 && strings.Trim(field, "0123456789abcdef") == "" && strings.Trim(field, "0") != ""
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

// neverCheckedOut reports whether a worktree's directory holds nothing but
// git's .git file and its index is empty: `git worktree add --no-checkout`
// ran and the checkout that follows it did not.
func (w *Worktrees) neverCheckedOut(ctx context.Context, v view) (bool, error) {
	entries, err := os.ReadDir(v.dir)
	if err != nil {
		return false, err
	}
	if len(entries) != 1 || entries[0].Name() != ".git" || entries[0].IsDir() {
		return false, nil
	}
	index, err := w.gitRawIn(ctx, v, "ls-files", "--stage", "-z")
	if err != nil {
		return false, err
	}
	return len(strings.TrimSpace(string(index))) == 0, nil
}

// untrackedOnDisk reports whether a worktree's directory holds anything that
// is not a file git tracks (an untracked or ignored file, a directory git has
// no file in), and separately whether a submodule's directory, which the
// checkout left empty, holds anything at all. Symlinks are not followed.
func (w *Worktrees) untrackedOnDisk(ctx context.Context, v view) (untracked, gitlinkContent bool, err error) {
	out, err := w.gitRawIn(ctx, v, "ls-files", "--stage", "-z")
	if err != nil {
		return false, false, err
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
	// Bounded: a tree too big to read in time, or a filesystem call that never
	// returns (a mount a worker left), is not proven clean, and must not hold
	// the connector's shutdown. The walk runs apart and is abandoned at the
	// deadline; a call stuck in the kernel keeps only its own goroutine.
	deadline := time.Now().Add(w.walkLimit)
	type verdict struct {
		untracked, gitlink bool
		err                error
	}
	walked := make(chan verdict, 1)
	go func() {
		var found verdict
		found.err = filepath.WalkDir(v.dir, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if time.Now().After(deadline) {
				return errors.New("connector: the worktree could not be read in time")
			}
			rel, err := filepath.Rel(v.dir, path)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			switch {
			case rel == ".git" && !d.IsDir():
				// The worktree's link to its repository.
				return nil
			case filepath.Base(rel) == ".git":
				// Git data of a repository inside the worktree — a submodule
				// git someone initialized, a repository a worker made, or a
				// .git the worktree's own was replaced with. No ref here can
				// keep its commits, so it is never removed, forced or not.
				found.gitlink = true
				return filepath.SkipAll
			case gitlinks[rel]:
				if !d.IsDir() {
					found.gitlink = true
					return filepath.SkipAll
				}
				entries, err := os.ReadDir(path)
				if err != nil {
					return err
				}
				if len(entries) > 0 {
					found.gitlink = true
					return filepath.SkipAll
				}
				return filepath.SkipDir
			case d.IsDir():
				if !dirs[rel] {
					found.untracked = true
					// A directory git does not track that is itself a
					// repository — `git init --bare` or a clone with no
					// worktree — is git data like any other: no ref here can
					// keep its commits, so it is never removed, forced or not.
					if isGitDir(path) {
						found.gitlink = true
						return filepath.SkipAll
					}
				}
				return nil
			case !files[rel]:
				found.untracked = true
			}
			return nil
		})
		walked <- found
	}()
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case found := <-walked:
		return found.untracked, found.gitlink, found.err
	case <-timer.C:
		return false, false, errors.New("connector: the worktree could not be read in time")
	case <-ctx.Done():
		return false, false, ctx.Err()
	}
}

// holder is a ref the connector keeps that contains commit, and the commit it
// points at; "" when there is none.
func (w *Worktrees) holder(ctx context.Context, r Worktree, commit string) (string, string, error) {
	own := RetainedRefPrefix + safeName(filepath.Base(r.Path)) + "/"
	out, err := w.gitOut(ctx, r.Repository, "for-each-ref", "--format=%(refname) %(objectname)", "--contains", commit, "refs/remotes", "refs/heads", own)
	if err != nil {
		return "", "", err
	}
	for line := range strings.SplitSeq(out, "\n") {
		ref, oid, ok := strings.Cut(line, " ")
		switch {
		case !ok, strings.HasPrefix(ref, "refs/heads/"+BranchPrefix):
		case strings.HasPrefix(ref, "refs/remotes/"), strings.HasPrefix(ref, "refs/heads/"), strings.HasPrefix(ref, own):
			return ref, oid, nil
		}
	}
	return "", "", nil
}

func (w *Worktrees) branchTip(ctx context.Context, r Worktree) (string, error) {
	out, err := w.gitRaw(ctx, r.Repository, "for-each-ref", "--format=%(objectname)", "refs/heads/"+r.Branch)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// deleteBranch deletes the task branch of a worktree whose directory is gone,
// at the commit it stands at and only when this row made it. An operator
// asked for it by naming the worktree, and was told what goes; nothing here
// judges what the branch reaches.
func (w *Worktrees) deleteBranch(ctx context.Context, r Worktree, commit string) {
	if commit == "" || !r.BranchCreated || !strings.HasPrefix(r.Branch, BranchPrefix) {
		return
	}
	stdin := "start\ndelete refs/heads/" + r.Branch + " " + commit + "\nprepare\ncommit\n"
	if err := w.gitStdin(ctx, r.Repository, stdin, "update-ref", "--stdin"); err != nil {
		w.log.Warn("connector: a task branch could not be deleted", "branch", r.Branch, "error", err)
	}
}

// endBranch is the last thing a removal does before the frozen copy goes: one
// ref transaction that verifies every ref the judgment leaned on is still
// where it was found and deletes the task branch at the tip judged held. Git
// refuses the whole transaction if any of them moved, and the removal stops.
// What it proves is that the judgment still stands when the deleting starts;
// what keeps standing while the deleting runs is the anchors.
// It reports whether the judgment still stands.
func (w *Worktrees) endBranch(ctx context.Context, r Worktree, judged judgment) bool {
	stdin := "start\n"
	seen := map[string]bool{}
	for _, h := range judged.holds {
		if seen[h.ref] {
			continue
		}
		seen[h.ref] = true
		stdin += "verify " + h.ref + " " + h.oid + "\n"
	}
	deleting := judged.tip != "" && r.BranchCreated && strings.HasPrefix(r.Branch, BranchPrefix)
	if deleting {
		stdin += "delete refs/heads/" + r.Branch + " " + judged.tip + "\n"
	}
	if !deleting && len(seen) == 0 {
		// Nothing held anything and no branch of this row's making: the
		// judgment leans on nothing that could have moved.
		return true
	}
	stdin += "prepare\ncommit\n"
	if err := w.gitStdin(ctx, r.Repository, stdin, "update-ref", "--stdin"); err != nil {
		w.log.Warn("connector: a worktree is kept: what held its commits moved while it was judged", "path", r.Path, "branch", r.Branch, "error", err)
		return false
	}
	return true
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
// content filter disabled, and a fixed environment (invariant 3).
func (w *Worktrees) gitRaw(ctx context.Context, dir string, args ...string) ([]byte, error) {
	return w.gitRawIn(ctx, view{dir: dir}, args...)
}

// gitRawIn is gitRaw for a view: a frozen worktree is reached through its
// record by its frozen name.
func (w *Worktrees) gitRawIn(ctx context.Context, v view, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	guard, err := w.filterOverrides(ctx, v)
	if err != nil {
		return nil, err
	}
	return w.run(ctx, guard, v.args(args...), args[0])
}

// safeGit is the configuration every git call runs with.
var safeGit = [][2]string{
	{"core.hooksPath", "/dev/null"},
	{"core.fsmonitor", "false"},
	// A reflog or log that verifies signatures runs gpg.program.
	{"log.showSignature", "false"},
}

// filterOverrides blanks every content filter git's configuration defines
// for dir. A checkout runs a path's smudge, clean or process filter, which is
// a command from configuration a worker in the checkout could have edited;
// an empty command is no filter. Reading the configuration runs nothing.
//
// The overrides travel as GIT_CONFIG_KEY_n/GIT_CONFIG_VALUE_n, not `-c`,
// which splits at the first "=" and would miss a driver whose name has one.
func (w *Worktrees) filterOverrides(ctx context.Context, v view) ([][2]string, error) {
	out, err := w.run(ctx, safeGit, v.args("config", "--name-only", "--get-regexp", `^filter\.`), "config")
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

// gitStdin runs git in dir, guarded as gitRaw is, with input on stdin.
func (w *Worktrees) gitStdin(ctx context.Context, dir, input string, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	guard, err := w.filterOverrides(ctx, view{dir: dir})
	if err != nil {
		return err
	}
	_, err = w.runInput(ctx, guard, view{dir: dir}.args(args...), args[0], input)
	return err
}

func (w *Worktrees) run(ctx context.Context, config [][2]string, args []string, what string) ([]byte, error) {
	return w.runInput(ctx, config, args, what, "")
}

func (w *Worktrees) runInput(ctx context.Context, config [][2]string, args []string, what, input string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, w.git, args...) //nolint:gosec // G204: git with the connector's own arguments
	if input != "" {
		cmd.Stdin = strings.NewReader(input)
	}
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
		return stdout.Bytes(), fmt.Errorf("git %s: %w: %s", what, err, w.red.Sanitize(msg))
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
