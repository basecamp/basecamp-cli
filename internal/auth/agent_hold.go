package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"net/http"
	"time"

	"github.com/basecamp/basecamp-cli/internal/output"
)

// Remembered mint verdicts.
//
// An agent credential renews by minting, and every process that finds its
// token inside the renewal window mints. For a person at a terminal that is
// one request per command. For an automated caller it is one request per
// poll, forever: a connector that shells out to `basecamp` on a timer, or
// the in-process `basecamp connect`, will present a dead secret to the
// token endpoint as often as it polls, and nothing stops it but a person
// noticing.
//
// That is not hypothetical. An agent whose secret was rotated server-side
// kept presenting the old one about six times a minute for more than twelve
// hours — some 350 requests an hour, nearly all of them answered 429 by the
// abuse tracker the first few 401s had tripped, and all of them for a
// secret that would never work again.
//
// So the token endpoint's verdict on a mint is kept with the credential, and
// the next mint reads it before it sends anything. It is kept for exactly
// the client credentials it was given about (see agentClientFingerprint): a
// login with a new secret writes a new credential, which carries no hold,
// and a hold that names other client credentials than the ones stored is
// ignored however it got there.

// Mint hold kinds. Anything else read back from a store — a newer CLI's
// kind, a damaged record — holds nothing, so a hold this version cannot
// read degrades to the old behavior of trying again, never to a refusal
// nobody can explain.
const (
	// mintHoldRefused is the token endpoint refusing the client
	// credentials themselves (clientRefusalCodes, or a bare 401/403).
	mintHoldRefused = "refused"

	// mintHoldRateLimited is a 429, held until its Retry-After.
	mintHoldRateLimited = "rate_limited"
)

// agentRefusalRecheck is how long a refusal that could reverse is held
// before the client credentials are presented once more.
//
// What bc3 says, and why, decides which refusals get one
// (app/controllers/oauth/tokens_controller.rb,
// handle_client_credentials_grant):
//
//   - invalid_client is the secret. authenticate_client! refuses a secret
//     that does not match the client's digest, and the mint refuses one
//     that stopped being the live digest while it was in flight or whose
//     client is quarantined. Oauth::AgentClients.rotate! replaces the
//     digest, disconnect! clears it, and a quarantine is one-way
//     (Oauth::Client::Disabling), so no later state of the server makes the
//     same secret good again. It is held with no recheck at all: asking
//     again cannot succeed, and each ask is charged to the abuse tracker.
//
//   - invalid_grant is "Agent is no longer active" — the agent's account is
//     not active. That is a fact about the account, not the secret, and an
//     account that is reactivated makes the same secret good again. Held
//     for an hour, then tried once, so a reactivated agent recovers on its
//     own without anyone having to find its secret again.
//
//   - A bare 401 or 403 names nothing. bc3 always names its refusals, so
//     one of these came from something else — a proxy, a WAF challenge —
//     and is no evidence the secret is dead. Held as invalid_grant is:
//     one try an hour costs nothing, and "never" would be a guess.
//
// An hour against the incident's 350: the abuse tracker blocks after ten
// invalid_client answers from one address, and one attempt an hour never
// gets there.
const agentRefusalRecheck = time.Hour

// defaultAgentRateLimitHold is how long a 429 with no readable Retry-After
// is held — the fallback the rest of the CLI takes for one (see
// resilience.GatingHooks.OnRequestEnd).
const defaultAgentRateLimitHold = 60 * time.Second

// maxAgentMintHold bounds any hold that expires. A Retry-After past it is
// not honored in full, and a stored expiry further out than this from now —
// a clock that stepped back, a damaged record — is not believed at all:
// that hold is ignored and the mint goes out, which is the old behavior and
// the safe direction to fail in.
const maxAgentMintHold = time.Hour

// MintHold is the token endpoint's last refusal of an agent credential's
// client, remembered so the next mint can answer it without asking again.
//
// Nothing in it came from the server as free text: Detail is the same fixed
// vocabulary agentMintRefusal renders (see oauthErrorCodes).
type MintHold struct {
	// Kind is mintHoldRefused or mintHoldRateLimited.
	Kind string `json:"kind"`

	// Detail is what the server said, as agentMintRefusal rendered it:
	// "token error: invalid_client", or "the server answered HTTP 401".
	Detail string `json:"detail"`

	// Client is agentClientFingerprint of the client credentials the
	// verdict was about. A hold for any others holds nothing.
	Client string `json:"client"`

	// At is when the verdict was given, in Unix seconds.
	At int64 `json:"at"`

	// Until is when the next mint may be sent, in Unix seconds. Zero on a
	// refusal means never with these client credentials.
	Until int64 `json:"until,omitempty"`
}

