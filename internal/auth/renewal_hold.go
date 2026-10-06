package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"net/http"
	"time"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

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
	// renewalHoldRefused is the token endpoint refusing the client
	// credentials themselves (clientRefusalCodes, or a bare 401/403).
	renewalHoldRefused = "refused"

	// renewalHoldRateLimited is a 429, held until its Retry-After.
	renewalHoldRateLimited = "rate_limited"
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

// invalidClientDetail is the Detail of the one refusal held with no
// recheck. A refusal read back without a deadline is believed only when it
// says this: any other refusal can reverse, so one whose deadline was lost
// is damaged, and holds nothing.
const invalidClientDetail = "token error: invalid_client"

// defaultAgentRateLimitHold is how long a 429 with no readable Retry-After
// is held — the fallback the rest of the CLI takes for one (see
// resilience.GatingHooks.OnRequestEnd).
const defaultAgentRateLimitHold = 60 * time.Second

// maxRenewalHold bounds any hold that expires, both measured to the whole
// second rounded up, as every stored deadline is. No hold is written past it
// (retryAfter already caps a Retry-After at maxAgentConnectLifetime, below
// it), and a stored expiry further out than this from now —
// a clock that stepped back, a damaged record — is not believed at all:
// that hold is ignored and the mint goes out, which is the old behavior and
// the safe direction to fail in.
const maxRenewalHold = time.Hour

// MaxServerWait is the longest any wait a server names is waited, whatever
// the status or the token path: the bound a rate-limit hold is written
// under, so a held rate limit and every other named wait agree. A connector
// that waited a server's word for years would be a hang, not a wait.
const MaxServerWait = maxAgentConnectLifetime

// RenewalHold is the token endpoint's last refusal of a credential's renewal
// — an agent's mint, or anyone else's refresh — remembered so the next
// renewal can answer it without asking again. A refresh is only ever held
// for a rate limit (holdRateLimitedRefresh).
//
// Nothing in it came from the server as free text: Detail is the same fixed
// vocabulary agentMintRefusal renders (see oauthErrorCodes).
type RenewalHold struct {
	// Kind is renewalHoldRefused or renewalHoldRateLimited.
	Kind string `json:"kind"`

	// Detail is what the server said, as agentMintRefusal rendered it:
	// "token error: invalid_client", or "the server answered HTTP 401".
	Detail string `json:"detail"`

	// Client is renewalSubject of the credential the verdict was about:
	// an agent's client credentials, or a refresh token. A hold for any
	// others holds nothing.
	Client string `json:"client"`

	// Until is when the next renewal may be sent, in Unix seconds, rounded
	// up. Zero on an invalid_client refusal means never with these client
	// credentials; any other hold without one is damaged, and holds nothing.
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

// renewalSubject is the fingerprint of what renewing creds presents to the
// token endpoint: an agent's client credentials, or anyone else's refresh
// token. A hold is about exactly one of these.
func renewalSubject(creds *Credentials) string {
	if creds.OAuthType == oauthTypeAgent {
		return agentClientFingerprint(creds.ClientID, creds.ClientSecret)
	}
	return refreshTokenFingerprint(creds.RefreshToken)
}

// holdRateLimitedRefresh remembers a 429 on a refresh as a hold on the stored
// login and returns the rate-limit error it is answered with; nil for any
// other failure.
//
// A refresh is renewed by every process that finds the access token near
// expiry, so a scheduled job resends it on every run. Into one of
// Basecamp's abuse blocks — which answers every OAuth request from the
// address for up to a day once enough refreshes have failed — that is a
// request per run for hours, none of which can succeed. Held, the next run
// answers locally until the wait is over. The hold is capped at
// MaxServerWait, as an agent's is: a request inside a block is answered 429
// without being counted against the address, so the cap costs one request
// per cap and notices a block lifted early.
func (m *Manager) holdRateLimitedRefresh(origin string, creds *Credentials, err error) error {
	var refusal *basecamp.Error
	if !errors.As(err, &refusal) || refusal.HTTPStatus != http.StatusTooManyRequests {
		return nil
	}
	now := m.now()
	named := time.Duration(refusal.RetryAfter) * time.Second
	wait := named
	if wait <= 0 {
		wait = defaultAgentRateLimitHold
	}
	hold := &RenewalHold{
		Kind:   renewalHoldRateLimited,
		Detail: fmt.Sprintf("the server answered HTTP %d", refusal.HTTPStatus),
		Client: refreshTokenFingerprint(creds.RefreshToken),
		Until:  ceilUnix(now.Add(min(wait, MaxServerWait))),
	}
	m.rememberRenewalHold(origin, hold)

	held := time.Unix(hold.Until, 0).UTC().Format(time.RFC3339)
	var msg string
	switch {
	case named > MaxServerWait:
		msg = fmt.Sprintf("Basecamp rate-limited refreshing this login until %s; the refresh is held until %s, then tried once to see whether the limit has lifted",
			now.Add(named).UTC().Format(time.RFC3339), held)
	case named > 0:
		msg = fmt.Sprintf("Basecamp rate-limited refreshing this login until %s; the refresh is held until then", held)
	default:
		msg = fmt.Sprintf("Basecamp rate-limited refreshing this login; the refresh is held until %s", held)
	}
	e := holdRateLimitError(hold, now, msg)
	if refusal.RetryAfter > holdWait(hold, now) {
		// Held for the cap; what the server asked for is kept too, so a
		// caller can say both.
		e.Cause = errors.Join(e.Cause, namedWaitError(refusal.RetryAfter))
	}
	return e
}

// refreshTokenFingerprint names a refresh token the way
// agentClientFingerprint names a client: a truncated SHA-256 under a label of
// its own, so a hold can say which refresh token it is about without
// carrying it. A hold for any other refresh token — one another process has
// rotated to, or a fresh login's — holds nothing.
func refreshTokenFingerprint(refreshToken string) string {
	sum := sha256.Sum256([]byte("basecamp-cli refresh hold\x00" + refreshToken))
	return hex.EncodeToString(sum[:16])
}

// mintHoldFor is the hold a refused mint leaves, or nil for a failure the
// next command should simply retry — a 5xx, a redirect, a response that
// does not name the client. A 429 is told by resp's status; refused is a
// verdict on the client credentials, and code the RFC 6749 §5.2 code the
// server named with it, if any.
func mintHoldFor(mint *agentMint, resp *http.Response, detail, code string, refused bool, now time.Time) *RenewalHold {
	hold := &RenewalHold{
		Detail: detail,
		Client: agentClientFingerprint(mint.clientID, mint.clientSecret),
	}
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		hold.Kind = renewalHoldRateLimited
		hold.Until = ceilUnix(now.Add(rateLimitHold(resp.Header, now)))
	case refused && code == "invalid_client":
		// Permanent for this secret; see agentRefusalRecheck.
		hold.Kind = renewalHoldRefused
	case refused:
		hold.Kind = renewalHoldRefused
		hold.Until = ceilUnix(now.Add(agentRefusalRecheck))
	default:
		return nil
	}
	return hold
}

