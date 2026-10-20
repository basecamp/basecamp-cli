package connector

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/connector/admission"
	"github.com/basecamp/basecamp-cli/internal/connector/ndjson"
)

// fakeBooster is Basecamp's boost endpoint: it records each boost, and what
// had been written to stdout when it was asked.
type fakeBooster struct {
	out    *bytes.Buffer
	err    error
	boosts []fakeBoost
	// during runs while a boost is in flight.
	during func()
}

type fakeBoost struct {
	RecordingID int64
	Content     string
	// Written is what stdout held when the boost was asked for.
	Written string
}

func (b *fakeBooster) Boost(_ context.Context, recordingID int64, content string) (int64, error) {
	b.boosts = append(b.boosts, fakeBoost{RecordingID: recordingID, Content: content, Written: b.out.String()})
	if b.during != nil {
		b.during()
	}
	if b.err != nil {
		return 0, b.err
	}
	return 9000 + int64(len(b.boosts)), nil
}

func ackOptions(ledger *Ledger, out *bytes.Buffer) (HandoffOptions, *fakeBooster) {
	booster := &fakeBooster{out: out}
	opts := handoffOptions(ledger, out)
	opts.AckContent = "👀"
	opts.Booster = booster
	return opts, booster
}

// A request someone made of the agent is boosted on its recording, as the
// agent, before its line is written, and the line says which boost.
func TestARequestIsAcknowledgedBeforeItsLineIsWritten(t *testing.T) {
	ledger := newTestLedger(t)
	admitOn(t, ledger, 1, "recording:1")
	var out bytes.Buffer
	opts, booster := ackOptions(ledger, &out)

	require.NoError(t, handOffReady(context.Background(), opts))

	require.Len(t, booster.boosts, 1)
	assert.Equal(t, fakeBoost{RecordingID: 10304028972, Content: "👀"}, booster.boosts[0], "the request's own recording, before anything was written")
	lines := handedOffLines(t, &out)
	require.Len(t, lines, 1)
	assert.Equal(t, &HandoffAck{BoostID: 9001, Content: "👀"}, lines[0].Ack)
	assert.False(t, lines[0].Acknowledge, "no acknowledgement is owed, so a reader that predates ack does not boost again")
	assert.Equal(t, ReasonHandedOff, getRecord(t, ledger, 1).Reason)
}

// With the acknowledgement off, nothing is posted and the line has no ack.
func TestNoAcknowledgementWhenItIsTurnedOff(t *testing.T) {
	ledger := newTestLedger(t)
	admitOn(t, ledger, 1, "recording:1")
	var out bytes.Buffer
	opts, booster := ackOptions(ledger, &out)
	opts.AckContent = ""

	require.NoError(t, handOffReady(context.Background(), opts))

	assert.Empty(t, booster.boosts)
	lines := handedOffLines(t, &out)
	require.Len(t, lines, 1)
	assert.True(t, lines[0].Acknowledge, "the reader still owes the acknowledgement")
	assert.NotContains(t, out.String(), `"ack"`)
}

// A boost that fails stops nothing: the request is handed off without an
// ack, for its reader to acknowledge, and the failure is logged. It is not
// tried again.
func TestAFailedAcknowledgementStillHandsTheRequestOff(t *testing.T) {
	ledger := newTestLedger(t)
	admitOn(t, ledger, 1, "recording:1")
	var out, logs bytes.Buffer
	opts, booster := ackOptions(ledger, &out)
	booster.err = errors.New("archived Pings refuse agents")
	opts.Logger = slog.New(slog.NewTextHandler(&logs, nil))

	require.NoError(t, handOffReady(context.Background(), opts))

	assert.Len(t, booster.boosts, 1, "a failed boost may have landed, so it is never retried")
	lines := handedOffLines(t, &out)
	require.Len(t, lines, 1)
	assert.Nil(t, lines[0].Ack)
	assert.True(t, lines[0].Acknowledge, "the reader still owes the acknowledgement")
	assert.NotContains(t, out.String(), `"ack"`)
	assert.Contains(t, logs.String(), "level=WARN")
	assert.Contains(t, logs.String(), "archived Pings refuse agents")
	assert.Equal(t, ReasonHandedOff, getRecord(t, ledger, 1).Reason)
}

