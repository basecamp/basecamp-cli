package mcpserver

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/basecamp/mcp/mcptest"
)

// feedSession serves one canned answer to whatever the feed asks for, and
// records the request that asked.
func feedSession(t *testing.T, status int, body string, seen *http.Request) *mcp.ClientSession {
	t.Helper()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if seen != nil {
			*seen = *r
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Request-Id", "req-feed-1")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(upstream.Close)

	srv, err := New(newTestAPI(upstream), Config{})
	require.NoError(t, err)
	return mcptest.Connect(t, srv.BuildMCPServer(slog.New(slog.DiscardHandler)))
}

// feedErrorOf reads the typed in-band error off a failed feed call.
func feedErrorOf(t *testing.T, text string) map[string]any {
	t.Helper()
	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(text), &payload), "the error must arrive as JSON: %s", text)
	detail, ok := payload["error"].(map[string]any)
	require.True(t, ok, "the error payload must be an object: %s", text)
	return detail
}

func callFeed(t *testing.T, session *mcp.ClientSession, action string, params map[string]any) (string, bool) {
	t.Helper()
	return mcptest.CallText(t, session, "basecamp_eventfeed", map[string]any{
		"action": action,
		"params": params,
	})
}

// The feed's filters go out comma-joined on one key per dimension, the form
// BC3 reads — never as repeated bare keys, which it does not.
func TestFeedPollBuildsTheAccountScopedRequest(t *testing.T) {
	var seen http.Request
	session := feedSession(t, http.StatusOK, `{"events":[],"position":"aBcD"}`, &seen)

	text, isError := callFeed(t, session, "poll_events", map[string]any{
		"since":              "1071915468",
		"types":              "message.created,comment.created",
		"buckets":            "2085958499",
		"performers":         "self",
		"exclude_performers": "1049715945,self",
		"actor_types":        "agent,person",
	})
	require.False(t, isError, text)

	assert.Equal(t, http.MethodGet, seen.Method)
	assert.Equal(t, "/999/events.json", seen.URL.Path)
	query := seen.URL.Query()
	assert.Equal(t, "1071915468", query.Get("since"))
	assert.Equal(t, "message.created,comment.created", query.Get("types"))
	assert.Equal(t, "2085958499", query.Get("buckets"))
	assert.Equal(t, "self", query.Get("performers"))
	assert.Equal(t, "1049715945,self", query.Get("exclude_performers"))
	assert.Equal(t, "agent,person", query.Get("actor_types"))
	assert.Len(t, query["types"], 1)
}

// The inbox is its own resource, not a filter over the feed: a different path,
// a different ledger, and positions that are never interchangeable.
func TestFeedInboxPollsItsOwnResource(t *testing.T) {
	var seen http.Request
	session := feedSession(t, http.StatusOK, `{"items":[],"position":"aBcD"}`, &seen)

	text, isError := callFeed(t, session, "poll_inbox", map[string]any{
		"since":   "0",
		"reasons": "mentioned,assigned",
		"buckets": "2085958499",
	})
	require.False(t, isError, text)

	assert.Equal(t, "/999/inbox.json", seen.URL.Path)
	assert.Equal(t, "0", seen.URL.Query().Get("since"))
	assert.Equal(t, "mentioned,assigned", seen.URL.Query().Get("reasons"))
	assert.Equal(t, "2085958499", seen.URL.Query().Get("buckets"))
}

