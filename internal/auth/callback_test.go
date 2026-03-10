package auth

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func listenLocal(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	return ln
}

func TestWaitForCallback_Success(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ln := listenLocal(t)
	addr := ln.Addr().String()
	state := "test-state-123"

	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)

	go func() {
		code, err := waitForCallback(ctx, state, ln)
		if err != nil {
			errCh <- err
		} else {
			codeCh <- code
		}
	}()

	time.Sleep(50 * time.Millisecond)

	resp, err := http.Get(fmt.Sprintf("http://%s/callback?state=%s&code=auth-code-456", addr, state))
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	select {
	case code := <-codeCh:
		assert.Equal(t, "auth-code-456", code)
	case err := <-errCh:
		t.Fatalf("unexpected error: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for callback")
	}
}

func TestWaitForCallback_StateMismatch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ln := listenLocal(t)
	addr := ln.Addr().String()
	errCh := make(chan error, 1)

	go func() {
		_, err := waitForCallback(ctx, "expected-state", ln)
		errCh <- err
	}()

	time.Sleep(50 * time.Millisecond)

	resp, err := http.Get(fmt.Sprintf("http://%s/callback?state=wrong-state&code=abc", addr))
	require.NoError(t, err)
	_ = resp.Body.Close()

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
		_, err := waitForCallback(ctx, "state", ln)
		errCh <- err
	}()

	time.Sleep(50 * time.Millisecond)

	resp, err := http.Get(fmt.Sprintf("http://%s/callback?error=access_denied", addr))
	require.NoError(t, err)
	_ = resp.Body.Close()

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
		_, err := waitForCallback(ctx, "state", ln)
		errCh <- err
	}()

	time.Sleep(50 * time.Millisecond)

	resp, err := http.Get(fmt.Sprintf("http://%s/callback?error=access_denied&error_description=User+denied+access", addr))
	require.NoError(t, err)
	_ = resp.Body.Close()

	select {
	case err := <-errCh:
		assert.Contains(t, err.Error(), "access_denied")
		assert.Contains(t, err.Error(), "User denied access")
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
		_, err := waitForCallback(ctx, "state", ln)
		errCh <- err
	}()

	time.Sleep(50 * time.Millisecond)

	resp, err := http.Get(fmt.Sprintf("http://%s/callback?state=state", addr))
	require.NoError(t, err)
	_ = resp.Body.Close()

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
		_, err := waitForCallback(ctx, "state", ln)
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

	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)

	go func() {
		code, err := waitForCallback(ctx, "state-123", ln)
		if err != nil {
			errCh <- err
		} else {
			codeCh <- code
		}
	}()

	time.Sleep(50 * time.Millisecond)

	// Request to /favicon.ico should get 404 and not kill the server
	resp, err := http.Get(fmt.Sprintf("http://%s/favicon.ico", addr))
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)

	// The real callback should still work after the spurious request
	resp, err = http.Get(fmt.Sprintf("http://%s/callback?state=state-123&code=real-code", addr))
	require.NoError(t, err)
	_ = resp.Body.Close()

	select {
	case code := <-codeCh:
		assert.Equal(t, "real-code", code)
	case err := <-errCh:
		t.Fatalf("unexpected error: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("timeout")
	}
}
