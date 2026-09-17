package connector

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"modernc.org/sqlite" // database/sql driver "sqlite", pure Go: no cgo on any of the five release targets.
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/basecamp/basecamp-cli/internal/connector/setup"
)

// isInMemory reports a path SQLite would read as its in-memory database
// rather than a file.
func isInMemory(path string) bool {
	trimmed := strings.TrimPrefix(path, "file:")
	return trimmed == ":memory:" || strings.HasPrefix(trimmed, ":memory:?") || trimmed == ""
}

// RecordState is where an event sits in the ledger's lifecycle.
//
// Intake only ever writes StateSeen. The rest of the vocabulary is declared
// here because the states are one lifecycle, and a store that cannot name the
// state a later card writes cannot recover it on start either.
type RecordState string

const (
	// StateSeen is a pointer intake wrote and nothing has judged yet. Every
	// seen record re-runs the gate on start.
	StateSeen RecordState = "seen"
	// StateAdmitted passed the gate and awaits dispatch.
	StateAdmitted RecordState = "admitted"
	// StateQueued waits behind another event on its conversation.
	StateQueued RecordState = "queued"
	// StateBlocked is retained and retried: a reason, never a transport
	// failure dressed up as a verdict.
	StateBlocked RecordState = "blocked"
	// StateDispatched was handed to a worker.
	StateDispatched RecordState = "dispatched"
	// StateCompleted is terminal with an outcome.
	StateCompleted RecordState = "completed"
	// StateDiscarded is terminal with a verified verdict.
	StateDiscarded RecordState = "discarded"
)

// Lane names which lane first served an event. It is diagnostic: dedupe is by
// id, and the same event ordinarily arrives on both.
type Lane string

const (
	// LaneLive is the WebSocket.
	LaneLive Lane = "live"
	// LanePoll is the catch-up or streaming poll walk.
	LanePoll Lane = "poll"
	// LaneRepair is a repair walk after an overflow — its own cursor, never
	// the feed's.
	LaneRepair Lane = "repair"
)

// Ledger is the connector's durable memory: the dedupe authority, the feed
// position, and the record of what was lost.
//
// It is a SQLite file rather than a set in memory because every promise the
// connector makes about a crash rests on the answer to "have I seen this id
// before?" surviving the crash.
type Ledger struct {
	db *sql.DB
	// file is this process's entry for the ledger file, shared with every
	// other Ledger open on it; closed releases it once.
	file   *openLedgerFile
	closed sync.Once
	now    func() time.Time
}

// OpenLedger opens (creating if absent) the ledger at path and brings its
// schema up to date.
func OpenLedger(path string) (*Ledger, error) {
	return openLedger(context.Background(), path, true)
}

// ErrLedgerSchema is a ledger whose schema is not the one this binary writes.
var ErrLedgerSchema = errors.New("the connector ledger's schema is not the version this basecamp writes")

// OpenExistingLedger opens a ledger the connector already created, for a
// process that reads and reports into it rather than owns it — a worker's
// MCP server. It never creates the file and never migrates: a different
// basecamp binary started as a worker must not change the schema under the
// connector that holds it, so a ledger at any other schema version is
// refused.
//
// Neither the privacy check nor SQLite may create the file on this path: the
// check only inspects, and the database is opened with mode=rw, so a ledger
// removed at any moment is an error rather than a new empty one.
func OpenExistingLedger(ctx context.Context, path string) (*Ledger, error) {
	return openLedger(ctx, path, false)
}

// ledgerDSN is the SQLite URI for the ledger at path. owner opens it the way
// the connector does, creating it when absent; otherwise mode=rw makes SQLite
// refuse a file that is not there.
//
// _txlock=immediate takes the write lock when a transaction opens rather than
// on its first write. Without it two connectors racing on one file can both
// start, both read, and one is refused at COMMIT with the work already done.
func ledgerDSN(path string, owner bool) string {
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_txlock=immediate"
	if !owner {
		dsn += "&mode=rw"
	}
	return dsn
}

