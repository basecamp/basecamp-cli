package commands

import (
	"context"
	"errors"
	"net/http"
	"sync"

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
	mu      sync.Mutex
	refusal error // retained even if cancellation wins a part's shutdown race

	// refused wakes the watch after a refusal has been retained.
	refused chan struct{}
	// suspect is signaled by a read answered 401; signals that arrive
	// while one is pending coalesce into it.
	suspect chan struct{}
}

func newCredentialWatch() *credentialWatch {
	return &credentialWatch{refused: make(chan struct{}, 1), suspect: make(chan struct{}, 1)}
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
// ends, and returns any refusal already observed, otherwise nil. confirm
// asks Basecamp whether it still takes the credential and must honor its
// context. Cancellation joins an in-flight confirmation before taking the
// final snapshot: it may already have deleted a refused credential.
func (w *credentialWatch) Run(ctx context.Context, confirm func(context.Context) bool) error {
	var confirming <-chan bool
	var cancelConfirm context.CancelFunc
	defer func() {
		if cancelConfirm != nil {
			cancelConfirm()
		}
	}()
	for {
		select {
		case <-ctx.Done():
			if refused := w.finalError(nil); refused != nil {
				return refused
			}
			if confirming != nil {
				cancelConfirm()
				<-confirming
			}
			return w.finalError(nil)
		case <-w.refused:
			return w.finalError(nil)
		case <-w.suspect:
			if confirming != nil {
				continue
			}
			checkCtx, cancel := context.WithCancel(ctx)
			cancelConfirm = cancel
			done := make(chan bool, 1)
			confirming = done
			go func() {
				refused := confirm(checkCtx)
				if refused {
					w.refuse(errCredentialNotTaken)
				}
				done <- refused
			}()
		case refused := <-confirming:
			cancelConfirm()
			confirming = nil
			// All suspicions that arrived during this check share its answer.
			select {
			case <-w.suspect:
			default:
			}
			if refused {
				return errCredentialNotTaken
			}
		}
	}
}

// finalError keeps a refusal observed by any part even if another part's
// missing-credential error canceled the run before the watch could return it.
func (w *credentialWatch) finalError(other error) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.refusal != nil {
		return w.refusal
	}
	return other
}

func (w *credentialWatch) refuse(err error) {
	w.mu.Lock()
	if w.refusal == nil {
		w.refusal = err
	}
	w.mu.Unlock()
	select {
	case w.refused <- struct{}{}:
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