// The decisive check on the whole lane. A 410 is a recovery instruction, not a
// failure message: the account lane fences on the feed's epoch and names it,
// the inbox lane fences on the 30-day retention window, has no epoch at all,
// and its resume re-enters at since=0 — the earliest retained item. Flattening
// the two into one error takes the way back in away from the consumer.
func TestFeedStalePositionsKeepTheirOwnRecoveryData(t *testing.T) {
	// The account lane's resume re-enters *after the epoch*, not at the
	// present. since=now would skip every event still servable above the
	// fence — history the caller is entitled to and asked for — so a fixture
	// written that way would have this test bless a recovery instruction that
	// silently loses data. The vendored schema says since=<epoch_after_id>
	// (model/openapi.json), and basecamp-sdk#912 makes the epoch required for
	// this reason.
	t.Run("the account lane names the epoch", func(t *testing.T) {
		session := feedSession(t, http.StatusGone,
			`{"error":"That position predates this feed's epoch.","epoch_after_id":1071900000,`+
				`"resume":"https://3.basecampapi.com/999/events.json?since=1071900000"}`, nil)

		text, isError := callFeed(t, session, "poll_events", map[string]any{"position": "stale"})
		require.True(t, isError, "a 410 is an in-band error")

		detail := feedErrorOf(t, text)
		assert.Equal(t, "stale_position", detail["type"])
		assert.Equal(t, float64(http.StatusGone), detail["http_status"])
		assert.Equal(t, "req-feed-1", detail["request_id"])

		data, ok := detail["data"].(map[string]any)
		require.True(t, ok, "the 410 must carry its recovery data")
		assert.Equal(t, float64(1071900000), data["epoch_after_id"])
		assert.Equal(t, "https://3.basecampapi.com/999/events.json?since=1071900000", data["resume"],
			"the way back in re-enters after the epoch, not at the present")
	})

	t.Run("the inbox lane resumes at since=0 with no epoch", func(t *testing.T) {
		session := feedSession(t, http.StatusGone,
			`{"error":"That position predates the inbox's retention window.",`+
				`"resume":"https://3.basecampapi.com/999/inbox.json?since=0"}`, nil)

		text, isError := callFeed(t, session, "poll_inbox", map[string]any{"position": "stale"})
		require.True(t, isError)

		detail := feedErrorOf(t, text)
		assert.Equal(t, "stale_position", detail["type"])

		data, ok := detail["data"].(map[string]any)
		require.True(t, ok, "the 410 must carry its recovery data")
		assert.Equal(t, "https://3.basecampapi.com/999/inbox.json?since=0", data["resume"])
		assert.NotContains(t, data, "epoch_after_id",
			"the inbox fences on retention, not on an epoch — a zero here would read as one")
	})
}

// A 409 is the only refusal that says which side of the filter set moved, and
// it says it in two digests. Without both, a caller cannot tell whether to
// re-enter or to fix its own configuration.
func TestFeedFilterMismatchKeepsBothDigests(t *testing.T) {
	session := feedSession(t, http.StatusConflict,
		`{"error":"Positions are bound to the filter set they were minted for.",`+
			`"position_digest":"0123456789abcdef","filters_digest":"fedcba9876543210"}`, nil)

	text, isError := callFeed(t, session, "poll_events", map[string]any{"position": "aBcD"})
	require.True(t, isError)

	detail := feedErrorOf(t, text)
	assert.Equal(t, "filter_mismatch", detail["type"])
	assert.Equal(t, float64(http.StatusConflict), detail["http_status"])
	data, ok := detail["data"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "0123456789abcdef", data["position_digest"])
	assert.Equal(t, "fedcba9876543210", data["filters_digest"])
}