// openLedger opens the ledger; owner is the connector itself, which creates
// and migrates it. Any other opener does neither.
func openLedger(ctx context.Context, path string, owner bool) (*Ledger, error) {
	if path == "" {
		return nil, errors.New("connector: ledger path is required")
	}
	if isInMemory(path) {
		// SQLite's in-memory URI accepts every write and loses it on close.
		// The ledger's whole promise is that a crash is a delay.
		return nil, fmt.Errorf("connector: ledger path %q names SQLite's in-memory database, which is not durable", path)
	}
	if strings.ContainsAny(path, "?#%") {
		// The driver reads the path as a URI; these would be taken as its
		// query, fragment or an escape, and open some other file.
		return nil, fmt.Errorf("connector: ledger path %q contains a character the SQLite URI cannot carry (?, # or %%)", path)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("connector: ledger path %q: %w", path, err)
	}
	// The descriptor check runs for the first Ledger on this file and never
	// while another one is open: its close would drop that one's locks.
	file := claimLedger(abs)
	if file.key != abs {
		releaseLedger(file)
		return nil, fmt.Errorf("connector: %s and %s are one file: %w", abs, file.key, ErrLedgerUnderAnotherName)
	}
	if err := checkLedgerFile(file, path, abs, owner); err != nil {
		releaseLedger(file)
		return nil, err
	}

	db, err := sql.Open("sqlite", ledgerDSN(path, owner))
	if err != nil {
		releaseLedger(file)
		return nil, fmt.Errorf("connector: open ledger: %w", err)
	}
	// One writer. SQLite serializes writers anyway, and a pool merely turns
	// that serialization into SQLITE_BUSY under load.
	db.SetMaxOpenConns(1)

	l := &Ledger{db: db, file: file, now: time.Now}
	if owner {
		if err := retryBusy(func() error { return l.migrate(ctx) }); err != nil {
			_ = l.Close()
			return nil, err
		}
	} else {
		var version int
		err := retryBusy(func() error {
			var err error
			version, err = l.schemaVersion(ctx)
			return err
		})
		if err != nil {
			_ = l.Close()
			return nil, fmt.Errorf("connector: read ledger schema: %w", err)
		}
		if version != len(migrations) {
			_ = l.Close()
			return nil, fmt.Errorf("connector: ledger at schema %d, this basecamp writes %d: %w", version, len(migrations), ErrLedgerSchema)
		}
	}
	// The WAL and shared-memory sidecars exist now and were created under the
	// process umask. The private directory already keeps other users out;
	// tightening them too costs nothing.
	for _, sidecar := range []string{path + "-wal", path + "-shm"} {
		if err := os.Chmod(sidecar, 0o600); err != nil && !os.IsNotExist(err) {
			_ = l.Close()
			return nil, fmt.Errorf("connector: secure ledger sidecar: %w", err)
		}
	}
	return l, nil
}

