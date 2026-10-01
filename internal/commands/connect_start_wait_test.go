package commands

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	surfguard "github.com/basecamp/surfguard/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/auth"
	"github.com/basecamp/basecamp-cli/internal/config"
	"github.com/basecamp/basecamp-cli/internal/connector/setup"
	"github.com/basecamp/basecamp-cli/internal/output"
)

const mintedToken = `{"access_token":"bc_at_minted","token_type":"bearer","expires_in":3600}`

// startingAgent is an Agent whose token has expired, against a token
// endpoint that gives the answers in order, the last one from then on.
func startingAgent(t *testing.T, answers ...func(http.ResponseWriter)) (*managerTokens, func() int) {
	t.Helper()
	t.Setenv("BASECAMP_NO_KEYRING", "1")
	t.Setenv("BASECAMP_TOKEN", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	var mu sync.Mutex
	mints := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/tokens" {
			t.Errorf("the start reached %s", r.URL.Path)
			w.WriteHeader(http.StatusTeapot)
			return
		}
		mu.Lock()
		answer := answers[min(mints, len(answers)-1)]
		mints++
		mu.Unlock()
		answer(w)
	}))
	t.Cleanup(srv.Close)

	cfg := &config.Config{AccountID: "555", BaseURL: srv.URL, ActiveProfile: "agent", Sources: map[string]string{}}
	mgr := auth.NewManager(cfg, srv.Client())
	store := auth.NewStore(config.GlobalConfigDir())
	mgr.SetStore(store)
	require.NoError(t, store.Save("profile:agent", &auth.Credentials{
		AccessToken:   "bc_at_expired",
		OAuthType:     "agent",
		ClientID:      "agent-client",
		ClientSecret:  "agent-secret",
		TokenEndpoint: srv.URL + "/oauth/tokens",
		ExpiresAt:     time.Now().Add(-time.Minute).Unix(),
	}))
	return &managerTokens{mgr: mgr}, func() int { mu.Lock(); defer mu.Unlock(); return mints }
}

// startWaits records the waits a start takes, and the words it logs for
// each, without spending them.
type startWaits struct {
	waits []time.Duration
	lines []string
}

func (w *startWaits) options() connectStartWait {
	return connectStartWait{
		Log: func(line string) { w.lines = append(w.lines, line) },
		Sleep: func(_ context.Context, d time.Duration) error {
			w.waits = append(w.waits, d)
			return nil
		},
	}
}

// A connector started while the token endpoint rate-limits the agent waits
// for as long as it was asked, says so once, and comes up.
func TestAStartWaitsOutARateLimitAndComesUp(t *testing.T) {
	tokens, mints := startingAgent(t,
		answerStatus(http.StatusTooManyRequests, map[string]string{"Retry-After": "42"}, `{"error":"slow_down"}`),
		answerStatus(http.StatusOK, nil, mintedToken))
	var w startWaits

	require.NoError(t, awaitConnectToken(t.Context(), tokens, w.options()))
	assert.Equal(t, []time.Duration{42 * time.Second}, w.waits)
	require.Len(t, w.lines, 1)
	assert.Contains(t, w.lines[0], "42s")
	assert.Equal(t, 2, mints())

	token, err := tokens.AccessToken(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "bc_at_minted", token, "what the start waited for is the token the run uses")
}

// The token endpoint's own trouble is waited out on the feed's backoff,
// however many times it repeats.
func TestAStartRidesOutServerFaults(t *testing.T) {
	unavailable := answerStatus(http.StatusServiceUnavailable, nil, "")
	tokens, mints := startingAgent(t, unavailable, unavailable, unavailable, answerStatus(http.StatusOK, nil, mintedToken))
	var w startWaits

	require.NoError(t, awaitConnectToken(t.Context(), tokens, w.options()))
	require.Len(t, w.waits, 3)
	for _, d := range w.waits {
		assert.True(t, d > 0 && d <= time.Minute, "%s is within the feed's backoff", d)
	}
	assert.Len(t, w.lines, 3)
	for _, line := range w.lines {
		assert.NotContains(t, line, "in 0s", "a wait is never logged as none")
	}
	assert.Equal(t, 4, mints())
}