// agentClientFingerprint names a client id and secret without carrying the
// secret: a truncated SHA-256 under a label of its own. The secret is a
// long random string, so the digest cannot be walked back to it, and it is
// stored beside the secret itself in any case — the point is only that
// nothing printed or compared needs the secret in hand.
func agentClientFingerprint(clientID, clientSecret string) string {
	sum := sha256.Sum256([]byte("basecamp-cli agent mint hold\x00" + clientID + "\x00" + clientSecret))
	return hex.EncodeToString(sum[:16])
}

// mintHoldFor is the hold a refused mint leaves, or nil for a failure the
// next command should simply retry — a 5xx, a redirect, a response that
// does not name the client. rateLimited is a 429; refused is a verdict on
// the client credentials, and code the RFC 6749 §5.2 code the server named
// with it, if any.
func mintHoldFor(mint *agentMint, resp *http.Response, detail, code string, refused bool, now time.Time) *MintHold {
	hold := &MintHold{
		Detail: detail,
		Client: agentClientFingerprint(mint.clientID, mint.clientSecret),
		At:     now.Unix(),
	}
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		wait := retryAfter(resp.Header, now)
		if wait <= 0 {
			wait = defaultAgentRateLimitHold
		}
		hold.Kind = mintHoldRateLimited
		hold.Until = now.Add(min(wait, maxAgentMintHold)).Unix()
	case refused && code == "invalid_client":
		// Permanent for this secret; see agentRefusalRecheck.
		hold.Kind = mintHoldRefused
	case refused:
		hold.Kind = mintHoldRefused
		hold.Until = now.Add(agentRefusalRecheck).Unix()
	default:
		return nil
	}
	return hold
}

// heldMint is the error a remembered verdict answers a mint with, or nil
// when the mint may be sent. It reads nothing but creds, so a report can
// ask it too.
func (m *Manager) heldMint(creds *Credentials) error {
	hold := creds.MintHold
	if hold == nil || hold.Client != agentClientFingerprint(creds.ClientID, creds.ClientSecret) {
		return nil
	}
	now := m.now()
	if hold.Until != 0 {
		until := time.Unix(hold.Until, 0)
		if !now.Before(until) || until.Sub(now) > maxAgentMintHold {
			return nil
		}
	}
	when := time.Unix(hold.Until, 0).UTC().Format(time.RFC3339)

	switch hold.Kind {
	case mintHoldRateLimited:
		e := output.ErrRateLimit(int(math.Ceil(time.Unix(hold.Until, 0).Sub(now).Seconds())))
		e.Message = fmt.Sprintf("Minting an agent token is held until %s: the token endpoint rate-limited the last attempt (%s)", when, hold.Detail)
		return e
	case mintHoldRefused:
		msg := "Minting an agent token was refused (" + hold.Detail + ")"
		if hold.Until == 0 {
			msg += "; the refusal is remembered, and this client secret will not be sent again — a login with a new secret replaces it"
		} else {
			msg += "; the refusal is remembered until " + when + ", when this client secret will be tried once more — a login with a new secret replaces it sooner"
		}
		return output.ErrAuth(msg)
	}
	return nil
}

// rememberMintHold records hold on the stored credential — if the stored
// credential is still the one the verdict was about.
//
// The caller holds the credential key's cross-process lock, so no login can
// land between the refusal and this write. The fingerprint is compared
// anyway, against a fresh read rather than the copy the mint was made from:
// on a host where the lock could not be taken at all, a login that stored a
// new secret while the old one was being refused must not have that
// refusal written over it. A failure to write is reported and otherwise
// changes nothing — the refusal is what the caller sees either way.
func (m *Manager) rememberMintHold(origin string, hold *MintHold) {
	current, err := m.store.Load(origin)
	if err != nil {
		return
	}
	if current.OAuthType != oauthTypeAgent || agentClientFingerprint(current.ClientID, current.ClientSecret) != hold.Client {
		return
	}
	current.MintHold = hold
	if err := m.store.Save(origin, current); err != nil {
		m.warnf("warning: could not remember the token endpoint's refusal for %s, so the next command will ask again: %v", origin, err)
	}
}

// now is the Manager's clock: the clock field when a test set one.
func (m *Manager) now() time.Time {
	if m.clock != nil {
		return m.clock()
	}
	return time.Now()
}