// retryBusy retries fn while SQLite reports the database busy, for up to five
// seconds.
//
// The busy timeout covers ordinary contention, but not all of it: switching a
// fresh database into WAL mode takes a lock the busy handler is not consulted
// for, so two processes opening one new ledger at once — `status` beside a
// starting connector — can get SQLITE_BUSY immediately. The migration is
// idempotent, so trying again is safe.
func retryBusy(fn func() error) error {
	deadline := time.Now().Add(5 * time.Second)
	for {
		err := fn()
		if err == nil || !isBusy(err) || time.Now().After(deadline) {
			return err
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func isBusy(err error) bool {
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) {
		return false
	}
	// The primary result code, without the extended bits.
	switch sqliteErr.Code() & 0xff {
	case sqlite3.SQLITE_BUSY, sqlite3.SQLITE_LOCKED:
		return true
	}
	return false
}

// securePath makes the ledger private or refuses it.
//
// The ledger holds feed positions — signed tokens that resume the account's
// feed — and every event's metadata, so it is a credential file and is
// treated as one. The check is the same one the connector's trust file and
// instance lock get: every directory on the way must be this user's own and
// unwritable by anyone else, and the ledger itself is opened without
// following symlinks and inspected through that descriptor rather than by
// name. Validating the name would validate whatever the name pointed at when
// it was asked, which is not necessarily what SQLite then opens.
//
// A file or directory that already exists with looser permissions is refused
// rather than tightened: something else chose those permissions, and silently
// changing them could break it or hide that the ledger was exposed.
//
// The check cannot be made on a platform without POSIX owners and modes, and
// a ledger whose privacy cannot be established is refused there rather than
// opened — the same way setup refuses to write a trust file it cannot vouch
// for.
func securePath(path string, create bool) error {
	check := setup.CheckPrivateFile
	if create {
		check = setup.EnsurePrivateFile
	}
	if err := check(path); err != nil {
		return fmt.Errorf("connector: secure the ledger: %w", err)
	}
	// One rule of the ledger's own, beyond what a trust file needs: its
	// directory must be 0700, not merely unwritable by others. SQLite writes
	// -wal and -shm beside the database, and a directory other users can read
	// is one whose entries they can list. The directory is already known not
	// to be a symlink, so Lstat here inspects the directory itself.
	dir := filepath.Dir(path)
	info, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("connector: inspect ledger directory: %w", err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		return fmt.Errorf("connector: ledger directory %s is readable by other users (mode %04o); it must be 0700", dir, perm)
	}
	return nil
}

// Close releases the ledger's handle. A second Close is harmless: the file is
// released once, so a defensive extra call cannot take the entry away from
// another Ledger still holding the same file.
func (l *Ledger) Close() error {
	l.closed.Do(func() { releaseLedger(l.file) })
	return l.db.Close()
}

// Opening one ledger file more than once in a process, safely.
//
// The privacy check opens the file and closes it, and POSIX drops every lock
// a process holds on a file when any descriptor for it is closed — including
// the locks SQLite is holding on another connection. So the check runs
// exactly once per file per process, while nothing else has it open. A later
// Ledger on the same file (a status read beside a running connector, a
// promote) is verified instead against what that check established: the same
// file, still this user's own, still owner-only, in a directory that is
// still 0700. Stat never opens anything, so it takes no locks away.
//
// ErrLedgerNotTheSameFile is a second open of a path that no longer names the
// file the check passed.
var ErrLedgerNotTheSameFile = errors.New("the ledger path no longer names the file this process checked")

// ErrLedgerUnderAnotherName is an open of a file this process already has
// open under a different path — a hardlink, or a route through a symlink.
// SQLite names its write-ahead log and shared-memory files after the path it
// was given, so one file opened under two names is two different logs for one
// database. It is refused here, before the check that would open the file and
// drop the live handle's locks.
var ErrLedgerUnderAnotherName = errors.New("this process already has this ledger open under another name")

var openLedgers struct {
	sync.Mutex
	files map[string]*openLedgerFile
}

type openLedgerFile struct {
	// key is this entry's key in the map, so it can be released by entry.
	key  string
	refs int
	// mu serializes the check itself, so opens that race each other on a
	// fresh file do not verify against a check that has not run yet.
	mu sync.Mutex
	// info is the file as the descriptor check saw it, nil until it has run.
	info os.FileInfo
}

// securePathRuns counts the checks that open the file. A test pins that a
// second Ledger on a live file runs none.
var securePathRuns atomic.Int64

// claimLedger records this process opening path and returns that file's
// entry, whose lock the caller takes to check it.
//
// The entry is found by what the path names, not by how it is spelled: a
// hardlink, a symlink or another route to the same file must meet the same
// entry, because the check this guards opens and closes the file itself.
func claimLedger(path string) *openLedgerFile {
	openLedgers.Lock()
	defer openLedgers.Unlock()
	if openLedgers.files == nil {
		openLedgers.files = map[string]*openLedgerFile{}
	}
	file := openLedgers.files[path]
	if file == nil {
		if info, err := os.Lstat(path); err == nil {
			for _, open := range openLedgers.files {
				if open.info != nil && os.SameFile(open.info, info) {
					file = open
					break
				}
			}
		}
	}
	if file == nil {
		file = &openLedgerFile{key: path}
		openLedgers.files[path] = file
	}
	file.refs++
	return file
}

func releaseLedger(file *openLedgerFile) {
	openLedgers.Lock()
	defer openLedgers.Unlock()
	if file.refs--; file.refs <= 0 {
		delete(openLedgers.files, file.key)
	}
}

// checkLedgerFile runs the descriptor check once per file, and holds every
// later open against what it established.
func checkLedgerFile(file *openLedgerFile, path, abs string, owner bool) error {
	file.mu.Lock()
	defer file.mu.Unlock()
	if file.info != nil {
		return verifySameFile(abs, file.info)
	}
	securePathRuns.Add(1)
	if err := securePath(path, owner); err != nil {
		return err
	}
	info, err := os.Lstat(abs)
	if err != nil {
		return fmt.Errorf("connector: inspect the ledger: %w", err)
	}
	file.info = info
	return nil
}

// verifySameFile holds a second open to what the first one's check
// established, without opening anything.
func verifySameFile(path string, checked os.FileInfo) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("connector: secure the ledger: %w", err)
	}
	if !info.Mode().IsRegular() || !os.SameFile(info, checked) {
		return fmt.Errorf("connector: secure the ledger: %s: %w", path, ErrLedgerNotTheSameFile)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		return fmt.Errorf("connector: secure the ledger: %s can be read by other users (mode %04o)", path, perm)
	}
	dir, err := os.Lstat(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("connector: inspect ledger directory: %w", err)
	}
	if perm := dir.Mode().Perm(); perm&0o077 != 0 {
		return fmt.Errorf("connector: ledger directory %s is readable by other users (mode %04o); it must be 0700", filepath.Dir(path), perm)
	}
	return nil
}

