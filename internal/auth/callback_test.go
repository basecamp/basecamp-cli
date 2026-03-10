package auth

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func listenLocal(t *testing.T) net.Listener {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	return ln
}

func httpGet(ctx context.Context, t *testing.T, url string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return string(body)
}

// httpGetBody performs a GET in a goroutine and returns a channel that delivers
// the response body. Used for the success path where the HTTP response is
// deferred until resolve() is called.
func httpGetBody(ctx context.Context, url string) <-chan string {
	ch := make(chan string, 1)
	go func() {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			ch <- ""
			return
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			ch <- ""
			return
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		ch <- string(body)
	}()
	return ch
}

type callbackResult struct {
	code    string
	resolve func(bool)
	err     error
}

func TestWaitForCallback_Success(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ln := listenLocal(t)
	addr := ln.Addr().String()
	state := "test-state-123"

	resultCh := make(chan callbackResult, 1)
	go func() {
		code, resolve, err := waitForCallback(ctx, state, ln)
		resultCh <- callbackResult{code, resolve, err}
	}()

	time.Sleep(50 * time.Millisecond)

	// HTTP response is deferred until resolve() — must be async.
	bodyCh := httpGetBody(ctx, fmt.Sprintf("http://%s/callback?state=%s&code=auth-code-456", addr, state))

	var r callbackResult
	select {
	case r = <-resultCh:
		require.NoError(t, r.err)
		assert.Equal(t, "auth-code-456", r.code)
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for callback")
	}

	r.resolve(true)

	select {
	case body := <-bodyCh:
		assert.Contains(t, body, "Authorization successful")
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for HTTP response")
	}
}

func TestWaitForCallback_ExchangeFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ln := listenLocal(t)
	addr := ln.Addr().String()
	state := "test-state-456"

	resultCh := make(chan callbackResult, 1)
	go func() {
		code, resolve, err := waitForCallback(ctx, state, ln)
		resultCh <- callbackResult{code, resolve, err}
	}()

	time.Sleep(50 * time.Millisecond)

	bodyCh := httpGetBody(ctx, fmt.Sprintf("http://%s/callback?state=%s&code=some-code", addr, state))

	var r callbackResult
	select {
	case r = <-resultCh:
		require.NoError(t, r.err)
		assert.Equal(t, "some-code", r.code)
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for callback")
	}

	r.resolve(false)

	select {
	case body := <-bodyCh:
		assert.Contains(t, body, "could not be completed")
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for HTTP response")
	}
}

func TestWaitForCallback_StateMismatch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ln := listenLocal(t)
	addr := ln.Addr().String()
	errCh := make(chan error, 1)

	go func() {
		_, _, err := waitForCallback(ctx, "expected-state", ln)
		errCh <- err
	}()

	time.Sleep(50 * time.Millisecond)

	resp := httpGet(ctx, t, fmt.Sprintf("http://%s/callback?state=wrong-state&code=abc", addr))
	body := readBody(t, resp)
	assert.Contains(t, body, "invalid or expired")

	select {
	case err := <-errCh:
		assert.Contains(t, err.Error(), "state mismatch")
	case <-time.After(3 * time.Second):
		t.Fatal("timeout")
	}
}

func TestWaitForCallback_OAuthError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ln := listenLocal(t)
	addr := ln.Addr().String()
	errCh := make(chan error, 1)

	go func() {
		_, _, err := waitForCallback(ctx, "state", ln)
		errCh <- err
	}()

	time.Sleep(50 * time.Millisecond)

	resp := httpGet(ctx, t, fmt.Sprintf("http://%s/callback?error=access_denied&state=state", addr))
	body := readBody(t, resp)
	assert.Contains(t, body, "chose not to authorize")

	select {
	case err := <-errCh:
		assert.Contains(t, err.Error(), "access_denied")
	case <-time.After(3 * time.Second):
		t.Fatal("timeout")
	}
}

