package connector

// Tasks and attempts: the dispatcher's half of the ledger, as an older build
// wrote it.
//
// A task was one conversation's work, bound to a token; an attempt was one
// worker run under it. Since #815 removed the worker side nothing writes
// either. Status reads what a ledger still holds (statusTasks,
// statusDispatches), a discard or an import reads the outcome an attempt
// settled (loadEventTask), and the schema below stays as it shipped.
//
// What the schema held an older build to, and still holds any write to:
//
//  1. One live task per conversation, one live attempt per task, and
//     (migration 5's task_events_one_live_task) one live task per event, by
//     unique partial indexes.
//  2. An ended task has no valid token and no live events: a trigger refuses
//     the end without the supersession.
//  3. Outcomes and stop reasons are separate. A stop reason is on the
//     attempt, an outcome on the task event.
//  4. Attempt states move forward only: launching → running → ended, or
//     launching → ended.
//
// migrationTasksAndAttempts is migration 7 as it shipped. Its route and
// work_dir columns and the tasks_live_work_dir index over them are gone —
// migration 13 drops them — and they are still written here because a
// migration that has been applied is never edited: a fresh ledger walks the
// same statements a ledger already at 7 walked. Do not edit it.
const migrationTasksAndAttempts = `
ALTER TABLE tasks ADD COLUMN conversation_key     TEXT    NOT NULL DEFAULT '';
ALTER TABLE tasks ADD COLUMN route                TEXT    NOT NULL DEFAULT '';
ALTER TABLE tasks ADD COLUMN work_dir             TEXT    NOT NULL DEFAULT '';
ALTER TABLE tasks ADD COLUMN driver               TEXT    NOT NULL DEFAULT '';
ALTER TABLE tasks ADD COLUMN originating_event_id INTEGER;
ALTER TABLE tasks ADD COLUMN deadline_at          TEXT;
ALTER TABLE tasks ADD COLUMN ended_at             TEXT;

CREATE UNIQUE INDEX tasks_live_conversation ON tasks (conversation_key)
  WHERE ended_at IS NULL AND conversation_key <> '';
CREATE UNIQUE INDEX tasks_live_work_dir ON tasks (work_dir)
  WHERE ended_at IS NULL AND work_dir <> '';

CREATE TRIGGER tasks_end_supersedes
BEFORE UPDATE OF ended_at ON tasks
WHEN NEW.ended_at IS NOT NULL AND NEW.superseded_at IS NULL
BEGIN
  SELECT RAISE(ABORT, 'a task ends with its token superseded');
END;

ALTER TABLE task_events ADD COLUMN exposed_attempt_id TEXT;
ALTER TABLE task_events ADD COLUMN adopted_reply_id   INTEGER;

CREATE TABLE attempts (
  id               TEXT    PRIMARY KEY,
  task_id          INTEGER NOT NULL REFERENCES tasks (id),
  seq              INTEGER NOT NULL,
  driver           TEXT    NOT NULL,
  state            TEXT    NOT NULL CHECK (state IN ('launching', 'running', 'ended')),
  pid              INTEGER,
  pgid             INTEGER,
  process_started  TEXT,
  session_id       TEXT    NOT NULL DEFAULT '',
  launched_at      TEXT    NOT NULL,
  running_at       TEXT,
  ended_at         TEXT,
  stop_reason      TEXT    NOT NULL DEFAULT ''
                   CHECK (stop_reason IN ('', 'finished', 'failed', 'deadline', 'shutdown', 'lost')),
  spawn_failed     INTEGER NOT NULL DEFAULT 0,
  refusals         INTEGER NOT NULL DEFAULT 0,
  progress_at      TEXT,
  still_running    INTEGER NOT NULL DEFAULT 0,
  -- The process the task token went to: the worker's MCP server, which an
  -- agent may start in a process group of its own, so a restart can end it
  -- too rather than leave a process of the connector's holding the token.
  taker_pid        INTEGER,
  taker_pgid       INTEGER,
  taker_started    TEXT,
  -- The token went out and the process holding it could not be accounted
  -- for: its identity could not be read, or the kernel stopped answering
  -- whether it is gone. Nothing is released around such an attempt, here or
  -- after a restart.
  taker_unaccounted INTEGER NOT NULL DEFAULT 0,
  UNIQUE (task_id, seq),
  CHECK ((state = 'ended') = (stop_reason <> ''))
);
CREATE UNIQUE INDEX attempts_live_per_task ON attempts (task_id) WHERE state <> 'ended';
CREATE INDEX attempts_state ON attempts (state);

CREATE TRIGGER attempts_state_moves_forward
BEFORE UPDATE OF state ON attempts
WHEN (CASE NEW.state WHEN 'launching' THEN 0 WHEN 'running' THEN 1 ELSE 2 END)
   < (CASE OLD.state WHEN 'launching' THEN 0 WHEN 'running' THEN 1 ELSE 2 END)
   OR (OLD.state = 'ended' AND NEW.state = 'ended' AND NEW.stop_reason <> OLD.stop_reason)
BEGIN
  SELECT RAISE(ABORT, 'an attempt state never goes back');
END;
`

// OutcomeUnknown is an event that was exposed to a worker and never
// reported: whatever ended the attempt, the worker may have acted on it.
const OutcomeUnknown Outcome = "unknown"
