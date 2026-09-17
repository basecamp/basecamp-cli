package connector

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Worktrees in the ledger: every git worktree the connector made for a task,
// from the moment it decided to make one until it is gone.
//
// A row is written creating before `git worktree add` runs, so a crash at any
// point leaves a row that says a directory may exist; live once the worktree
// is there; retained, with a reason, when the task ended and the worktree
// held work that was not safe to remove; removing while a removal holds the
// worktrees lock; removed at the end, with who removed it (straight from any
// open state when the directory is found gone). The states move along those
// edges only, held by a trigger.
const migrationWorktrees = `
CREATE TABLE worktrees (
  id                   INTEGER PRIMARY KEY AUTOINCREMENT,
  path                 TEXT    NOT NULL,
  work_dir             TEXT    NOT NULL,
  route                TEXT    NOT NULL,
  repository           TEXT    NOT NULL,
  branch               TEXT    NOT NULL,
  base_commit          TEXT    NOT NULL,
  originating_event_id INTEGER NOT NULL,
  branch_created       INTEGER NOT NULL DEFAULT 0,
  admin_dir            TEXT    NOT NULL DEFAULT '',
  task_id              INTEGER REFERENCES tasks (id),
  state                TEXT    NOT NULL
                       CHECK (state IN ('creating', 'live', 'retained', 'removing', 'removed')),
  retained_reason      TEXT    NOT NULL DEFAULT ''
                       CHECK (retained_reason IN ('', 'dirty', 'unpushed', 'locked', 'moved', 'unverified', 'finished', 'orphaned')),
  created_at           TEXT    NOT NULL,
  finished_at          TEXT,
  retained_at          TEXT,
  removed_at           TEXT,
  removed_by           TEXT    NOT NULL DEFAULT ''
                       CHECK (removed_by IN ('', 'prune', 'prune_forced', 'missing', 'never_created')),
  CHECK (state <> 'retained' OR retained_reason <> ''),
  CHECK ((state = 'removed') = (removed_by <> ''))
);
CREATE UNIQUE INDEX worktrees_open_path ON worktrees (path) WHERE state <> 'removed';
CREATE UNIQUE INDEX worktrees_open_work_dir ON worktrees (work_dir) WHERE state <> 'removed';
CREATE INDEX worktrees_state ON worktrees (state);

CREATE TRIGGER worktrees_state_edges
BEFORE UPDATE OF state ON worktrees
WHEN NEW.state <> OLD.state AND NOT (
     (OLD.state = 'creating' AND NEW.state IN ('live', 'retained', 'removing', 'removed'))
  OR (OLD.state = 'live'     AND NEW.state IN ('retained', 'removing', 'removed'))
  OR (OLD.state = 'retained' AND NEW.state IN ('removing', 'removed'))
  OR (OLD.state = 'removing' AND NEW.state IN ('retained', 'removed')))
BEGIN
  SELECT RAISE(ABORT, 'a worktree state moves along its edges only');
END;
`

// WorktreeState is where a task's worktree is.
type WorktreeState string

const (
	WorktreeCreating WorktreeState = "creating"
	WorktreeLive     WorktreeState = "live"
	WorktreeRetained WorktreeState = "retained"
	WorktreeRemoving WorktreeState = "removing"
	WorktreeRemoved  WorktreeState = "removed"
)

// RetainedReason is why a worktree was kept.
type RetainedReason string

