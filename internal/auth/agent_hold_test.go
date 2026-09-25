package auth

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/output"
)

// mintEndpoint is a token endpoint that counts what reaches it and answers
// each request with whatever answer returns for it.
type mintEndpoint struct {
	srv    *httptest.Server
	calls  atomic.Int32
	answer func(call int) (status int, header http.Header, body string)
}

func startMintEndpoint(t *testing.T) *mintEndpoint {
	t.Helper()
	e := &mintEndpoint{}
	e.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		call := int(e.calls.Add(1)) - 1
		status, header, body := e.answer(call)
		for k, v := range header {
			w.Header()[k] = v
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}))
	t.Cleanup(e.srv.Close)
	return e
}

func (e *mintEndpoint) respond(status int, body string) {
	e.answer = func(int) (int, http.Header, string) { return status, nil, body }
}

func (e *mintEndpoint) url() string { return e.srv.URL + "/oauth/tokens" }

const mintedToken = `{"access_token":"minted","token_type":"bearer","expires_in":3600}`

// heldManager is a manager holding an agent credential due for renewal,
// with a clock the test moves by hand.
func heldManager(t *testing.T, e *mintEndpoint) (*Manager, string, *time.Time) {
	t.Helper()
	m := newDeviceTestManager(t, e.srv.URL)
	now := time.Now()
	m.clock = func() time.Time { return now }
	key := storeAgent(t, m, agentCredential(e.url(), time.Now().Add(-time.Minute)))
	return m, key, &now
}

// TestARefusedSecretIsNeverSentAgain is the production incident: a secret
// rotated server-side, and a caller that polls. After the first
// invalid_client, the next mint — in this process or any later one, however
// much later — answers from the store and sends nothing.
func TestARefusedSecretIsNeverSentAgain(t *testing.T) {
	e := startMintEndpoint(t)
	e.respond(http.StatusUnauthorized, `{"error":"invalid_client"}`)
	m, key, now := heldManager(t, e)

	_, err := m.AccessToken(context.Background())
	require.Error(t, err)
	require.EqualValues(t, 1, e.calls.Load())

	stored, loadErr := m.store.Load(key)
	require.NoError(t, loadErr)
	require.NotNil(t, stored.MintHold)
	assert.Zero(t, stored.MintHold.Until, "invalid_client is permanent for the secret it refused")
	assert.NotContains(t, stored.MintHold.Client, "agent-secret")

	*now = now.Add(30 * 24 * time.Hour)
	for range 3 {
		_, err = m.AccessToken(context.Background())
		require.Error(t, err)
	}
	assert.EqualValues(t, 1, e.calls.Load(), "a remembered refusal sent the dead secret again")

	e2 := output.AsError(err)
	assert.Equal(t, output.CodeAuth, e2.Code)
	assert.Contains(t, e2.Message, "invalid_client")
	assert.Contains(t, e2.Message, "remembered")
	assert.Contains(t, e2.Hint, "--with-client-credentials", "the remedy is the one the refusal itself carried")
	assert.NotContains(t, err.Error(), "agent-secret")

	// A later process reads the same verdict.
	fresh := newDeviceTestManager(t, e.srv.URL)
	fresh.store = m.store
	_, err = fresh.AccessToken(context.Background())
	require.Error(t, err)
	assert.EqualValues(t, 1, e.calls.Load())

	// And the report says so before anyone asks.
	refusal := m.RefreshRefusal(stored)
	require.Error(t, refusal)
	assert.Contains(t, output.AsError(refusal).Message, "remembered")
}

// TestANewSecretIsNotHeldToTheOldOnesRefusal: the hold names the client
// credentials it was about. A credential carrying a hold for a different
// secret — however it came to — mints as if it had none, and the success
// clears it.
func TestANewSecretIsNotHeldToTheOldOnesRefusal(t *testing.T) {
	e := startMintEndpoint(t)
	e.respond(http.StatusUnauthorized, `{"error":"invalid_client"}`)
	m, key, _ := heldManager(t, e)

	_, err := m.AccessToken(context.Background())
	require.Error(t, err)

	stored, err := m.store.Load(key)
	require.NoError(t, err)
	require.NotNil(t, stored.MintHold)
	stored.ClientSecret = "a-new-secret"
	require.NoError(t, m.store.Save(key, stored))

	e.respond(http.StatusOK, mintedToken)
	token, err := m.AccessToken(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "minted", token)
	assert.EqualValues(t, 2, e.calls.Load())

	stored, err = m.store.Load(key)
	require.NoError(t, err)
	assert.Nil(t, stored.MintHold, "a successful mint left the old refusal behind")
}

