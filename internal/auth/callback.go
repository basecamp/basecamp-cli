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

    .logo {
      display: inline-block;
      margin-block-end: 4px;
      position: relative;
    }

    .logo svg { display: block; }

    .badge {
      align-items: center;
      block-size: 24px;
      border: 2px solid #fff;
      border-radius: 50%%;
      bottom: -2px;
      display: flex;
      inline-size: 24px;
      justify-content: center;
      position: absolute;
      right: -2px;
    }

    .badge svg {
      block-size: 14px;
      inline-size: 14px;
    }

    .badge-success { background: #34a853; }
    .badge-error   { background: #d93025; }

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
    <div class="logo">
      <svg width="69" height="69" viewBox="0 0 69 69" fill="none" xmlns="http://www.w3.org/2000/svg">
        <circle cx="34.07" cy="34.07" r="34.07" fill="#FFE200"/>
        <path d="M34.26 51.82C27.2241 51.9201 20.4903 48.9654 15.8 43.72C15.3147 43.1745 15.1652 42.4079 15.41 41.72C16.67 38.31 20.08 30.4 24.46 30.37C26.71 30.37 28.31 32.11 29.64 33.51C30.0795 33.997 30.5506 34.4547 31.05 34.88C32.05 33.88 34.05 30.53 35.63 27.17C36.0967 26.1648 37.2898 25.7283 38.295 26.195C39.3002 26.6617 39.7367 27.8548 39.27 28.86C34.54 39 32 39 31.2 39C29.35 39 28.02 37.59 26.73 36.22C26.15 35.61 24.95 34.34 24.48 34.34V34.34C23.48 34.51 21.22 38.04 19.62 41.91C23.4848 45.8006 28.7775 47.9336 34.26 47.81C42.19 47.81 48.11 45.66 50.66 41.87C49.78 31.81 44.24 18.15 34.26 18.15C25.82 18.15 19.46 24.03 15.36 35.63C14.9927 36.6738 13.8488 37.2223 12.805 36.855C11.7612 36.4877 11.2127 35.3438 11.58 34.3C16.31 20.93 23.94 14.15 34.26 14.15C47.75 14.15 53.96 31.38 54.71 42.24C54.7368 42.6341 54.6464 43.0273 54.45 43.37C51.31 48.82 44.14 51.82 34.26 51.82Z" fill="#1D2D35"/>
      </svg>
      <div class="badge badge-success">
        <svg fill="none" viewBox="0 0 24 24" stroke="#fff" stroke-width="3">
          <path stroke-linecap="round" stroke-linejoin="round" d="M5 13l4 4L19 7"/>
        </svg>
      </div>
    </div>
    <h1>Authorization successful</h1>
    <p>You can close this window and return to your terminal.</p>`)

	callbackError = fmt.Sprintf(callbackPage, `
    <div class="logo">
      <svg width="69" height="69" viewBox="0 0 69 69" fill="none" xmlns="http://www.w3.org/2000/svg">
        <circle cx="34.07" cy="34.07" r="34.07" fill="#FFE200"/>
        <path d="M34.26 51.82C27.2241 51.9201 20.4903 48.9654 15.8 43.72C15.3147 43.1745 15.1652 42.4079 15.41 41.72C16.67 38.31 20.08 30.4 24.46 30.37C26.71 30.37 28.31 32.11 29.64 33.51C30.0795 33.997 30.5506 34.4547 31.05 34.88C32.05 33.88 34.05 30.53 35.63 27.17C36.0967 26.1648 37.2898 25.7283 38.295 26.195C39.3002 26.6617 39.7367 27.8548 39.27 28.86C34.54 39 32 39 31.2 39C29.35 39 28.02 37.59 26.73 36.22C26.15 35.61 24.95 34.34 24.48 34.34V34.34C23.48 34.51 21.22 38.04 19.62 41.91C23.4848 45.8006 28.7775 47.9336 34.26 47.81C42.19 47.81 48.11 45.66 50.66 41.87C49.78 31.81 44.24 18.15 34.26 18.15C25.82 18.15 19.46 24.03 15.36 35.63C14.9927 36.6738 13.8488 37.2223 12.805 36.855C11.7612 36.4877 11.2127 35.3438 11.58 34.3C16.31 20.93 23.94 14.15 34.26 14.15C47.75 14.15 53.96 31.38 54.71 42.24C54.7368 42.6341 54.6464 43.0273 54.45 43.37C51.31 48.82 44.14 51.82 34.26 51.82Z" fill="#1D2D35"/>
      </svg>
      <div class="badge badge-error">
        <svg fill="none" viewBox="0 0 24 24" stroke="#fff" stroke-width="3">
          <path stroke-linecap="round" stroke-linejoin="round" d="M6 18L18 6M6 6l12 12"/>
        </svg>
      </div>
    </div>
    <h1>Authorization failed</h1>
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