func TestWaitForCallback_OAuthErrorWithDescription(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ln := listenLocal(t)
	addr := ln.Addr().String()
	errCh := make(chan error, 1)

	go func() {
		_, _, err := waitForCallback(ctx, "state", ln)
		errCh <- err
	}()

	time.Sleep(50 * time.Millisecond)

	resp := httpGet(ctx, t, fmt.Sprintf("http://%s/callback?error=access_denied&error_description=User+denied+access&state=state", addr))
	body := readBody(t, resp)
	assert.Contains(t, body, "chose not to authorize")

	select {
	case err := <-errCh:
		assert.Contains(t, err.Error(), "access_denied")
		assert.Contains(t, err.Error(), "User denied access")
	case <-time.After(3 * time.Second):
		t.Fatal("timeout")
	}
}

func TestWaitForCallback_OAuthErrorServerError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ln := listenLocal(t)
	addr := ln.Addr().String()
	errCh := make(chan error, 1)

	go func() {
		_, _, err := waitForCallback(ctx, "state", ln)
		errCh <- err
	}()

	time.Sleep(50 * time.Millisecond)

	resp := httpGet(ctx, t, fmt.Sprintf("http://%s/callback?error=server_error&error_description=Internal+error&state=state", addr))
	body := readBody(t, resp)
	assert.Contains(t, body, "Authorization failed")
	assert.NotContains(t, body, "chose not to authorize")

	select {
	case err := <-errCh:
		assert.Contains(t, err.Error(), "server_error")
		assert.Contains(t, err.Error(), "Internal error")
	case <-time.After(3 * time.Second):
		t.Fatal("timeout")
	}
}

func TestWaitForCallback_OAuthErrorWithBadState(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ln := listenLocal(t)
	addr := ln.Addr().String()
	errCh := make(chan error, 1)

	go func() {
		_, _, err := waitForCallback(ctx, "expected-state", ln)
		errCh <- err
	}()

	time.Sleep(50 * time.Millisecond)

	resp := httpGet(ctx, t, fmt.Sprintf("http://%s/callback?error=access_denied&state=wrong", addr))
	body := readBody(t, resp)
	assert.Contains(t, body, "invalid or expired")

	select {
	case err := <-errCh:
		assert.Contains(t, err.Error(), "state mismatch")
		assert.NotContains(t, err.Error(), "OAuth error")
	case <-time.After(3 * time.Second):
		t.Fatal("timeout")
	}
}

func TestWaitForCallback_MissingCode(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ln := listenLocal(t)
	addr := ln.Addr().String()
	errCh := make(chan error, 1)

	go func() {
		_, _, err := waitForCallback(ctx, "state", ln)
		errCh <- err
	}()

	time.Sleep(50 * time.Millisecond)

	resp := httpGet(ctx, t, fmt.Sprintf("http://%s/callback?state=state", addr))
	body := readBody(t, resp)
	assert.Contains(t, body, "Authorization failed")

	select {
	case err := <-errCh:
		assert.Contains(t, err.Error(), "missing authorization code")
	case <-time.After(3 * time.Second):
		t.Fatal("timeout")
	}
}

func TestWaitForCallback_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	ln := listenLocal(t)
	errCh := make(chan error, 1)

	go func() {
		_, _, err := waitForCallback(ctx, "state", ln)
		errCh <- err
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-errCh:
		assert.Error(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("timeout")
	}
}

func TestWaitForCallback_IgnoresNonCallbackPaths(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ln := listenLocal(t)
	addr := ln.Addr().String()

	resultCh := make(chan callbackResult, 1)
	go func() {
		code, resolve, err := waitForCallback(ctx, "state-123", ln)
		resultCh <- callbackResult{code, resolve, err}
	}()

	time.Sleep(50 * time.Millisecond)

	// Request to /favicon.ico should get 404 and not kill the server
	resp := httpGet(ctx, t, fmt.Sprintf("http://%s/favicon.ico", addr))
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)

	// The real callback's response is deferred until resolve().
	bodyCh := httpGetBody(ctx, fmt.Sprintf("http://%s/callback?state=state-123&code=real-code", addr))

	var r callbackResult
	select {
	case r = <-resultCh:
		require.NoError(t, r.err)
		assert.Equal(t, "real-code", r.code)
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for callback")
	}

	r.resolve(true)

	select {
	case body := <-bodyCh:
		assert.Contains(t, body, "Authorization successful")
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for HTTP response")
	}
}
