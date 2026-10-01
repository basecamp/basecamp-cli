package commands

import (
	"context"
	"crypto/tls"
	"crypto/x509"
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
	"github.com/basecamp/basecamp-cli/internal/connector"
	"github.com/basecamp/basecamp-cli/internal/connector/setup"
	"github.com/basecamp/basecamp-cli/internal/output"
)

const mintedToken = `{"access_token":"bc_at_minted","token_type":"bearer","expires_in":3600}`

// startTokens is a starting agent's tokens over a clock the test drives,
// so a wait spent in a fake sleep is time the mint hold sees pass.
type startTokens struct {
	managerTokens
	now *time.Time
}

// startingAgent is an Agent whose token has expired, against a token
// endpoint that gives the answers in order, the last one from then on.
func startingAgent(t *testing.T, answers ...func(http.ResponseWriter)) (*startTokens, func() int) {
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
	// On a whole second, as a hold's deadline is stored: a wait read back
	// from it is then exactly the one the endpoint named.
	now := time.Now().Truncate(time.Second)
	mgr.SetClock(func() time.Time { return now })
	return &startTokens{managerTokens: managerTokens{mgr: mgr}, now: &now}, func() int { mu.Lock(); defer mu.Unlock(); return mints }
}

// startWaits records the waits a start takes, and the words it logs for
// each, without spending them.
type startWaits struct {
	waits []time.Duration
	lines []string
}

func (w *startWaits) options(tokens *startTokens) connectStartWait {
	return connectStartWait{
		Log: func(line string) { w.lines = append(w.lines, line) },
		Sleep: func(_ context.Context, d time.Duration) error {
			w.waits = append(w.waits, d)
			*tokens.now = tokens.now.Add(d)
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

	require.NoError(t, awaitConnectToken(t.Context(), tokens, w.options(tokens)))
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

	require.NoError(t, awaitConnectToken(t.Context(), tokens, w.options(tokens)))
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

	require.NoError(t, awaitConnectToken(t.Context(), tokens, w.options(tokens)))
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

		err := awaitConnectToken(t.Context(), tokens, w.options(tokens))
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
// or one the client could not send at all. A request that got no response
// is, a misconfigured TLS or proxy included.
func TestOnlyARequestThatGotNoResponseIsRetried(t *testing.T) {
	wrap := func(target string, err error) error {
		return fmt.Errorf("minting an agent token: %w", &url.Error{Op: "Post", URL: target, Err: err})
	}
	const endpoint = "https://example.test/oauth/tokens"
	for name, err := range map[string]error{
		"blocked by policy":  wrap(endpoint, &net.OpError{Op: "dial", Net: "tcp", Err: surfguard.ErrBlocked}),
		"unsupported scheme": wrap("ftp://example.test/oauth/tokens", errors.New(`unsupported protocol scheme "ftp"`)),
		"no host":            wrap("https:///oauth/tokens", errors.New("http: no Host in request URL")),
	} {
		_, ok := tokenRetry(err)
		assert.False(t, ok, name)
	}
	for name, err := range map[string]error{
		"refused":       wrap(endpoint, &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}),
		"dropped":       wrap(endpoint, io.EOF),
		"no name":       wrap(endpoint, &net.DNSError{Err: "no such host", Name: "example.test", IsNotFound: true}),
		"cut off":       wrap(endpoint, io.ErrUnexpectedEOF),
		"untrusted TLS": wrap(endpoint, &tls.CertificateVerificationError{Err: x509.UnknownAuthorityError{}}),
		"proxy":         wrap(endpoint, &net.OpError{Op: "proxyconnect", Net: "tcp", Err: syscall.ECONNREFUSED}),
	} {
		wait, ok := tokenRetry(err)
		assert.True(t, ok, name)
		assert.Zero(t, wait, name)
	}
}

// A token endpoint whose certificate is not trusted got no response to
// classify, and the start keeps waiting — saying why each time — rather
// than exiting.
func TestAStartKeepsWaitingOnAnUntrustedCertificate(t *testing.T) {
	t.Setenv("BASECAMP_NO_KEYRING", "1")
	t.Setenv("BASECAMP_TOKEN", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("a request got through an untrusted certificate")
	}))
	t.Cleanup(srv.Close)

	cfg := &config.Config{AccountID: "555", BaseURL: srv.URL, ActiveProfile: "agent", Sources: map[string]string{}}
	mgr := auth.NewManager(cfg, &http.Client{})
	store := auth.NewStore(config.GlobalConfigDir())
	mgr.SetStore(store)
	require.NoError(t, store.Save("profile:agent", &auth.Credentials{
		AccessToken: "bc_at_expired", OAuthType: "agent", ClientID: "agent-client", ClientSecret: "agent-secret",
		TokenEndpoint: srv.URL + "/oauth/tokens", ExpiresAt: time.Now().Add(-time.Minute).Unix(),
	}))

	ctx, cancel := context.WithCancel(t.Context())
	var lines []string
	waits := 0
	err := awaitConnectToken(ctx, &managerTokens{mgr: mgr}, connectStartWait{
		Log: func(line string) { lines = append(lines, line) },
		Sleep: func(context.Context, time.Duration) error {
			if waits++; waits == 2 {
				cancel()
			}
			return nil
		},
	})
	assert.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 2, waits)
	require.NotEmpty(t, lines)
	assert.Contains(t, lines[0], "certificate")
}