// A token endpoint that does not answer at all is waited out the same way.
func TestAStartRidesOutAnUnreachableTokenEndpoint(t *testing.T) {
	dropped := func(w http.ResponseWriter) {
		hj, ok := w.(http.Hijacker)
		if !assert.True(t, ok) {
			return
		}
		conn, _, err := hj.Hijack()
		if !assert.NoError(t, err) {
			return
		}
		_ = conn.Close()
	}
	tokens, _ := startingAgent(t, dropped, answerStatus(http.StatusOK, nil, mintedToken))
	var w startWaits

	require.NoError(t, awaitConnectToken(t.Context(), tokens, w.options()))
	assert.Len(t, w.waits, 1)
}

// A refused credential is not waited on: the start ends at once, as the
// disconnect it is.
func TestAStartEndsAtOnceOnARefusedCredential(t *testing.T) {
	for name, answer := range map[string]func(http.ResponseWriter){
		"invalid_client": answerStatus(http.StatusBadRequest, nil, `{"error":"invalid_client"}`),
		"invalid_grant":  answerStatus(http.StatusBadRequest, nil, `{"error":"invalid_grant"}`),
		"bare 401":       answerStatus(http.StatusUnauthorized, nil, ""),
		"bare 403":       answerStatus(http.StatusForbidden, nil, ""),
	} {
		tokens, mints := startingAgent(t, answer)
		var w startWaits

		err := awaitConnectToken(t.Context(), tokens, w.options())
		require.Error(t, err, name)
		assert.True(t, errors.Is(err, auth.ErrAgentCredentialRefused), name)
		assert.True(t, agentDisconnectedAtStart(setup.KindAgent, err), name)
		assert.Empty(t, w.waits, name)
		assert.Equal(t, 1, mints(), name)
	}
}

// A connector waiting to start stops promptly when it is told to: canceled,
// or sent SIGTERM or an interrupt, it does not sit out the wait.
func TestAStartStopsPromptlyWhileItWaits(t *testing.T) {
	limited := answerStatus(http.StatusTooManyRequests, map[string]string{"Retry-After": "3600"}, "")

	t.Run("canceled", func(t *testing.T) {
		tokens, _ := startingAgent(t, limited)
		ctx, cancel := context.WithCancel(t.Context())
		time.AfterFunc(50*time.Millisecond, cancel)
		start := time.Now()
		err := awaitConnectToken(ctx, tokens, connectStartWait{Log: func(string) {}})
		assert.ErrorIs(t, err, context.Canceled)
		assert.Less(t, time.Since(start), 5*time.Second)
	})

	for sig, want := range map[os.Signal]string{syscall.SIGTERM: output.CodeTerminated, os.Interrupt: output.CodeInterrupted} {
		t.Run(sig.String(), func(t *testing.T) {
			tokens, _ := startingAgent(t, limited)
			signals := make(chan os.Signal, 1)
			time.AfterFunc(50*time.Millisecond, func() { signals <- sig })
			start := time.Now()
			err := awaitConnectToken(t.Context(), tokens, connectStartWait{Log: func(string) {}, Signals: signals})
			var e *output.Error
			require.ErrorAs(t, err, &e)
			assert.Equal(t, want, e.Code)
			assert.Less(t, time.Since(start), 5*time.Second)
		})
	}
}

// A stop that arrives while a renewal is in flight cancels it; one that
// arrives as the renewal succeeds still stops the start.
func TestAStopDuringARenewalStopsTheStart(t *testing.T) {
	t.Run("hung renewal", func(t *testing.T) {
		release := make(chan struct{})
		tokens, _ := startingAgent(t, func(w http.ResponseWriter) {
			<-release
			answerStatus(http.StatusOK, nil, mintedToken)(w)
		})
		// After the server's own cleanup is registered, so it runs first:
		// the server cannot close while its handler is held.
		t.Cleanup(func() { close(release) })
		signals := make(chan os.Signal, 1)
		time.AfterFunc(50*time.Millisecond, func() { signals <- syscall.SIGTERM })
		start := time.Now()
		err := awaitConnectToken(t.Context(), tokens, connectStartWait{Log: func(string) {}, Signals: signals})
		var e *output.Error
		require.ErrorAs(t, err, &e)
		assert.Equal(t, output.CodeTerminated, e.Code)
		assert.Less(t, time.Since(start), 5*time.Second)
	})

	t.Run("renewal succeeds", func(t *testing.T) {
		signals := make(chan os.Signal, 1)
		tokens, _ := startingAgent(t, func(w http.ResponseWriter) {
			signals <- os.Interrupt
			answerStatus(http.StatusOK, nil, mintedToken)(w)
		})
		err := awaitConnectToken(t.Context(), tokens, connectStartWait{Log: func(string) {}, Signals: signals})
		var e *output.Error
		require.ErrorAs(t, err, &e)
		assert.Equal(t, output.CodeInterrupted, e.Code)
	})
}