// The two 400s carry opposite instructions — re-enter with since=, or fix the
// filters and stop resetting — and the remedy BC3 renders is what tells them
// apart. The reason member (bc3#13362) is read first and wins over the
// sentence; the sentence is the reading for a deploy that predates it. A 400
// that offers neither is not dressed as either.
func TestFeedTellsTheTwo400sApart(t *testing.T) {
	cases := []struct {
		name   string
		action string
		body   string
		want   string
	}{
		{
			name:   "a filter error that sounds internal is still a filter error",
			action: "poll_events",
			body:   `{"error":"The types filter has an unknown type: connection timeout. Fix the filters; a position reset won't help."}`,
			want:   "invalid_filter",
		},
		{
			name:   "an unrecognized position is its own answer",
			action: "poll_events",
			body:   `{"error":"Unrecognized position. Resume with since=<event id> or since=now."}`,
			want:   "invalid_position",
		},
		{
			name:   "the inbox tells them apart the same way",
			action: "poll_inbox",
			body:   `{"error":"Unrecognized position. Resume with since=<id> or since=now."}`,
			want:   "invalid_position",
		},
		{
			name:   "a 400 offering no remedy is not given one",
			action: "poll_events",
			body:   `{"error":"Something went wrong."}`,
			want:   "bad_request",
		},
		{
			name:   "the reason names a filter error even when the sentence says otherwise",
			action: "poll_events",
			body:   `{"error":"Unrecognized position. Resume with since=now.","reason":"invalid_filter"}`,
			want:   "invalid_filter",
		},
		{
			name:   "the reason names a position error on the inbox",
			action: "poll_inbox",
			body:   `{"error":"Something about the request.","reason":"invalid_position"}`,
			want:   "invalid_position",
		},
		{
			name:   "a reason this code does not know is not guessed at through the sentence",
			action: "poll_events",
			body:   `{"error":"Unrecognized position. Resume with since=now.","reason":"invalid_cursor_epoch"}`,
			want:   "bad_request",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			session := feedSession(t, http.StatusBadRequest, tc.body, nil)

			text, isError := callFeed(t, session, tc.action, map[string]any{})
			require.True(t, isError)
			assert.Equal(t, tc.want, feedErrorOf(t, text)["type"])
		})
	}
}

// The inbox is agents-only for now and refuses everyone else. Naming that
// keeps a caller from retrying a principal that will never be admitted; the
// feed lane has no principal guard, so nothing there claims it.
func TestFeedInboxNamesTheAgentsOnlyRefusal(t *testing.T) {
	session := feedSession(t, http.StatusForbidden, "", nil)
	text, isError := callFeed(t, session, "poll_inbox", map[string]any{})
	require.True(t, isError)
	assert.Equal(t, "agents_only", feedErrorOf(t, text)["type"])

	session = feedSession(t, http.StatusForbidden, `{"error":"access denied"}`, nil)
	text, isError = callFeed(t, session, "poll_events", map[string]any{})
	require.True(t, isError)
	assert.Equal(t, "request_failed", feedErrorOf(t, text)["type"])
}

// A refusal this server cannot recognize keeps its status and says no more
// than that — and a rate limit keeps the delay the server named, because
// retrying is the consumer's own loop to drive.
func TestFeedUnrecognizedRefusalsSayOnlyWhatTheyKnow(t *testing.T) {
	session := feedSession(t, http.StatusTooManyRequests, `{"error":"rate limited - try again later"}`, nil)

	text, isError := callFeed(t, session, "poll_events", map[string]any{})
	require.True(t, isError)

	detail := feedErrorOf(t, text)
	assert.Equal(t, "request_failed", detail["type"])
	assert.Equal(t, float64(http.StatusTooManyRequests), detail["http_status"])
	assert.Equal(t, true, detail["retryable"])
	assert.NotContains(t, detail, "data", "no recovery data was offered, so none is claimed")
}

// Both poll lanes are reads, and --read-only must keep them: a read-scoped
// consumer following the feed needs nothing else from this surface. The
// stream-ticket mint is not served in either mode — the sync script drops it by
// policy, because its result is a bearer credential this surface would hand to
// a model transcript and could not use.
func TestFeedReadOnlyServesBothPollLanes(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected HTTP request: %s %s", r.Method, r.URL.Path)
	}))
	t.Cleanup(upstream.Close)

	srv, err := New(newTestAPI(upstream), Config{ReadOnly: true})
	require.NoError(t, err)
	session := mcptest.Connect(t, srv.BuildMCPServer(slog.New(slog.DiscardHandler)))

	tools := mcptest.ListTools(t, session)
	require.Contains(t, tools, "basecamp_eventfeed")
	description := tools["basecamp_eventfeed"].Description
	for _, action := range []string{"poll_events", "poll_inbox"} {
		assert.Contains(t, description, action, "read-only mode must still serve %s", action)
	}
	assert.NotContains(t, description, "create_stream_ticket", "the mint is not served on this surface")
}

