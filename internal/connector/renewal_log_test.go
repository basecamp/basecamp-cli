package connector

import (
	"bufio"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp/eventfeed"
	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp/eventfeed/feedtest"
)

// logLines decodes a JSON log, one record per line.
func logLines(t *testing.T, raw string) []map[string]any {
	t.Helper()
	var lines []map[string]any
	scanner := bufio.NewScanner(strings.NewReader(raw))
	for scanner.Scan() {
		var line map[string]any
		require.NoError(t, json.Unmarshal(scanner.Bytes(), &line), "log line %q", scanner.Text())
		lines = append(lines, line)
	}
	require.NoError(t, scanner.Err())
	return lines
}

// logLine returns the one record whose message is msg, or nil.
func logLine(t *testing.T, raw, msg string) map[string]any {
	t.Helper()
	var found map[string]any
	for _, line := range logLines(t, raw) {
		if line["msg"] == msg {
			require.Nil(t, found, "more than one %q line", msg)
			found = line
		}
	}
	return found
}

// writtenCommand returns the identifier of the first write on conn whose
// command is command.
func writtenCommand(conn *feedtest.Conn, command string) (string, bool) {
	for _, write := range conn.Writes() {
		var c struct {
			Command    string `json:"command"`
			Identifier string `json:"identifier"`
		}
		if json.Unmarshal(write, &c) == nil && c.Command == command && c.Identifier != "" {
			return c.Identifier, true
		}
	}
	return "", false
}

// A server that bounds the stream advertises its deadline in the welcome, and
// the package renews the socket in band before it. Through the real run loop:
// the advert and the renewal each reach the connector's log, with the
// deadlines they carry, and the fresh ticket the renewal sends does not.
func TestAnInBandRenewalReachesTheConnectorLog(t *testing.T) {
	var logs safeBuffer
	ledger := newTestLedger(t)
	intake, transport, minter, polls := newFeedIntake(t, ledger, Options{
		Logger: slog.New(slog.NewJSONHandler(&logs, nil)),
	})
	minter.ScriptTicket(ticket())
	const renewalTicket = "renewal-ticket-that-must-stay-out-of-the-log"
	minter.ScriptTicket(eventfeed.StreamTicket{Ticket: renewalTicket, ExpiresIn: 120,
		URL: "wss://cable.basecamp.com/cable?ticket=" + renewalTicket})
	polls.ScriptPage(eventfeed.PollPage{Position: "p1"})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runInBackground(ctx, t, intake)

	var conn *feedtest.Conn
	require.Eventually(t, func() bool {
		conn = transport.LastConn()
		return conn != nil
	}, 5*time.Second, 5*time.Millisecond, "the connector should dial")

	// Inside the package's renewal lead already, so the renewal is due as soon
	// as the socket is streaming: no wait on the wall clock.
	advertised := time.Now().Add(2 * time.Minute).UTC().Truncate(time.Millisecond)
	conn.Serve(mustJSON(t, map[string]any{"type": "welcome", "stream_expires_at": advertised.Format(time.RFC3339Nano)}))

	var identifier string
	require.Eventually(t, func() bool {
		var ok bool
		identifier, ok = writtenCommand(conn, "subscribe")
		return ok
	}, 5*time.Second, 5*time.Millisecond, "the connector should subscribe")
	conn.Serve(confirmFrame(t, identifier))

	require.Eventually(t, func() bool {
		_, ok := writtenCommand(conn, "renew")
		return ok
	}, 5*time.Second, 5*time.Millisecond, "a socket streaming inside its renewal lead renews")

	renewed := advertised.Add(time.Hour)
	conn.Serve(mustJSON(t, map[string]any{"identifier": identifier, "type": "renewed", "stream_expires_at": renewed.Format(time.RFC3339Nano)}))

	require.Eventually(t, func() bool {
		return strings.Contains(logs.String(), "feed stream renewed in band")
	}, 5*time.Second, 5*time.Millisecond, "the renewal reaches the log")

	cancel()
	awaitReturn(t, done, "Run should return on shutdown")

	raw := logs.String()
	deadline := logLine(t, raw, "feed stream deadline advertised; renewing in band before it")
	require.NotNil(t, deadline, "the welcome's advert reaches the log:\n%s", raw)
	assert.Equal(t, "INFO", deadline["level"])
	assert.Equal(t, advertised.Format(time.RFC3339Nano), deadline["expires_at"])

	renewal := logLine(t, raw, "feed stream renewed in band")
	require.NotNil(t, renewal)
	assert.Equal(t, "INFO", renewal["level"])
	assert.Equal(t, renewed.Format(time.RFC3339Nano), renewal["expires_at"])

	for _, line := range logLines(t, raw) {
		assert.NotContains(t, line["msg"], "renewal failed", "nothing failed here")
		assert.NotContains(t, line["msg"], "renewal gave up", "nothing failed here")
	}
	assert.NotContains(t, raw, renewalTicket, "the renewal's ticket is a bearer credential")
}

// A failed renewal is retried once, then left to the server's lease, whose
// `remote` close the connector recovers from as from any other. The log says
// which: the first is a retry, and the second explains the close that follows
// it. The kind is the package's closed vocabulary, never the server's reason.
func TestAFailedRenewalSaysWhetherItIsRetriedOrLeftToTheLease(t *testing.T) {
	var logs safeBuffer
	intake, _, _ := newTestIntake(t, nil, nil)
	intake.log = slog.New(slog.NewJSONHandler(&logs, nil))

	observer := intake.observer(context.Background())
	require.NotNil(t, observer.RenewFailed)
	observer.RenewFailed(eventfeed.RenewRefused, true)
	observer.RenewFailed(eventfeed.RenewNoReply, false)

	lines := logLines(t, logs.String())
	require.Len(t, lines, 2)

	assert.Equal(t, "WARN", lines[0]["level"])
	assert.Equal(t, "feed stream renewal failed; retrying", lines[0]["msg"])
	assert.Equal(t, "refused", lines[0]["kind"])

	assert.Equal(t, "WARN", lines[1]["level"])
	assert.Equal(t, "feed stream renewal gave up; the server closes the socket at its deadline, and the connector reconnects and catches up", lines[1]["msg"])
	assert.Equal(t, "no_reply", lines[1]["kind"])
}