const (
	// RetainedDirty is uncommitted work: modified or untracked files, or a
	// merge, rebase, cherry-pick, revert or bisect in progress.
	RetainedDirty RetainedReason = "dirty"
	// RetainedUnpushed is a commit no remote branch and no other local branch
	// holds.
	RetainedUnpushed RetainedReason = "unpushed"
	// RetainedLocked is a worktree someone locked with `git worktree lock`.
	RetainedLocked RetainedReason = "locked"
	// RetainedUnverified is a worktree whose state could not be read. It is
	// kept, because a check that failed proves nothing is safe to delete.
	RetainedUnverified RetainedReason = "unverified"
	// RetainedMoved is a worktree that is no longer where the ledger says:
	// someone moved it, and its files are theirs to deal with.
	RetainedMoved RetainedReason = "moved"
	// RetainedOrphaned is a worktree whose directory something outside the
	// connector removed. Git's record of it and the task branch are still
	// there, reaching whatever they reach; the connector neither judges that
	// nor deletes any of it. An operator's explicit discard does, and is
	// told what goes.
	RetainedOrphaned RetainedReason = "orphaned"
	// RetainedFinished is a worktree whose task ended. Nothing the connector
	// does removes a worktree, so this is why most kept worktrees are kept:
	// the work is done with, and an operator says when it goes.
	RetainedFinished RetainedReason = "finished"
)

// RemovedBy is who removed a worktree.
type RemovedBy string

const (
	// There is no connector: nothing the connector does of its own accord
	// removes a worktree.
	RemovedByPrune       RemovedBy = "prune"
	RemovedByPruneForced RemovedBy = "prune_forced"
	RemovedMissing       RemovedBy = "missing"
	RemovedNeverCreated  RemovedBy = "never_created"
)

// Worktree is a worktree's ledger row.
type Worktree struct {
	ID int64
	// Path is the worktree's root; WorkDir is where the task worked in it,
	// the route's place inside the repository.
	Path               string
	WorkDir            string
	Route              string
	Repository         string
	Branch             string
	BaseCommit         string
	OriginatingEventID int64
	// BranchCreated is this row's proof that the connector made the task
	// branch, so deleting it can never delete someone else's.
	BranchCreated bool
	// AdminDir is the repository's own record of this worktree
	// (<repo>/.git/worktrees/<name>), which says where it is even after
	// someone moves it or changes what it has checked out.
	AdminDir string
	// TaskID is the task that last worked in it; zero before one launched.
	TaskID         int64
	State          WorktreeState
	RetainedReason RetainedReason
	CreatedAt      time.Time
	FinishedAt     time.Time
	RetainedAt     time.Time
	RemovedAt      time.Time
	RemovedBy      RemovedBy
}

const worktreeColumns = `id, path, work_dir, route, repository, branch, base_commit, originating_event_id, branch_created, admin_dir, COALESCE(task_id, 0),
state, retained_reason, created_at, finished_at, retained_at, removed_at, removed_by`

func scanWorktree(row interface{ Scan(...any) error }) (Worktree, error) {
	var (
		w                                 Worktree
		state, reason, removedBy, created string
		finished, retained, removed       sql.NullString
	)
	if err := row.Scan(&w.ID, &w.Path, &w.WorkDir, &w.Route, &w.Repository, &w.Branch, &w.BaseCommit, &w.OriginatingEventID, &w.BranchCreated, &w.AdminDir, &w.TaskID,
		&state, &reason, &created, &finished, &retained, &removed, &removedBy); err != nil {
		return Worktree{}, err
	}
	w.State, w.RetainedReason, w.RemovedBy = WorktreeState(state), RetainedReason(reason), RemovedBy(removedBy)
	var err error
	if w.CreatedAt, err = parseStamp(created); err != nil {
		return Worktree{}, err
	}
	for _, f := range []struct {
		src sql.NullString
		dst *time.Time
	}{{finished, &w.FinishedAt}, {retained, &w.RetainedAt}, {removed, &w.RemovedAt}} {
		if f.src.Valid {
			if *f.dst, err = parseStamp(f.src.String); err != nil {
				return Worktree{}, err
			}
		}
	}
	return w, nil
}

// ErrWorktreeState is a worktree transition from a state it cannot leave that
// way, or for a row that is not there.
var ErrWorktreeState = errors.New("the worktree is not in a state that allows this")

