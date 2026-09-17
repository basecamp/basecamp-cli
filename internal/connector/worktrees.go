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
// side. When the task ends its worktree is removed if nothing in it could be
// lost, and retained otherwise, recorded in the ledger with the reason, for
// `basecamp connect worktrees prune`.
//
// # One worktree, one removal
//
// WHEN. A worktree is removed only once no task can still write to it: from
// Finish, which the dispatcher calls at its release point, after its task has
// ended and ConfirmGroupGone has confirmed the worker's process group gone;
// from Recover, before anything is dispatched, for worktrees no live task
// holds; and from prune, which touches only retained worktrees. Every removal
// holds the worktrees lock and goes through removeWorktree. Nothing else in
// the connector deletes a worktree's directory or git's record of it
// (<repo>/.git/worktrees/<name>), and nothing runs `git worktree remove`.
//
// WHAT is work. Anything on the disk that is not a tracked file, unchanged:
// a modified, staged, untracked or ignored file, a directory git has no file
// in, anything inside a submodule's directory, an index entry that hides an
// edit. An operation in progress (merge, rebase, cherry-pick, revert,
// bisect). A lock someone set. A submodule's git data. And every commit the
// worktree reaches — HEAD, the task branch, their reflogs, per-worktree refs
// (refs/worktree, refs/bisect, refs/rewritten) — that no ref the connector
// keeps holds, a kept ref being a remote branch, a local branch that is not a
// task's, a ref a forced removal of this worktree kept it under, or the base
// it was made from. A stash
// is in refs/stash, which belongs to the repository and is never touched.
//
// WHAT happens to work. The connector never discards it. Without an
// operator's force the worktree is retained, with its reason, and listed by
// `worktrees list`. With it, every commit the worktree reaches that nothing
// holds is first kept under refs/basecamp-connect/retained/<name>/<commit>;
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
// both names are restored and the worktree retained. A crash while frozen
// leaves a removing row, and the next start restores the names and judges
// again. The one writer outside the rule is a process that escaped the
// task's process group and holds a descriptor inside the directory.
//
// WHO forces. Only an operator, naming the worktree's path in `basecamp
// connect worktrees prune --force <path>`.
//
// # Invariants
//
// Each is held by a test in worktrees_test.go.
//
//  1. The rule above.
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
//     verifies the ref holding that commit has not moved.
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
			if exists(record.Path) {
				// A checkout that never happened is removed; anything more is
				// judged no further, and kept.
				w.removeWorktree(settleCtx, record, RemovedNeverCreated, removal{unpopulated: true}, nil)
			} else {
				w.settle(settleCtx, record)
			}
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
	if after := w.settle(ctx, record); after.State == WorktreeRemoving {
		if exists(after.Path) || exists(frozenName(after.Path)) {
			return fmt.Errorf("connector: worktree %s is kept, but the ledger could not record it; the next start does", record.Path)
		}
		return fmt.Errorf("connector: worktree %s was removed but not recorded; the next start records it", record.Path)
	}
	return nil
}

// Recover implements RecoveringWorkspaces: every worktree a crash left
// creating, live or removing with no live task in it is settled under the
// same rule as a finished task's, after a removal the crash interrupted has
// its names restored. It runs in the connector that holds the instance lock,
// before anything is dispatched.
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
		w.settle(ctx, r)
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

// RetainedRefPrefix names the refs a forced removal keeps commits under.
const RetainedRefPrefix = "refs/basecamp-connect/retained/"

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
	case after.State == WorktreeRemoved && after.RemovedBy == RemovedMissing:
		result.Action = PruneMissing
	case force && (after.State == WorktreeRemoved || gone):
		result.Action = PruneForced
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

// settle judges one worktree for the connector and removes or retains it. The
// caller holds the lock. It returns the row as it now stands.
func (w *Worktrees) settle(ctx context.Context, r Worktree) Worktree {
	return w.settleKeeping(ctx, r, RemovedByConnector, false, nil)
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
		if w.movedElsewhere(ctx, r) {
			// Moved out from under the connector: its files are someone's.
			return w.retain(ctx, r, RetainedMoved, from)
		}
		// Nothing on disk, and nothing deleted: git's record of the worktree
		// is git's to prune. A record that still reaches a commit nothing
		// else holds keeps the row, so the operator hears of it.
		if !w.recordHoldsNothing(ctx, r) {
			return w.retain(ctx, r, RetainedUnverified, from)
		}
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
	return w.removeWorktree(ctx, r, by, removal{force: force}, refs)
}

// removal is how removeWorktree judges.
type removal struct {
	// force is an operator's explicit discard: unheld commits are kept under
	// refs and the worktree goes.
	force bool
	// unpopulated removes only a worktree holding nothing but git's .git
	// file: a checkout that never happened.
	unpopulated bool
}

