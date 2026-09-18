package connector

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// PromoteOptions names the two state directories of one account and agent.
type PromoteOptions struct {
	// ShadowDir is the shadow run's state directory, StateDir the normal
	// one. Both must be on one filesystem: the ledger moves by rename.
	ShadowDir string
	StateDir  string
	AccountID string
	AgentID   int64
	// By records who promoted.
	By string
}

// PromoteResult is what a promote did.
type PromoteResult struct {
	// Already says an earlier promote finished: the normal ledger stands
	// under its hold and there was no shadow ledger left to move.
	Already bool `json:"already_promoted,omitempty"`
	Hold    Hold `json:"hold"`
	Tagged  int  `json:"tagged_for_review"`
	Held    int  `json:"held"`
	// Ledger is the promoted ledger's path.
	Ledger string `json:"ledger"`
}

// Errors from promote.
var (
	// ErrNoShadowLedger is a shadow directory without a ledger.
	ErrNoShadowLedger = errors.New("there is no shadow ledger to promote")
	// ErrLedgerExists is a normal state directory that already has a ledger.
	ErrLedgerExists = errors.New("the connector already has a ledger")
)

// promoteStep is a test seam: a crash test kills the process at a named step.
var promoteStep = func(string) {}

// PromoteShadow turns a shadow run's ledger into the connector's, held
// (invariant 7). In order:
//
//  1. Both instance locks are taken, the shadow's and the connector's: no
//     shadow writer and no connector is running.
//  2. In the shadow ledger, in one transaction: the hold marker, a new
//     generation, every non-terminal record tagged for review, waiting
//     records held.
//  3. The shadow ledger is checkpointed into its single database file.
//  4. That file is renamed into the connector's state directory, which
//     exposes it; the directory is synced.
//
// A crash before 2 commits leaves the untouched shadow. After it, the ledger
// at either path is held, and running promote again finishes the move. The
// hold is committed before the rename, so no unheld ledger is ever at the
// normal path.
func PromoteShadow(ctx context.Context, opts PromoteOptions) (PromoteResult, error) {
	switch {
	case opts.ShadowDir == "" || opts.StateDir == "":
		return PromoteResult{}, errors.New("connector: promote needs the shadow and the normal state directory")
	case filepath.Clean(opts.ShadowDir) == filepath.Clean(opts.StateDir):
		return PromoteResult{}, errors.New("connector: the shadow and the normal state directory are the same directory")
	case strings.TrimSpace(opts.By) == "":
		return PromoteResult{}, errors.New("connector: a promote records who promoted")
	}
	shadowPath := filepath.Join(opts.ShadowDir, LedgerFile)
	statePath := filepath.Join(opts.StateDir, LedgerFile)

	if _, err := os.Lstat(opts.ShadowDir); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return PromoteResult{}, fmt.Errorf("connector: inspect the shadow state: %w", err)
		}
		// No shadow state at all: this can only be a promote run again, and
		// it still says so under the connector's own lock.
		stateLock, err := AcquireInstanceLock(opts.StateDir, opts.AccountID, opts.AgentID, time.Now())
		if err != nil {
			return PromoteResult{}, fmt.Errorf("connector: the connector must be stopped first: %w", err)
		}
		defer func() { _ = stateLock.Release() }()
		return promoted(ctx, opts, statePath)
	}
	shadowLock, err := AcquireInstanceLock(opts.ShadowDir, opts.AccountID, opts.AgentID, time.Now())
	if err != nil {
		return PromoteResult{}, fmt.Errorf("connector: the shadow connector must be stopped first: %w", err)
	}
	defer func() { _ = shadowLock.Release() }()
	stateLock, err := AcquireInstanceLock(opts.StateDir, opts.AccountID, opts.AgentID, time.Now())
	if err != nil {
		return PromoteResult{}, fmt.Errorf("connector: the connector must be stopped first: %w", err)
	}
	defer func() { _ = stateLock.Release() }()
	promoteStep("locked")

	if _, err := os.Lstat(shadowPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return promoted(ctx, opts, statePath)
		}
		return PromoteResult{}, fmt.Errorf("connector: inspect the shadow ledger: %w", err)
	}
	for _, p := range []string{statePath, statePath + "-wal", statePath + "-shm", statePath + "-journal"} {
		switch _, err := os.Lstat(p); {
		case err == nil:
			return PromoteResult{}, fmt.Errorf("connector: %s: %w", p, ErrLedgerExists)
		case !errors.Is(err, os.ErrNotExist):
			return PromoteResult{}, fmt.Errorf("connector: inspect %s: %w", p, err)
		}
	}

	ledger, err := OpenLedger(shadowPath) //nolint:contextcheck // OpenLedger migrates on its own context
	if err != nil {
		return PromoteResult{}, err
	}
	closed := false
	defer func() {
		if !closed {
			_ = ledger.Close()
		}
	}()
	held, err := ledger.SetHold(ctx, opts.By, HoldByPromote)
	if err != nil {
		return PromoteResult{}, err
	}
	promoteStep("held")

	// One file, so one rename moves all of it: the WAL is folded into the
	// database and the journal mode leaves no sidecar behind.
	if err := checkpointToOneFile(ctx, ledger.db); err != nil {
		return PromoteResult{}, err
	}
	if err := ledger.Close(); err != nil {
		return PromoteResult{}, fmt.Errorf("connector: close the shadow ledger: %w", err)
	}
	closed = true
	for _, p := range []string{shadowPath + "-wal", shadowPath + "-shm", shadowPath + "-journal"} {
		switch _, err := os.Lstat(p); {
		case err == nil:
			return PromoteResult{}, fmt.Errorf("connector: the shadow ledger still has %s after its checkpoint; it is held, run promote again", filepath.Base(p))
		case !errors.Is(err, os.ErrNotExist):
			return PromoteResult{}, fmt.Errorf("connector: inspect %s: %w", p, err)
		}
	}
	promoteStep("checkpointed")

	if err := os.Rename(shadowPath, statePath); err != nil {
		return PromoteResult{}, fmt.Errorf("connector: move the shadow ledger: %w", err)
	}
	promoteStep("renamed")
	if err := syncDirectory(opts.StateDir); err != nil {
		return PromoteResult{}, err
	}
	if err := syncDirectory(opts.ShadowDir); err != nil {
		return PromoteResult{}, err
	}
	promoteStep("synced")

	// Opened once more the normal way, which vets the file where it now is
	// and puts it back in WAL mode, and read: the hold must stand.
	moved, err := OpenLedger(statePath) //nolint:contextcheck // OpenLedger migrates on its own context
	if err != nil {
		return PromoteResult{}, err
	}
	defer func() { _ = moved.Close() }()
	hold, ok, err := moved.HoldMarker(ctx)
	if err != nil {
		return PromoteResult{}, err
	}
	if !ok {
		return PromoteResult{}, errors.New("connector: the promoted ledger has no hold marker")
	}
	return PromoteResult{Hold: hold, Tagged: held.Tagged, Held: held.Held, Ledger: statePath}, nil
}

