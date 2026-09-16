package commands

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// The positions, tickets and cable URLs in these fixtures are invented
// strings. Real ones are credentials — a position is a signed resume token, a
// ticket is a replayable bearer — and neither belongs in a repository.

const (
	feedEventsPath = "/99999/events.json"
	feedInboxPath  = "/99999/inbox.json"
	feedTicketPath = "/99999/events/stream_ticket.json"
	feedBaseURL    = "https://3.basecampapi.com"
)

func feedEventJSON(id int64) string {
	return fmt.Sprintf(`{
		"id": %d,
		"kind": "message_created",
		"action": "created",
		"event_type": "message.created",
		"bucket_id": 2085958499,
		"creator_id": 1049715945,
		"performed_by_id": null,
		"recording_id": 1069479766,
		"created_at": "2026-07-14T06:10:00.159Z"
	}`, id)
}

func feedPageJSON(position, next string, ids ...int64) string {
	events := make([]string, 0, len(ids))
	for _, id := range ids {
		events = append(events, feedEventJSON(id))
	}
	body := fmt.Sprintf(`{"events":[%s],"position":%q`, joinJSON(events), position)
	if next != "" {
		body += fmt.Sprintf(`,"next":%q`, next)
	}
	return body + "}"
}

func inboxPageJSON(position, next string, addressingIDs ...int64) string {
	items := make([]string, 0, len(addressingIDs))
	for _, id := range addressingIDs {
		items = append(items, fmt.Sprintf(
			`{"addressing_id":%d,"reason":"mentioned","addressed_at":"2026-07-14T06:10:00.159Z","event":%s}`,
			id, feedEventJSON(1071915468)))
	}
	body := fmt.Sprintf(`{"items":[%s],"position":%q`, joinJSON(items), position)
	if next != "" {
		body += fmt.Sprintf(`,"next":%q`, next)
	}
	return body + "}"
}

func joinJSON(parts []string) string {
	out := ""
	for i, part := range parts {
		if i > 0 {
			out += ","
		}
		out += part
	}
	return out
}

// setupFeedApp is the recording harness with a JSON envelope writer and the
// configured API base the continuation check reads.
func setupFeedApp(t *testing.T, routes ...stubRoute) (*appctx.App, *recordingTransport, *bytes.Buffer) {
	t.Helper()
	app, transport := setupRecordingTestApp(t, routes...)
	app.Config.BaseURL = feedBaseURL
	out := &bytes.Buffer{}
	app.Output = output.New(output.Options{Format: output.FormatJSON, Writer: out})
	return app, transport, out
}

// feedEnvelope is the slice of the success envelope these tests read.
type feedEnvelope struct {
	OK      bool            `json:"ok"`
	Data    json.RawMessage `json:"data"`
	Summary string          `json:"summary"`
	Notice  string          `json:"notice"`
}

func decodeFeedEnvelope(t *testing.T, out *bytes.Buffer) feedEnvelope {
	t.Helper()
	var envelope feedEnvelope
	require.NoError(t, json.Unmarshal(out.Bytes(), &envelope))
	return envelope
}

type feedPollData struct {
	Events []struct {
		ID int64 `json:"id"`
	} `json:"events"`
	Position string `json:"position"`
	Next     string `json:"next"`
}

type inboxPollData struct {
	Items []struct {
		AddressingID int64 `json:"addressing_id"`
	} `json:"items"`
	Position string `json:"position"`
	Next     string `json:"next"`
}

func requireFeedError(t *testing.T, err error) *output.Error {
	t.Helper()
	require.Error(t, err)
	cliErr := output.AsError(err)
	require.NotNil(t, cliErr)
	return cliErr
}

func eventsRoute(status int, bodies ...string) stubRoute {
	return stubRoute{method: http.MethodGet, path: feedEventsPath, status: status, bodies: bodies}
}

func inboxRoute(status int, bodies ...string) stubRoute {
	return stubRoute{method: http.MethodGet, path: feedInboxPath, status: status, bodies: bodies}
}

