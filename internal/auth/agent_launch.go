package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/basecamp/basecamp-cli/internal/output"
)

// Agent sessions: one self-token per launch, attributed to that launch.
//
// An agent profile's shared token is cached and served to every process on
// the host, so Basecamp cannot tell one sandboxed box from another: every
// request reads as the agent, from one token. A session mint asks the token
// endpoint for a token of the session's own instead, carrying a random id
// the launcher generated and a label it chose, which Basecamp stores on the
// token and writes beside the agent in its request logs.
//
// The CLI calls this a session; on the wire, and in bc3, it is a launch
// (launch_id, launch_label), because "session" already means the web
// sign-in there.
//
// The contract is "a token bound to that session, or nothing":
//
//   - The token is minted fresh on every call and never cached here. The
//     caller is the per-launch cache: the sandbox's credential capture holds
//     each token for a few minutes inside the launch's own proxy, which dies
//     with the launch. Caching here instead would write one keyring entry
//     per launch, which nothing would ever clean up, and contend every launch
//     on the one credential lock for a token only one launch may use.
//
//   - The shared cached token is neither served nor written: a session that
//     fell back to it would be indistinguishable from every other process.
//
//   - A server that does not echo the session back has not bound the token
//     to it — it predates session attribution and ignored the parameters —
//     and the token is discarded. Handing it out would be the failure that
//     reads as working: attribution silently missing, and once Basecamp can
//     stop a session, a token that stopping it would not reach.
//
//   - A refusal is reported, never retried without the session.

// launchIDPattern is a session id as bc3 accepts it: 128 random bits as
// canonical lowercase hex.
var launchIDPattern = regexp.MustCompile(`\A[0-9a-f]{32}\z`)

// maxLaunchLabelChars is bc3's bound on a session label, in characters.
const maxLaunchLabelChars = 100

// agentLaunch is the session a mint attributes its token to.
type agentLaunch struct {
	id    string
	label string
}

// ValidateSession checks a session id and label against the rules the
// token endpoint holds them to, so a malformed one is a usage error here
// rather than a refused mint. The label is optional; the id is not.
func ValidateSession(id, label string) error {
	if !launchIDPattern.MatchString(id) {
		return output.ErrUsageHint("A session id must be 32 lowercase hex characters (128 random bits)",
			"Generate one with: openssl rand -hex 16")
	}
	if label == "" {
		return nil
	}
	if !utf8.ValidString(label) {
		return output.ErrUsage("A session label must be valid UTF-8")
	}
	if strings.TrimSpace(label) == "" {
		return output.ErrUsage("A session label must not be blank")
	}
	if n := utf8.RuneCountInString(label); n > maxLaunchLabelChars {
		return output.ErrUsage(fmt.Sprintf("A session label must be at most %d characters (this one is %d)", maxLaunchLabelChars, n))
	}
	for _, r := range label {
		if !labelRune(r) {
			return output.ErrUsage(fmt.Sprintf("A session label must not contain control, formatting or line-separator characters (found U+%04X)", r))
		}
	}
	return nil
}

// labelRune reports whether r may appear in a session label: anything but
// the "other" category (control, format, private use, surrogate) and the
// line and paragraph separators — exactly the set bc3 refuses. The label is
// shown to people, so nothing that reorders, hides or breaks the text around
// it. Unassigned code points are deliberately allowed: a host's Unicode
// tables can be newer than this build's or the server's, and a label refused
// over that skew costs the launch its whole token.
func labelRune(r rune) bool {
	return !unicode.In(r, unicode.Cc, unicode.Cf, unicode.Co, unicode.Cs, unicode.Zl, unicode.Zp)
}

// addTo puts the session on a client_credentials form. A nil launch is the
// shared token's mint, which sends nothing extra.
func (l *agentLaunch) addTo(form url.Values) {
	if l == nil {
		return
	}
	form.Set("launch_id", l.id)
	if l.label != "" {
		form.Set("launch_label", l.label)
	}
}

// errLaunchNotRecorded is the cause of a session mint whose token came back
// without the session on it.
var errLaunchNotRecorded = errors.New("the token endpoint did not record the session")

