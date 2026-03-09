package auth

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"
)

// callbackPage is the HTML template for the OAuth callback page.
// The %s placeholder is replaced with the status-specific content block.
const callbackPage = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Basecamp CLI</title>
  <style>
    * {
			box-sizing: border-box;
			margin: 0;
			padding: 0;
		}

    body {
      align-items: center;
      background: #f6f2ef;
      color: #333;
      display: flex;
			font-family: -apple-system, system-ui, BlinkMacSystemFont, "Segoe UI", Helvetica, Arial, sans-serif, "Apple Color Emoji", "Segoe UI Emoji", "Segoe UI Symbol";
      font-size: 17px;
      justify-content: center;
			line-height: 1.4;
      min-height: 100vh;
    }

    .card {
			background: #fff;
			border-radius: 6px;
			box-shadow: 0 1px 3px 0 rgba(0, 0, 0, 0.25);
      inline-size: 100%%;
      max-inline-size: 420px;
			padding: 25px;
      text-align: center;
    }

    .icon {
      block-size: 48px;
      align-items: center;
      border-radius: 50%%;
      display: inline-flex;
      inline-size: 48px;
      justify-content: center;
      margin-bottom: 20px;
    }

    .icon-success { background: #e6f4ea; }
    .icon-error   { background: #fce8e6; }

    .icon svg { width: 24px; height: 24px; }

    h1 {
      font-size: 20px;
      font-weight: 600;
			line-height: 1.2;
			margin: 0 0 4px;
    }

    p {
      color: #7f7f7f;
      font-size: 15px;
			margin: 0;
    }
  </style>
</head>
<body>
  <div class="card">
    %s
  </div>
</body>
</html>`

var (
	callbackSuccess = fmt.Sprintf(callbackPage, `
    <div class="icon icon-success">
      <svg fill="none" viewBox="0 0 24 24" stroke="#34a853" stroke-width="2.5">
        <path stroke-linecap="round" stroke-linejoin="round" d="M5 13l4 4L19 7"/>
      </svg>
    </div>
    <h1>Basecamp authorization successful</h1>
    <p>You can close this window and return to your terminal.</p>`)

	callbackError = fmt.Sprintf(callbackPage, `
    <div class="icon icon-error">
      <svg fill="none" viewBox="0 0 24 24" stroke="#d93025" stroke-width="2.5">
        <path stroke-linecap="round" stroke-linejoin="round" d="M6 18L18 6M6 6l12 12"/>
      </svg>
    </div>
    <h1>Basecamp authorization failed </h1>
    <p>Something went wrong. Please return to your terminal and try again.</p>`)
)

// waitForCallback starts a local HTTP server on the provided listener and
// waits for the OAuth provider to redirect back with an authorization code.
// It validates the state parameter for CSRF protection and returns the code.
func waitForCallback(ctx context.Context, expectedState string, listener net.Listener) (string, error) {
	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)
	var once sync.Once

	server := &http.Server{
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       30 * time.Second,
	}

	shutdown := func() {
		once.Do(func() {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			go func() { defer cancel(); _ = server.Shutdown(shutdownCtx) }()
		})
	}

	server.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")

		state := r.URL.Query().Get("state")
		code := r.URL.Query().Get("code")
		errParam := r.URL.Query().Get("error")

		if errParam != "" {
			select {
			case errCh <- fmt.Errorf("OAuth error: %s", errParam):
			default:
			}
			_, _ = fmt.Fprint(w, callbackError)
			shutdown()
			return
		}

		if state != expectedState {
			select {
			case errCh <- fmt.Errorf("state mismatch: CSRF protection failed"):
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

		select {
		case codeCh <- code:
		default:
		}
		_, _ = fmt.Fprint(w, callbackSuccess)
		shutdown()
	})

	go func() { _ = server.Serve(listener) }()

	select {
	case code := <-codeCh:
		return code, nil
	case err := <-errCh:
		return "", err
	case <-ctx.Done():
		return "", ctx.Err()
	case <-time.After(5 * time.Minute):
		return "", fmt.Errorf("authentication timeout waiting for callback on %s", listener.Addr())
	}
}