// A filter the caller named but gave nothing usable must be refused, never
// dropped. Dropping it leaves the dimension unset, and an unset dimension on
// the wire means unfiltered — so a malformed filter would come back as the
// whole account feed, with nothing in the answer to say the filter had been
// thrown away. Narrow to a refusal, never widen to everything.
func TestFeedRefusesAFilterThatNarrowsNothing(t *testing.T) {
	cases := []struct {
		name   string
		action string
		params map[string]any
	}{
		{name: "buckets is a bare comma", action: "poll_events", params: map[string]any{"buckets": ","}},
		{name: "types is a bare comma", action: "poll_events", params: map[string]any{"types": ","}},
		{name: "types is empty", action: "poll_events", params: map[string]any{"types": ""}},
		{name: "buckets is empty", action: "poll_events", params: map[string]any{"buckets": ""}},
		{name: "types is only separators", action: "poll_events", params: map[string]any{"types": ",,"}},
		{name: "types is only whitespace", action: "poll_events", params: map[string]any{"types": " , "}},
		{name: "performers is a bare comma", action: "poll_events", params: map[string]any{"performers": ","}},
		{name: "actor_types is a bare comma", action: "poll_events", params: map[string]any{"actor_types": ","}},
		{name: "reasons is a bare comma", action: "poll_inbox", params: map[string]any{"reasons": ","}},
		{name: "inbox buckets is a bare comma", action: "poll_inbox", params: map[string]any{"buckets": ","}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var reached bool
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				reached = true
				w.Header().Set("Content-Type", "application/json")
				// The page a widened read would have returned. If the
				// refusal regresses, this is what the caller gets — which is
				// why the assertion below is on the text, not merely on
				// isError.
				_, _ = w.Write([]byte(`{"events":[],"items":[],"position":"aBcD"}`))
			}))
			t.Cleanup(upstream.Close)

			srv, err := New(newTestAPI(upstream), Config{})
			require.NoError(t, err)
			session := mcptest.Connect(t, srv.BuildMCPServer(slog.New(slog.DiscardHandler)))

			text, isError := callFeed(t, session, tc.action, tc.params)

			require.True(t, isError, "a filter that narrows nothing must be refused, got: %s", text)
			assert.Contains(t, text, "had no usable values",
				"the refusal must name the fail-open it prevented, not merely fail")
			assert.False(t, reached, "a refused filter must not reach Basecamp as an unfiltered read")
		})
	}
}