// A request handed off again, after a run stopped between its boost and
// closing its record, carries the boost it already got rather than another.
// Stdout failing is that stop: the boost is given, and the record is left
// open as a crash leaves it.
func TestARequestHandedOffAgainIsNotBoostedTwice(t *testing.T) {
	ledger := newTestLedger(t)
	admitOn(t, ledger, 1, "recording:1")
	var out bytes.Buffer
	opts, booster := ackOptions(ledger, &out)

	crashed := opts
	crashed.Lines = ndjson.NewWriter(failingWriter{})
	require.Error(t, handOffReady(context.Background(), crashed))
	require.Len(t, booster.boosts, 1)
	assert.Equal(t, StateAdmitted, getRecord(t, ledger, 1).State, "left to be handed off again")

	require.NoError(t, handOffReady(context.Background(), opts))

	assert.Len(t, booster.boosts, 1, "the boost already given is reused")
	lines := handedOffLines(t, &out)
	require.Len(t, lines, 1)
	assert.Equal(t, &HandoffAck{BoostID: 9001, Content: "👀"}, lines[0].Ack)
}

// Context is not a request: a comment on a followed thread, a completion and
// a boost are handed off unacknowledged.
func TestContextIsNotAcknowledged(t *testing.T) {
	for _, trigger := range []admission.Trigger{admission.TriggerSubscribed, admission.TriggerCompleted, admission.TriggerBoosted} {
		t.Run(string(trigger), func(t *testing.T) {
			ledger := newTestLedger(t)
			seenRecord(t, ledger, 1)
			v := admittedVerdict(1, 0, "recording:1")
			v.Trigger, v.Acknowledge = trigger, false
			_, err := ledger.Admission().Commit(context.Background(), v)
			require.NoError(t, err)
			var out bytes.Buffer
			opts, booster := ackOptions(ledger, &out)

			require.NoError(t, handOffReady(context.Background(), opts))

			assert.Empty(t, booster.boosts)
			lines := handedOffLines(t, &out)
			require.Len(t, lines, 1)
			assert.Nil(t, lines[0].Ack)
		})
	}
}

// A hold hands nothing off, so it boosts nothing either.
func TestAHoldAcknowledgesNothing(t *testing.T) {
	ledger := newTestLedger(t)
	_, err := ledger.SetHold(context.Background(), "operator", HoldByOperator)
	require.NoError(t, err)
	admitOn(t, ledger, 1, "recording:1")
	var out bytes.Buffer
	opts, booster := ackOptions(ledger, &out)

	require.NoError(t, handOffReady(context.Background(), opts))

	assert.Empty(t, booster.boosts)
	assert.Empty(t, out.String())
}

// The line's ack is the shape the skill reads.
func TestTheAckOnTheLineIsItsBoostIDAndContent(t *testing.T) {
	ledger := newTestLedger(t)
	admitOn(t, ledger, 1, "recording:1")
	var out bytes.Buffer
	opts, _ := ackOptions(ledger, &out)

	require.NoError(t, handOffReady(context.Background(), opts))

	var line map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(out.String())), &line))
	assert.JSONEq(t, `{"boost_id":9001,"content":"👀"}`, string(line["ack"]))
}

// A hold that lands while the boost is in flight keeps the request from
// being handed off, and the connector running; the boost stays recorded for
// a later redispatch rather than being given again.
func TestAHoldDuringTheAcknowledgementStopsTheHandoff(t *testing.T) {
	ledger := newTestLedger(t)
	admitOn(t, ledger, 1, "recording:1")
	var out bytes.Buffer
	opts, booster := ackOptions(ledger, &out)
	booster.during = func() {
		_, err := ledger.SetHold(context.Background(), "operator", HoldByOperator)
		require.NoError(t, err)
	}

	require.NoError(t, handOffReady(context.Background(), opts))

	assert.Len(t, booster.boosts, 1)
	assert.Empty(t, strings.TrimSpace(out.String()), "nothing is handed off once held")
	assert.Equal(t, StateHeld, getRecord(t, ledger, 1).State)
	ack, ok, err := ledger.ack(context.Background(), 1)
	require.NoError(t, err)
	require.True(t, ok, "the boost given is recorded")
	assert.Equal(t, int64(9001), ack.BoostID)
}

// A request handed off again after a crash carries the boost it got even when
// the restart turns boosting off, so its reader does not boost a second time.
func TestABoostAlreadyGivenSurvivesTurningAcknowledgementOff(t *testing.T) {
	ledger := newTestLedger(t)
	admitOn(t, ledger, 1, "recording:1")
	var out bytes.Buffer
	opts, booster := ackOptions(ledger, &out)

	crashed := opts
	crashed.Lines = ndjson.NewWriter(failingWriter{})
	require.Error(t, handOffReady(context.Background(), crashed))
	require.Len(t, booster.boosts, 1)

	opts.AckContent = ""
	require.NoError(t, handOffReady(context.Background(), opts))

	assert.Len(t, booster.boosts, 1, "no second boost")
	lines := handedOffLines(t, &out)
	require.Len(t, lines, 1)
	assert.Equal(t, &HandoffAck{BoostID: 9001, Content: "👀"}, lines[0].Ack)
	assert.False(t, lines[0].Acknowledge, "nothing is owed")
}