// The start's backoff is the feed's: a full-jitter draw under an envelope
// that doubles from a second and stops at a minute.
func TestTheStartBacksOffAsTheFeedDoes(t *testing.T) {
	for attempt, envelope := range map[int]time.Duration{1: time.Second, 2: 2 * time.Second, 6: 32 * time.Second, 7: time.Minute, 1000: time.Minute} {
		for range 100 {
			d := connectStartBackoff(attempt)
			assert.True(t, d > 0 && d <= envelope, "attempt %d drew %s", attempt, d)
		}
	}
}

// What no wait can change is not retried: a URL the egress policy refused,
// or one the client could not send at all. A request that got no answer is.
func TestOnlyATransportFailureWithoutAnAnswerIsRetried(t *testing.T) {
	wrap := func(err error) error {
		return fmt.Errorf("minting an agent token: %w", &url.Error{Op: "Post", URL: "https://example.test/oauth/tokens", Err: err})
	}
	for name, err := range map[string]error{
		"blocked by policy":  wrap(&net.OpError{Op: "dial", Net: "tcp", Err: surfguard.ErrBlocked}),
		"unsupported scheme": wrap(errors.New("unsupported protocol scheme \"ftp\"")),
	} {
		_, ok := tokenRetry(err)
		assert.False(t, ok, name)
	}
	for name, err := range map[string]error{
		"refused": wrap(&net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}),
		"dropped": wrap(io.EOF),
		"no name": wrap(&net.DNSError{Err: "no such host", Name: "example.test", IsNotFound: true}),
		"cut off": wrap(io.ErrUnexpectedEOF),
	} {
		_, ok := tokenRetry(err)
		assert.True(t, ok, name)
	}
}

// An OAuth refresh carries its rate limit as the SDK's error, under the
// CLI's: the wait it names is the one taken.
func TestARefreshRateLimitKeepsItsWait(t *testing.T) {
	refused := &output.Error{Code: output.CodeRateLimit, Message: "token refresh failed: rate limited", HTTPStatus: 429, Retryable: true,
		Cause: basecamp.ErrRateLimit(30)}
	wait, ok := tokenRetry(refused)
	assert.True(t, ok)
	assert.Equal(t, 30*time.Second, wait)
}

// A start that cannot learn who it is says so in the profile's terms,
// unless it was stopped or the agent was disconnected.
func TestAStartFailureKeepsItsFraming(t *testing.T) {
	refused := output.ErrAuth("Minting an agent token was refused (invalid_client)")
	refused.Cause = auth.ErrAgentCredentialRefused
	terminated := output.ErrTerminated("connector terminated")

	assert.Same(t, terminated, connectStartFailure(setup.KindAgent, "agent", terminated))
	assert.ErrorIs(t, connectStartFailure(setup.KindAgent, "agent", context.Canceled), context.Canceled)
	assert.Equal(t, errAgentDisconnected("", "agent"), connectStartFailure(setup.KindAgent, "agent", refused))

	var e *output.Error
	require.ErrorAs(t, connectStartFailure(setup.KindAgent, "agent", output.ErrAPI(404, "minting an agent token: the server answered HTTP 404")), &e)
	assert.Equal(t, output.CodeAuth, e.Code)
	assert.Equal(t, `Could not read who profile "agent" is: minting an agent token: the server answered HTTP 404`, e.Message)
}
