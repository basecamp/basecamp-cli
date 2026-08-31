package workspace

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"
)

// ReadingsWatcher opens the stream that says the sidebar's notifications changed.
// Basecamp pushes those over ActionCable to the channel the web sidebar at
// /<account>/my/sidebar/inbox subscribes to, and this is the seam a client for
// it plugs into.
//
// Nothing serves that stream yet. Until something does, the workspace runs with
// no watcher and the sidebar is the snapshot it was read as — which is why every
// command here answers with nil rather than erroring on a missing watcher.
//
// The stream closes when ctx is done, or when whatever is behind it has given up
// for good. A closed stream is retried on the same backoff a failed open is: to
// the reader those are the same thing.
type ReadingsWatcher func(ctx context.Context) (<-chan struct{}, error)

const (
	// One change lands as several broadcasts — Basecamp writes a reading per
	// recording — so the doorbell is answered once, after they have all rung.
	liveRefreshDelay = 500 * time.Millisecond

	// A watch that will not open backs off, doubling to a ceiling. Losing it
	// costs staleness rather than a broken screen, so it retries quietly.
	watchFirstRetry   = 2 * time.Second
	watchMaximumRetry = 30 * time.Second
)

// watchStartedMsg carries the stream a watcher opened, or the reason there is
// not one. The attempt identifies the state that asked, so an answer to a watch
// that has since been replaced is dropped rather than applied over its
// replacement.
type watchStartedMsg struct {
	attempt uint64
	changes <-chan struct{}
	err     error
}

// watchChangedMsg is one ring of the doorbell, or a stream that has closed.
type watchChangedMsg struct {
	attempt uint64
	closed  bool
}

// watchRetryMsg asks for a new watch after a failed open or a stream that
// stopped. The attempt is what keeps a timer left behind by a dead watch from
// replacing a live one.
type watchRetryMsg struct{ attempt uint64 }

// readingsRefreshDueMsg is the re-read a change asked for, once its delay has
// passed.
type readingsRefreshDueMsg struct{}

func startWatchCmd(ctx context.Context, watch ReadingsWatcher, attempt uint64) tea.Cmd {
	if watch == nil {
		return nil
	}
	return func() tea.Msg {
		changes, err := watch(ctx)
		return watchStartedMsg{attempt: attempt, changes: changes, err: err}
	}
}

// waitForChangeCmd blocks until the next ring, then reports it once. The handler
// re-arms it.
func waitForChangeCmd(attempt uint64, changes <-chan struct{}) tea.Cmd {
	if changes == nil {
		return nil
	}
	return func() tea.Msg {
		if _, open := <-changes; !open {
			return watchChangedMsg{attempt: attempt, closed: true}
		}
		return watchChangedMsg{attempt: attempt}
	}
}

func retryWatchLaterCmd(attempt uint64, delay time.Duration) tea.Cmd {
	return tea.Tick(delay, func(time.Time) tea.Msg { return watchRetryMsg{attempt: attempt} })
}

func refreshReadingsLaterCmd(delay time.Duration) tea.Cmd {
	return tea.Tick(delay, func(time.Time) tea.Msg { return readingsRefreshDueMsg{} })
}

// watchRetryDelay doubles from the first retry to the ceiling.
func watchRetryDelay(failures int) time.Duration {
	delay := watchFirstRetry
	for range max(failures-1, 0) {
		if delay >= watchMaximumRetry/2 {
			return watchMaximumRetry
		}
		delay *= 2
	}
	return min(delay, watchMaximumRetry)
}

// --- The model's side of it ---

// startWatch opens the notifications stream, if there is one to open.
func (m *model) startWatch() tea.Cmd {
	m.watchAttempt++
	return startWatchCmd(m.ctx.Ctx(), m.watch, m.watchAttempt)
}

// watchStarted takes the stream a watcher opened, and catches up on what changed
// while there was none: broadcasts sent before it connected were missed.
func (m *model) watchStarted(msg watchStartedMsg) tea.Cmd {
	if msg.err != nil {
		return m.retryWatch()
	}
	m.changes = msg.changes
	m.watchFailures = 0
	return tea.Batch(waitForChangeCmd(msg.attempt, msg.changes), m.refreshReadings())
}

// retryWatch comes back from a failed open or a closed stream, quietly: a watch
// that is down costs staleness, not a broken screen, and the sidebar is still
// whatever it last read.
func (m *model) retryWatch() tea.Cmd {
	m.changes = nil
	if m.watch == nil {
		return nil
	}
	m.watchFailures++
	return retryWatchLaterCmd(m.watchAttempt, watchRetryDelay(m.watchFailures))
}

// readingsChanged is the doorbell. One write rings it several times, so the
// re-read is delayed and only one is ever armed.
func (m *model) readingsChanged() tea.Cmd {
	if m.refreshDue {
		return nil
	}
	m.refreshDue = true
	return refreshReadingsLaterCmd(liveRefreshDelay)
}

// refreshReadings re-reads the notifications now.
func (m *model) refreshReadings() tea.Cmd {
	m.refreshDue = false
	if m.ctx.AccountID() == "" {
		return nil
	}
	return loadReadings(m.ctx.Ctx(), m.ctx.app, time.Now())
}
