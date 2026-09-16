package setup

import (
	"context"
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

func TestOperatorCheck(t *testing.T) {
	ctx := context.Background()
	people := map[int64]Person{
		operatorID: {ID: operatorID, Name: "Operator"},
		7:          {ID: 7, PersonableType: PersonableAgent},
		8:          {ID: 8, Client: true},
	}
	r := &fakeReader{people: people}
	forbidden := &fakeReader{personErr: status(http.StatusForbidden)}

	assert.Equal(t, StatusPass, OperatorCheck(ctx, r, Person{ID: operatorID}, agentID, "", false).Status)
	assert.Equal(t, StatusPass, OperatorCheck(ctx, forbidden, people[operatorID], agentID, "me", false).Status,
		"an operator proved by their own profile needs no read as the agent")
	assert.Equal(t, StatusFail, OperatorCheck(ctx, r, Person{ID: agentID}, agentID, "", false).Status, "the agent is never the operator")
	assert.Equal(t, StatusFail, OperatorCheck(ctx, r, Person{ID: 7}, agentID, "", false).Status, "an Agent is not an operator")
	assert.Equal(t, StatusFail, OperatorCheck(ctx, r, Person{ID: 8}, agentID, "", false).Status, "a client is not an operator")
	assert.Equal(t, StatusFail, OperatorCheck(ctx, forbidden, people[7], agentID, "me", false).Status,
		"a profile that reads back as an Agent is refused too")
	assert.Equal(t, StatusFail, OperatorCheck(ctx, forbidden, people[8], agentID, "me", false).Status,
		"a profile that reads back as a client is refused too")

	recorded := OperatorCheck(ctx, forbidden, Person{ID: operatorID}, agentID, "", true)
	assert.Equal(t, StatusWarn, recorded.Status, "the operator connect.json already holds was verified when recorded")
	assert.Equal(t, StatusFail, OperatorCheck(ctx, r, Person{ID: 8}, agentID, "", true).Status,
		"a recorded operator that now reads back as a client still fails")

	c := OperatorCheck(ctx, forbidden, Person{ID: operatorID}, agentID, "", false)
	assert.Equal(t, StatusFail, c.Status, "an id that cannot be verified is not recorded as the trust anchor")
	assert.Contains(t, c.Hint, "--operator-profile")
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