// BeginWorktree records a worktree about to be created. Nothing is on disk
// yet.
func (l *Ledger) BeginWorktree(ctx context.Context, w Worktree) (int64, error) {
	if w.Path == "" || w.WorkDir == "" || w.Route == "" || w.Repository == "" || w.Branch == "" || w.BaseCommit == "" {
		return 0, errors.New("connector: a worktree needs its path, working directory, route, repository, branch and base commit")
	}
	var id int64
	err := retryBusy(func() error {
		res, err := l.db.ExecContext(ctx, `
INSERT INTO worktrees (path, work_dir, route, repository, branch, base_commit, originating_event_id, state, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, 'creating', ?)`,
			w.Path, w.WorkDir, w.Route, w.Repository, w.Branch, w.BaseCommit, w.OriginatingEventID, l.timestamp())
		if err != nil {
			return fmt.Errorf("connector: record worktree %s: %w", w.Path, err)
		}
		id, err = res.LastInsertId()
		return err
	})
	return id, err
}

// WorktreeBranchCreated records that the connector created the task branch
// for a worktree, which is what lets it be deleted again.
func (l *Ledger) WorktreeBranchCreated(ctx context.Context, id int64) error {
	return retryBusy(func() error {
		_, err := l.db.ExecContext(ctx, `UPDATE worktrees SET branch_created = 1 WHERE id = ?`, id)
		if err != nil {
			return fmt.Errorf("connector: worktree %d: %w", id, err)
		}
		return nil
	})
}

// WorktreeAdminDir records the repository's directory for a worktree.
func (l *Ledger) WorktreeAdminDir(ctx context.Context, id int64, dir string) error {
	return retryBusy(func() error {
		_, err := l.db.ExecContext(ctx, `UPDATE worktrees SET admin_dir = ? WHERE id = ?`, dir, id)
		if err != nil {
			return fmt.Errorf("connector: worktree %d: %w", id, err)
		}
		return nil
	})
}

// MoveWorktree moves a worktree from one of from to state. It reports
// ErrWorktreeState when the row is in none of them.
func (l *Ledger) MoveWorktree(ctx context.Context, id int64, state WorktreeState, from ...WorktreeState) error {
	return l.moveWorktree(ctx, id, state, "", "", from)
}

// RetainWorktree keeps a worktree, with the reason, from one of from.
func (l *Ledger) RetainWorktree(ctx context.Context, id int64, reason RetainedReason, from ...WorktreeState) error {
	if reason == "" {
		return errors.New("connector: a retained worktree needs a reason")
	}
	return l.moveWorktree(ctx, id, WorktreeRetained, reason, "", from)
}

// RemovedWorktree records a worktree gone, and by whom, from one of from.
func (l *Ledger) RemovedWorktree(ctx context.Context, id int64, by RemovedBy, from ...WorktreeState) error {
	if by == "" {
		return errors.New("connector: a removed worktree needs who removed it")
	}
	return l.moveWorktree(ctx, id, WorktreeRemoved, "", by, from)
}