// promoted answers a promote with no shadow ledger left: an earlier promote
// finished when the normal ledger stands under a promote's hold. It finishes
// what that promote may not have: a crash after the rename leaves the move
// without its durability barrier, so both directories are synced again before
// this says it is done.
func promoted(ctx context.Context, opts PromoteOptions, statePath string) (PromoteResult, error) {
	if _, err := os.Lstat(statePath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return PromoteResult{}, fmt.Errorf("connector: %w", ErrNoShadowLedger)
		}
		return PromoteResult{}, err
	}
	ledger, err := OpenLedgerReadOnly(ctx, statePath)
	if err != nil {
		return PromoteResult{}, err
	}
	defer func() { _ = ledger.Close() }()
	hold, ok, err := ledger.HoldMarker(ctx)
	if err != nil {
		return PromoteResult{}, err
	}
	// The promote's own decision is the mark, not the hold's cause: a shadow
	// already held by --hold keeps its first cause through the promote.
	var promotedHere bool
	if err := ledger.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM decisions WHERE action = 'shadow_promote')`).Scan(&promotedHere); err != nil {
		return PromoteResult{}, fmt.Errorf("connector: read the ledger's promote: %w", err)
	}
	if !ok || !promotedHere {
		return PromoteResult{}, fmt.Errorf("connector: %w", ErrNoShadowLedger)
	}
	for _, dir := range []string{opts.StateDir, opts.ShadowDir} {
		if err := syncDirectory(dir); err != nil && !errors.Is(err, os.ErrNotExist) {
			return PromoteResult{}, err
		}
	}
	return PromoteResult{Already: true, Hold: hold, Ledger: statePath}, nil
}

func checkpointToOneFile(ctx context.Context, db *sql.DB) error {
	var busy, logged, checkpointed int
	if err := db.QueryRowContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &logged, &checkpointed); err != nil {
		return fmt.Errorf("connector: checkpoint the shadow ledger: %w", err)
	}
	if busy != 0 {
		return errors.New("connector: the shadow ledger is in use; stop whatever holds it and run promote again")
	}
	var mode string
	if err := db.QueryRowContext(ctx, `PRAGMA journal_mode = DELETE`).Scan(&mode); err != nil {
		return fmt.Errorf("connector: leave WAL mode: %w", err)
	}
	if !strings.EqualFold(mode, "delete") {
		return fmt.Errorf("connector: the shadow ledger stayed in %s mode; it is held, run promote again", mode)
	}
	return nil
}

func syncDirectory(dir string) error {
	f, err := os.Open(dir)
	if errors.Is(err, os.ErrNotExist) {
		// A shadow directory a person has already cleared away.
		return err
	}
	if err != nil {
		return fmt.Errorf("connector: sync %s: %w", dir, err)
	}
	defer f.Close()
	if err := f.Sync(); err != nil {
		return fmt.Errorf("connector: sync %s: %w", dir, err)
	}
	return nil
}
