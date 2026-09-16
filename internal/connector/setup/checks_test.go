//go:build unix

package setup

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/connector/admission"
)

// fakeReader answers each read from a table; an absent entry succeeds.
type fakeReader struct {
	people        map[int64]Person
	personErr     error
	mintErr       error
	projectErr    map[int64]error
	projectPplErr map[int64]error
}

func (f *fakeReader) Me(context.Context) (Person, error) { return f.people[agentID], nil }
func (f *fakeReader) Person(_ context.Context, id int64) (Person, error) {
	if f.personErr != nil {
		return Person{}, f.personErr
	}
	return f.people[id], nil
}
func (f *fakeReader) MintStreamTicket(context.Context) error { return f.mintErr }
func (f *fakeReader) Project(_ context.Context, id int64) error {
	return f.projectErr[id]
}
func (f *fakeReader) ProjectPeople(_ context.Context, id int64) error {
	return f.projectPplErr[id]
}

func status(code int) error {
	return &basecamp.Error{Code: "api_error", Message: http.StatusText(code), HTTPStatus: code}
}

// The known bc3 limitation: an Agent identity is refused the reads admission
// makes. Setup has to say so, rather than write a file the connector would
// start on and then block every event against.
func TestRouteChecksNameTheAgentReadRefusal(t *testing.T) {
	f := validFile(t)
	r := &fakeReader{projectErr: map[int64]error{projectID: status(http.StatusForbidden)}}

	checks := RouteChecks(context.Background(), r, f)
	require.Len(t, checks, 2)
	byName := map[string]Check{}
	for _, c := range checks {
		byName[c.Name] = c
	}

	refused := byName[fmt.Sprintf("Project %d", projectID)]
	assert.Equal(t, StatusFail, refused.Status)
	assert.Contains(t, refused.Message, "Agent identity")
	assert.Contains(t, refused.Hint, "--expect-identity", "the way to run today is named")

	assert.Equal(t, StatusPass, byName[fmt.Sprintf("Project %d", otherProj)].Status)
}

func TestRouteChecksReadProjectPeopleToo(t *testing.T) {
	f := validFile(t)
	r := &fakeReader{projectPplErr: map[int64]error{otherProj: status(http.StatusForbidden)}}

	var failed []Check
	for _, c := range RouteChecks(context.Background(), r, f) {
		if c.Status == StatusFail {
			failed = append(failed, c)
		}
	}
	require.Len(t, failed, 1)
	assert.Equal(t, fmt.Sprintf("Project %d", otherProj), failed[0].Name)
	assert.Contains(t, failed[0].Message, "people")
}

func TestRouteChecksForABotUserSayToAddTheAgent(t *testing.T) {
	f := validFile(t)
	f.Agent = Agent{PersonID: agentID, Kind: KindBotUser, IdentityID: 99}
	r := &fakeReader{projectErr: map[int64]error{projectID: status(http.StatusNotFound)}}

	for _, c := range RouteChecks(context.Background(), r, f) {
		if c.Status != StatusFail {
			continue
		}
		assert.NotContains(t, c.Message, "Agent identity", "a bot user is not an Agent")
		assert.Contains(t, c.Hint, "Add the agent to the project")
		return
	}
	t.Fatal("the refused project did not fail")
}

func TestRouteChecksWarnWithNoRoutes(t *testing.T) {
	f := validFile(t)
	f.Projects = map[int64]admission.Route{}
	checks := RouteChecks(context.Background(), &fakeReader{}, f)
	require.Len(t, checks, 1)
	assert.Equal(t, StatusFail, checks[0].Status, "a connector with no route does no work, so it is not ready")
}

func TestTicketCheck(t *testing.T) {
	assert.Equal(t, StatusPass, TicketCheck(context.Background(), &fakeReader{}, KindAgent).Status)

	c := TicketCheck(context.Background(), &fakeReader{mintErr: status(http.StatusForbidden)}, KindAgent)
	assert.Equal(t, StatusFail, c.Status)
	assert.Contains(t, c.Message, "403")

	c = TicketCheck(context.Background(), &fakeReader{mintErr: ErrMalformedTicket}, KindAgent)
	assert.Equal(t, StatusFail, c.Status)
}