func TestEventsPollServesOnePageAndItsPosition(t *testing.T) {
	app, transport, out := setupFeedApp(t, eventsRoute(http.StatusOK, feedPageJSON("pos-1", "", 11, 12)))

	err := executeRecordingCommand(NewEventsCmd(), app, "poll", "--since", "now")
	require.NoError(t, err)

	envelope := decodeFeedEnvelope(t, out)
	var data feedPollData
	require.NoError(t, json.Unmarshal(envelope.Data, &data))
	assert.Equal(t, []int64{11, 12}, []int64{data.Events[0].ID, data.Events[1].ID})
	assert.Equal(t, "pos-1", data.Position)
	assert.Empty(t, data.Next)
	assert.Equal(t, "2 event(s)", envelope.Summary)

	assert.Equal(t, []string{"since=now"}, transport.queriesFor(feedEventsPath))
}

func TestEventsPollSendsEveryFilterCommaJoined(t *testing.T) {
	app, transport, _ := setupFeedApp(t, eventsRoute(http.StatusOK, feedPageJSON("pos-1", "")))

	err := executeRecordingCommand(NewEventsCmd(), app, "poll",
		"--types", "message.created,comment.created",
		"--buckets", "1,2",
		"--creators", "9",
		"--performers", "self,7",
		"--exclude-performers", "self",
		"--actor-types", "agent,person",
	)
	require.NoError(t, err)

	query, err := url.ParseQuery(transport.last(t).Query)
	require.NoError(t, err)
	assert.Equal(t, "message.created,comment.created", query.Get("types"))
	assert.Equal(t, "1,2", query.Get("buckets"))
	assert.Equal(t, "9", query.Get("creators"))
	assert.Equal(t, "self,7", query.Get("performers"))
	assert.Equal(t, "self", query.Get("exclude_performers"))
	assert.Equal(t, "agent,person", query.Get("actor_types"))
}