// requireEcho refuses a token response that does not carry the session it
// was asked for. bc3 echoes launch_id, and launch_label when one was given,
// once it has stored them on the token; a server that ignored them echoes
// nothing.
func (l *agentLaunch) requireEcho(status int, body []byte) error {
	if l == nil {
		return nil
	}
	var echoed struct {
		ID    *string `json:"launch_id"`
		Label *string `json:"launch_label"`
	}
	_ = json.Unmarshal(body, &echoed)

	idMatches := echoed.ID != nil && *echoed.ID == l.id
	labelMatches := (l.label == "" && echoed.Label == nil) || (echoed.Label != nil && *echoed.Label == l.label)
	if idMatches && labelMatches {
		return nil
	}

	// Say which way the echo missed. Only a server that echoed no id at
	// all is one that predates sessions; one that echoed this id records
	// them, and what it got wrong is the label.
	msg := fmt.Sprintf("Basecamp minted a token but did not bind it to session %s, so it was discarded", l.id)
	hint := "A session token needs a Basecamp that records agent sessions; without one, leave out --session-id and --session-label"
	switch {
	case echoed.ID == nil:
		msg += ": this Basecamp predates agent session attribution"
	case !idMatches:
		msg += ": Basecamp answered for a different session"
		hint = "Run it again; if it keeps answering for another session, report it"
	case echoed.Label == nil:
		msg += ": Basecamp recorded the session but did not record the label"
		hint = "Leave out --session-label to mint for the session alone"
	default:
		msg += ": Basecamp recorded a different label for the session"
		hint = "Leave out --session-label to mint for the session alone"
	}
	e := output.ErrAPI(status, msg)
	e.Hint = hint
	e.Cause = errLaunchNotRecorded
	return e
}

// SessionAccessToken mints a fresh self-token for one agent session from
// the active profile's stored agent credential, and returns it without
// storing it. See the top of this file for why it is never cached and why
// a server that does not bind it to the session gets no token at all.
//
// Holds are honored and written exactly as the shared mint's are, because
// they are verdicts on the client secret both mints present: a refused or
// rate-limited secret is answered locally here too, and a refusal of the
// secret seen here is remembered for every later mint. A refusal of the
// request itself — the session's parameters, above all — is not about the
// secret and holds nothing, so one malformed launch cannot stop the rest.
func (m *Manager) SessionAccessToken(ctx context.Context, id, label string) (string, error) {
	if err := ValidateSession(id, label); err != nil {
		return "", err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	credKey := m.credentialKey()
	creds, err := m.store.LoadContext(ctx, credKey)
	if err != nil {
		return "", m.unreadable("No stored credentials for", credKey, err)
	}
	m.remember(creds)
	if creds.OAuthType != oauthTypeAgent {
		return "", output.ErrUsage(fmt.Sprintf(
			"A session token is minted from an agent's own credential, and %s holds a person's login; run without --session-id", credKey))
	}

	launch := &agentLaunch{id: id, label: label}
	mint, err := m.prepareAgentMint(creds)
	if err != nil {
		return "", forSession(launch, m.hintLogin(err))
	}
	mint.launch = launch

	// No credential lock across the round trip: nothing is written on
	// success, so there is nothing for concurrent launches to be exclusive
	// with. Only a hold is written, and that under the lock, as the shared
	// path writes it.
	token, hold, err := m.mintAgentToken(ctx, mint)
	if err != nil {
		if hold != nil {
			lockErr := m.store.withKeyLock(ctx, credKey, func() error {
				m.rememberRenewalHold(credKey, hold)
				return nil
			})
			if lockErr != nil {
				m.warnf("warning: could not lock the credential for %s to remember the token endpoint's refusal, so the next command will ask again: %v", credKey, lockErr)
			}
		}
		return "", forSession(launch, m.hintLogin(err))
	}
	return token.AccessToken, nil
}

// forSession names the session in a failed session mint's message, keeping
// its class, hint and cause.
func forSession(l *agentLaunch, err error) error {
	if errors.Is(err, errLaunchNotRecorded) {
		// Already says which session.
		return err
	}
	var e *output.Error
	if !errors.As(err, &e) {
		return fmt.Errorf("session %s: %w", l.id, err)
	}
	named := *e
	named.Message = fmt.Sprintf("Session %s: %s", l.id, e.Message)
	if named.Cause == nil {
		named.Cause = err
	}
	return &named
}
