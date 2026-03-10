package auth

import (
	"bytes"
	"context"
	"embed"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"text/template"
	"time"
)

//go:embed callback.html callback_success.html callback_error.html callback_denied.html callback_invalid.html callback_exchange_failed.html
var callbackFS embed.FS

var callbackTmpl = template.Must(template.ParseFS(callbackFS, "callback.html"))

type callbackData struct{ Content string }

func renderCallback(filename string) string {
	content, _ := callbackFS.ReadFile(filename)
	var buf bytes.Buffer
	_ = callbackTmpl.Execute(&buf, callbackData{Content: string(content)})
	return buf.String()
}

var (
	callbackSuccess        = renderCallback("callback_success.html")
	callbackError          = renderCallback("callback_error.html")
	callbackDenied         = renderCallback("callback_denied.html")
	callbackInvalid        = renderCallback("callback_invalid.html")
	callbackExchangeFailed = renderCallback("callback_exchange_failed.html")
)

// waitForCallback starts a local HTTP server on the provided listener and
// waits for the OAuth provider to redirect back with an authorization code.
// It validates the state parameter for CSRF protection and returns the code.
//
// On success the returned resolve function must be called to signal the
// outcome of the token exchange: true for success, false for failure. The
// HTTP response to the browser is deferred until resolve is called so the
// user sees an accurate result page. resolve is safe to call multiple times;
// only the first call takes effect.
//
// On error (state mismatch, OAuth error, missing code, context cancellation)
// the handler responds immediately and the returned resolve is a no-op.
func waitForCallback(ctx context.Context, expectedState string, listener net.Listener) (string, func(bool), error) {
	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)
	doneCh := make(chan bool, 1)

	var shutdownOnce sync.Once
	server := &http.Server{
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      0, // disabled: success handler blocks until resolve() signals exchange outcome
		IdleTimeout:       30 * time.Second,
	}

	shutdown := func() { //nolint:contextcheck // fire-and-forget shutdown; no parent context to propagate
		shutdownOnce.Do(func() {
			go func() {
				shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = server.Shutdown(shutdownCtx)
			}()
		})
	}

	var resolveOnce sync.Once
	resolve := func(success bool) {
		resolveOnce.Do(func() {
			select {
			case doneCh <- success:
			default:
			}
		})
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")

		state := r.URL.Query().Get("state")
		code := r.URL.Query().Get("code")
		errParam := r.URL.Query().Get("error")

		if state != expectedState {
			select {
			case errCh <- fmt.Errorf("state mismatch: CSRF protection failed"):
			default:
			}
			_, _ = fmt.Fprint(w, callbackInvalid)
			shutdown()
			return
		}

		if errParam == "access_denied" {
			msg := "OAuth error: " + errParam
			if desc := r.URL.Query().Get("error_description"); desc != "" {
				msg += " — " + desc
			}
			select {
			case errCh <- errors.New(msg):
			default:
			}
			_, _ = fmt.Fprint(w, callbackDenied)
			shutdown()
			return
		}

		if errParam != "" {
			msg := "OAuth error: " + errParam
			if desc := r.URL.Query().Get("error_description"); desc != "" {
				msg += " — " + desc
			}
			select {
			case errCh <- errors.New(msg):
			default:
			}
			_, _ = fmt.Fprint(w, callbackError)
			shutdown()
			return
		}

		if code == "" {
			select {
			case errCh <- fmt.Errorf("OAuth callback missing authorization code"):
			default:
			}
			_, _ = fmt.Fprint(w, callbackError)
			shutdown()
			return
		}

		// Signal code to caller; defer the browser response until the
		// token exchange result arrives via doneCh.
		select {
		case codeCh <- code:
		default:
		}

		select {
		case success := <-doneCh:
			if success {
				_, _ = fmt.Fprint(w, callbackSuccess)
			} else {
				_, _ = fmt.Fprint(w, callbackExchangeFailed)
			}
		case <-r.Context().Done():
			// Client disconnected or server shutting down.
		}
		shutdown()
	})
	server.Handler = mux

	go func() { _ = server.Serve(listener) }()

	select {
	case code := <-codeCh:
		return code, resolve, nil
	case err := <-errCh:
		shutdown()
		return "", resolve, err
	case <-ctx.Done():
		shutdown()
		return "", resolve, ctx.Err()
	}
}