// TestARateLimitIsHeldUntilItsRetryAfter: nothing goes out until the
// server's deadline, and the caller hears a retryable rate limit saying
// when. After it, one request goes out, and its success clears the hold.
func TestARateLimitIsHeldUntilItsRetryAfter(t *testing.T) {
	e := startMintEndpoint(t)
	e.answer = func(call int) (int, http.Header, string) {
		if call == 0 {
			return http.StatusTooManyRequests, http.Header{"Retry-After": {"300"}}, `{"error":"invalid_client"}`
		}
		return http.StatusOK, nil, mintedToken
	}
	m, key, now := heldManager(t, e)

	_, err := m.AccessToken(context.Background())
	require.Error(t, err)
	assert.Equal(t, output.CodeRateLimit, output.AsError(err).Code)

	*now = now.Add(299 * time.Second)
	_, err = m.AccessToken(context.Background())
	require.Error(t, err)
	assert.EqualValues(t, 1, e.calls.Load(), "a mint went out inside the Retry-After")
	held := output.AsError(err)
	assert.Equal(t, output.CodeRateLimit, held.Code)
	assert.True(t, held.Retryable)
	assert.Contains(t, held.Message, "held until")
	assert.Contains(t, held.Hint, "1 seconds")
	assert.NotContains(t, held.Hint, "--with-client-credentials", "a rate limit is not a refused secret")

	*now = now.Add(time.Second)
	token, err := m.AccessToken(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "minted", token)
	assert.EqualValues(t, 2, e.calls.Load())

	stored, err := m.store.Load(key)
	require.NoError(t, err)
	assert.Nil(t, stored.MintHold)
}

// TestARateLimitHoldIsBounded: a 429 with no Retry-After is held for the
// default minute, and one asking for a day is not believed past the cap.
func TestARateLimitHoldIsBounded(t *testing.T) {
	for name, c := range map[string]struct {
		header http.Header
		want   time.Duration
	}{
		"no Retry-After":     {nil, defaultAgentRateLimitHold},
		"an absurd one":      {http.Header{"Retry-After": {"86400"}}, maxAgentConnectLifetime},
		"an HTTP-date":       {http.Header{"Retry-After": {time.Now().Add(10 * time.Minute).UTC().Format(http.TimeFormat)}}, 10 * time.Minute},
		"one that means now": {http.Header{"Retry-After": {"0"}}, defaultAgentRateLimitHold},
	} {
		t.Run(name, func(t *testing.T) {
			e := startMintEndpoint(t)
			e.answer = func(int) (int, http.Header, string) { return http.StatusTooManyRequests, c.header, `{}` }
			m, key, now := heldManager(t, e)

			_, err := m.AccessToken(context.Background())
			require.Error(t, err)

			stored, err := m.store.Load(key)
			require.NoError(t, err)
			require.NotNil(t, stored.MintHold)
			assert.Equal(t, mintHoldRateLimited, stored.MintHold.Kind)
			assert.InDelta(t, now.Add(c.want).Unix(), stored.MintHold.Until, 2)
			assert.LessOrEqual(t, stored.MintHold.Until, now.Add(maxAgentMintHold).Unix())
		})
	}
}

// TestAServerFaultHoldsNothing: a 5xx is the server's bad minute, not a
// verdict, and the next command asks again exactly as it always has.
func TestAServerFaultHoldsNothing(t *testing.T) {
	for name, status := range map[string]int{
		"internal error":     http.StatusInternalServerError,
		"bad gateway":        http.StatusBadGateway,
		"one naming client":  http.StatusServiceUnavailable,
		"a proxy's bare 400": http.StatusBadRequest,
	} {
		t.Run(name, func(t *testing.T) {
			e := startMintEndpoint(t)
			e.respond(status, `{"error":"invalid_client"}`)
			if status == http.StatusBadRequest {
				e.respond(status, `<html>Bad Request</html>`)
			}
			m, key, _ := heldManager(t, e)

			for range 2 {
				_, err := m.AccessToken(context.Background())
				require.Error(t, err)
			}
			assert.EqualValues(t, 2, e.calls.Load(), "a server fault was held")

			stored, err := m.store.Load(key)
			require.NoError(t, err)
			assert.Nil(t, stored.MintHold)
		})
	}
}