// truncatedAnswer sends status and headers promising more body than it
// sends, then drops the connection.
func truncatedAnswer(t *testing.T, status int) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", "500")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"error":`))
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
}

// A response classifies by its status even when its body is cut off: a
// truncated 401 is still a refusal, and ends the start as a disconnect.
func TestATruncatedRefusalStillEndsTheStart(t *testing.T) {
	tokens, mints := startingAgent(t, truncatedAnswer(t, http.StatusUnauthorized))
	var w startWaits

	err := awaitConnectToken(t.Context(), tokens, w.options(tokens))
	require.Error(t, err)
	assert.ErrorIs(t, err, auth.ErrAgentCredentialRefused)
	assert.Equal(t, errAgentDisconnected("", "agent"), connectStartFailure(setup.KindAgent, "agent", err))
	assert.Empty(t, w.waits)
	assert.Equal(t, 1, mints())
}

// A truncated 503 is still the server's own trouble, and is waited out; a
// truncated 200 never delivered its token, and is waited out too.
func TestATruncatedServerFaultIsWaitedOut(t *testing.T) {
	for _, status := range []int{http.StatusServiceUnavailable, http.StatusOK} {
		tokens, mints := startingAgent(t, truncatedAnswer(t, status), answerStatus(http.StatusOK, nil, mintedToken))
		var w startWaits

		require.NoError(t, awaitConnectToken(t.Context(), tokens, w.options(tokens)), status)
		assert.Len(t, w.waits, 1, status)
		assert.Equal(t, 2, mints(), status)
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

// stuckTokens is a renewal that does not stop for its context, as saving a
// renewed credential does not.
type stuckTokens struct{ release chan struct{} }

func (s stuckTokens) AccessToken(context.Context) (string, error) {
	<-s.release
	return "bc_at_minted", nil
}

// A second signal ends a start whose renewal will not stop, as it ends a
// running connector.
func TestASecondSignalForcesAStuckStartToEnd(t *testing.T) {
	tokens := stuckTokens{release: make(chan struct{})}
	signals := make(chan os.Signal, 2)
	exited := make(chan int, 1)
	signals <- syscall.SIGTERM
	signals <- syscall.SIGTERM

	result := make(chan error, 1)
	go func() {
		result <- awaitConnectToken(t.Context(), tokens, connectStartWait{Log: func(string) {}, Signals: signals, Exit: func(code int) { exited <- code }})
	}()
	select {
	case code := <-exited:
		assert.Equal(t, connector.ExitCodeForSignal(syscall.SIGTERM), code)
	case <-time.After(5 * time.Second):
		t.Fatal("a second signal did not end the stuck start")
	}
	close(tokens.release)
	var e *output.Error
	require.ErrorAs(t, <-result, &e)
	assert.Equal(t, output.CodeTerminated, e.Code)
}

// A rate limit the CLI holds itself is waited out to its stored deadline,
// not on backoff: the hold answers locally until then, so any earlier
// attempt is spent for nothing.
func TestAStartWaitsOutAStoredRateLimitHold(t *testing.T) {
	tokens, mints := startingAgent(t, answerStatus(http.StatusTooManyRequests, map[string]string{"Retry-After": "90"}, ""))
	ctx, cancel := context.WithCancel(t.Context())
	var waits []time.Duration
	err := awaitConnectToken(ctx, tokens, connectStartWait{
		Log: func(string) {},
		Sleep: func(_ context.Context, d time.Duration) error {
			if waits = append(waits, d); len(waits) == 2 {
				cancel()
			}
			// Short of the deadline: the hold, not the endpoint, answers next.
			*tokens.now = tokens.now.Add(d / 2)
			return nil
		},
	})
	require.ErrorIs(t, err, context.Canceled)
	require.Len(t, waits, 2)
	assert.InDelta(t, 90*time.Second, waits[0], float64(2*time.Second), "waited %s, not the hold's deadline", waits[0])
	assert.InDelta(t, 45*time.Second, waits[1], float64(2*time.Second), "waited %s, not what is left of the hold", waits[1])
	assert.Equal(t, 1, mints(), "the second attempt was the stored hold's to answer")

}