// frozenName is where removeWorktree moves a name while it judges.
func frozenName(path string) string { return path + ".removing" }

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
	if !how.unpopulated && slices.Contains(from, WorktreeCreating) {
		// A crash between `worktree add --no-checkout` and the checkout
		// leaves a directory holding only git's .git file: never checked out,
		// so nothing in it to lose, though the full rule would read an empty
		// index against HEAD as every file deleted.
		if reason, _, _ := w.judge(ctx, r, v, removal{unpopulated: true}); reason == "" {
			how.unpopulated = true
		}
	}
	if w.whileFrozen != nil {
		if err := w.whileFrozen(v.dir); err != nil {
			// A test standing in for a crash: names stay frozen.
			return r
		}
	}

	reason, tip, keep := w.judge(ctx, r, v, how)
	if reason == "" && how.force && len(keep) > 0 {
		kept, err := w.keepCommits(ctx, r, keep)
		if err != nil {
			reason = RetainedUnverified
		} else if refs != nil {
			*refs = kept
		}
	}
	if reason != "" {
		if !w.restore(r, v, admin) {
			w.log.Warn("connector: a frozen worktree could not be restored; the next start restores it", "path", r.Path)
			return r
		}
		return w.retain(ctx, r, reason, removing)
	}

	// The branch goes first: it is deleted only at a commit judged held, so
	// a crash between the two leaves nothing unreachable, while the other
	// order would leave a branch nothing later settles.
	if how.force {
		w.deleteBranchIfHeld(ctx, r)
	} else {
		w.deleteBranchAt(ctx, r, tip)
	}
	// Delete the frozen copy: the directory, then the record.
	if err := os.RemoveAll(v.dir); err != nil {
		w.log.Warn("connector: a frozen worktree could not be deleted; kept", "path", r.Path, "error", err)
		if w.restore(r, v, admin) {
			return w.retain(ctx, r, RetainedUnverified, removing)
		}
		return r
	}
	if err := os.RemoveAll(v.gitDir); err != nil {
		w.log.Warn("connector: a worktree's record could not be deleted", "path", r.Path, "error", err)
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

// judge decides whether a frozen worktree holds anything that could be lost.
// It returns the reason to keep it, or "" with the task branch's tip (""
// when the branch is gone) and, for a force, the commits nothing holds.
func (w *Worktrees) judge(ctx context.Context, r Worktree, v view, how removal) (RetainedReason, string, []string) {
	if how.unpopulated {
		entries, err := os.ReadDir(v.dir)
		if err != nil || len(entries) != 1 || entries[0].Name() != ".git" || entries[0].IsDir() {
			return RetainedUnverified, "", nil
		}
		// The branch was made at the base and never moved: that commit is
		// what compare-and-delete may remove it at.
		return "", r.BaseCommit, nil
	}
	gitPath := func(name string) string { return filepath.Join(v.gitDir, name) }
	switch _, err := os.Lstat(gitPath("locked")); {
	case err == nil && !ownRecordLock(gitPath("locked")):
		return RetainedLocked, "", nil
	case err == nil:
	case !errors.Is(err, os.ErrNotExist):
		return RetainedUnverified, "", nil
	}
	// A submodule's git data is never lost, and never forced away: no ref
	// here can keep it.
	switch entries, err := os.ReadDir(gitPath("modules")); {
	case err == nil && len(entries) > 0:
		return RetainedDirty, "", nil
	case err != nil && !errors.Is(err, os.ErrNotExist):
		return RetainedUnverified, "", nil
	}
	if !how.force {
		for _, marker := range []string{"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "BISECT_LOG", "rebase-merge", "rebase-apply", "sequencer"} {
			switch _, err := os.Lstat(gitPath(marker)); {
			case err == nil:
				return RetainedDirty, "", nil
			case !errors.Is(err, os.ErrNotExist):
				return RetainedUnverified, "", nil
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
		return RetainedUnverified, "", nil
	case gitlinkContent:
		return RetainedDirty, "", nil
	case untracked && !how.force:
		return RetainedDirty, "", nil
	}
	if !how.force {
		status, err := w.gitRawIn(ctx, v, "status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignored=traditional", "--ignore-submodules=all")
		if err != nil {
			return RetainedUnverified, "", nil
		}
		if len(status) > 0 {
			return RetainedDirty, "", nil
		}
		// An index entry marked skip-worktree or assume-unchanged hides its
		// edits from status.
		entries, err := w.gitRawIn(ctx, v, "ls-files", "-v", "-z")
		if err != nil {
			return RetainedUnverified, "", nil
		}
		for entry := range strings.SplitSeq(string(entries), "\x00") {
			if entry == "" {
				continue
			}
			if tag := entry[0]; tag == 'S' || (tag >= 'a' && tag <= 'z') {
				return RetainedDirty, "", nil
			}
		}
	}

	tip, err := w.branchTip(ctx, r)
	if err != nil {
		return RetainedUnverified, "", nil
	}
	// Every commit the worktree or its branch reaches, and that removing it
	// would forget: HEAD, the branch, their reflogs, per-worktree refs.
	var tips []string
	head, err := w.gitRawIn(ctx, v, "rev-parse", "--verify", "--end-of-options", "HEAD^{commit}")
	if err != nil {
		return RetainedUnverified, "", nil
	}
	tips = append(tips, strings.TrimSpace(string(head)))
	if tip != "" {
		tips = append(tips, tip)
		out, err := w.gitOut(ctx, r.Repository, "reflog", "show", "--format=%H", "refs/heads/"+r.Branch, "--")
		if err != nil {
			return RetainedUnverified, "", nil
		}
		tips = append(tips, strings.Fields(out)...)
	}
	for _, args := range [][]string{
		{"reflog", "show", "--format=%H", "HEAD", "--"},
		{"for-each-ref", "--format=%(objectname)", "refs/worktree/", "refs/bisect/", "refs/rewritten/"},
	} {
		out, err := w.gitRawIn(ctx, v, args...)
		if err != nil {
			return RetainedUnverified, "", nil
		}
		tips = append(tips, strings.Fields(string(out))...)
	}
	// Those refs' own reflogs are not read: git logs ref updates only for
	// HEAD, refs/heads, refs/remotes and refs/notes, so a per-worktree ref has
	// none to read.
	slices.Sort(tips)
	var unheld []string
	for _, commit := range slices.Compact(tips) {
		held, err := w.held(ctx, r, commit)
		if err != nil {
			return RetainedUnverified, "", nil
		}
		if !held {
			if !how.force {
				return RetainedUnpushed, "", nil
			}
			unheld = append(unheld, commit)
		}
	}
	return "", tip, unheld
}

// keepCommits keeps each commit under refs/basecamp-connect/retained/<name>/
// <commit>, create-only; a ref already there at that commit is the same keep.
func (w *Worktrees) keepCommits(ctx context.Context, r Worktree, commits []string) ([]string, error) {
	name := filepath.Base(r.Path)
	refs := make([]string, 0, len(commits))
	for _, commit := range commits {
		ref := RetainedRefPrefix + safeName(name) + "/" + commit
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

// recordHoldsNothing reports whether git's record of a missing worktree
// (<repo>/.git/worktrees/<name>) reaches only commits held elsewhere: its HEAD,
// its reflog, its per-worktree refs. It reads and deletes nothing, and any
// doubt is false.
func (w *Worktrees) recordHoldsNothing(ctx context.Context, r Worktree) bool {
	if r.AdminDir == "" {
		return true
	}
	if _, err := os.Lstat(r.AdminDir); errors.Is(err, os.ErrNotExist) {
		return true
	} else if err != nil {
		return false
	}
	// A submodule's git data in the record is its own commits, which no ref
	// here reaches: the row is kept.
	switch entries, err := os.ReadDir(filepath.Join(r.AdminDir, "modules")); {
	case err == nil && len(entries) > 0:
		return false
	case err != nil && !errors.Is(err, os.ErrNotExist):
		return false
	}
	var tips []string
	for _, args := range [][]string{
		{"reflog", "show", "--format=%H", "HEAD", "--"},
		{"for-each-ref", "--format=%(objectname)", "refs/worktree/", "refs/bisect/", "refs/rewritten/"},
	} {
		out, err := w.run(ctx, safeGit, append([]string{"--git-dir", r.AdminDir}, args...), args[0])
		if err != nil {
			return false
		}
		tips = append(tips, strings.Fields(string(out))...)
	}
	head, err := w.run(ctx, safeGit, []string{"--git-dir", r.AdminDir, "rev-parse", "--verify", "--end-of-options", "HEAD^{commit}"}, "rev-parse")
	if err != nil {
		return false
	}
	tips = append(tips, strings.TrimSpace(string(head)))
	slices.Sort(tips)
	for _, commit := range slices.Compact(tips) {
		if held, err := w.held(ctx, r, commit); err != nil || !held {
			return false
		}
	}
	return true
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

// held reports whether a commit is safe to lose from this worktree: it is the
// base the worktree was made from, or a ref the connector keeps contains it —
// a remote branch, a local branch that is not a task's, or a ref a forced
// removal of this same worktree kept it under.
func (w *Worktrees) held(ctx context.Context, r Worktree, commit string) (bool, error) {
	if commit == r.BaseCommit {
		return true, nil
	}
	ref, _, err := w.holder(ctx, r, commit)
	return ref != "", err
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

// deleteBranchAt deletes the task branch only while it still points at
// commit, which was verified held (invariant 2), and only when this row made
// it.
func (w *Worktrees) deleteBranchAt(ctx context.Context, r Worktree, commit string) {
	if commit == "" || !r.BranchCreated || !strings.HasPrefix(r.Branch, BranchPrefix) {
		return
	}
	// One ref transaction: the branch goes only while it is still at commit
	// and, unless commit is the base, only while the ref that holds commit is
	// still where it was when it was found to hold it. A fetch or reset that
	// moves the holder in between makes git refuse the whole transaction.
	stdin := "start\n"
	if commit != r.BaseCommit {
		ref, oid, err := w.holder(ctx, r, commit)
		if err != nil || ref == "" {
			w.log.Debug("connector: task branch kept: nothing holds its commit", "branch", r.Branch)
			return
		}
		stdin += "verify " + ref + " " + oid + "\n"
	}
	stdin += "delete refs/heads/" + r.Branch + " " + commit + "\nprepare\ncommit\n"
	if err := w.gitStdin(ctx, r.Repository, stdin, "update-ref", "--stdin"); err != nil {
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
