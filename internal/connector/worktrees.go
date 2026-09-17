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
//     when it is clean (no modified or untracked file, no operation in
//     progress, not locked) and every commit it holds — its HEAD and its
//     task branch — is the base it was made from or is held by a remote
//     branch or by a local branch that is not another task's. Any error
//     while deciding that retains it.
//  2. Git refuses too. The removal itself is `git worktree remove` without
//     --force, so a file written between the check and the removal still
//     stops it, and a task branch is deleted only by compare-and-delete
//     against the commit that was verified.
//  3. The ledger first. A worktree is recorded creating before `git worktree
//     add` runs, and removing before `git worktree remove` does, so a crash
//     at any point leaves a row that says where a directory may be; the
//     connector's next start reconciles every such row under the same rules.
//  4. One remover at a time. Every check-and-remove, the connector's and
//     prune's, holds the worktrees lock, so a prune and a finishing task never
//     remove one worktree twice, and prune touches only retained worktrees.
//  5. Prune refuses work. A retained worktree still holding work is removed
//     only when the operator names it with --force, and even then its branch
//     is kept unless its commits are held elsewhere.
//  6. The repository's own code does not run: git runs with hooks disabled
//     and a fixed environment.
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
}

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
	return &Worktrees{ledger: opts.Ledger, root: opts.Root, git: opts.Git, env: env, path: opts.Path, log: opts.Logger}, nil
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
func (w *Worktrees) PerTaskDirs() bool { return true }

// Prepare implements Workspaces: a new worktree on a new task branch at the
// route's HEAD, and the route's place inside it.
func (w *Worktrees) Prepare(ctx context.Context, route string, originatingEventID int64) (string, error) {
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

	err = w.add(ctx, record)
	if err == nil {
		err = w.ledger.MoveWorktree(ctx, id, WorktreeLive, WorktreeCreating)
	}
	if err != nil {
		// Whatever git left is judged like any finished worktree; a lock not
		// had leaves the row for the next start.
		settleCtx := context.WithoutCancel(ctx)
		if unlock, lockErr := w.lock(settleCtx); lockErr == nil {
			w.settle(settleCtx, record, RemovedByConnector)
			unlock()
		}
		return "", fmt.Errorf("connector: create a worktree for event %d: %w", originatingEventID, err)
	}
	return workDir, nil
}

func (w *Worktrees) add(ctx context.Context, r Worktree) error {
	if err := os.MkdirAll(w.root, 0o700); err != nil {
		return err
	}
	if err := setup.EnsurePrivateDir(filepath.Dir(r.Path)); err != nil {
		return err
	}
	_, err := w.gitOut(ctx, r.Repository, "worktree", "add", "-b", r.Branch, "--end-of-options", r.Path, r.BaseCommit)
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
	w.settle(ctx, record, RemovedByConnector)
	return nil
}

// Recover implements RecoveringWorkspaces: every worktree a crash left
// creating, live or removing with no live task in it is settled under the
// same rules as a finished task's. It runs in the connector that holds the
// instance lock, before anything is dispatched.
func (w *Worktrees) Recover(ctx context.Context) error {
	unlock, err := w.lock(ctx)
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
	case after.State == WorktreeRemoved:
		result.Action = PruneRemoved
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
	kept := PruneResult{Worktree: r, Action: PruneKept, Reason: r.RetainedReason}
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
	if err := w.ledger.RemovedWorktree(ctx, r.ID, RemovedByPruneForced, WorktreeRemoving); err != nil {
		return kept
	}
	r.State, r.RemovedBy = WorktreeRemoved, RemovedByPruneForced
	return PruneResult{Worktree: r, Action: PruneForced, BranchKept: branchKept}
}

// settle judges one worktree and removes or retains it (invariants 1 to 3).
// The caller holds the lock. It returns the row as it now stands.
func (w *Worktrees) settle(ctx context.Context, r Worktree, by RemovedBy) Worktree {
	from := []WorktreeState{r.State}
	if _, err := os.Lstat(r.Path); errors.Is(err, os.ErrNotExist) {
		// Nothing on disk. A branch git made stays unless it still points at
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

	reason, tip := w.inspect(ctx, r)
	if reason != "" {
		return w.retain(ctx, r, reason, from)
	}
	if err := w.ledger.MoveWorktree(ctx, r.ID, WorktreeRemoving, from...); err != nil {
		w.log.Warn("connector: claiming a worktree for removal", "path", r.Path, "error", err)
		return r
	}
	r.State = WorktreeRemoving
	if _, err := w.gitOut(ctx, r.Repository, "worktree", "remove", "--end-of-options", r.Path); err != nil {
		// Git's own refusal (a file written since the check) or a failure:
		// either way the worktree is kept.
		return w.retain(ctx, r, RetainedUnverified, []WorktreeState{WorktreeRemoving})
	}
	w.deleteBranchAt(ctx, r, tip)
	if err := w.ledger.RemovedWorktree(ctx, r.ID, by, WorktreeRemoving); err != nil {
		w.log.Warn("connector: recording a worktree removed", "path", r.Path, "error", err)
		return r
	}
	r.State, r.RemovedBy = WorktreeRemoved, by
	return r
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
	status, err := w.gitRaw(ctx, r.Path, "status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignore-submodules=none")
	if err != nil {
		return RetainedUnverified, ""
	}
	if len(status) > 0 {
		return RetainedDirty, ""
	}

	head, err := w.gitOut(ctx, r.Path, "rev-parse", "--verify", "--end-of-options", "HEAD^{commit}")
	if err != nil {
		return RetainedUnverified, ""
	}
	tips := []string{head}
	tip, err := w.branchTip(ctx, r)
	if err != nil {
		return RetainedUnverified, ""
	}
	if tip != "" && tip != head {
		tips = append(tips, tip)
	}
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
// commit, which was verified held (invariant 2).
func (w *Worktrees) deleteBranchAt(ctx context.Context, r Worktree, commit string) {
	if commit == "" || !strings.HasPrefix(r.Branch, BranchPrefix) {
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

// lock takes the worktrees lock (invariant 4), waiting for another holder.
func (w *Worktrees) lock(ctx context.Context) (func(), error) {
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

// gitRaw runs git in dir with hooks disabled and a fixed environment
// (invariant 6).
func (w *Worktrees) gitRaw(ctx context.Context, dir string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	full := append([]string{"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-C", dir}, args...)
	cmd := exec.CommandContext(ctx, w.git, full...) //nolint:gosec // G204: git with the connector's own arguments
	cmd.Env = w.env
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return nil, fmt.Errorf("git %s: %w: %s", args[0], err, driver.Redact(msg))
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
