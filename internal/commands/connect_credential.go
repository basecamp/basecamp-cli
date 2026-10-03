package commands

import (
	"context"
	"errors"
	"net/http"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/auth"
)

// errCredentialNotTaken is the credential watch's stop when Basecamp, asked,
// confirms that it no longer takes the agent's credential.
var errCredentialNotTaken = errors.New("the agent's credential is no longer taken by Basecamp")

// credentialWatch stops a running connector once Basecamp no longer takes
// its agent's credential, whichever of its reads meets that first.
//
// The feed stops on a refused credential itself, but only when it asks
// Basecamp for something, and while its live connection stays up it asks
// for nothing. Admission and intake ask all the time, and to them a refusal
// is one failed read among others: admission blocks the record it was
// deciding, intake keeps the projects it last read, and both carry on. A
// connector whose Agent was disconnected in Basecamp, or connected on
// another computer, or whose bot user's login was revoked, would then run
// on for as long as its socket held, blocking every mention addressed to
// it. The watch sees what their reads meet, and ends the run instead, with
// the refusal said in the words the feed's own stop uses.
//
// It hears two things. A token renewal the token endpoint refuses, an
// Agent's mint or a bot user's refresh, is Basecamp's answer already, and
// stops the run at once. A read answered 401 is only a reason to ask:
// something other than Basecamp can answer 401, so the watch asks Basecamp
// itself (confirmCredentialRefused), and stops the run only when it
// confirms. A proxy that answers 401 to every request answers the confirm's
// read too, so the connector stops, and its message then names the wrong
// cause; stopping is still right, since no read can get through it.
type credentialWatch struct {
	// refused carries the first refused renewal.
	refused chan error
	// suspect is signaled by a read answered 401; signals that arrive
	// while one is pending coalesce into it.
	suspect chan struct{}
}

func newCredentialWatch() *credentialWatch {
	return &credentialWatch{refused: make(chan error, 1), suspect: make(chan struct{}, 1)}
}

// Tokens is p, watched for a renewal the token endpoint refuses.
func (w *credentialWatch) Tokens(p basecamp.TokenProvider) basecamp.TokenProvider {
	return watchedTokens{provider: p, watch: w}
}

// WrapTransport implements basecamp.TransportWrapper: inner, watched for a
// response answered 401.
func (w *credentialWatch) WrapTransport(inner http.RoundTripper) http.RoundTripper {
	return watchedTransport{inner: inner, watch: w}
}

// Run waits until the credential is refused, and returns why, or until ctx
// ends, and returns nil. confirm asks Basecamp whether it still takes the
// credential, and reports true when it does not.
func (w *credentialWatch) Run(ctx context.Context, confirm func(context.Context) bool) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-w.refused:
			return err
		case <-w.suspect:
			if confirm(ctx) {
				return errCredentialNotTaken
			}
		}
	}
}

func (w *credentialWatch) refuse(err error) {
	select {
	case w.refused <- err:
	default:
	}
}

func (w *credentialWatch) unauthorized() {
	select {
	case w.suspect <- struct{}{}:
	default:
	}
}

type watchedTokens struct {
	provider basecamp.TokenProvider
	watch    *credentialWatch
}

func (t watchedTokens) AccessToken(ctx context.Context) (string, error) {
	token, err := t.provider.AccessToken(ctx)
	if renewalRefused(err) {
		t.watch.refuse(err)
	}
	return token, err
}

// renewalRefused reports whether err is a token renewal the token endpoint
// refused for the credential itself: an Agent's mint, or a bot user's
// refresh. A rate limit, a server fault or the network is not a refusal.
func renewalRefused(err error) bool {
	return errors.Is(err, auth.ErrAgentCredentialRefused) || errors.Is(err, auth.ErrLoginRefused)
}

type watchedTransport struct {
	inner http.RoundTripper
	watch *credentialWatch
}

func (t watchedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.inner.RoundTrip(req)
	if err == nil && resp.StatusCode == http.StatusUnauthorized {
		t.watch.unauthorized()
	}
	return resp, err
}