// TestARefusalThatCanReverseIsRecheckedHourly: invalid_grant is bc3's
// "Agent is no longer active", which reactivating the account undoes, and a
// bare 401 names nothing at all. Each is held an hour, then tried once: a
// second refusal holds another hour, and a success clears it without
// anyone finding the secret again.
func TestARefusalThatCanReverseIsRecheckedHourly(t *testing.T) {
	for name, refusal := range map[string]struct {
		status int
		body   string
	}{
		"invalid_grant": {http.StatusBadRequest, `{"error":"invalid_grant"}`},
		"a bare 401":    {http.StatusUnauthorized, `nothing useful`},
		"a bare 403":    {http.StatusForbidden, `nothing useful`},
	} {
		t.Run(name, func(t *testing.T) {
			e := startMintEndpoint(t)
			e.answer = func(call int) (int, http.Header, string) {
				if call < 2 {
					return refusal.status, nil, refusal.body
				}
				return http.StatusOK, nil, mintedToken
			}
			m, key, now := heldManager(t, e)

			_, err := m.AccessToken(context.Background())
			require.Error(t, err)

			*now = now.Add(agentRefusalRecheck - time.Second)
			_, err = m.AccessToken(context.Background())
			require.Error(t, err)
			assert.EqualValues(t, 1, e.calls.Load(), "a held refusal was rechecked early")
			assert.Equal(t, output.CodeAuth, output.AsError(err).Code)
			assert.Contains(t, output.AsError(err).Message, "tried once more")

			*now = now.Add(time.Second)
			_, err = m.AccessToken(context.Background())
			require.Error(t, err)
			assert.EqualValues(t, 2, e.calls.Load(), "the hourly recheck did not go out")

			_, err = m.AccessToken(context.Background())
			require.Error(t, err)
			assert.EqualValues(t, 2, e.calls.Load(), "a second refusal was not held again")

			*now = now.Add(agentRefusalRecheck)
			token, err := m.AccessToken(context.Background())
			require.NoError(t, err)
			assert.Equal(t, "minted", token)

			stored, err := m.store.Load(key)
			require.NoError(t, err)
			assert.Nil(t, stored.MintHold)
		})
	}
}

// TestAStaleRefusalNeverLandsOnANewSecret: the hold is written against a
// fresh read, and only while that read still holds the client credentials
// the refusal was about. A login that stored a new secret between the
// refusal and the write keeps its credential clean.
func TestAStaleRefusalNeverLandsOnANewSecret(t *testing.T) {
	e := startMintEndpoint(t)
	m, key, now := heldManager(t, e)

	stale := &MintHold{
		Kind:   mintHoldRefused,
		Detail: "token error: invalid_client",
		Client: agentClientFingerprint("agent-client", "agent-secret"),
		At:     now.Unix(),
	}

	relogged := agentCredential(e.url(), time.Now().Add(time.Hour))
	relogged.ClientSecret = "a-new-secret"
	require.NoError(t, m.store.Save(key, relogged))

	m.rememberMintHold(key, stale)

	stored, err := m.store.Load(key)
	require.NoError(t, err)
	assert.Nil(t, stored.MintHold, "a refusal of the old secret was written over the new one")
	assert.Equal(t, "a-new-secret", stored.ClientSecret)
}

// TestAHoldTooFarOutIsNotBelieved: a clock that stepped back, or a damaged
// record, must not be able to hold a mint for longer than any hold is
// allowed to last — nor can a kind this version does not know hold one at
// all. Either is ignored, which is the old behavior: ask again.
func TestAHoldTooFarOutIsNotBelieved(t *testing.T) {
	for name, hold := range map[string]MintHold{
		"a day out":      {Kind: mintHoldRateLimited, Until: time.Now().Add(24 * time.Hour).Unix()},
		"a kind unknown": {Kind: "something_newer"},
	} {
		t.Run(name, func(t *testing.T) {
			e := startMintEndpoint(t)
			e.respond(http.StatusOK, mintedToken)
			m, key, _ := heldManager(t, e)

			stored, err := m.store.Load(key)
			require.NoError(t, err)
			hold.Client = agentClientFingerprint(stored.ClientID, stored.ClientSecret)
			stored.MintHold = &hold
			require.NoError(t, m.store.Save(key, stored))

			_, err = m.AccessToken(context.Background())
			require.NoError(t, err)
			assert.EqualValues(t, 1, e.calls.Load())
		})
	}
}
