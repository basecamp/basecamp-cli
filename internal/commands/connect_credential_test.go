package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/auth"
	"github.com/basecamp/basecamp-cli/internal/config"
	"github.com/basecamp/basecamp-cli/internal/connector"
	"github.com/basecamp/basecamp-cli/internal/connector/setup"
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

	state, _, exit := connectorStoppedBy(stop, anAgent("Ryan Singer (agent)", "agent"), mustNotAsk(t))
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
	require.ErrorIs(t, stop, errCredentialNotTaken)
	assert.Equal(t, 1, asked)

	state, _, exit := connectorStoppedBy(stop, anAgent("Ryan Singer (agent)", "agent"), mustNotAsk(t))
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

// A bot user's refresh the token endpoint refuses is Basecamp's answer
// already, just as an Agent's refused mint is: the run stops at once, and
// says the bot is signed out.
func TestTheCredentialWatchStopsOnARefusedBotRefresh(t *testing.T) {
	w := newCredentialWatch()
	tokens := w.Tokens(tokenFunc(func() (string, error) { return "", refusedLogin() }))

	_, err := tokens.AccessToken(t.Context())
	require.ErrorIs(t, err, auth.ErrLoginRefused, "the read still sees its own failure")
	stop := runWatch(t, w, mustNotConfirm(t))
	require.ErrorIs(t, stop, auth.ErrLoginRefused)

	state, _, exit := connectorStoppedBy(stop, aBot("Triage Bot", "bot", 4242), mustNotAsk(t))
	var e *output.Error
	require.ErrorAs(t, exit, &e)
	assert.Equal(t, "Triage Bot is no longer signed in: Basecamp refused its login", e.Message)
	assert.Equal(t, connector.ConnectionSignedOut, state)
}

// A 401 for a bot user is asked about with a fresh token: its login is
// refreshed, and only a 401 for the fresh token too is Basecamp's answer.
// The watch then stops the run, and says the bot is signed out.
func TestABotUsers401ConfirmedWithAFreshTokenStopsTheRun(t *testing.T) {
	bc := newBotBasecamp(t)
	bc.answerMe(func(string) int { return http.StatusUnauthorized })
	app := bc.app(t)

	w := newCredentialWatch()
	roundTrip(t, w.WrapTransport(statusTransport(http.StatusUnauthorized)))
	stop := runWatch(t, w, func(ctx context.Context) bool { return confirmCredentialRefused(ctx, app, setup.KindBotUser, "bot") })
	require.ErrorIs(t, stop, errCredentialNotTaken)
	assert.Equal(t, []string{"refresh", "me fresh-1"}, bc.asked(), "asked who the fresh token is, not the old one")

	state, _, exit := connectorStoppedBy(stop, aBot("Triage Bot", "bot", 4242), mustNotAsk(t))
	var e *output.Error
	require.ErrorAs(t, exit, &e)
	assert.Equal(t, output.CodeAuth, e.Code)
	assert.Equal(t, connector.ConnectionSignedOut, state)
}

// A 401 that a fresh token clears, an access token turned away before its
// time say, leaves the run going: the bot's login stands.
func TestABotUsers401AFreshTokenClearsRunsOn(t *testing.T) {
	bc := newBotBasecamp(t)
	bc.answerMe(func(token string) int {
		if token == "fresh-1" {
			return http.StatusOK
		}
		return http.StatusUnauthorized
	})

	assert.False(t, confirmCredentialRefused(t.Context(), bc.app(t), setup.KindBotUser, "bot"))
	assert.Equal(t, []string{"refresh", "me fresh-1"}, bc.asked())
}

// A refresh the token endpoint refuses, met by the confirm itself, is
// Basecamp's answer, and nothing more is asked.
func TestABotUsersRefreshRefusedWhileConfirmingIsTheAnswer(t *testing.T) {
	bc := newBotBasecamp(t)
	bc.answerRefresh(http.StatusBadRequest, `{"error":"invalid_grant"}`)

	assert.True(t, confirmCredentialRefused(t.Context(), bc.app(t), setup.KindBotUser, "bot"))
	assert.Equal(t, []string{"refresh"}, bc.asked())
}

// A refresh that is rate-limited or meets a server fault says nothing about
// the login: the confirm answers no, and nothing is asked of a token it
// could not renew.
func TestABotUsersRefreshThatCanPassDoesNotStopTheRun(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusServiceUnavailable} {
		bc := newBotBasecamp(t)
		bc.answerRefresh(status, `{"error":"temporarily_unavailable"}`)
		bc.answerMe(func(string) int { return http.StatusUnauthorized })

		assert.False(t, confirmCredentialRefused(t.Context(), bc.app(t), setup.KindBotUser, "bot"), status)
		assert.Equal(t, []string{"refresh"}, bc.asked(), status)
	}
}

