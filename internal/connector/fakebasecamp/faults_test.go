package fakebasecamp_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/connector/fakebasecamp"
)

func mintForm(secret string) string {
	return url.Values{
		"grant_type": {"client_credentials"}, "client_id": {fakebasecamp.AgentClientID}, "client_secret": {secret},
	}.Encode()
}

// The token endpoint's faults: a rate limit asking for a second by
// default, an outage, and a refused client; each lapses when removed.
func TestTokenFaults(t *testing.T) {
	s, _ := start(t)

	limited := s.Inject(fakebasecamp.TokenRateLimited(0))
	status, header, _ := do(t, s, http.MethodPost, "/oauth/tokens", "", mintForm(fakebasecamp.AgentSecret))
	assert.Equal(t, http.StatusTooManyRequests, status)
	assert.Equal(t, "1", header.Get("Retry-After"))
	limited.Remove()

	down := s.Inject(fakebasecamp.TokenUnavailable())
	status, _, _ = do(t, s, http.MethodPost, "/oauth/tokens", "", mintForm(fakebasecamp.AgentSecret))
	assert.Equal(t, http.StatusServiceUnavailable, status)
	refused := s.Inject(fakebasecamp.TokenInvalidClient())
	status, _, body := do(t, s, http.MethodPost, "/oauth/tokens", "", mintForm(fakebasecamp.AgentSecret))
	assert.Equal(t, http.StatusUnauthorized, status, "the fault injected last answers")
	assert.JSONEq(t, `{"error":"invalid_client"}`, body)
	refused.Remove()
	down.Remove()

	status, _, body = do(t, s, http.MethodPost, "/oauth/tokens", "", mintForm(fakebasecamp.AgentSecret))
	require.Equal(t, http.StatusOK, status, body)
	var token struct {
		AccessToken string `json:"access_token"`
		Scope       string `json:"scope"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &token))
	assert.Equal(t, fakebasecamp.ScopeFull, token.Scope)
	status, _, _ = do(t, s, http.MethodGet, "/999/my/profile.json", token.AccessToken, "")
	assert.Equal(t, http.StatusOK, status, "the minted token reads as the agent")
	assert.Equal(t, 4, s.Count(fakebasecamp.RouteToken), "a faulted mint is still a mint")
}

// A mint carries the scope it asks for, within what the client was
// approved for: read from a full client, never full from a read one.
func TestTokenMintHonorsTheAskedScope(t *testing.T) {
	s, _ := start(t)
	mint := func(scope string) (int, string) {
		t.Helper()
		form := url.Values{
			"grant_type": {"client_credentials"}, "client_id": {fakebasecamp.AgentClientID},
			"client_secret": {fakebasecamp.AgentSecret}, "scope": {scope},
		}
		status, _, body := do(t, s, http.MethodPost, "/oauth/tokens", "", form.Encode())
		return status, body
	}
	scopeOf := func(body string) string {
		t.Helper()
		var token struct {
			Scope string `json:"scope"`
		}
		require.NoError(t, json.Unmarshal([]byte(body), &token), body)
		return token.Scope
	}

	status, body := mint(fakebasecamp.ScopeRead)
	require.Equal(t, http.StatusOK, status, body)
	assert.Equal(t, fakebasecamp.ScopeRead, scopeOf(body), "narrowed to what was asked")
	status, body = mint("admin")
	assert.Equal(t, http.StatusBadRequest, status)
	assert.JSONEq(t, `{"error":"invalid_scope"}`, body)

	s.Update(func(w *fakebasecamp.World) { w.Agents[fakebasecamp.AgentClientID].Scope = fakebasecamp.ScopeRead })
	status, body = mint(fakebasecamp.ScopeFull)
	assert.Equal(t, http.StatusBadRequest, status, "a read client is not widened")
	assert.JSONEq(t, `{"error":"invalid_scope"}`, body)
	status, body = mint("")
	require.Equal(t, http.StatusOK, status, body)
	assert.Equal(t, fakebasecamp.ScopeRead, scopeOf(body), "no scope asked is the client's own")
}

// A fault can be limited to a number of requests, and to the requests a
// predicate picks, such as an Agent's.
func TestFaultsNarrowAndLapse(t *testing.T) {
	s, bearer := start(t)
	s.Update(func(w *fakebasecamp.World) {
		w.Recordings[101] = &fakebasecamp.Recording{ID: 101, Type: "Comment", BucketID: fakebasecamp.ProjectID, CreatorID: fakebasecamp.OperatorID}
	})

	failing := s.Inject(fakebasecamp.ServerError(2, fakebasecamp.RecordingReads...))
	for range 2 {
		status, _, _ := do(t, s, http.MethodGet, "/999/comments/101", bearer, "")
		assert.Equal(t, http.StatusInternalServerError, status)
	}
	status, _, _ := do(t, s, http.MethodGet, "/999/comments/101", bearer, "")
	assert.Equal(t, http.StatusOK, status, "the fault lapsed after two")
	assert.Equal(t, 2, failing.Hits())

	agentsOnly := fakebasecamp.Refused(http.StatusForbidden, fakebasecamp.RouteProject, fakebasecamp.RouteProjectPeople)
	agentsOnly.When = fakebasecamp.ByAgents
	s.Inject(agentsOnly)
	status, _, _ = do(t, s, http.MethodGet, "/999/projects/48699913", bearer, "")
	assert.Equal(t, http.StatusForbidden, status)
	status, _, _ = do(t, s, http.MethodGet, "/999/projects/48699913", fakebasecamp.OperatorToken, "")
	assert.Equal(t, http.StatusOK, status)

	// A fault's answer can be a success of its own.
	s.Inject(fakebasecamp.Fault{Routes: []fakebasecamp.Route{fakebasecamp.RouteProfile}, Status: http.StatusOK, Body: `{"id":0}`})
	_, _, body := do(t, s, http.MethodGet, "/999/my/profile.json", bearer, "")
	assert.JSONEq(t, `{"id":0}`, body)
}

// A gate holds a route's requests unanswered until it is released, and a
// hook runs before a request is answered, in time to change what it is
// answered with.
func TestGatesAndHooks(t *testing.T) {
	s, bearer := start(t)
	gate := s.Gate(fakebasecamp.RouteProjects)
	answered := make(chan int, 1)
	go func() {
		status, _, _ := do(t, s, http.MethodGet, "/999/projects.json", bearer, "")
		answered <- status
	}()
	await(t, s, "the held request", func() bool { return gate.Waiting() == 1 })
	requests := s.Requests()
	require.Len(t, requests, 1)
	assert.Zero(t, requests[0].Status, "held, not answered")
	assert.Equal(t, fakebasecamp.AgentID, requests[0].Caller)
	assert.True(t, requests[0].Agent)
	gate.Release()
	assert.Equal(t, http.StatusOK, <-answered)
	assert.Equal(t, http.StatusOK, s.Requests()[0].Status, "logged before the client had it")

	remove := s.Before(fakebasecamp.RouteStreamTicket, func(fakebasecamp.Request) {
		s.Update(func(w *fakebasecamp.World) { delete(w.Tokens, bearer) })
	})
	status, _, _ := do(t, s, http.MethodPost, "/999/events/stream_ticket.json", bearer, "")
	assert.Equal(t, http.StatusUnauthorized, status, "the token the hook removed is refused")
	remove()
}

// Overlapping gates hold a request until every one of them is released, in
// either order.
func TestOverlappingGates(t *testing.T) {
	s, bearer := start(t)
	first := s.Gate(fakebasecamp.RouteProjects)
	second := s.Gate(fakebasecamp.RouteProjects, fakebasecamp.RouteProfile)
	answered := make(chan int, 1)
	go func() {
		status, _, _ := do(t, s, http.MethodGet, "/999/projects.json", bearer, "")
		answered <- status
	}()
	await(t, s, "the request at the first gate", func() bool { return first.Waiting() == 1 })
	second.Release()
	first.Release()
	await(t, s, "the answer", func() bool { return s.Requests()[0].Status != 0 })
	assert.Equal(t, http.StatusOK, <-answered)

	first = s.Gate(fakebasecamp.RouteProjects)
	second = s.Gate(fakebasecamp.RouteProjects)
	go func() {
		status, _, _ := do(t, s, http.MethodGet, "/999/projects.json", bearer, "")
		answered <- status
	}()
	await(t, s, "the request at the first gate", func() bool { return first.Waiting() == 1 })
	first.Release()
	await(t, s, "the request at the second gate", func() bool { return second.Waiting() == 1 })
	assert.Zero(t, s.Requests()[1].Status, "still held by the second gate")
	second.Release()
	assert.Equal(t, http.StatusOK, <-answered)
}

// Await gives up once the fake is closed, rather than waiting out its
// context for a change that can no longer come.
func TestAwaitEndsWhenClosed(t *testing.T) {
	s, _ := start(t)
	done := make(chan error, 1)
	go func() { done <- s.Await(t.Context(), func() bool { return false }) }()
	s.Close()
	assert.ErrorIs(t, <-done, fakebasecamp.ErrClosed)
	assert.ErrorIs(t, s.Await(t.Context(), func() bool { return false }), fakebasecamp.ErrClosed)
	assert.NoError(t, s.Await(t.Context(), func() bool { return true }), "a condition that holds still returns")
}

// Requests hands out copies: changing one changes nothing the fake logged.
func TestRequestsAreCopies(t *testing.T) {
	s, bearer := start(t)
	do(t, s, http.MethodGet, "/999/events.json?since=now", bearer, "")
	do(t, s, http.MethodPost, "/oauth/tokens", "", mintForm(fakebasecamp.AgentSecret))
	requests := s.Requests()
	requests[0].Query.Set("since", "0")
	requests[1].Form.Set("client_secret", "changed")
	assert.Equal(t, "now", s.Requests()[0].Query.Get("since"))
	assert.Equal(t, fakebasecamp.AgentSecret, s.Requests()[1].Form.Get("client_secret"))
}

// The poll lane's own contract: the present is an empty page at the head,
// a malformed position is the 400 eventfeed recovers from, and a page that
// stops short says where to continue with the request's filters intact.
func TestPollLaneContract(t *testing.T) {
	s, bearer := start(t, fakebasecamp.WithPageSize(1))
	first := s.Emit(commentBy(fakebasecamp.OperatorID, fakebasecamp.ProjectID), fakebasecamp.Both)
	s.Emit(commentBy(fakebasecamp.OperatorID, fakebasecamp.ProjectID), fakebasecamp.Live)
	last := s.Emit(commentBy(fakebasecamp.OperatorID, fakebasecamp.ProjectID), fakebasecamp.Poll)

	_, _, body := do(t, s, http.MethodGet, "/999/events.json?since=now", bearer, "")
	assert.JSONEq(t, `{"events":[],"position":"fake-3"}`, body)

	status, _, body := do(t, s, http.MethodGet, "/999/events.json?position=garbage", bearer, "")
	assert.Equal(t, http.StatusBadRequest, status)
	assert.JSONEq(t, `{"error":"Unrecognized position","reason":"invalid_position"}`, body)

	status, _, _ = do(t, s, http.MethodGet, "/999/events.json?since=0&buckets=x", bearer, "")
	assert.Equal(t, http.StatusBadRequest, status)

	type pollPage struct {
		Events []struct {
			ID int64 `json:"id"`
		} `json:"events"`
		Position string `json:"position"`
		Next     string `json:"next"`
	}
	poll := func(query string) (pollPage, string) {
		t.Helper()
		_, _, body := do(t, s, http.MethodGet, "/999/events.json?"+query, bearer, "")
		var page pollPage
		require.NoError(t, json.Unmarshal([]byte(body), &page), body)
		return page, body
	}
	page, body := poll("since=0&exclude_performers=self")
	require.Len(t, page.Events, 1)
	assert.Equal(t, first.ID, page.Events[0].ID)
	assert.Contains(t, body, `"performed_by_id":null`)
	assert.Equal(t, s.URL()+"/999/events.json?exclude_performers=self&position=fake-1", page.Next)

	page, _ = poll("exclude_performers=self&position=fake-1")
	require.Len(t, page.Events, 1, "the live-only event is not on the poll lane")
	assert.Equal(t, last.ID, page.Events[0].ID)
	assert.Empty(t, page.Next)

	// Published to the poll lane later, an event is served to a walk that
	// has not passed it.
	s.Publish(2, fakebasecamp.Poll)
	page, _ = poll("position=fake-1")
	require.Len(t, page.Events, 1)
	assert.Equal(t, int64(2), page.Events[0].ID)
}

// A request no route serves is answered 404 and reported, so a client that
// starts calling something the fake does not know fails where it is seen.
func TestUnroutedRequestsAreReported(t *testing.T) {
	r := &reporter{T: t}
	s := fakebasecamp.Start(r, fakebasecamp.DefaultWorld())
	status, _, _ := do(t, s, http.MethodGet, "/999/vaults/1", fakebasecamp.OperatorToken, "")
	assert.Equal(t, http.StatusNotFound, status)
	status, _, _ = do(t, s, http.MethodDelete, "/999/projects.json", fakebasecamp.OperatorToken, "")
	assert.Equal(t, http.StatusNotFound, status)
	assert.Equal(t, []string{
		"fakebasecamp: no route serves GET /999/vaults/1",
		"fakebasecamp: no route serves DELETE /999/projects.json",
	}, r.reported())
	assert.Equal(t, []fakebasecamp.Route{"", ""}, []fakebasecamp.Route{s.Requests()[0].Route, s.Requests()[1].Route})
}