// An empty page renders as [] rather than null, and next is always present —
// empty means the walk is done — so a consumer iterating .data.events or
// testing .data.next never has to handle a missing key.
func TestEventsPollEmptyPageKeepsTheEnvelopesShape(t *testing.T) {
	app, _, out := setupFeedApp(t, eventsRoute(http.StatusOK, feedPageJSON("pos-1", "")))

	require.NoError(t, executeRecordingCommand(NewEventsCmd(), app, "poll", "--since", "now"))

	var envelope struct {
		Data struct {
			Events   *[]json.RawMessage `json:"events"`
			Next     *string            `json:"next"`
			Position string             `json:"position"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &envelope))
	require.NotNil(t, envelope.Data.Events)
	assert.Empty(t, *envelope.Data.Events)
	require.NotNil(t, envelope.Data.Next)
	assert.Empty(t, *envelope.Data.Next)
	assert.Equal(t, "pos-1", envelope.Data.Position)
}

func TestEventsPollWithoutAllStopsAtOnePageAndReportsNext(t *testing.T) {
	next := feedBaseURL + feedEventsPath + "?position=pos-1"
	app, transport, out := setupFeedApp(t, eventsRoute(http.StatusOK,
		feedPageJSON("pos-1", next, 11),
		feedPageJSON("pos-2", "", 12),
	))

	require.NoError(t, executeRecordingCommand(NewEventsCmd(), app, "poll", "--since", "0"))

	envelope := decodeFeedEnvelope(t, out)
	var data feedPollData
	require.NoError(t, json.Unmarshal(envelope.Data, &data))
	assert.Len(t, data.Events, 1)
	assert.Equal(t, next, data.Next)
	assert.Contains(t, envelope.Summary, "more to serve")
	assert.Len(t, transport.queriesFor(feedEventsPath), 1)
}

func TestEventsPollAllFollowsNextUntilItIsAbsent(t *testing.T) {
	next := feedBaseURL + feedEventsPath + "?position=pos-1&types=message.created"
	app, transport, out := setupFeedApp(t, eventsRoute(http.StatusOK,
		feedPageJSON("pos-1", next, 11),
		feedPageJSON("pos-2", "", 12, 13),
	))

	require.NoError(t, executeRecordingCommand(NewEventsCmd(), app, "poll", "--since", "0", "--all"))

	envelope := decodeFeedEnvelope(t, out)
	var data feedPollData
	require.NoError(t, json.Unmarshal(envelope.Data, &data))
	assert.Len(t, data.Events, 3)
	assert.Equal(t, "pos-2", data.Position)
	assert.Empty(t, data.Next)
	assert.Empty(t, envelope.Notice)

	queries := transport.queriesFor(feedEventsPath)
	require.Len(t, queries, 2)
	assert.Equal(t, "since=0", queries[0])
	// The second request re-issues the continuation's own query, filters
	// included, rather than the flags the first call was given.
	second, err := url.ParseQuery(queries[1])
	require.NoError(t, err)
	assert.Equal(t, "pos-1", second.Get("position"))
	assert.Equal(t, "message.created", second.Get("types"))
	assert.Empty(t, second.Get("since"))
}

func TestEventsPollAllStopsAtMaxPagesAndSaysSo(t *testing.T) {
	next := feedBaseURL + feedEventsPath + "?position=pos-1"
	app, transport, out := setupFeedApp(t, eventsRoute(http.StatusOK,
		feedPageJSON("pos-1", next, 11),
		feedPageJSON("pos-2", next, 12),
	))

	require.NoError(t, executeRecordingCommand(NewEventsCmd(), app, "poll", "--since", "0", "--all", "--max-pages", "1"))

	envelope := decodeFeedEnvelope(t, out)
	assert.Contains(t, envelope.Notice, "--max-pages 1")
	assert.NotContains(t, envelope.Summary, "more to serve")
	assert.Len(t, transport.queriesFor(feedEventsPath), 1)
}

func TestEventsPollRefusesACrossOriginContinuation(t *testing.T) {
	app, transport, _ := setupFeedApp(t, eventsRoute(http.StatusOK,
		feedPageJSON("pos-1", "https://evil.example/99999/events.json?position=pos-1", 11),
	))

	err := executeRecordingCommand(NewEventsCmd(), app, "poll", "--since", "0", "--all")

	cliErr := requireFeedError(t, err)
	assert.Equal(t, output.CodeAPI, cliErr.Code)
	assert.Contains(t, cliErr.Message, "outside the configured Basecamp API host")
	assert.Len(t, transport.queriesFor(feedEventsPath), 1)
}

func TestEventsPollFilterMismatchNamesBothDigests(t *testing.T) {
	app, _, _ := setupFeedApp(t, eventsRoute(http.StatusConflict,
		`{"error":"position was minted for a different filter set","position_digest":"44136fa355b3678a","filters_digest":"38b223c13c89dc89"}`))

	err := executeRecordingCommand(NewEventsCmd(), app, "poll", "--position", "stale",
		"--types", "message.created", "--exclude-performers", "self")

	cliErr := requireFeedError(t, err)
	assert.Equal(t, output.CodeUsage, cliErr.Code)
	assert.Equal(t, output.ExitUsage, cliErr.ExitCode())
	assert.Equal(t, http.StatusConflict, cliErr.HTTPStatus)
	assert.Contains(t, cliErr.Message, "44136fa355b3678a")
	assert.Contains(t, cliErr.Message, "38b223c13c89dc89")
	// The filters the caller presented survive the recovery command: a
	// re-entry without them is a different filter set again.
	assert.Contains(t, cliErr.Hint,
		"basecamp events poll --since now --types message.created --exclude-performers self")
}

func TestEventsPollPositionGoneCarriesTheResumeURLAndTheEpoch(t *testing.T) {
	resume := feedBaseURL + feedEventsPath + "?since=900"
	app, _, _ := setupFeedApp(t, eventsRoute(http.StatusGone,
		fmt.Sprintf(`{"error":"position predates the feed epoch","epoch_after_id":900,"resume":%q}`, resume)))

	err := executeRecordingCommand(NewEventsCmd(), app, "poll", "--position", "ancient")

	cliErr := requireFeedError(t, err)
	assert.Equal(t, output.CodeNotFound, cliErr.Code)
	assert.Equal(t, output.ExitNotFound, cliErr.ExitCode())
	assert.Equal(t, http.StatusGone, cliErr.HTTPStatus)
	assert.Contains(t, cliErr.Message, resume)
	assert.Equal(t, "Re-enter with: basecamp events poll --since 900", cliErr.Hint)
}

func TestEventsPollBadRequestKeepsTheServersMessage(t *testing.T) {
	app, _, _ := setupFeedApp(t, eventsRoute(http.StatusBadRequest,
		`{"error":"unknown type: message.exploded"}`))

	err := executeRecordingCommand(NewEventsCmd(), app, "poll", "--types", "message.exploded")

	cliErr := requireFeedError(t, err)
	assert.Contains(t, cliErr.Error(), "message.exploded")
	assert.Equal(t, http.StatusBadRequest, cliErr.HTTPStatus)
}

func TestEventsPollRejectsUnservableFlagsBeforeAnyRequest(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"since and position together", []string{"--since", "now", "--position", "p"}, "mutually exclusive"},
		{"since that is not an id", []string{"--since", "2026-07-14"}, "Invalid --since"},
		{"bucket that is not an id", []string{"--buckets", "one"}, "Invalid --buckets id"},
		{"creator that is not an id", []string{"--creators", "1,two"}, "Invalid --creators id"},
		{"performer that is neither an id nor self", []string{"--performers", "everyone"}, "Invalid --performers"},
		{"actor type outside the pair", []string{"--actor-types", "robot"}, "Invalid --actor-types"},
		{"max pages below one", []string{"--all", "--max-pages", "0"}, "--max-pages must be at least 1"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			app, transport, _ := setupFeedApp(t, eventsRoute(http.StatusOK, feedPageJSON("pos-1", "")))

			err := executeRecordingCommand(NewEventsCmd(), app, append([]string{"poll"}, testCase.args...)...)

			cliErr := requireFeedError(t, err)
			assert.Equal(t, output.CodeUsage, cliErr.Code)
			assert.Contains(t, cliErr.Message, testCase.want)
			assert.Empty(t, transport.recorded())
		})
	}
}

func TestEventsPollTrailingCommaIsNotAnEmptyFilter(t *testing.T) {
	app, transport, _ := setupFeedApp(t, eventsRoute(http.StatusOK, feedPageJSON("pos-1", "")))

	require.NoError(t, executeRecordingCommand(NewEventsCmd(), app, "poll", "--types", "message.created,"))

	query, err := url.ParseQuery(transport.last(t).Query)
	require.NoError(t, err)
	assert.Equal(t, "message.created", query.Get("types"))
}

func TestBareEventsShowsHelpRatherThanPolling(t *testing.T) {
	app, transport, _ := setupFeedApp(t)

	require.NoError(t, executeRecordingCommand(NewEventsCmd(), app))

	assert.Empty(t, transport.recorded())
}

func TestInboxServesItemsAndItsPosition(t *testing.T) {
	app, transport, out := setupFeedApp(t, inboxRoute(http.StatusOK, inboxPageJSON("inbox-pos-1", "", 991, 992)))

	require.NoError(t, executeRecordingCommand(NewInboxCmd(), app, "--since", "0", "--reasons", "mentioned,assigned"))

	envelope := decodeFeedEnvelope(t, out)
	var data inboxPollData
	require.NoError(t, json.Unmarshal(envelope.Data, &data))
	assert.Equal(t, int64(991), data.Items[0].AddressingID)
	assert.Equal(t, "inbox-pos-1", data.Position)
	assert.Equal(t, "2 addressed item(s)", envelope.Summary)

	query, err := url.ParseQuery(transport.last(t).Query)
	require.NoError(t, err)
	assert.Equal(t, "0", query.Get("since"))
	assert.Equal(t, "mentioned,assigned", query.Get("reasons"))
}

func TestInboxAllFollowsNextUntilItIsAbsent(t *testing.T) {
	next := feedBaseURL + feedInboxPath + "?position=inbox-pos-1&reasons=mentioned"
	app, transport, out := setupFeedApp(t, inboxRoute(http.StatusOK,
		inboxPageJSON("inbox-pos-1", next, 991),
		inboxPageJSON("inbox-pos-2", "", 992),
	))

	require.NoError(t, executeRecordingCommand(NewInboxCmd(), app, "--since", "0", "--all"))

	envelope := decodeFeedEnvelope(t, out)
	var data inboxPollData
	require.NoError(t, json.Unmarshal(envelope.Data, &data))
	assert.Len(t, data.Items, 2)
	assert.Equal(t, "inbox-pos-2", data.Position)

	queries := transport.queriesFor(feedInboxPath)
	require.Len(t, queries, 2)
	second, err := url.ParseQuery(queries[1])
	require.NoError(t, err)
	assert.Equal(t, "inbox-pos-1", second.Get("position"))
	assert.Equal(t, "mentioned", second.Get("reasons"))
}

func TestInboxForbiddenSaysItIsAgentsOnly(t *testing.T) {
	app, _, _ := setupFeedApp(t, inboxRoute(http.StatusForbidden, ``))

	err := executeRecordingCommand(NewInboxCmd(), app, "--since", "now")

	cliErr := requireFeedError(t, err)
	assert.Equal(t, output.CodeForbidden, cliErr.Code)
	assert.Equal(t, output.ExitForbidden, cliErr.ExitCode())
	assert.Equal(t, "The inbox is served to agent principals only", cliErr.Message)
	assert.Contains(t, cliErr.Hint, "basecamp events poll")
}

func TestInboxPositionGoneReEntersAtTheEarliestRetainedItem(t *testing.T) {
	resume := feedBaseURL + feedInboxPath + "?since=0"
	app, _, _ := setupFeedApp(t, inboxRoute(http.StatusGone,
		fmt.Sprintf(`{"error":"position fell behind the retention window","resume":%q}`, resume)))

	err := executeRecordingCommand(NewInboxCmd(), app, "--position", "ancient")

	cliErr := requireFeedError(t, err)
	assert.Equal(t, output.CodeNotFound, cliErr.Code)
	assert.Contains(t, cliErr.Message, resume)
	assert.Equal(t, "Re-enter with: basecamp inbox --since 0", cliErr.Hint)
}

// The two lanes number different things, so the remedy an invalid --since
// offers has to name the lane's own identifier rather than the feed's.
func TestInvalidSinceNamesTheLanesOwnIdentifier(t *testing.T) {
	app, _, _ := setupFeedApp(t)
	err := executeRecordingCommand(NewEventsCmd(), app, "poll", "--since", "yesterday")
	assert.Contains(t, requireFeedError(t, err).Hint, "event id")

	app, _, _ = setupFeedApp(t)
	err = executeRecordingCommand(NewInboxCmd(), app, "--since", "yesterday")
	assert.Contains(t, requireFeedError(t, err).Hint, "inbox item id")
}

func TestInboxRejectsUnservableFlagsBeforeAnyRequest(t *testing.T) {
	app, transport, _ := setupFeedApp(t, inboxRoute(http.StatusOK, inboxPageJSON("inbox-pos-1", "")))

	err := executeRecordingCommand(NewInboxCmd(), app, "--since", "now", "--position", "p")

	cliErr := requireFeedError(t, err)
	assert.Equal(t, output.CodeUsage, cliErr.Code)
	assert.Empty(t, transport.recorded())
}

func TestEventsTicketRedactsTheTicketAndItsURL(t *testing.T) {
	app, _, out := setupFeedApp(t, stubRoute{
		method: http.MethodPost,
		path:   feedTicketPath,
		status: http.StatusOK,
		body:   `{"ticket":"tkt-secret","expires_in":120,"url":"wss://chat.example.test/195539477?ticket=tkt-secret"}`,
	})

	require.NoError(t, executeRecordingCommand(NewEventsCmd(), app, "ticket"))

	rendered := out.String()
	assert.NotContains(t, rendered, "tkt-secret")
	assert.Contains(t, rendered, "195539477")
	assert.Contains(t, decodeFeedEnvelope(t, out).Summary, "120s")
}

func TestEventsTicketShowSecretPrintsItVerbatim(t *testing.T) {
	app, _, out := setupFeedApp(t, stubRoute{
		method: http.MethodPost,
		path:   feedTicketPath,
		status: http.StatusOK,
		body:   `{"ticket":"tkt-secret","expires_in":120,"url":"wss://chat.example.test/195539477?ticket=tkt-secret"}`,
	})

	require.NoError(t, executeRecordingCommand(NewEventsCmd(), app, "ticket", "--show-secret"))

	assert.Contains(t, out.String(), "wss://chat.example.test/195539477?ticket=tkt-secret")
	assert.Empty(t, decodeFeedEnvelope(t, out).Notice)
}

func TestRedactTicketURLWithholdsWhatItCannotReduce(t *testing.T) {
	assert.Equal(t, redactedTicket, redactTicketURL("wss://chat.example.test/195539477"))
	assert.Equal(t, redactedTicket, redactTicketURL("://"))

	redacted := redactTicketURL("wss://chat.example.test/195539477?ticket=tkt-secret")
	assert.NotContains(t, redacted, "tkt-secret")
	assert.Contains(t, redacted, "chat.example.test/195539477")
}

// A 410 on a filtered poll must hand back a re-entry that still carries the
// filters — the loop guard above all, since an agent that re-enters without
// it recovers straight into its own activity.
func TestPositionGoneRebuildsTheFiltersFromTheResumeURL(t *testing.T) {
	resume := feedBaseURL + feedEventsPath +
		"?since=900&types=message.created,comment.created&buckets=1,2&exclude_performers=51177542&actor_types=agent"
	app, _, _ := setupFeedApp(t, eventsRoute(http.StatusGone,
		fmt.Sprintf(`{"error":"position predates the feed epoch","epoch_after_id":900,"resume":%q}`, resume)))

	err := executeRecordingCommand(NewEventsCmd(), app, "poll", "--position", "ancient",
		"--types", "message.created,comment.created")

	assert.Equal(t,
		"Re-enter with: basecamp events poll --since 900 --types message.created,comment.created"+
			" --buckets 1,2 --exclude-performers 51177542 --actor-types agent",
		requireFeedError(t, err).Hint)
}

func TestInboxPositionGoneRebuildsItsOwnFilters(t *testing.T) {
	resume := feedBaseURL + feedInboxPath + "?since=0&reasons=mentioned,assigned"
	app, _, _ := setupFeedApp(t, inboxRoute(http.StatusGone,
		fmt.Sprintf(`{"error":"position fell behind the retention window","resume":%q}`, resume)))

	err := executeRecordingCommand(NewInboxCmd(), app, "--position", "ancient")

	assert.Equal(t, "Re-enter with: basecamp inbox --since 0 --reasons mentioned,assigned",
		requireFeedError(t, err).Hint)
}

// A resume URL carrying anything outside srv2's alphabet is not what the
// contract describes, so the hint falls back to the bare re-entry rather than
// pasting server text into a command line.
func TestPositionGoneFallsBackWhenTheResumeURLIsNotRenderable(t *testing.T) {
	resume := feedBaseURL + feedEventsPath + "?since=900&types=" + url.QueryEscape("message.created; rm -rf /")
	app, _, _ := setupFeedApp(t, eventsRoute(http.StatusGone,
		fmt.Sprintf(`{"error":"position predates the feed epoch","epoch_after_id":900,"resume":%q}`, resume)))

	err := executeRecordingCommand(NewEventsCmd(), app, "poll", "--position", "ancient")

	assert.Equal(t, "Re-enter with: basecamp events poll --since 900", requireFeedError(t, err).Hint)
}

// --count counts the page's rows and --ids-only prints their identities; for
// the inbox that identity is the addressing id, never the event id.
func TestEnumeratingOutputModesSeeTheRows(t *testing.T) {
	app, _, out := setupFeedApp(t, eventsRoute(http.StatusOK, feedPageJSON("pos-1", "", 11, 12)))
	app.Output = output.New(output.Options{Format: output.FormatCount, Writer: out})
	require.NoError(t, executeRecordingCommand(NewEventsCmd(), app, "poll", "--since", "now"))
	assert.Equal(t, "2\n", out.String())

	app, _, out = setupFeedApp(t, eventsRoute(http.StatusOK, feedPageJSON("pos-1", "", 11, 12)))
	app.Output = output.New(output.Options{Format: output.FormatIDs, Writer: out})
	require.NoError(t, executeRecordingCommand(NewEventsCmd(), app, "poll", "--since", "now"))
	assert.Equal(t, "11\n12\n", out.String())

	app, _, out = setupFeedApp(t, inboxRoute(http.StatusOK, inboxPageJSON("inbox-pos-1", "", 991, 992)))
	app.Output = output.New(output.Options{Format: output.FormatIDs, Writer: out})
	require.NoError(t, executeRecordingCommand(NewInboxCmd(), app, "--since", "now"))
	assert.Equal(t, "991\n992\n", out.String())
}

func TestContinuationOriginComparisonNormalizesDefaultPortsAndCase(t *testing.T) {
	require.NoError(t, checkContinuation("https://3.basecampapi.com:443", feedBaseURL+feedEventsPath+"?position=p"))
	require.NoError(t, checkContinuation(feedBaseURL, "HTTPS://3.BasecampAPI.com"+feedEventsPath+"?position=p"))
	require.Error(t, checkContinuation(feedBaseURL, "https://3.basecampapi.com:8443"+feedEventsPath))
}

// A position is bound to its filter set, so a resume breadcrumb that dropped
// the filters would hand back a command that 409s.
func TestResumeBreadcrumbCarriesTheFilters(t *testing.T) {
	app, _, out := setupFeedApp(t, eventsRoute(http.StatusOK, feedPageJSON("pos-1", "", 11)))
	app.Flags.Hints = true

	require.NoError(t, executeRecordingCommand(NewEventsCmd(), app, "poll", "--since", "now",
		"--types", "message.created", "--buckets", "1,2", "--exclude-performers", "self"))

	assert.Equal(t,
		"basecamp events poll --position <position> --types message.created"+
			" --buckets 1,2 --exclude-performers self",
		resumeBreadcrumb(t, out))
}

func TestInboxResumeBreadcrumbCarriesItsOwnFilters(t *testing.T) {
	app, _, out := setupFeedApp(t, inboxRoute(http.StatusOK, inboxPageJSON("inbox-pos-1", "", 991)))
	app.Flags.Hints = true

	require.NoError(t, executeRecordingCommand(NewInboxCmd(), app, "--since", "now", "--reasons", "mentioned"))

	assert.Equal(t, "basecamp inbox --position <position> --reasons mentioned", resumeBreadcrumb(t, out))
}

// resumeBreadcrumb returns the command the response's resume breadcrumb
// offers, failing the test when there is none.
func resumeBreadcrumb(t *testing.T, out *bytes.Buffer) string {
	t.Helper()
	var envelope struct {
		Breadcrumbs []struct {
			Action string `json:"action"`
			Cmd    string `json:"cmd"`
		} `json:"breadcrumbs"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &envelope))
	for _, crumb := range envelope.Breadcrumbs {
		if crumb.Action == "resume" {
			return crumb.Cmd
		}
	}
	t.Fatal("no resume breadcrumb in the response")
	return ""
}

