package fakebasecamp_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/connector/fakebasecamp"
)

// otherProject is a second project the agent is on.
const otherProject int64 = 48699914

// waitFor is how long anything here waits for the fake or a client to get
// somewhere. Nothing waits on a timer: this is only the bound.
const waitFor = 15 * time.Second

// start serves the default world, with a second project the agent is on and
// a bearer token for the agent, which it returns.
func start(t *testing.T, opts ...fakebasecamp.Option) (*fakebasecamp.Server, string) {
	t.Helper()
	s := fakebasecamp.Start(t, fakebasecamp.DefaultWorld(), opts...)
	const token = "agent-token"
	s.Update(func(w *fakebasecamp.World) {
		w.Projects[otherProject] = &fakebasecamp.Project{
			ID: otherProject, Name: "Launch", Members: []int64{fakebasecamp.OperatorID, fakebasecamp.AgentID},
		}
		w.Tokens[token] = &fakebasecamp.Token{PersonID: fakebasecamp.AgentID, Scope: fakebasecamp.ScopeFull}
	})
	return s, token
}

// bounded is the test's context, ending after waitFor.
func bounded(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), waitFor)
	t.Cleanup(cancel)
	return ctx
}

// await waits for cond, failing the test when it does not come to hold.
func await(t *testing.T, s *fakebasecamp.Server, what string, cond func() bool) {
	t.Helper()
	require.NoError(t, s.Await(bounded(t), cond), "waiting for %s", what)
}

// accountClient is the SDK, in the fake's account, as the bearer.
func accountClient(s *fakebasecamp.Server, bearer string) *basecamp.AccountClient {
	return basecamp.NewClient(&basecamp.Config{BaseURL: s.URL()}, &basecamp.StaticTokenProvider{Token: bearer},
		basecamp.WithMaxRetries(1)).ForAccount("999")
}

// do makes one request to the fake and returns the status and the body.
func do(t *testing.T, s *fakebasecamp.Server, method, path, bearer, form string) (int, http.Header, string) {
	t.Helper()
	var body io.Reader
	if form != "" {
		body = strings.NewReader(form)
	}
	req, err := http.NewRequestWithContext(bounded(t), method, s.URL()+path, body)
	require.NoError(t, err)
	if form != "" {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, resp.Header, string(data)
}

// reporter is a Reporter that keeps what the fake reports, for a test of
// the reporting itself.
type reporter struct {
	*testing.T
	mu     sync.Mutex
	errors []string
}

func (r *reporter) Errorf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.errors = append(r.errors, fmt.Sprintf(format, args...))
}

func (r *reporter) reported() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.errors...)
}