func TestVerifyTrust(t *testing.T) {
	ctx := context.Background()
	people := map[int64]Person{
		operatorID: {ID: operatorID, Name: "Operator"},
		7:          {ID: 7, PersonableType: PersonableAgent},
		8:          {ID: 8, Client: true},
		9:          {ID: 9, Name: "Member"},
	}
	r := &fakeReader{people: people}
	forbidden := &fakeReader{personErr: status(http.StatusForbidden)}
	statuses := func(checks []Check) []string {
		out := make([]string, 0, len(checks))
		for _, c := range checks {
			out = append(out, c.Name+"="+c.Status)
		}
		return out
	}
	verify := func(r Reader, trust Trust) []string { return statuses(VerifyTrust(ctx, r, trust, agentID)) }

	assert.Equal(t, []string{"Operator=pass"}, verify(r, Trust{Operator: Person{ID: operatorID}}))
	assert.Equal(t, []string{"Operator=pass"}, verify(forbidden, Trust{Operator: people[operatorID], OperatorProfile: "me"}),
		"an operator proved by their own profile needs no read")

	t.Run("property 1: a Person id", func(t *testing.T) {
		assert.Equal(t, []string{"Operator=fail"}, verify(r, Trust{}))
	})
	t.Run("property 2: never the agent", func(t *testing.T) {
		assert.Equal(t, []string{"Operator=fail"}, verify(r, Trust{Operator: Person{ID: agentID}}))
		assert.Equal(t, []string{"Operator=fail"}, verify(r, Trust{Operator: Person{ID: agentID}, OperatorProfile: "me"}))
		assert.Equal(t, []string{"Operator=pass", fmt.Sprintf("Allowlist %d=fail", agentID)},
			verify(r, Trust{Operator: Person{ID: operatorID}, Allowlist: []int64{agentID}}))
	})
	t.Run("property 3: verified, unless already recorded", func(t *testing.T) {
		assert.Equal(t, []string{"Operator=fail"}, verify(forbidden, Trust{Operator: Person{ID: operatorID}}))
		assert.Equal(t, []string{"Operator=warn"}, verify(forbidden, Trust{Operator: Person{ID: operatorID}, Recorded: admission.Trust{OperatorID: operatorID}}))
		assert.Equal(t, []string{"Operator=fail"}, verify(forbidden, Trust{Operator: Person{ID: 9}, Recorded: admission.Trust{OperatorID: operatorID}}),
			"a different operator is a new trust anchor")

		trust := Trust{Operator: people[operatorID], OperatorProfile: "me", Allowlist: []int64{9, 10}}
		assert.Equal(t, []string{"Operator=pass", "Allowlist 9=fail", "Allowlist 10=fail"}, verify(forbidden, trust), "allowlisted ids are verified too")
		trust.Recorded = admission.Trust{AllowlistIDs: []int64{9}}
		assert.Equal(t, []string{"Operator=pass", "Allowlist 9=warn", "Allowlist 10=fail"}, verify(forbidden, trust))
	})
	t.Run("property 4: not an Agent", func(t *testing.T) {
		assert.Equal(t, []string{"Operator=fail"}, verify(r, Trust{Operator: Person{ID: 7}}))
		assert.Equal(t, []string{"Operator=fail"}, verify(r, Trust{Operator: people[7], OperatorProfile: "me"}))
		assert.Equal(t, []string{"Operator=fail"}, verify(r, Trust{Operator: Person{ID: 7}, Recorded: admission.Trust{OperatorID: 7}}),
			"recorded is no excuse when the read answers")
		assert.Equal(t, []string{"Operator=pass", "Allowlist 7=fail"}, verify(r, Trust{Operator: Person{ID: operatorID}, Allowlist: []int64{7}}))
	})
	t.Run("property 5: not a client", func(t *testing.T) {
		assert.Equal(t, []string{"Operator=fail"}, verify(r, Trust{Operator: Person{ID: 8}}))
		assert.Equal(t, []string{"Operator=pass", "Allowlist 8=fail"}, verify(r, Trust{Operator: Person{ID: operatorID}, Allowlist: []int64{8}}))
	})
	t.Run("property 6: the read answers for the id asked", func(t *testing.T) {
		liar := &fakeReader{people: map[int64]Person{operatorID: {ID: 9, Name: "Someone else"}}}
		assert.Equal(t, []string{"Operator=fail"}, verify(liar, Trust{Operator: Person{ID: operatorID}}))
	})
}