// A filter that is only partly malformed keeps its components verbatim, so BC3
// refuses it by name rather than this server guessing which component was
// meant. What must not happen is the empty component vanishing and the rest
// being served as if the caller had written it that way.
func TestFeedKeepsMalformedFilterComponentsVerbatim(t *testing.T) {
	t.Run("a string filter reaches BC3 as written", func(t *testing.T) {
		var seen http.Request
		session := feedSession(t, http.StatusOK, `{"events":[],"position":"aBcD"}`, &seen)

		text, isError := callFeed(t, session, "poll_events", map[string]any{"types": "message.created,,comment.created"})
		require.False(t, isError, text)
		assert.Equal(t, "message.created,,comment.created", seen.URL.Query().Get("types"),
			"the empty component must survive so BC3 judges the filter, not this server")
	})

	t.Run("an id filter is refused rather than silently narrowed", func(t *testing.T) {
		var reached bool
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			reached = true
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"events":[],"position":"aBcD"}`))
		}))
		t.Cleanup(upstream.Close)

		srv, err := New(newTestAPI(upstream), Config{})
		require.NoError(t, err)
		session := mcptest.Connect(t, srv.BuildMCPServer(slog.New(slog.DiscardHandler)))

		for _, value := range []string{",,1", "1,", "1,abc"} {
			text, isError := callFeed(t, session, "poll_events", map[string]any{"buckets": value})
			require.True(t, isError, "buckets=%q must be refused, got: %s", value, text)
			assert.Contains(t, text, "takes decimal ids",
				"the refusal must say what buckets takes")
		}
		assert.False(t, reached, "a refused filter must not reach Basecamp")
	})
}

// The filters a caller does give still travel whole — the refusals above must
// not be paid for by a dimension that silently stops working.
func TestFeedFiltersStillReachTheWire(t *testing.T) {
	var seen http.Request
	session := feedSession(t, http.StatusOK, `{"events":[],"position":"aBcD"}`, &seen)

	text, isError := callFeed(t, session, "poll_events", map[string]any{
		"types":              " message.created , comment.created ",
		"buckets":            "1,2",
		"creators":           "3",
		"performers":         "self",
		"exclude_performers": "4,self",
		"actor_types":        "agent",
	})
	require.False(t, isError, text)

	query := seen.URL.Query()
	assert.Equal(t, "message.created,comment.created", query.Get("types"))
	assert.Equal(t, "1,2", query.Get("buckets"))
	assert.Equal(t, "3", query.Get("creators"))
	assert.Equal(t, "self", query.Get("performers"))
	assert.Equal(t, "4,self", query.Get("exclude_performers"))
	assert.Equal(t, "agent", query.Get("actor_types"))
}

// A refusal this server makes on the caller's arguments must arrive in the
// same typed shape as the ones BC3 makes. These are the refusals a caller
// hits most; serving them as prose while every wire refusal carries a type
// would leave the common path untyped and make one contract true only of the
// rare cases.
//
// The assertion is on the shape, not on the words — an earlier test read only
// the message text, which prose satisfies just as well as a payload does, so
// it could not have seen this.
func TestFeedArgumentRefusalsAreTypedToo(t *testing.T) {
	cases := []struct {
		name   string
		action string
		params map[string]any
		says   string
	}{
		{
			name:   "a filter that narrows nothing",
			action: "poll_events",
			params: map[string]any{"types": ","},
			says:   "had no usable values",
		},
		{
			name:   "an id filter that is not decimal",
			action: "poll_events",
			params: map[string]any{"buckets": "abc"},
			says:   "takes decimal ids",
		},
		{
			name:   "both entry points at once",
			action: "poll_events",
			params: map[string]any{"since": "0", "position": "aBcD"},
			says:   "two ways to enter",
		},
		{
			name:   "both entry points at once on the inbox",
			action: "poll_inbox",
			params: map[string]any{"since": "0", "position": "aBcD"},
			says:   "two ways to enter",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			session := feedSession(t, http.StatusOK, `{"events":[],"items":[],"position":"aBcD"}`, nil)

			text, isError := callFeed(t, session, tc.action, tc.params)
			require.True(t, isError, text)

			detail := feedErrorOf(t, text)
			assert.Equal(t, "invalid_arguments", detail["type"],
				"an argument refusal carries the same vocabulary as a wire refusal")
			assert.Contains(t, detail["message"], tc.says)
		})
	}
}

// The 409 is a comparison of two digests. With only one of them a caller
// cannot tell which side moved, so the type is not claimed — and the missing
// digest is never written as "", which is a value a consumer would compare
// against and find unequal. Same fabricated-zero defect the epoch avoids, one
// refusal over.
func TestFeedFilterMismatchNeedsBothDigests(t *testing.T) {
	for _, body := range []string{
		`{"error":"filters changed","position_digest":"abc123"}`,
		`{"error":"filters changed","filters_digest":"def456"}`,
	} {
		session := feedSession(t, http.StatusConflict, body, nil)

		text, isError := callFeed(t, session, "poll_events", map[string]any{"since": "0"})
		require.True(t, isError, text)

		detail := feedErrorOf(t, text)
		assert.NotEqual(t, "filter_mismatch", detail["type"],
			"a half-populated 409 is not the documented filter mismatch: %s", body)

		data, _ := detail["data"].(map[string]any)
		assert.NotContains(t, data, "position_digest", "no digest is better than an empty one")
		assert.NotContains(t, data, "filters_digest")
	}
}

// The inbox has no epoch. It fences on a 30-day retention window, and its 410
// is its own type in the SDK, with no field an epoch could ride in. An epoch
// that turns up on the wire anyway must not reach the caller: a consumer
// handed epoch_after_id on this lane would re-enter at a fence the inbox does
// not have. The SDK drops it today; this pins that nothing between the SDK
// and the payload puts one back.
func TestFeedInbox410NeverCarriesAnEpoch(t *testing.T) {
	// An epoch on the wire the inbox lane must not pass on.
	const body = `{"error":"position expired","resume":"https://3.basecampapi.com/999/inbox.json?since=0","epoch_after_id":991}`
	session := feedSession(t, http.StatusGone, body, nil)

	text, isError := callFeed(t, session, "poll_inbox", map[string]any{"position": "aBcD"})
	require.True(t, isError, text)

	detail := feedErrorOf(t, text)
	assert.Equal(t, "stale_position", detail["type"])

	data, ok := detail["data"].(map[string]any)
	require.True(t, ok, "the recovery data must travel: %s", text)
	assert.Equal(t, "https://3.basecampapi.com/999/inbox.json?since=0", data["resume"],
		"the inbox's way back in is the earliest retained item")
	assert.NotContains(t, data, "epoch_after_id",
		"the inbox fences on retention, not on an epoch")
}

// agents_only is a claim that a principal will never be admitted, so it may
// only be made on the refusal that means that. A 403 BC3 gave a reason for
// came from scope or access; those are fixable, and telling a caller to stop
// trying about a repairable condition is a false promise.
func TestFeedAgentsOnlyIsClaimedOnlyOnTheBodylessRefusal(t *testing.T) {
	t.Run("a bodyless 403 names the guard", func(t *testing.T) {
		session := feedSession(t, http.StatusForbidden, ``, nil)

		text, isError := callFeed(t, session, "poll_inbox", nil)
		require.True(t, isError, text)

		detail := feedErrorOf(t, text)
		assert.Equal(t, "agents_only", detail["type"])
		assert.Equal(t, "the event inbox is served to agent principals only", detail["message"],
			"the claim must say what the guard is, not repeat the SDK's stand-in text")
	})

	t.Run("a 403 with a reason of its own is not claimed", func(t *testing.T) {
		session := feedSession(t, http.StatusForbidden, `{"error":"your token lacks the read scope"}`, nil)

		text, isError := callFeed(t, session, "poll_inbox", nil)
		require.True(t, isError, text)

		detail := feedErrorOf(t, text)
		assert.NotEqual(t, "agents_only", detail["type"],
			"a scope refusal is fixable and must not be typed as a permanent one")
		assert.Contains(t, detail["message"], "read scope",
			"the server's own reason must survive")
	})
}

// A feed 410 is a recovery only when it names the epoch it re-enters at. One
// that carries a resume and no epoch is not the documented refusal, and typing
// it stale_position would promise a fence the caller has no way to place — so
// it is served as a refusal we cannot explain. The resume does not travel
// either: re-entering without knowing where the history begins is a guess.
func TestFeedGoneWithoutAnEpochIsNotAStalePosition(t *testing.T) {
	session := feedSession(t, http.StatusGone,
		`{"error":"That position predates this feed's epoch.",`+
			`"resume":"https://3.basecampapi.com/999/events.json?since=1071900000"}`, nil)

	text, isError := callFeed(t, session, "poll_events", map[string]any{"position": "stale"})
	require.True(t, isError, text)

	detail := feedErrorOf(t, text)
	assert.Equal(t, "request_failed", detail["type"])
	assert.Equal(t, float64(http.StatusGone), detail["http_status"])
	assert.NotContains(t, detail, "data", "no epoch, so no recovery is claimed")
}

// The lane tables name every parameter this server hands to the feed, and the
// model names every parameter describe advertises and buildRequest accepts.
// Where the two disagree, a filter a caller was told exists would be refused
// at call time — or, before the refusal existed, silently dropped. This fails
// the day a model sync gives a lane a parameter the dispatcher does not pass.
func TestFeedLaneParamsMatchTheModel(t *testing.T) {
	cat := loadForTest(t)
	seen := map[string]bool{}
	for _, d := range cat.Domains {
		for _, op := range d.Operations {
			if !isFeedOperation(op.ID) {
				continue
			}
			seen[op.ID] = true
			var model []string
			for _, p := range op.Params {
				if p.In == "query" {
					model = append(model, p.Name)
				}
			}
			assert.ElementsMatch(t, model, feedLaneParams[op.ID],
				"%s: the model's query parameters and the ones handed to the SDK must be the same set", op.ID)
		}
	}
	assert.True(t, seen[opPollEvents] && seen[opPollInbox], "both poll lanes must be in the catalog")
}

// A parameter the model accepts and this server does not pass to the SDK is
// refused, not dropped. Dropped, the caller who asked to narrow is served the
// unfiltered lane. The table is shrunk here to stand in for a model sync that
// got ahead of the dispatcher.
func TestFeedRefusesAParameterItWouldDrop(t *testing.T) {
	original := feedLaneParams[opPollEvents]
	feedLaneParams[opPollEvents] = slices.DeleteFunc(slices.Clone(original), func(name string) bool { return name == "creators" })
	t.Cleanup(func() { feedLaneParams[opPollEvents] = original })

	var reached bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"events":[],"position":"aBcD"}`))
	}))
	t.Cleanup(upstream.Close)
	srv, err := New(newTestAPI(upstream), Config{})
	require.NoError(t, err)
	session := mcptest.Connect(t, srv.BuildMCPServer(slog.New(slog.DiscardHandler)))

	text, isError := callFeed(t, session, "poll_events", map[string]any{"creators": "1049715914"})
	require.True(t, isError, "a filter this server cannot pass on must be refused, got: %s", text)
	detail := feedErrorOf(t, text)
	assert.Equal(t, "invalid_arguments", detail["type"])
	assert.Contains(t, detail["message"], "creators")
	assert.False(t, reached, "a dropped filter must not reach Basecamp as an unfiltered read")
}