// A bot login stored from a bare token has no refresh to try; the token it
// has is the only one it will ever have, so a 401 for it is the answer.
func TestABotLoginWithNoRefreshIsAskedAboutWithItsOwnToken(t *testing.T) {
	bc := newBotBasecamp(t)
	bc.answerMe(func(string) int { return http.StatusUnauthorized })
	app := bc.app(t)
	creds, err := app.Auth.GetStore().Load("profile:bot")
	require.NoError(t, err)
	creds.RefreshToken, creds.ExpiresAt = "", 0
	require.NoError(t, app.Auth.GetStore().Save("profile:bot", creds))

	assert.True(t, confirmCredentialRefused(t.Context(), app, setup.KindBotUser, "bot"))
	assert.Equal(t, []string{"me old-tok"}, bc.asked())
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

// botBasecamp is a Basecamp that a bot user's profile refreshes against and
// reads who it is from, recording what it was asked.
type botBasecamp struct {
	srv           *httptest.Server
	mu            sync.Mutex
	log           []string
	refreshStatus int
	refreshBody   string
	refreshes     int
	me            func(token string) int
}

func newBotBasecamp(t *testing.T) *botBasecamp {
	t.Helper()
	bc := &botBasecamp{refreshStatus: http.StatusOK, me: func(string) int { return http.StatusOK }}
	bc.srv = httptest.NewServer(http.HandlerFunc(bc.serve))
	t.Cleanup(bc.srv.Close)
	return bc
}

func (bc *botBasecamp) serve(w http.ResponseWriter, r *http.Request) {
	bc.mu.Lock()
	defer bc.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/oauth/tokens":
		bc.log = append(bc.log, "refresh")
		if bc.refreshStatus != http.StatusOK {
			w.WriteHeader(bc.refreshStatus)
			fmt.Fprint(w, bc.refreshBody)
			return
		}
		bc.refreshes++
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": fmt.Sprintf("fresh-%d", bc.refreshes), "refresh_token": "ref-next", "expires_in": 3600,
		})
	case "/555/my/profile.json":
		token := r.Header.Get("Authorization")[len("Bearer "):]
		bc.log = append(bc.log, "me "+token)
		status := bc.me(token)
		w.WriteHeader(status)
		if status == http.StatusOK {
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 777, "name": "Triage Bot"})
		}
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (bc *botBasecamp) answerRefresh(status int, body string) {
	bc.mu.Lock()
	defer bc.mu.Unlock()
	bc.refreshStatus, bc.refreshBody = status, body
}

func (bc *botBasecamp) answerMe(status func(token string) int) {
	bc.mu.Lock()
	defer bc.mu.Unlock()
	bc.me = status
}

func (bc *botBasecamp) asked() []string {
	bc.mu.Lock()
	defer bc.mu.Unlock()
	return append([]string(nil), bc.log...)
}

// app is an App whose profile "bot", bound to account 555, holds a bot
// user's BC5 login that refreshes against bc, with an access token still
// inside its lifetime.
func (bc *botBasecamp) app(t *testing.T) *appctx.App {
	t.Helper()
	t.Setenv("BASECAMP_NO_KEYRING", "1")
	t.Setenv("BASECAMP_TOKEN", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	cfg := &config.Config{
		BaseURL: bc.srv.URL, ActiveProfile: "bot", Sources: map[string]string{},
		Profiles: map[string]*config.ProfileConfig{"bot": {BaseURL: bc.srv.URL, AccountID: "555"}},
	}
	mgr := auth.NewManager(cfg, bc.srv.Client())
	store := auth.NewStore(config.GlobalConfigDir())
	mgr.SetStore(store)
	require.NoError(t, store.Save("profile:bot", &auth.Credentials{
		AccessToken:   "old-tok",
		RefreshToken:  "old-ref",
		OAuthType:     "bc5",
		TokenEndpoint: bc.srv.URL + "/oauth/tokens",
		Scope:         "full",
		ExpiresAt:     time.Now().Add(time.Hour).Unix(),
	}))
	return &appctx.App{
		Config: cfg,
		Auth:   mgr,
		SDK:    basecamp.NewClient(&basecamp.Config{BaseURL: bc.srv.URL}, mgr),
	}
}
