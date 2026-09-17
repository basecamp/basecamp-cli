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
	db  *sql.DB
	now func() time.Time
}

// OpenLedger opens (creating if absent) the ledger at path and brings its
// schema up to date.
func OpenLedger(path string) (*Ledger, error) {
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
	if err := securePath(path); err != nil {
		return nil, err
	}

	// _txlock=immediate takes the write lock when a transaction opens rather
	// than on its first write. Without it two connectors racing on one file
	// can both start, both read, and one is refused at COMMIT with the work
	// already done.
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_txlock=immediate"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("connector: open ledger: %w", err)
	}
	// One writer. SQLite serializes writers anyway, and a pool merely turns
	// that serialization into SQLITE_BUSY under load.
	db.SetMaxOpenConns(1)

	l := &Ledger{db: db, now: time.Now}
	if err := retryBusy(func() error { return l.migrate(context.Background()) }); err != nil {
		_ = db.Close()
		return nil, err
	}
	// The WAL and shared-memory sidecars exist now and were created under the
	// process umask. The private directory already keeps other users out;
	// tightening them too costs nothing.
	for _, sidecar := range []string{path + "-wal", path + "-shm"} {
		if err := os.Chmod(sidecar, 0o600); err != nil && !os.IsNotExist(err) {
			_ = db.Close()
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
func securePath(path string) error {
	if err := setup.EnsurePrivateFile(path); err != nil {
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

// Close releases the ledger's handle.
func (l *Ledger) Close() error { return l.db.Close() }

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
	// get_dispatch cancels it or the connector fires it.
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
  PRIMARY KEY (task_id, event_id)
);

CREATE TRIGGER task_events_delivery_moves_forward
BEFORE UPDATE OF delivery ON task_events
WHEN (CASE NEW.delivery WHEN 'admitted' THEN 0 WHEN 'exposed' THEN 1 WHEN 'delivered' THEN 2 ELSE 3 END)
   < (CASE OLD.delivery WHEN 'admitted' THEN 0 WHEN 'exposed' THEN 1 WHEN 'delivered' THEN 2 ELSE 3 END)
BEGIN
  SELECT RAISE(ABORT, 'a delivery state never goes back');
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