// rateLimitHold is how long a 429 holds the mint: its Retry-After, or
// defaultAgentRateLimitHold when it has none, never past MaxServerWait.
func rateLimitHold(header http.Header, now time.Time) time.Duration {
	wait := retryAfter(header, now)
	if wait <= 0 {
		wait = defaultAgentRateLimitHold
	}
	return min(wait, MaxServerWait)
}

// ceilUnix is t in Unix seconds, rounded up: a deadline stored in whole
// seconds must not end before the one it was given.
func ceilUnix(t time.Time) int64 {
	return t.Add(time.Second - time.Nanosecond).Unix()
}

// holdWait is the wait a hold names, in whole seconds: from now to its
// stored deadline, rounded up. The stored deadline is the one source of
// truth; every wait reported for a hold is read from it, so a caller that
// retries after the wait it was told is never held locally.
func holdWait(hold *RenewalHold, now time.Time) int {
	return int(math.Ceil(time.Unix(hold.Until, 0).Sub(now).Seconds()))
}

// holdRateLimitError is the rate-limit error for a rate_limited hold,
// whether the 429 that set it or a later mint it held. It carries the
// hold's wait, read from its deadline, for a caller that reschedules the
// mint itself (RetryAfter): one sent any sooner is answered by the hold.
func holdRateLimitError(hold *RenewalHold, now time.Time, message string) *output.Error {
	wait := holdWait(hold, now)
	e := output.ErrRateLimit(wait)
	e.Message = message
	if wait > 0 {
		e.Cause = retryAfterError(wait)
	}
	return e
}

// errRenewalHeld is in the cause of every error a stored hold answers a mint
// with, so a report can tell the hold's answer from any other.
var errRenewalHeld = errors.New("a stored hold answered the mint")

// heldRenewal is the error a remembered verdict answers a mint with, or nil
// when the mint may be sent. It reads nothing but creds, so a report can
// ask it too.
func (m *Manager) heldRenewal(creds *Credentials) error {
	hold := creds.RenewalHold
	now := m.now()
	if !hold.holds(creds, now) {
		return nil
	}
	when := time.Unix(hold.Until, 0).UTC().Format(time.RFC3339)

	switch hold.Kind {
	case renewalHoldRateLimited:
		what := "Minting an agent token"
		if creds.OAuthType != oauthTypeAgent {
			what = "Refreshing this login"
		}
		e := holdRateLimitError(hold, now, fmt.Sprintf("%s is held until %s: the token endpoint rate-limited the last attempt (%s)", what, when, hold.Detail))
		e.Cause = errors.Join(e.Cause, errRenewalHeld)
		return e
	case renewalHoldRefused:
		msg := "Minting an agent token was refused (" + hold.Detail + ")"
		if hold.Until == 0 {
			msg += "; the refusal is remembered, and this client secret will not be sent again — a login with a new secret replaces it"
		} else {
			msg += "; the refusal is remembered until " + when + ", when this client secret will be tried once more — a login with a new secret replaces it sooner"
		}
		e := output.ErrAuth(msg)
		e.Cause = errors.Join(ErrAgentCredentialRefused, errRenewalHeld)
		if hold.Until != 0 {
			// The hold ends on its own, so the remedy is to wait for it.
			e.Hint = fmt.Sprintf("Wait until %s (%d seconds); the next mint after that sends this client secret once more", when, holdWait(hold, now))
		}
		return e
	}
	return nil
}

