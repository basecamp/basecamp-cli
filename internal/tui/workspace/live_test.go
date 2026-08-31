package workspace

import (
	"context"
	"errors"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/config"
)

// watchedModel is a workspace holding a watcher whose stream the test owns.
func watchedModel(t *testing.T, watch ReadingsWatcher) model {
	t.Helper()
	t.Setenv("NO_COLOR", "1")

	cfg := config.Default()
	cfg.AccountID = "1234567"
	m := newModel(&appctx.App{Config: cfg})
	t.Cleanup(m.cancel)
	m.watch = watch

	updated, _ := m.Update(tea.WindowSizeMsg{Width: 84, Height: 20})
	return updated.(model)
}

// Without a watcher the workspace runs on what it read at startup: nothing to
// open, nothing to wait on, nothing to retry.
func TestNoWatcherOpensNoStream(t *testing.T) {
	assert.Nil(t, startWatchCmd(context.Background(), nil, 1))
	assert.Nil(t, waitForChangeCmd(1, nil))

	m := watchedModel(t, nil)
	assert.Nil(t, m.retryWatch())
}

func TestWatchOpensAndWaits(t *testing.T) {
	changes := make(chan struct{}, 1)
	m := watchedModel(t, func(context.Context) (<-chan struct{}, error) { return changes, nil })

	started := startWatchCmd(m.ctx.Ctx(), m.watch, m.watchAttempt)().(watchStartedMsg)
	require.NoError(t, started.err)
	assert.Equal(t, m.watchAttempt, started.attempt)

	updated, cmd := m.Update(started)
	m = updated.(model)
	assert.NotNil(t, m.changes)
	assert.NotNil(t, cmd, "connecting did not arm the wait or the catch-up read")
}

// Connecting catches up on what changed while there was no stream: broadcasts
// sent before it opened were missed.
func TestWatchCatchesUpOnConnect(t *testing.T) {
	changes := make(chan struct{})
	m := watchedModel(t, func(context.Context) (<-chan struct{}, error) { return changes, nil })

	cmd := m.watchStarted(watchStartedMsg{attempt: m.watchAttempt, changes: changes})
	assert.NotNil(t, cmd)
	assert.False(t, m.refreshDue, "the catch-up read is immediate, not delayed")
}

// A ring is answered once, after the delay: one write lands as several
// broadcasts, and each rings the doorbell.
func TestChangesAreCollectedIntoOneReRead(t *testing.T) {
	m := watchedModel(t, nil)

	first := m.readingsChanged()
	require.NotNil(t, first)
	assert.True(t, m.refreshDue)

	assert.Nil(t, m.readingsChanged(), "a second ring armed a second re-read")

	m.refreshReadings()
	assert.False(t, m.refreshDue)
	assert.NotNil(t, m.readingsChanged(), "the next ring after a re-read was ignored")
}

// A stream that closes is retried on the same backoff a failed open is: to the
// reader those are the same thing.
func TestClosedStreamRetries(t *testing.T) {
	changes := make(chan struct{})
	m := watchedModel(t, func(context.Context) (<-chan struct{}, error) { return changes, nil })
	m.changes = changes

	updated, cmd := m.Update(watchChangedMsg{attempt: m.watchAttempt, closed: true})
	m = updated.(model)

	assert.Nil(t, m.changes)
	assert.Equal(t, 1, m.watchFailures)
	require.NotNil(t, cmd)
}

func TestFailedOpenRetries(t *testing.T) {
	m := watchedModel(t, func(context.Context) (<-chan struct{}, error) {
		return nil, errors.New("no route to host")
	})

	updated, cmd := m.Update(watchStartedMsg{attempt: m.watchAttempt, err: errors.New("boom")})
	m = updated.(model)

	assert.Nil(t, m.changes)
	assert.Equal(t, 1, m.watchFailures)
	require.NotNil(t, cmd)
}

// An answer to a watch that has since been replaced is dropped, rather than
// applied over its replacement.
func TestStaleWatchAnswersAreIgnored(t *testing.T) {
	changes := make(chan struct{})
	m := watchedModel(t, func(context.Context) (<-chan struct{}, error) { return changes, nil })
	stale := m.watchAttempt - 1

	updated, cmd := m.Update(watchStartedMsg{attempt: stale, changes: changes})
	m = updated.(model)
	assert.Nil(t, m.changes)
	assert.Nil(t, cmd)

	updated, cmd = m.Update(watchChangedMsg{attempt: stale})
	assert.Nil(t, cmd)
	assert.False(t, updated.(model).refreshDue)
}

// A timer left behind by a dead watch must not replace a live one.
func TestStaleRetryDoesNotReplaceALiveWatch(t *testing.T) {
	changes := make(chan struct{})
	m := watchedModel(t, func(context.Context) (<-chan struct{}, error) { return changes, nil })
	m.changes = changes

	_, cmd := m.Update(watchRetryMsg{attempt: m.watchAttempt})
	assert.Nil(t, cmd, "a retry fired while the stream was live")
}

func TestRetryNumbersTheNextAttempt(t *testing.T) {
	changes := make(chan struct{})
	m := watchedModel(t, func(context.Context) (<-chan struct{}, error) { return changes, nil })
	before := m.watchAttempt

	m.startWatch()
	assert.Equal(t, before+1, m.watchAttempt)
}

// The backoff doubles from the first retry to the ceiling, and stays there.
func TestWatchRetryDelay(t *testing.T) {
	assert.Equal(t, watchFirstRetry, watchRetryDelay(0))
	assert.Equal(t, watchFirstRetry, watchRetryDelay(1))
	assert.Equal(t, 4*time.Second, watchRetryDelay(2))
	assert.Equal(t, 8*time.Second, watchRetryDelay(3))
	assert.Equal(t, watchMaximumRetry, watchRetryDelay(9))
	assert.Equal(t, watchMaximumRetry, watchRetryDelay(50))
}

// A stream that closes reports it once so the model can retry, rather than
// spinning on a closed channel.
func TestWaitForChangeReportsAClosedStream(t *testing.T) {
	changes := make(chan struct{})
	close(changes)

	assert.Equal(t, watchChangedMsg{attempt: 7, closed: true}, waitForChangeCmd(7, changes)())
}

func TestWaitForChangeReportsARing(t *testing.T) {
	changes := make(chan struct{}, 1)
	changes <- struct{}{}

	assert.Equal(t, watchChangedMsg{attempt: 7}, waitForChangeCmd(7, changes)())
}

// With no account there is nothing to read notifications for.
func TestRefreshWithoutAnAccountReadsNothing(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	m := newModelWithAccount(t, "")

	assert.Nil(t, m.refreshReadings())
}
