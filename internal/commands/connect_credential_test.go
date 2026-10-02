package commands

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/auth"
	"github.com/basecamp/basecamp-cli/internal/connector"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// A token renewal Basecamp refuses, met by any read the watch sees, stops
// the run at once, and the run says it as the disconnect it is without
// asking Basecamp again.
func TestTheCredentialWatchStopsOnARefusedRenewal(t *testing.T) {
	w := newCredentialWatch()
	refused := output.ErrAuth("Minting an agent token was refused (invalid_client)")
	refused.Cause = auth.ErrAgentCredentialRefused
	tokens := w.Tokens(tokenFunc(func() (string, error) { return "", refused }))

	_, err := tokens.AccessToken(t.Context())
	require.ErrorIs(t, err, auth.ErrAgentCredentialRefused, "the read still sees its own failure")
	stop := runWatch(t, w, mustNotConfirm(t))
	require.ErrorIs(t, stop, auth.ErrAgentCredentialRefused)

	state, _, exit := connectorStoppedBy(stop, "Ryan Singer (agent)", "agent", mustNotAsk(t))
	var e *output.Error
	require.ErrorAs(t, exit, &e)
	assert.Equal(t, "Ryan Singer (agent) was disconnected in Basecamp, or connected on another computer", e.Message)
	assert.Equal(t, connector.ConnectionDisconnected, state)
}

// A read answered 401 asks Basecamp, and stops the run only when Basecamp
// confirms it no longer takes the credential.
func TestTheCredentialWatchAsksAboutAnUnauthorizedRead(t *testing.T) {
	w := newCredentialWatch()
	transport := w.WrapTransport(statusTransport(http.StatusUnauthorized))
	roundTrip(t, transport)

	asked := 0
	stop := runWatch(t, w, func(context.Context) bool { asked++; return true })
	require.ErrorIs(t, stop, errAgentCredentialNotTaken)
	assert.Equal(t, 1, asked)

	state, _, exit := connectorStoppedBy(stop, "Ryan Singer (agent)", "agent", mustNotAsk(t))
	var e *output.Error
	require.ErrorAs(t, exit, &e)
	assert.Equal(t, output.CodeAuth, e.Code)
	assert.Equal(t, connector.ConnectionDisconnected, state)
}

// A 401 Basecamp does not confirm, from a proxy say, leaves the run going,
// and the next one asks again.
func TestTheCredentialWatchRunsOnWhenBasecampStillTakesTheCredential(t *testing.T) {
	w := newCredentialWatch()
	transport := w.WrapTransport(statusTransport(http.StatusUnauthorized))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	asks := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- w.Run(ctx, func(context.Context) bool { asks <- struct{}{}; return false })
	}()

	for range 2 {
		roundTrip(t, transport)
		select {
		case <-asks:
		case <-time.After(10 * time.Second):
			t.Fatal("the watch did not ask about a 401")
		}
	}
	cancel()
	require.NoError(t, <-done, "ended by its context, the watch stops nothing")
}

// Every other failure is the reads' own: neither a renewal that failed for
// another reason nor another status asks Basecamp or stops the run.
func TestTheCredentialWatchIgnoresOtherFailures(t *testing.T) {
	w := newCredentialWatch()
	tokens := w.Tokens(tokenFunc(func() (string, error) { return "", output.ErrRateLimit(1) }))
	_, err := tokens.AccessToken(t.Context())
	require.Error(t, err)
	for _, status := range []int{http.StatusForbidden, http.StatusNotFound, http.StatusTooManyRequests, http.StatusInternalServerError} {
		roundTrip(t, w.WrapTransport(statusTransport(status)))
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	assert.NoError(t, w.Run(ctx, mustNotConfirm(t)))
	assert.Empty(t, w.refused)
	assert.Empty(t, w.suspect)
}

type tokenFunc func() (string, error)

func (f tokenFunc) AccessToken(context.Context) (string, error) { return f() }

type statusTransport int

func (s statusTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: int(s), Body: http.NoBody, Request: req}, nil
}

func roundTrip(t *testing.T, rt http.RoundTripper) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://basecamp.test/999/my/profile.json", nil)
	require.NoError(t, err)
	resp, err := rt.RoundTrip(req)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
}

// runWatch runs w until it stops on its own, failing the test if it does
// not within a deadline that only bounds a failure.
func runWatch(t *testing.T, w *credentialWatch, confirm func(context.Context) bool) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	err := w.Run(ctx, confirm)
	if ctx.Err() != nil {
		t.Fatal("the watch did not stop")
	}
	return err
}

func mustNotConfirm(t *testing.T) func(context.Context) bool {
	return func(context.Context) bool { t.Error("asked Basecamp to confirm what it had already said"); return true }
}