// A 410 body is server text. Its resume URL is displayed as authoritative and
// read for filters, which is acting on it, so it faces the same origin check
// the walk applies to a continuation — and the re-entry falls back to the
// filters this request was invoked with.
func TestPositionGoneRefusesAForeignResumeURL(t *testing.T) {
	resume := "https://evil.example/99999/events.json?since=900&types=attacker.controlled"
	app, _, _ := setupFeedApp(t, eventsRoute(http.StatusGone,
		fmt.Sprintf(`{"error":"position predates the feed epoch","epoch_after_id":900,"resume":%q}`, resume)))

	err := executeRecordingCommand(NewEventsCmd(), app, "poll", "--position", "ancient",
		"--types", "message.created")

	cliErr := requireFeedError(t, err)
	assert.NotContains(t, cliErr.Message, "evil.example")
	assert.NotContains(t, cliErr.Hint, "attacker.controlled")
	assert.Equal(t, "Re-enter with: basecamp events poll --since 900 --types message.created", cliErr.Hint)
}

func TestCheckContinuationFallsBackToTheDefaultHost(t *testing.T) {
	require.NoError(t, checkContinuation("", feedBaseURL+feedEventsPath+"?position=p"))
	require.Error(t, checkContinuation("", "http://3.basecampapi.com/99999/events.json"))
	require.Error(t, checkContinuation("", "https://evil.example/99999/events.json"))
}