// migrations are applied in order, each exactly once. A migration is never
// edited after it ships: the ledger outlives the binary that created it.
var migrations = []string{
	`
CREATE TABLE events (
  id                 INTEGER PRIMARY KEY,
  state              TEXT    NOT NULL,
  reason             TEXT    NOT NULL DEFAULT '',
  lane               TEXT    NOT NULL,
  event_type         TEXT    NOT NULL,
  kind               TEXT    NOT NULL,
  action             TEXT    NOT NULL,
  bucket_id          INTEGER NOT NULL,
  creator_id         INTEGER NOT NULL,
  performed_by_id    INTEGER,
  recording_id       INTEGER NOT NULL,
  details            BLOB,
  actor_type         TEXT    NOT NULL DEFAULT '',
  visible_to_clients INTEGER,
  created_at         TEXT    NOT NULL,
  seen_at            TEXT    NOT NULL,
  updated_at         TEXT    NOT NULL,
  content_dropped    INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX events_state_id ON events (state, id);

CREATE TABLE checkpoints (
  flat_key            TEXT PRIMARY KEY,
  lineage             TEXT NOT NULL,
  position            TEXT NOT NULL,
  last_poll_served_id INTEGER NOT NULL DEFAULT 0,
  updated_at          TEXT NOT NULL
);
CREATE INDEX checkpoints_lineage ON checkpoints (lineage);

CREATE TABLE losses (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  detected_at   TEXT    NOT NULL,
  dropped_count INTEGER NOT NULL,
  repair_since  INTEGER NOT NULL,
  repair_cursor TEXT    NOT NULL DEFAULT '',
  deadline_at   TEXT    NOT NULL,
  resolved_at   TEXT
);

CREATE TABLE loss_ids (
  loss_id  INTEGER NOT NULL REFERENCES losses (id) ON DELETE CASCADE,
  event_id INTEGER NOT NULL,
  state    TEXT    NOT NULL,
  PRIMARY KEY (loss_id, event_id)
);
CREATE INDEX loss_ids_event ON loss_ids (event_id, state);

CREATE TABLE gaps (
  id             INTEGER PRIMARY KEY AUTOINCREMENT,
  detected_at    TEXT    NOT NULL,
  class          TEXT    NOT NULL,
  epoch_after_id INTEGER,
  entry_class    TEXT    NOT NULL DEFAULT '',
  note           TEXT    NOT NULL DEFAULT ''
);
`,
	// Migration 2. A loss is repaired under the filter set it was recorded
	// with: a connector restarted with different filters must not walk the
	// wrong lane for it and condemn events the original filters would have
	// served. Migrations only ever append, so an existing ledger takes this
	// one and its open losses carry an empty set, which reads as "the
	// connector's own".
	`ALTER TABLE losses ADD COLUMN filters TEXT NOT NULL DEFAULT ''`,
	// Migration 3. Terminal means terminal, in the database and not only in
	// the code that writes to it. A completed or discarded record that could
	// be moved back into the working states could be dispatched a second
	// time, or — once its payload has been dropped and only the tombstone
	// remains — requeued as work with nothing in it. The Go side refuses
	// every edge the lifecycle does not have; this refuses the two that
	// matter to anything that ever writes to this file.
	`
CREATE TRIGGER events_terminal_is_terminal
BEFORE UPDATE OF state ON events
WHEN OLD.state IN ('completed', 'discarded') AND NEW.state <> OLD.state
BEGIN
  SELECT RAISE(ABORT, 'a terminal record cannot change state');
END;
`,
	// Migration 4. What admission decides is written onto the record it
	// decided, in the transaction that decides it.
	//
	// revision is the guard on that write: a decision carries the revision
	// the record was loaded at, and applies only while the record is still
	// there, so one event gets one verdict however many fetches decide it and
	// an older decision never overwrites a newer one. Every state change bumps
	// it, not only admission's.
	//
	// decided_at is when the latest verdict was written, blocked_at when the
	// record entered its current run of blocked verdicts (the retry window
	// counts from it), and retry_at a throttled verdict's server deadline.
	// The rest is the verdict itself — what dispatch starts a task from, and
	// what a blocked(no_route) record's holding reply needs. snapshot is the
	// recording's content as admission read it. Only an admitted verdict
	// writes one, whether the ledger writes it as admitted or as queued; a
	// record keeps it through dispatch and completion until retention drops
	// it, and loses it on any move to blocked or discarded. Nothing else in
	// the ledger is content.
	//
	// Nothing has shipped that wrote a version 3 ledger, but a migration is
	// how the schema changes regardless: the next time something has, this is
	// the path that has to work.
	`
ALTER TABLE events ADD COLUMN revision           INTEGER NOT NULL DEFAULT 0;
ALTER TABLE events ADD COLUMN decided_at         TEXT;
ALTER TABLE events ADD COLUMN blocked_at         TEXT;
ALTER TABLE events ADD COLUMN retry_at           TEXT;
ALTER TABLE events ADD COLUMN trigger_name       TEXT    NOT NULL DEFAULT '';
ALTER TABLE events ADD COLUMN acknowledge        INTEGER NOT NULL DEFAULT 0;
ALTER TABLE events ADD COLUMN conversation_key   TEXT    NOT NULL DEFAULT '';
ALTER TABLE events ADD COLUMN reply_kind         TEXT    NOT NULL DEFAULT '';
ALTER TABLE events ADD COLUMN reply_recording_id INTEGER NOT NULL DEFAULT 0;
ALTER TABLE events ADD COLUMN routed             INTEGER NOT NULL DEFAULT 0;
ALTER TABLE events ADD COLUMN route              TEXT    NOT NULL DEFAULT '';
ALTER TABLE events ADD COLUMN class              TEXT    NOT NULL DEFAULT '';
ALTER TABLE events ADD COLUMN recording_url      TEXT    NOT NULL DEFAULT '';
ALTER TABLE events ADD COLUMN requester_id       INTEGER NOT NULL DEFAULT 0;
ALTER TABLE events ADD COLUMN snapshot           BLOB;
CREATE INDEX events_conversation ON events (conversation_key, state);
`,
	// Migration 5. The worker's side of a dispatch: the task a worker's token
	// names, and each event's delivery state on it.
	//
	// These are the columns the basecamp_connect domain reads and writes and
	// nothing more. The dispatcher's attempts, deadlines and working
	// directories extend these tables rather than replace them.
	//
	// A task keeps a hash of its token, never the token: the ledger is a
	// file other processes open, and the token is what binds a worker to its
	// task. superseded_at is set when a redispatch replaces the worker, and a
	// superseded token is refused.
	//
	// delivery is admitted → exposed → delivered → completed and never goes
	// back, held by the trigger as the events lifecycle is. guard is the
	// thirty-second acknowledgement guard: '' where none applies, armed until
	// get_dispatch cancels it or the connector fires it, and settled once.
	// A record a worker was handed leaves dispatched only to completed. The
	// whole lifecycle these enforce is written down at the top of
	// ledger_dispatch.go.
	//
	// An event is on at most one live task. retired_at is set on every row of
	// a task when it is superseded, and the unique index over the rows not
	// retired is what refuses a second live task for the same event — in the
	// database, so a dispatcher retrying a launch cannot hand one event to two
	// workers whatever order its writes land in.
	`
CREATE TABLE tasks (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  token_sha256  TEXT NOT NULL UNIQUE,
  created_at    TEXT NOT NULL,
  superseded_at TEXT
);

CREATE TABLE task_events (
  task_id      INTEGER NOT NULL REFERENCES tasks (id),
  event_id     INTEGER NOT NULL REFERENCES events (id),
  delivery     TEXT    NOT NULL DEFAULT 'admitted'
               CHECK (delivery IN ('admitted', 'exposed', 'delivered', 'completed')),
  guard        TEXT    NOT NULL DEFAULT ''
               CHECK (guard IN ('', 'armed', 'canceled', 'fired')),
  exposed_at   TEXT,
  delivered_at TEXT,
  completed_at TEXT,
  ack_id       INTEGER,
  outcome      TEXT    NOT NULL DEFAULT '',
  links        TEXT    NOT NULL DEFAULT '[]',
  reply_id     INTEGER,
  retired_at   TEXT,
  pulled_at    TEXT,
  withdrawn_at TEXT,
  PRIMARY KEY (task_id, event_id)
);

CREATE UNIQUE INDEX task_events_one_live_task ON task_events (event_id) WHERE retired_at IS NULL;
CREATE INDEX task_events_event ON task_events (event_id, delivery);

CREATE TRIGGER tasks_supersession_is_final
BEFORE UPDATE OF superseded_at ON tasks
WHEN OLD.superseded_at IS NOT NULL AND NEW.superseded_at IS NOT OLD.superseded_at
BEGIN
  SELECT RAISE(ABORT, 'a superseded task stays superseded');
END;

CREATE TRIGGER task_events_retirement_is_final
BEFORE UPDATE OF retired_at ON task_events
WHEN OLD.retired_at IS NOT NULL AND NEW.retired_at IS NOT OLD.retired_at
BEGIN
  SELECT RAISE(ABORT, 'a retired task event stays retired');
END;

CREATE TRIGGER task_events_are_not_deleted
BEFORE DELETE ON task_events
BEGIN
  SELECT RAISE(ABORT, 'a task event is retired, never deleted');
END;

CREATE TRIGGER task_events_withdrawal_is_for_a_failed_spawn
BEFORE UPDATE OF withdrawn_at ON task_events
WHEN NEW.withdrawn_at IS NOT OLD.withdrawn_at AND (
  OLD.withdrawn_at IS NOT NULL
  OR OLD.delivery <> 'exposed'
  OR OLD.pulled_at IS NOT NULL
  OR NOT EXISTS (SELECT 1 FROM tasks WHERE tasks.id = OLD.task_id AND tasks.superseded_at IS NOT NULL)
  OR EXISTS (SELECT 1 FROM task_events live WHERE live.event_id = OLD.event_id AND live.retired_at IS NULL))
BEGIN
  SELECT RAISE(ABORT, 'only a launch exposure no worker pulled, on a superseded task and no live one, is withdrawn, and only once');
END;

CREATE TRIGGER task_events_pull_is_recorded_once
BEFORE UPDATE OF pulled_at ON task_events
WHEN OLD.pulled_at IS NOT NULL AND NEW.pulled_at IS NOT OLD.pulled_at
BEGIN
  SELECT RAISE(ABORT, 'a pull is recorded once');
END;

CREATE TRIGGER task_events_withdrawn_is_final
BEFORE UPDATE OF delivery ON task_events
WHEN OLD.withdrawn_at IS NOT NULL AND NEW.delivery <> OLD.delivery
BEGIN
  SELECT RAISE(ABORT, 'a withdrawn exposure does not move');
END;

CREATE TRIGGER events_handed_work_settles_first
BEFORE UPDATE OF state ON events
WHEN OLD.state = 'dispatched' AND NEW.state NOT IN ('dispatched', 'completed')
  AND EXISTS (SELECT 1 FROM task_events WHERE event_id = OLD.id AND delivery IN ('exposed', 'delivered') AND withdrawn_at IS NULL)
BEGIN
  SELECT RAISE(ABORT, 'a worker was handed this event; it leaves dispatched only when completed');
END;

CREATE TRIGGER events_dispatched_while_on_a_live_task
BEFORE UPDATE OF state ON events
WHEN NEW.state <> OLD.state AND (
  (NEW.state = 'dispatched'
    AND NOT EXISTS (SELECT 1 FROM task_events WHERE event_id = OLD.id AND retired_at IS NULL))
  OR (OLD.state = 'dispatched' AND NEW.state <> 'completed'
    AND EXISTS (SELECT 1 FROM task_events WHERE event_id = OLD.id AND retired_at IS NULL)))
BEGIN
  SELECT RAISE(ABORT, 'a record is dispatched exactly while a live task carries it');
END;

CREATE TRIGGER task_events_guard_settles_once
BEFORE UPDATE OF guard ON task_events
WHEN NEW.guard <> OLD.guard AND NOT (OLD.guard = 'armed' AND NEW.guard IN ('canceled', 'fired'))
BEGIN
  SELECT RAISE(ABORT, 'a guard only goes from armed to canceled or fired');
END;

CREATE TRIGGER task_events_delivery_moves_forward
BEFORE UPDATE OF delivery ON task_events
WHEN (CASE NEW.delivery WHEN 'admitted' THEN 0 WHEN 'exposed' THEN 1 WHEN 'delivered' THEN 2 ELSE 3 END)
   < (CASE OLD.delivery WHEN 'admitted' THEN 0 WHEN 'exposed' THEN 1 WHEN 'delivered' THEN 2 ELSE 3 END)
BEGIN
  SELECT RAISE(ABORT, 'a delivery state never goes back');
END;

CREATE TRIGGER task_events_exposure_comes_first
BEFORE UPDATE OF delivery ON task_events
WHEN OLD.delivery = 'admitted' AND NEW.delivery IN ('delivered', 'completed')
BEGIN
  SELECT RAISE(ABORT, 'nothing a worker was never handed is acknowledged or completed');
END;
`,
}

