//go:build linux || darwin

package connect

import (
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/connector/fakebasecamp"
)

// The handoff grace the after-grace test runs with, and how long before it
// is emitted the fake says its request was made. The age is past the grace
// and well inside connector.HandoffGrace, the minute a release build waits:
// the request is discarded only because the shortened grace is in effect.
const (
	shortGrace = 2 * time.Second
	requestAge = 10 * time.Second
)

// MsgBeforeThisRun is logged when the handoff closes a request made before
// the run's grace began, without writing it.
const MsgBeforeThisRun = "connector: a request from before this run started is not handed off"

// The connector admits a request but crashes before it is handed off: here
// it is held with --hold, so nothing is handed off, and killed outright.
// The operator releases the hold. The connector starts again only after the
// grace has passed since the request was made, and by then nobody is there
// to take it: the request is closed as before_this_run, and no request line
// is written for it.
//
// The fake stamps the request requestAge before it is emitted, rather than
// the test waiting out the grace. That is the same request to the
// connector: whether it hands a record off depends only on the event's
// created_at, as the feed served it, against when the run started
// (handOff in internal/connector/handoff.go), and nothing else in intake or
// admission reads created_at. The age exceeds the grace on the restart
// however fast the steps between run, and no step waits on a timer.
//
// Within the grace, the same steps hand the request off; that is the
// knob-free failure scenarios' test, under the default grace.
func TestARequestMadeBeforeTheGraceIsNotHandedOffOnARestart(t *testing.T) {
	if !devBuild {
		t.Skip("the handoff grace can be shortened only in a dev build (go test -tags dev, as make test and CI run it); a release build reads no environment for it")
	}
	t.Parallel()
	h := NewHarness(t, world(), fakebasecamp.WithClock(func() time.Time { return time.Now().Add(-requestAge) }))
	h.env = append(h.env, "BASECAMP_CONNECT_HANDOFF_GRACE="+shortGrace.String())
	h.Setup(t)

	held := h.Connect(t, "--hold")
	held.WaitStreaming(t, 1)
	ev := comment(h, fakebasecamp.OperatorID, askAgent(), fakebasecamp.Live)
	out := held.WaitFor(t, "the event line", func(o Output) bool { return len(o.EventsFor(ev.ID)) > 0 })
	events := out.EventsFor(ev.ID)
	require.Len(t, events, 1)
	require.Equal(t, "admitted", events[0].State, "a trusted mention is admitted under the hold")
	held.Kill(t)
	out = held.Wait(t)
	require.Empty(t, out.RequestsFor(ev.ID), "nothing is handed off under the hold")

	h.MustRun(t, "connect", "release", "-P", Agent)

	// The request's age as the connector will judge it, from the created_at
	// the feed served: it is already past the grace, before the restart.
	pointers := out.PointersFor(ev.ID)
	require.Len(t, pointers, 1)
	createdAt, err := time.Parse(time.RFC3339Nano, pointers[0].CreatedAt)
	require.NoError(t, err)
	require.Greater(t, time.Since(createdAt), shortGrace, "the request is older than the grace before the connector starts again")

	restarted := h.Connect(t)
	id := strconv.FormatInt(ev.ID, 10)
	restarted.WaitFor(t, "the request closed as before this run", func(o Output) bool {
		for _, l := range o.Logged(MsgBeforeThisRun) {
			if l.Attrs["event_id"] == id {
				return true
			}
		}
		return false
	})
	out = restarted.Stop(t)

	assert.Empty(t, out.Requests(), "no request line on the restart")
	overridden := out.Logged("connector: the handoff grace is overridden for this dev build")
	require.Len(t, overridden, 1)
	assert.Equal(t, shortGrace.String(), overridden[0].Attrs["grace"])

	// The ledger agrees: the record is closed, so no later run hands it
	// off either. The connector logs the close only once it is written.
	res := h.MustRun(t, "connect", "status", "-P", Agent, "--json")
	var status struct {
		Data struct {
			Status struct {
				Queues map[string]int `json:"queues"`
			} `json:"status"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(res.Stdout), &status), res.Stdout)
	require.NotNil(t, status.Data.Status.Queues, "status reports its queues\n%s", res.Stdout)
	assert.Zero(t, status.Data.Status.Queues["admitted"], res.Stdout)
	assert.Zero(t, status.Data.Status.Queues["queued"], res.Stdout)
}