func TestReportIsReadyOnlyWhenEveryCheckPassed(t *testing.T) {
	r := &Report{Written: true}
	assert.False(t, r.Ready(), "no check ran")

	r.Add(Check{Name: "a", Status: StatusPass}, Check{Name: "b", Status: StatusWarn})
	assert.True(t, r.Ready())

	r.Add(Check{Name: "c", Status: ""})
	assert.False(t, r.Ready(), "a check with no status has not passed")

	r = &Report{Written: true}
	r.Add(Check{Name: "a", Status: StatusFail})
	assert.False(t, r.Ready())

	r = &Report{}
	r.Add(Check{Name: "a", Status: StatusPass})
	assert.False(t, r.Ready(), "nothing written is not ready")

	r = &Report{Written: true}
	r.Add(Check{Name: "a", Status: StatusFail})
	data, err := json.Marshal(r)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"ready":false`)
	assert.Contains(t, string(data), `"written":true`)
}

// The ticket is a bearer credential. The mint check posts to the account
// feed's ticket endpoint and keeps nothing: not in its result, not in an
// error.
func TestSDKReaderMintsAndDiscardsTheTicket(t *testing.T) {
	const ticket = "not-a-real-ticket"
	var mu sync.Mutex
	var seen []string
	answer := fmt.Sprintf(`{"ticket":%q,"expires_in":120,"url":"wss://example.test/cable?ticket=%s"}`, ticket, ticket)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, answer)
	}))
	t.Cleanup(srv.Close)

	client := basecamp.NewClient(&basecamp.Config{BaseURL: srv.URL}, &basecamp.StaticTokenProvider{Token: "t"}).ForAccount("999")
	require.NoError(t, SDKReader{Client: client}.MintStreamTicket(context.Background()))
	assert.Equal(t, []string{"POST /999/events/stream_ticket.json"}, seen)

	answer = `{"expires_in":120}`
	err := SDKReader{Client: client}.MintStreamTicket(context.Background())
	assert.ErrorIs(t, err, ErrMalformedTicket)

	answer = fmt.Sprintf(`{"ticket":%q}`, ticket)
	err = SDKReader{Client: client}.MintStreamTicket(context.Background())
	require.ErrorIs(t, err, ErrMalformedTicket)
	assert.False(t, strings.Contains(err.Error(), ticket))
}

// A route directory's name reaches a one-line terminal sink; control
// characters in it must not restyle or break that line.
func TestRouteChecksSanitizeThePath(t *testing.T) {
	f := validFile(t)
	f.Projects = map[int64]admission.Route{projectID: {Path: "/work/evil\x1b[31m\nFAKE ✓ line"}}
	checks := RouteChecks(context.Background(), &fakeReader{}, f)
	require.Len(t, checks, 1)
	assert.NotContains(t, checks[0].Message, "\x1b")
	assert.NotContains(t, checks[0].Message, "\n")
}

// ErrorText is the one formatter for read errors: an HTTP answer is its
// status and nothing the server wrote.
func TestErrorTextKeepsNothingTheServerWrote(t *testing.T) {
	const canary = "CANARY-bearer"
	httpErr := &basecamp.Error{Code: "validation", Message: "ticket " + canary, Hint: canary, HTTPStatus: 422}
	assert.Equal(t, "HTTP 422", ErrorText(httpErr))
	assert.Equal(t, "HTTP 422", ErrorText(fmt.Errorf("wrapped: %w", httpErr)))
	assert.NotContains(t, ErrorText(&basecamp.Error{Code: "network", Message: canary}), canary)
	assert.Equal(t, "the stream ticket response carries no ticket or no URL", ErrorText(ErrMalformedTicket))
}

func TestScopeCheck(t *testing.T) {
	for _, tc := range []struct {
		oauthType, granted, want string
	}{
		{"agent", "full", StatusPass},
		{"bc5", "full", StatusPass},
		{"launchpad", "", StatusPass},
		{"agent", "read", StatusFail},
		{"bc5", "read", StatusFail},
		{"agent", "", StatusFail},
		{"bc5", "", StatusFail},
		{"launchpad", "read", StatusFail},
		{"agent", "Full", StatusFail},
		{"agent", "full read", StatusFail},
		{"agent", "write", StatusFail},
		{"agent", "admin", StatusFail},
		{"agent", " full", StatusFail},
		{"", "", StatusFail},
		{"unknown", "", StatusFail},
	} {
		t.Run(tc.oauthType+"/"+tc.granted, func(t *testing.T) {
			assert.Equal(t, tc.want, ScopeCheck(tc.oauthType, tc.granted).Status, "unknown scopes are never full")
		})
	}
}