// RenewalHoldStatus is a stored mint hold as a report shows it: what would
// happen to a renewal sent now.
type RenewalHoldStatus struct {
	// Kind is "refused" or "rate_limited".
	Kind string `json:"kind"`

	// Detail is what the server said: "token error: invalid_client", or
	// "the server answered HTTP 401".
	Detail string `json:"detail"`

	// Until is when the hold ends, RFC 3339 in UTC. Empty for a hold that
	// never ends with these client credentials.
	Until string `json:"until,omitempty"`

	// Message and Hint are the error a mint held now would return.
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`
}

// Permanent reports whether the hold never ends with these client
// credentials — only a login with a new secret replaces it.
func (s *RenewalHoldStatus) Permanent() bool { return s.Until == "" }

// RenewalHoldStatus is the stored hold of creds as a report shows it, when
// refusal — RefreshRefusal's answer for the same creds — is that hold's
// answer; nil otherwise. It reads no clock: whether the hold is what a
// renewal sent now would meet was decided once, by RefreshRefusal, and a
// local failure checked ahead of the hold (a missing token endpoint) is
// not masked by it.
func (m *Manager) RenewalHoldStatus(creds *Credentials, refusal error) *RenewalHoldStatus {
	if creds == nil || creds.RenewalHold == nil || !errors.Is(refusal, errRenewalHeld) {
		return nil
	}
	hold := creds.RenewalHold
	e := output.AsError(refusal)
	status := &RenewalHoldStatus{Kind: hold.Kind, Detail: hold.Detail, Message: e.Message, Hint: e.Hint}
	if hold.Until != 0 {
		status.Until = time.Unix(hold.Until, 0).UTC().Format(time.RFC3339)
	}
	return status
}

// holds reports whether a stored hold is one this version believes: about
// these client credentials, of a kind it knows, and — where it expires —
// expiring in the future and within maxRenewalHold. Only an
// invalid_client refusal never expires. Anything else is ignored, and the mint goes out.
func (hold *RenewalHold) holds(creds *Credentials, now time.Time) bool {
	if hold == nil || hold.Client != renewalSubject(creds) {
		return false
	}
	// A refresh is only ever held for a rate limit: a refused refresh token
	// is forgotten outright rather than held (refreshCredential).
	if creds.OAuthType != oauthTypeAgent && hold.Kind != renewalHoldRateLimited {
		return false
	}
	switch {
	case hold.Kind == renewalHoldRefused && hold.Until == 0:
		return hold.Detail == invalidClientDetail
	case hold.Kind == renewalHoldRefused, hold.Kind == renewalHoldRateLimited:
		// The bound is rounded up as a stored deadline is, so a hold of
		// the full maxRenewalHold set partway through a second is
		// believed.
		return hold.Until > 0 && now.Before(time.Unix(hold.Until, 0)) && hold.Until <= ceilUnix(now.Add(maxRenewalHold))
	default:
		return false
	}
}

// rememberRenewalHold records hold on the stored credential — if the stored
// credential is still the one the verdict was about.
//
// The caller holds the credential key's cross-process lock, so no login can
// land between the refusal and this write. The fingerprint is compared
// anyway, against a fresh read rather than the copy the mint was made from,
// for a host where that lock could not be taken at all (lock.go runs such a
// host unsynchronized, with a warning). There it narrows the window a
// concurrent login can be written over from the token round trip to this
// load and save; it does not close it, and nothing without a lock can — the
// store has no compare-and-swap, and the whole-store lock lives in the same
// directory the key lock could not be made in. A successful mint on such a
// host saves the copy it loaded before its round trip, a wider window than
// this one, and has since before holds existed. Skipping the write there
// instead would leave exactly those hosts minting on every poll.
//
// A failure to write is reported and otherwise changes nothing — the
// refusal is what the caller sees either way.
func (m *Manager) rememberRenewalHold(origin string, hold *RenewalHold) {
	current, err := m.store.Load(origin)
	if err != nil {
		// A credential removed since the mint (a logout) has nothing to
		// remember a refusal on; any other failure is worth saying.
		if !errors.Is(err, ErrNoCredential) {
			m.warnf("warning: could not read the credential for %s to remember the token endpoint's refusal, so the next command will ask again: %v", origin, err)
		}
		return
	}
	if renewalSubject(current) != hold.Client {
		return
	}
	current.RenewalHold = hold
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