// An entry point passed empty is refused. The SDK reads it as no entry point,
// which is the present, so a consumer whose stored position came back empty
// would skip everything it had not yet read and be told nothing.
func TestFeedRefusesAnEmptyEntryPoint(t *testing.T) {
	for _, tc := range []struct{ action, name string }{
		{"poll_events", "position"},
		{"poll_events", "since"},
		{"poll_inbox", "position"},
		{"poll_inbox", "since"},
	} {
		t.Run(tc.action+" "+tc.name, func(t *testing.T) {
			var reached bool
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				reached = true
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"events":[],"items":[],"position":"aBcD"}`))
			}))
			t.Cleanup(upstream.Close)
			srv, err := New(newTestAPI(upstream), Config{})
			require.NoError(t, err)
			session := mcptest.Connect(t, srv.BuildMCPServer(slog.New(slog.DiscardHandler)))

			text, isError := callFeed(t, session, tc.action, map[string]any{tc.name: ""})
			require.True(t, isError, "an empty %s must be refused, got: %s", tc.name, text)
			assert.Equal(t, "invalid_arguments", feedErrorOf(t, text)["type"])
			assert.False(t, reached, "an empty entry point must not reach Basecamp as a read from the present")
		})
	}
}

// A 400 whose body is not the feed's own is not given the feed's recovery.
// The SDK falls back to a `message` member when there is no `error`, so text
// that happens to read like BC3's remedy can arrive here from a body BC3's
// feed did not write; reading a recovery out of it would be a guess.
func TestFeedDoesNotReadARemedyOutOfAForeign400(t *testing.T) {
	session := feedSession(t, http.StatusBadRequest, `{"message":"Unrecognized position. Resume with since=now."}`, nil)

	text, isError := callFeed(t, session, "poll_events", map[string]any{"position": "aBcD"})
	require.True(t, isError, text)
	assert.Equal(t, "bad_request", feedErrorOf(t, text)["type"])
}

// The refusals the SDK leaves untyped land somewhere honest: their status, no
// recovery claimed, and no recovery data invented.
func TestFeedUntypedRefusalsClaimNoRecovery(t *testing.T) {
	cases := []struct {
		name   string
		action string
		status int
		body   string
	}{
		{"a 409 on the inbox with one digest", "poll_inbox", http.StatusConflict, `{"error":"filters changed","filters_digest":"def456"}`},
		{"a feed 410 with an epoch and no resume", "poll_events", http.StatusGone, `{"error":"gone","epoch_after_id":1071900000}`},
		{"an inbox 410 with no resume", "poll_inbox", http.StatusGone, `{"error":"gone"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			session := feedSession(t, tc.status, tc.body, nil)

			text, isError := callFeed(t, session, tc.action, map[string]any{"position": "aBcD"})
			require.True(t, isError, text)

			detail := feedErrorOf(t, text)
			assert.Equal(t, "request_failed", detail["type"])
			assert.Equal(t, float64(tc.status), detail["http_status"])
			assert.NotContains(t, detail, "data", "no recovery was offered, so none is claimed")
		})
	}
	t.Run("a 409 on the inbox with both digests is a filter mismatch there too", func(t *testing.T) {
		session := feedSession(t, http.StatusConflict,
			`{"error":"filters changed","position_digest":"abc123","filters_digest":"def456"}`, nil)

		text, isError := callFeed(t, session, "poll_inbox", map[string]any{"position": "aBcD"})
		require.True(t, isError, text)
		detail := feedErrorOf(t, text)
		assert.Equal(t, "filter_mismatch", detail["type"])
		assert.Equal(t, map[string]any{"position_digest": "abc123", "filters_digest": "def456"}, detail["data"])
	})
}