func (l *Ledger) migrate(ctx context.Context) error {
	if _, err := l.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
  version    INTEGER PRIMARY KEY,
  applied_at TEXT NOT NULL
)`); err != nil {
		return fmt.Errorf("connector: create migration table: %w", err)
	}

	for i := range migrations {
		version := i + 1
		// The version is read inside the migration's own transaction, which
		// takes the write lock as it opens: two processes opening a fresh
		// ledger at once — `status` beside a starting connector — must not
		// both decide migration 1 is theirs to apply.
		tx, err := l.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("connector: begin migration %d: %w", version, err)
		}
		var applied int
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&applied); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("connector: read schema version: %w", err)
		}
		if applied >= version {
			_ = tx.Rollback()
			continue
		}
		if _, err := tx.ExecContext(ctx, migrations[i]); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("connector: apply migration %d: %w", version, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`, version, l.timestamp()); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("connector: record migration %d: %w", version, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("connector: commit migration %d: %w", version, err)
		}
	}
	return nil
}

// SchemaVersion reports the highest applied migration.
func (l *Ledger) SchemaVersion(ctx context.Context) (int, error) {
	return l.schemaVersion(ctx)
}

// schemaVersion reads the version, and reads a ledger with no migration table
// as version 0 rather than failing.
func (l *Ledger) schemaVersion(ctx context.Context) (int, error) {
	var tables int
	if err := l.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'schema_migrations'`).Scan(&tables); err != nil {
		return 0, err
	}
	if tables == 0 {
		return 0, nil
	}
	var version int
	err := l.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&version)
	return version, err
}

func (l *Ledger) timestamp() string { return stamp(l.now()) }

// ledgerTime is the one format every ledger timestamp is stored in: UTC, with
// all nine fractional digits. Fixed width is what makes SQLite's text
// comparison agree with time order. time.RFC3339Nano trims trailing zeros, so
// "12:00:00Z" would sort after the later "12:00:00.5Z" and retention would
// keep a record past its window.
const ledgerTime = "2006-01-02T15:04:05.000000000Z07:00"