func (l *Ledger) moveWorktree(ctx context.Context, id int64, state WorktreeState, reason RetainedReason, by RemovedBy, from []WorktreeState) error {
	if len(from) == 0 {
		return errors.New("connector: a worktree transition names the states it leaves")
	}
	return retryBusy(func() error {
		tx, err := l.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("connector: begin worktree update: %w", err)
		}
		defer func() { _ = tx.Rollback() }()
		var (
			current, workDir string
			finished         sql.NullString
		)
		switch err := tx.QueryRowContext(ctx, `SELECT state, work_dir, finished_at FROM worktrees WHERE id = ?`, id).Scan(&current, &workDir, &finished); {
		case errors.Is(err, sql.ErrNoRows):
			return fmt.Errorf("connector: worktree %d: %w", id, ErrWorktreeState)
		case err != nil:
			return fmt.Errorf("connector: worktree %d: %w", id, err)
		}
		allowed := false
		for _, f := range from {
			allowed = allowed || WorktreeState(current) == f
		}
		if !allowed {
			return fmt.Errorf("connector: worktree %d is %s: %w", id, current, ErrWorktreeState)
		}
		now := l.timestamp()
		// The task that last worked in the directory, for status.
		var taskID sql.NullInt64
		if err := tx.QueryRowContext(ctx, `SELECT MAX(id) FROM tasks WHERE work_dir = ?`, workDir).Scan(&taskID); err != nil {
			return fmt.Errorf("connector: worktree %d: %w", id, err)
		}
		switch state {
		case WorktreeRetained:
			_, err = tx.ExecContext(ctx, `
UPDATE worktrees SET state = 'retained', retained_reason = ?, retained_at = ?, finished_at = COALESCE(finished_at, ?),
       task_id = COALESCE(?, task_id) WHERE id = ?`, string(reason), now, now, taskID, id)
		case WorktreeRemoved:
			_, err = tx.ExecContext(ctx, `
UPDATE worktrees SET state = 'removed', removed_by = ?, removed_at = ?, finished_at = COALESCE(finished_at, ?),
       task_id = COALESCE(?, task_id) WHERE id = ?`, string(by), now, now, taskID, id)
		case WorktreeRemoving:
			_, err = tx.ExecContext(ctx, `
UPDATE worktrees SET state = 'removing', finished_at = COALESCE(finished_at, ?), task_id = COALESCE(?, task_id) WHERE id = ?`, now, taskID, id)
		default:
			_, err = tx.ExecContext(ctx, `UPDATE worktrees SET state = ? WHERE id = ?`, string(state), id)
		}
		if err != nil {
			return fmt.Errorf("connector: worktree %d to %s: %w", id, state, err)
		}
		return tx.Commit()
	})
}

// WorktreeByWorkDir is the open (not removed) worktree a task works in.
func (l *Ledger) WorktreeByWorkDir(ctx context.Context, workDir string) (Worktree, bool, error) {
	row := l.db.QueryRowContext(ctx, `SELECT `+worktreeColumns+` FROM worktrees WHERE work_dir = ? AND state <> 'removed'`, workDir)
	w, err := scanWorktree(row)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return Worktree{}, false, nil
	case err != nil:
		return Worktree{}, false, fmt.Errorf("connector: worktree for %s: %w", workDir, err)
	}
	return w, true, nil
}

// Worktrees lists worktrees in the given states, oldest first; every state
// when none is given.
func (l *Ledger) Worktrees(ctx context.Context, states ...WorktreeState) ([]Worktree, error) {
	query := `SELECT ` + worktreeColumns + ` FROM worktrees`
	var args []any
	if len(states) > 0 {
		query += ` WHERE state IN (`
		for i, s := range states {
			if i > 0 {
				query += `, `
			}
			query += `?`
			args = append(args, string(s))
		}
		query += `)`
	}
	rows, err := l.db.QueryContext(ctx, query+` ORDER BY id`, args...)
	if err != nil {
		return nil, fmt.Errorf("connector: list worktrees: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Worktree
	for rows.Next() {
		w, err := scanWorktree(rows)
		if err != nil {
			return nil, fmt.Errorf("connector: list worktrees: %w", err)
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// RetainedWorktrees are the worktrees kept for a person to deal with.
func (l *Ledger) RetainedWorktrees(ctx context.Context) ([]Worktree, error) {
	return l.Worktrees(ctx, WorktreeRetained)
}

// UnfinishedWorktrees are worktrees a crash left between their creation and
// their task's end: creating, live or removing, with no live task working in
// them.
func (l *Ledger) UnfinishedWorktrees(ctx context.Context) ([]Worktree, error) {
	rows, err := l.db.QueryContext(ctx, `SELECT `+worktreeColumns+` FROM worktrees w
WHERE state IN ('creating', 'live', 'removing')
  AND NOT EXISTS (SELECT 1 FROM tasks t WHERE t.ended_at IS NULL AND t.work_dir = w.work_dir)
ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("connector: unfinished worktrees: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Worktree
	for rows.Next() {
		w, err := scanWorktree(rows)
		if err != nil {
			return nil, fmt.Errorf("connector: unfinished worktrees: %w", err)
		}
		out = append(out, w)
	}
	return out, rows.Err()
}