// A page comes back with what it carried: the rows, the durable position, and
// the continuation. The request-side tests would pass against an encoder that
// returned {}, so the answer is read here.
func TestFeedServesThePageItFetched(t *testing.T) {
	t.Run("feed", func(t *testing.T) {
		session := feedSession(t, http.StatusOK, `{
			"events":[{"id":11,"kind":"message_created","action":"created","event_type":"message.created",
				"bucket_id":2085958499,"creator_id":1049715914,"performed_by_id":52007412,
				"recording_id":9007199254,"created_at":"2026-09-16T09:00:00Z","details":{"column_id":7}}],
			"position":"cG9zOjEx",
			"next":"https://3.basecampapi.com/999/events.json?position=cG9zOjEx"}`, nil)

		text, isError := callFeed(t, session, "poll_events", map[string]any{"since": "0"})
		require.False(t, isError, text)

		var page map[string]any
		require.NoError(t, json.Unmarshal([]byte(text), &page), text)
		assert.Equal(t, "cG9zOjEx", page["position"])
		assert.Equal(t, "https://3.basecampapi.com/999/events.json?position=cG9zOjEx", page["next"])
		events, ok := page["events"].([]any)
		require.True(t, ok && len(events) == 1, "the page's events must travel: %s", text)
		event := events[0].(map[string]any)
		assert.Equal(t, float64(11), event["id"])
		assert.Equal(t, "message.created", event["event_type"])
		assert.Equal(t, float64(52007412), event["performed_by_id"])
		assert.Equal(t, float64(9007199254), event["recording_id"])
		assert.Equal(t, map[string]any{"column_id": float64(7)}, event["details"])
	})

	t.Run("inbox", func(t *testing.T) {
		session := feedSession(t, http.StatusOK, `{
			"items":[{"addressing_id":801,"reason":"mentioned","addressed_at":"2026-09-16T09:00:00Z",
				"event":{"id":11,"event_type":"comment.created","bucket_id":2085958499,"creator_id":1049715914,
				"performed_by_id":null,"recording_id":9007199254,"created_at":"2026-09-16T09:00:00Z"}}],
			"position":"aW5ib3g6ODAx"}`, nil)

		text, isError := callFeed(t, session, "poll_inbox", map[string]any{"since": "0"})
		require.False(t, isError, text)

		var page map[string]any
		require.NoError(t, json.Unmarshal([]byte(text), &page), text)
		assert.Equal(t, "aW5ib3g6ODAx", page["position"])
		items, ok := page["items"].([]any)
		require.True(t, ok && len(items) == 1, "the page's items must travel: %s", text)
		item := items[0].(map[string]any)
		assert.Equal(t, float64(801), item["addressing_id"])
		assert.Equal(t, "mentioned", item["reason"])
		assert.Equal(t, float64(11), item["event"].(map[string]any)["id"])
	})
}
