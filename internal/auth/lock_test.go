package auth

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gofrs/flock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/config"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// The concurrency tests below run REAL processes, not goroutines and not
// mocks: the failure they exist to catch is two `basecamp` invocations
// racing over one credential file, and a goroutine in one process shares a
// Manager lock that separate invocations never do. Each child is this test
// binary re-invoked at helperTest, which does one credential operation and
// exits with what happened.

// helperModeEnv selects what a child process does; unset means this binary
// is not a child and helperTest skips.
const helperModeEnv = "BASECAMP_AUTH_LOCK_TEST_MODE"

// helperDirEnv is the config (and so credential, and so lock) directory a
// child shares with its siblings.
const helperDirEnv = "BASECAMP_AUTH_LOCK_TEST_DIR"

// helperKeyEnv is the credential key a child operates on.
const helperKeyEnv = "BASECAMP_AUTH_LOCK_TEST_KEY"

// helperTest is the test name children are invoked at.
const helperTest = "TestCredentialLockHelperProcess"

// TestCredentialLockHelperProcess is not a test. It is the body of the
// child processes the concurrency tests spawn — one credential operation,
// as a `basecamp` command would do it, in a process of its own. It exits
// before the testing package can print, so its stdout carries only the one
// line its parent reads.
func TestCredentialLockHelperProcess(t *testing.T) {
	mode := os.Getenv(helperModeEnv)
	if mode == "" {
		t.Skip("helper process, run by the concurrency tests")
	}

	dir := os.Getenv(helperDirEnv)
	key := os.Getenv(helperKeyEnv)
	m := NewManager(&config.Config{BaseURL: "https://3.basecampapi.com", ActiveProfile: key}, http.DefaultClient)
	m.SetStore(NewStore(dir))

	if mode == "token" || mode == "save" {
		// Announce readiness and wait to be released. Without the barrier
		// the scheduler is free to run each child to completion before the
		// next one starts, and a test of what happens when they overlap
		// would never overlap.
		fmt.Println("READY")
		if _, err := bufio.NewReader(os.Stdin).ReadString('\n'); err != nil {
			fmt.Fprintf(os.Stderr, "ERR=never released: %v\n", err)
			os.Exit(5)
		}
	}

	switch mode {
	case "token":
		// What every command does before its first request.
		token, err := m.AccessToken(context.Background())
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERR=%v\n", err)
			os.Exit(3)
		}
		fmt.Printf("TOKEN=%s\n", token)
	case "hold":
		// A process that takes the credential lock and never gives it
		// back, for the parent to kill while it is holding.
		release, err := m.store.acquire(context.Background(), keyLockName("profile:"+key))
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERR=%v\n", err)
			os.Exit(3)
		}
		defer release()
		fmt.Printf("HELD=%s\n", key)
		os.Stdout.Sync() //nolint:errcheck // best effort: the parent reads a line or times out
		time.Sleep(time.Minute)
	case "save":
		// What a login does: write this process's own credential key.
		err := m.store.Save("profile:"+key, &Credentials{
			AccessToken: "access-" + key,
			OAuthType:   oauthTypeBC5,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERR=%v\n", err)
			os.Exit(3)
		}
		fmt.Printf("SAVED=%s\n", key)
	default:
		fmt.Fprintf(os.Stderr, "ERR=unknown mode %q\n", mode)
		os.Exit(4)
	}
	os.Exit(0)
}

// rotatingTokenEndpoint is an authorization server that rotates the refresh
// token on every grant and REFUSES a stale one, which is what Basecamp
// does. Presenting a refresh token that has already been spent is exactly
// the symptom of a lost rotation, so it is counted rather than tolerated.
type rotatingTokenEndpoint struct {
	mu sync.Mutex
	// current is the only refresh token the server will accept.
	current string
	// issued is the last access token handed out.
	issued string
	// grants counts successful refreshes.
	grants int
	// stale counts refreshes presenting a token that had been rotated
	// away — one per credential a racing process lost.
	stale int
	// delay widens the window a competing process could slip into.
	delay time.Duration
}

func (e *rotatingTokenEndpoint) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		presented := r.Form.Get("refresh_token")

		e.mu.Lock()
		defer e.mu.Unlock()
		if presented != e.current {
			e.stale++
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":"invalid_grant","error_description":"refresh token already used"}`)
			return
		}
		time.Sleep(e.delay)
		e.grants++
		e.current = fmt.Sprintf("refresh-%d", e.grants)
		e.issued = fmt.Sprintf("access-%d", e.grants)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"access_token":%q,"refresh_token":%q,"token_type":"bearer","expires_in":3600}`, e.issued, e.current)
	})
}

func (e *rotatingTokenEndpoint) snapshot() (grants, stale int, issued string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.grants, e.stale, e.issued
}

// TestConcurrentProcessesKeepTheRotatedCredential is the card's own
// criterion: twenty `basecamp` calls on one profile, all starting with the
// same expired credential, must leave the credential intact.
//
// The server rotates the refresh token on every grant and refuses a spent
// one. Without a cross-process lock all twenty load the same refresh token,
// all twenty present it, nineteen are refused with invalid_grant, and the
// credential is gone — which is the bug this is a regression test for.
// With it, one process refreshes under the lock and the other nineteen,
// re-reading under the same lock, find the fresh credential and send
// nothing: exactly one grant, no stale presentations, twenty usable tokens.
func TestConcurrentProcessesKeepTheRotatedCredential(t *testing.T) {
	endpoint := &rotatingTokenEndpoint{current: "refresh-0", delay: 40 * time.Millisecond}
	srv := httptest.NewServer(endpoint.handler())
	defer srv.Close()

	dir := t.TempDir()
	store := newTestStore(t, dir)
	require.NoError(t, store.Save("profile:agentbot", &Credentials{
		AccessToken:   "access-0",
		RefreshToken:  "refresh-0",
		OAuthType:     oauthTypeBC5,
		TokenEndpoint: srv.URL + "/token",
		// Already inside the refresh window, so every process arrives
		// wanting to refresh.
		ExpiresAt: time.Now().Add(-time.Minute).Unix(),
	}))

	const processes = 20
	keys := make([]string, processes)
	for i := range keys {
		keys[i] = "agentbot"
	}
	results := runHelpers(t, "token", dir, keys)

	for i, r := range results {
		assert.NoErrorf(t, r.err, "process %d failed: %s", i, r.stderr)
	}

	grants, stale, issued := endpoint.snapshot()
	assert.Equal(t, 0, stale, "a process presented a refresh token another had already rotated away")
	assert.Equal(t, 1, grants, "the refresh should happen once and be shared, not once per process")

	// The credential survived, and it is the one the server last issued.
	final, err := store.Load("profile:agentbot")
	require.NoError(t, err)
	assert.Equal(t, issued, final.AccessToken)
	assert.Equal(t, "refresh-1", final.RefreshToken)

	// And every process was served that token.
	for i, r := range results {
		assert.Equalf(t, issued, r.value, "process %d served a different token", i)
	}
}

// TestConcurrentProcessesKeepEveryProfilesCredential covers the other half
// of the report: "Not authenticated for profile:" on a profile that was
// logged in a moment ago.
//
// The keyring fallback keeps every profile in one credentials.json and
// rewrites the whole document on each Save, so twenty processes saving
// twenty DIFFERENT keys at once used to leave one document holding
// whichever handful happened not to be overwritten. All twenty must
// survive.
func TestConcurrentProcessesKeepEveryProfilesCredential(t *testing.T) {
	dir := t.TempDir()
	store := newTestStore(t, dir)

	const processes = 20
	keys := make([]string, processes)
	for i := range keys {
		keys[i] = "p" + strconv.Itoa(i)
	}

	results := runHelpers(t, "save", dir, keys)

	for i, r := range results {
		assert.NoErrorf(t, r.err, "process %d failed: %s", i, r.stderr)
	}
	for _, key := range keys {
		creds, err := store.Load("profile:" + key)
		if assert.NoErrorf(t, err, "credential for %q was lost", key) {
			assert.Equal(t, "access-"+key, creds.AccessToken)
		}
	}
}

// helperResult is what one child process reported.
type helperResult struct {
	value  string
	stderr string
	err    error
}

// helper is one started, not-yet-released child process.
type helper struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	// stderr is the package's mutex-guarded test buffer: os/exec writes a
	// child's stderr from a copying goroutine of its own, so a parent
	// reading it before Wait has returned — a readiness timeout's
	// diagnostic, above all — would race that goroutine.
	stderr *syncBuffer

	// waitOnce makes this the only owner of cmd.Wait. exec.Cmd.Wait is not
	// safe to call twice, and both the collector and the test cleanup that
	// kills a stuck child want to.
	waitOnce sync.Once
	waitErr  error
}

// runHelpers starts one child per key, waits until EVERY one of them says
// it is ready, releases them all at once, and collects what each reported.
//
// The barrier is the whole point. Starting processes from goroutines does
// not make them overlap — the scheduler may run each to completion before
// the next one starts — so without it a test of concurrent access could
// pass having exercised no concurrency at all.
func runHelpers(t *testing.T, mode, dir string, keys []string) []helperResult {
	t.Helper()

	helpers := make([]*helper, len(keys))
	for i, key := range keys {
		helpers[i] = startHelper(t, mode, dir, key)
	}
	for i, h := range helpers {
		require.NoErrorf(t, h.await("READY", 60*time.Second), "process %d never became ready: %s", i, h.stderr)
	}
	for _, h := range helpers {
		_, _ = io.WriteString(h.stdin, "go\n")
		_ = h.stdin.Close()
	}

	results := make([]helperResult, len(helpers))
	for i, h := range helpers {
		results[i] = h.collect()
	}
	return results
}

// startHelper starts one child process of this test binary, stopped at the
// barrier.
func startHelper(t *testing.T, mode, dir, key string) *helper {
	t.Helper()
	// #nosec G204 -- the command is this test binary, at a fixed test name.
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^"+helperTest+"$", "-test.v=false")
	cmd.Env = append(os.Environ(),
		helperModeEnv+"="+mode,
		helperDirEnv+"="+dir,
		helperKeyEnv+"="+key,
		"BASECAMP_NO_KEYRING=1",
		// A child must not pick up the developer's own configuration or
		// an ambient token.
		"BASECAMP_TOKEN=",
		"XDG_CONFIG_HOME="+dir,
	)
	stdin, err := cmd.StdinPipe()
	require.NoError(t, err)
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	h := &helper{cmd: cmd, stdin: stdin, stdout: bufio.NewReader(stdout), stderr: &syncBuffer{}}
	cmd.Stderr = h.stderr
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = h.wait()
	})
	return h
}

// await reads lines until one carries want, or the deadline passes.
func (h *helper) await(want string, within time.Duration) error {
	line := make(chan string, 1)
	fail := make(chan error, 1)
	go func() {
		for {
			text, err := h.stdout.ReadString('\n')
			if strings.Contains(text, want) {
				line <- text
				return
			}
			if err != nil {
				fail <- err
				return
			}
		}
	}()
	select {
	case <-line:
		return nil
	case err := <-fail:
		return err
	case <-time.After(within):
		return errors.New("timed out")
	}
}

// wait reaps the child exactly once, whichever of the collector and the
// cleanup gets there first; the other blocks on the Once and takes the
// same answer.
func (h *helper) wait() error {
	h.waitOnce.Do(func() { h.waitErr = h.cmd.Wait() })
	return h.waitErr
}

// collect drains the child's remaining output, reaps it, and only THEN
// reads stderr — os/exec's stderr copier is only known to be finished once
// Wait has returned.
func (h *helper) collect() helperResult {
	value := ""
	for {
		line, err := h.stdout.ReadString('\n')
		if _, rest, found := strings.Cut(line, "="); found {
			value = strings.TrimSpace(rest)
		}
		if err != nil {
			break
		}
	}
	err := h.wait()
	return helperResult{value: value, stderr: h.stderr.String(), err: err}
}

// TestCredentialLockSerializesTheWholeRefresh proves the lock is held
// across the network round trip and not merely around the write: while a
// lock is held from outside, a refresh that would otherwise go straight out
// makes no request at all.
func TestCredentialLockSerializesTheWholeRefresh(t *testing.T) {
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"access_token":"fresh","refresh_token":"rotated","token_type":"bearer","expires_in":3600}`)
	}))
	defer srv.Close()

	dir := t.TempDir()
	store := newTestStore(t, dir)
	require.NoError(t, store.Save("profile:bot", &Credentials{
		AccessToken:   "stale",
		RefreshToken:  "refresh-0",
		OAuthType:     oauthTypeBC5,
		TokenEndpoint: srv.URL + "/token",
		ExpiresAt:     time.Now().Add(-time.Minute).Unix(),
	}))

	// Hold the credential's lock the way another process would.
	require.NoError(t, os.MkdirAll(store.lockDir(), 0o700))
	held := flock.New(filepath.Join(store.lockDir(), keyLockName("profile:bot")))
	locked, err := held.TryLock()
	require.NoError(t, err)
	require.True(t, locked)
	defer func() { _ = held.Close() }()

	restoreLockWait(t, 100*time.Millisecond)

	m := NewManager(&config.Config{BaseURL: "https://3.basecampapi.com", ActiveProfile: "bot"}, srv.Client())
	m.SetStore(store)

	_, err = m.AccessToken(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "waiting for another basecamp process")
	assert.Zero(t, requests, "the refresh was sent while another process held the lock")
}

// TestHeldStoreLockBlocksAnotherProcessSave shows the contention directly:
// while this test holds the whole-store lock, a real second process's Save
// cannot complete, and it completes the moment the lock is released. The
// concurrency tests above assert the OUTCOME of that exclusion; this
// asserts the exclusion itself, so neither can pass by never overlapping.
func TestHeldStoreLockBlocksAnotherProcessSave(t *testing.T) {
	dir := t.TempDir()
	store := newTestStore(t, dir)
	require.NoError(t, os.MkdirAll(store.lockDir(), 0o700))

	held := flock.New(filepath.Join(store.lockDir(), storeLockName))
	locked, err := held.TryLock()
	require.NoError(t, err)
	require.True(t, locked)
	defer func() { _ = held.Close() }()

	h := startHelper(t, "save", dir, "blocked")
	require.NoError(t, h.await("READY", 60*time.Second), h.stderr)
	_, _ = io.WriteString(h.stdin, "go\n")
	require.NoError(t, h.stdin.Close())

	done := make(chan helperResult, 1)
	go func() { done <- h.collect() }()

	select {
	case r := <-done:
		t.Fatalf("the save completed while the store lock was held (err %v, stderr %q)", r.err, r.stderr)
	case <-time.After(300 * time.Millisecond):
	}

	require.NoError(t, held.Close())

	select {
	case r := <-done:
		require.NoError(t, r.err, r.stderr)
		creds, loadErr := store.Load("profile:blocked")
		require.NoError(t, loadErr)
		assert.Equal(t, "access-blocked", creds.AccessToken)
	case <-time.After(30 * time.Second):
		t.Fatal("the save never completed after the lock was released")
	}
}

// TestCanceledSaveWaitingOnTheStoreLockWritesNothing: a login's last
// cancellation check happens under the KEY lock, and the store lock is
// taken after it. Without a cancellation-aware wait there, a Ctrl-C landing
// while the store lock is held by someone else would still store the
// credential the person stopped, the moment the holder released.
func TestCanceledSaveWaitingOnTheStoreLockWritesNothing(t *testing.T) {
	dir := t.TempDir()
	store := newTestStore(t, dir)
	require.NoError(t, store.Save("profile:bot", &Credentials{AccessToken: "original"}))

	require.NoError(t, os.MkdirAll(store.lockDir(), 0o700))
	held := flock.New(filepath.Join(store.lockDir(), storeLockName))
	locked, err := held.TryLock()
	require.NoError(t, err)
	require.True(t, locked)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- store.SaveContext(ctx, "profile:bot", &Credentials{AccessToken: "replacement"})
	}()

	// It must still be waiting, not writing.
	select {
	case saveErr := <-done:
		t.Fatalf("the save completed while the store lock was held: %v", saveErr)
	case <-time.After(200 * time.Millisecond):
	}

	cancel()
	require.NoError(t, held.Close())

	select {
	case saveErr := <-done:
		require.Error(t, saveErr)
		assert.ErrorIs(t, saveErr, context.Canceled)
	case <-time.After(30 * time.Second):
		t.Fatal("the canceled save never returned")
	}

	creds, err := store.Load("profile:bot")
	require.NoError(t, err)
	assert.Equal(t, "original", creds.AccessToken, "a canceled save wrote anyway")
}

// TestHeldStoreLockBlocksAFileBackedRead: replacing credentials.json is
// not atomic on every platform — where the rename over an existing file
// fails, the underlying store removes it and renames into its place — so a
// read landing in that gap would report the store empty and produce the
// "Not authenticated for profile:" this package exists to stop. Readers
// therefore take the store's SHARED lock and wait out a writer.
func TestHeldStoreLockBlocksAFileBackedRead(t *testing.T) {
	dir := t.TempDir()
	store := newTestStore(t, dir)
	require.NoError(t, store.Save("profile:bot", &Credentials{AccessToken: "original"}))

	held := flock.New(filepath.Join(store.lockDir(), storeLockName))
	locked, err := held.TryLock()
	require.NoError(t, err)
	require.True(t, locked)

	done := make(chan *Credentials, 1)
	go func() {
		creds, loadErr := store.Load("profile:bot")
		assert.NoError(t, loadErr)
		done <- creds
	}()

	select {
	case <-done:
		t.Fatal("a read completed while a writer held the store lock")
	case <-time.After(200 * time.Millisecond):
	}

	require.NoError(t, held.Close())

	select {
	case creds := <-done:
		assert.Equal(t, "original", creds.AccessToken)
	case <-time.After(30 * time.Second):
		t.Fatal("the read never completed after the lock was released")
	}
}

// TestFileBackedReadsDoNotQueueBehindEachOther: the read lock is shared,
// so twenty commands asking for a token at once do not serialize on it.
func TestFileBackedReadsDoNotQueueBehindEachOther(t *testing.T) {
	dir := t.TempDir()
	store := newTestStore(t, dir)
	require.NoError(t, store.Save("profile:bot", &Credentials{AccessToken: "original"}))

	// One reader holds its shared lock while another takes one too.
	release, err := store.lockFile(lockRequest{name: storeLockName, what: "credential store", shared: true})
	require.NoError(t, err)
	defer release()

	restoreLockWait(t, time.Second)
	creds, err := store.Load("profile:bot")
	require.NoError(t, err, "a read waited for another read")
	assert.Equal(t, "original", creds.AccessToken)
}

// TestUnreadableStoreIsNotReportedAsNotAuthenticated: "not authenticated"
// is a statement about the credential, and a store that could not be
// reached makes none. Reporting contention as an auth failure would send
// the operator to a login that fixes nothing and throw away a
// classification they can act on.
func TestUnreadableStoreIsNotReportedAsNotAuthenticated(t *testing.T) {
	dir := t.TempDir()
	store := newTestStore(t, dir)
	require.NoError(t, store.Save("profile:bot", &Credentials{AccessToken: "live"}))

	held := flock.New(filepath.Join(store.lockDir(), storeLockName))
	locked, err := held.TryLock()
	require.NoError(t, err)
	require.True(t, locked)
	defer func() { _ = held.Close() }()

	restoreLockWait(t, 100*time.Millisecond)

	m := NewManager(&config.Config{BaseURL: "https://3.basecampapi.com", ActiveProfile: "bot"}, http.DefaultClient)
	m.SetStore(store)

	_, err = m.AccessToken(context.Background())
	require.Error(t, err)
	e := output.AsError(err)
	assert.Equal(t, output.CodeRateLimit, e.Code, "contention was reported as an auth failure")
	assert.True(t, e.Retryable)
	assert.NotContains(t, e.Message, "Not authenticated")
}

// TestCanceledReadStopsWaitingForTheStoreLock: a read waits for a writer,
// so it has to stop waiting when the command does — otherwise a canceled
// request sits on the manager lock for the whole budget.
func TestCanceledReadStopsWaitingForTheStoreLock(t *testing.T) {
	dir := t.TempDir()
	store := newTestStore(t, dir)
	require.NoError(t, store.Save("profile:bot", &Credentials{AccessToken: "live"}))

	held := flock.New(filepath.Join(store.lockDir(), storeLockName))
	locked, err := held.TryLock()
	require.NoError(t, err)
	require.True(t, locked)
	defer func() { _ = held.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, loadErr := store.LoadContext(ctx, "profile:bot")
		done <- loadErr
	}()

	select {
	case <-done:
		t.Fatal("the read did not wait for the writer")
	case <-time.After(100 * time.Millisecond):
	}

	cancel()
	select {
	case loadErr := <-done:
		assert.ErrorIs(t, loadErr, context.Canceled)
	case <-time.After(30 * time.Second):
		t.Fatal("a canceled read kept waiting")
	}
}

// TestARemedyNeverReadsTheCredentialStore: the remedy on an error names
// the login to run, and building it must not be able to turn one failure
// into a hang — the store is usually the OS keyring, and a keychain that
// locks after its availability probe waits on a person who may never
// answer. So it comes from what the Manager already saw, not from a read.
func TestARemedyNeverReadsTheCredentialStore(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BASECAMP_NO_KEYRING", "1")
	store := NewStore(dir)
	reads := &countingStore{}
	store.inner = reads
	store.initOnce.Do(func() {})

	m := NewManager(&config.Config{BaseURL: "https://3.basecampapi.com", ActiveProfile: "bot"}, http.DefaultClient)
	m.SetStore(store)

	// Nothing seen yet: the interactive form, and no read to find that out.
	assert.Contains(t, m.LoginHint(), "Run: basecamp auth login -P bot")
	assert.Zero(t, reads.loads, "building a remedy read the credential store")

	// Once an agent credential has been in hand, the remedy is its login —
	// still without going back for it.
	m.remember(&Credentials{OAuthType: oauthTypeAgent, ClientID: "agent-client"})
	hint := m.LoginHint()
	assert.Contains(t, hint, "--with-client-credentials")
	assert.Contains(t, hint, "--client-id agent-client")
	assert.Contains(t, hint, "-P bot")
	assert.Zero(t, reads.loads, "building a remedy read the credential store")
}

// TestCanceledReadIsReportedAsCancellation: a canceled command must not be
// told it is not authenticated. The credential is fine; the wait ended.
func TestCanceledReadIsReportedAsCancellation(t *testing.T) {
	dir := t.TempDir()
	store := newTestStore(t, dir)
	require.NoError(t, store.Save("profile:bot", &Credentials{AccessToken: "live"}))

	held := flock.New(filepath.Join(store.lockDir(), storeLockName))
	locked, err := held.TryLock()
	require.NoError(t, err)
	require.True(t, locked)
	defer func() { _ = held.Close() }()

	m := NewManager(&config.Config{BaseURL: "https://3.basecampapi.com", ActiveProfile: "bot"}, http.DefaultClient)
	m.SetStore(store)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, tokenErr := m.AccessToken(ctx)
		done <- tokenErr
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case tokenErr := <-done:
		require.Error(t, tokenErr)
		assert.ErrorIs(t, tokenErr, context.Canceled, "cancellation was reported as something else")
		assert.NotContains(t, tokenErr.Error(), "Not authenticated")
	case <-time.After(30 * time.Second):
		t.Fatal("a canceled token lookup kept waiting")
	}
}

// TestAuthenticationGateWaitsOutAWriterInsteadOfGuessing: replacing
// credentials.json is not atomic everywhere, so a gate that read without
// the lock could see the file missing mid-replacement and refuse to start
// for a credential that is sitting right there. The gate waits for the
// writer and gets the right answer.
func TestAuthenticationGateWaitsOutAWriterInsteadOfGuessing(t *testing.T) {
	dir := t.TempDir()
	store := newTestStore(t, dir)
	require.NoError(t, store.Save("profile:bot", &Credentials{AccessToken: "live"}))

	// A writer holding the lock, with the file gone the way a non-atomic
	// replacement leaves it.
	held := flock.New(filepath.Join(store.lockDir(), storeLockName))
	locked, err := held.TryLock()
	require.NoError(t, err)
	require.True(t, locked)
	saved, err := os.ReadFile(filepath.Join(dir, "credentials.json"))
	require.NoError(t, err)
	require.NoError(t, os.Remove(filepath.Join(dir, "credentials.json")))

	m := NewManager(&config.Config{BaseURL: "https://3.basecampapi.com", ActiveProfile: "bot"}, http.DefaultClient)
	m.SetStore(store)

	answered := make(chan bool, 1)
	go func() { answered <- m.IsAuthenticated() }()

	select {
	case <-answered:
		t.Fatal("the gate answered from inside the writer's window")
	case <-time.After(200 * time.Millisecond):
	}

	// The writer finishes: the file is back before the lock is released.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "credentials.json"), saved, 0o600))
	require.NoError(t, held.Close())

	select {
	case authenticated := <-answered:
		assert.True(t, authenticated, "the gate reported no credential for one that was being replaced")
	case <-time.After(30 * time.Second):
		t.Fatal("the gate never answered")
	}
}

// TestCheckAuthenticatedTellsAbsentFromUnreadable: a bool cannot carry "I
// could not reach the store", and a gate that reads that as "you are not
// logged in" refuses work for a credential that is there. Anything that
// stops a command asks this instead.
func TestCheckAuthenticatedTellsAbsentFromUnreadable(t *testing.T) {
	dir := t.TempDir()
	store := newTestStore(t, dir)

	m := NewManager(&config.Config{BaseURL: "https://3.basecampapi.com", ActiveProfile: "bot"}, http.DefaultClient)
	m.SetStore(store)

	// Nothing stored is an answer, not a failure.
	authenticated, err := m.CheckAuthenticated(context.Background())
	require.NoError(t, err)
	assert.False(t, authenticated)

	require.NoError(t, store.Save("profile:bot", &Credentials{AccessToken: "live"}))
	authenticated, err = m.CheckAuthenticated(context.Background())
	require.NoError(t, err)
	assert.True(t, authenticated)

	// A store it could not reach is not an answer at all.
	held := flock.New(filepath.Join(store.lockDir(), storeLockName))
	locked, lockErr := held.TryLock()
	require.NoError(t, lockErr)
	require.True(t, locked)
	defer func() { _ = held.Close() }()

	restoreLockWait(t, 100*time.Millisecond)
	authenticated, err = m.CheckAuthenticated(context.Background())
	require.Error(t, err)
	assert.False(t, authenticated)
	assert.Equal(t, output.CodeRateLimit, output.AsError(err).Code)

	// And a canceled caller is told that, not that it is logged out.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = m.CheckAuthenticated(ctx)
	assert.ErrorIs(t, err, context.Canceled)
}

// TestCanceledIdentityWritebackDoesNotHoldTheCommand: recording who a
// credential belongs to is best effort — its callers discard the error —
// so it must not be able to hold a finished or canceled command for the
// length of another process's refresh.
func TestCanceledIdentityWritebackDoesNotHoldTheCommand(t *testing.T) {
	dir := t.TempDir()
	store := newTestStore(t, dir)
	require.NoError(t, store.Save("profile:bot", &Credentials{AccessToken: "live"}))

	// Another process mid-refresh, holding the credential's lock.
	require.NoError(t, os.MkdirAll(store.lockDir(), 0o700))
	held := flock.New(filepath.Join(store.lockDir(), keyLockName("profile:bot")))
	locked, err := held.TryLock()
	require.NoError(t, err)
	require.True(t, locked)
	defer func() { _ = held.Close() }()

	restoreLockWait(t, time.Hour)

	m := NewManager(&config.Config{BaseURL: "https://3.basecampapi.com", ActiveProfile: "bot"}, http.DefaultClient)
	m.SetStore(store)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- m.SetUserIdentity(ctx, "1", "someone@example.com") }()

	select {
	case <-done:
		t.Fatal("the writeback did not wait for the lock")
	case <-time.After(100 * time.Millisecond):
	}

	cancel()
	select {
	case writeErr := <-done:
		assert.ErrorIs(t, writeErr, context.Canceled)
	case <-time.After(30 * time.Second):
		t.Fatal("a canceled command was held by a best-effort writeback")
	}

	// And the credential it could not write to is untouched.
	creds, loadErr := store.Load("profile:bot")
	require.NoError(t, loadErr)
	assert.Empty(t, creds.UserID)
}

// TestCanceledCallerNeverEntersTheCriticalSection: the lock is free, so
// it would be taken and the work done — a credential revoked, deleted,
// replaced — for a command the person already stopped. Every caller is
// covered by the check inside the lock rather than by each of them
// remembering to make it.
func TestCanceledCallerNeverEntersTheCriticalSection(t *testing.T) {
	dir := t.TempDir()
	store := newTestStore(t, dir)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	ran := false
	err := store.withKeyLock(ctx, "profile:bot", func() error {
		ran = true
		return nil
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.False(t, ran, "a canceled caller ran the critical section")
}

// TestCanceledCallerIsRefusedEvenWhereLockingIsImpossible: a host where
// the lock cannot be created falls through unlocked, which must not become
// a way for a canceled command to do its work after all. The check comes
// before the fall-through, not after it.
func TestCanceledCallerIsRefusedEvenWhereLockingIsImpossible(t *testing.T) {
	dir := t.TempDir()
	store := newTestStore(t, dir)
	require.NoError(t, store.Save("profile:bot", &Credentials{AccessToken: "original"}))
	// A file where the lock directory has to go: MkdirAll cannot succeed.
	require.NoError(t, os.RemoveAll(store.lockDir()))
	require.NoError(t, os.WriteFile(store.lockDir(), []byte("not a directory"), 0o600))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	ran := false
	err := store.withKeyLock(ctx, "profile:bot", func() error {
		ran = true
		return nil
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.False(t, ran, "a canceled caller ran unlocked instead of being refused")

	require.ErrorIs(t, store.SaveContext(ctx, "profile:bot", &Credentials{AccessToken: "replacement"}), context.Canceled)
	creds, loadErr := store.Load("profile:bot")
	require.NoError(t, loadErr)
	assert.Equal(t, "original", creds.AccessToken, "a canceled save wrote anyway where locking was impossible")
}

// TestCancellationDuringLockSetupIsStillRefused: the check at the top of
// lockFile is not a sufficient gate on its own. The steps after it touch a
// filesystem that can block, so a wait can end while one of them runs —
// and every unlocked fall-through has to notice, not just the entry.
//
// The cause is stubbed rather than raced: it is live on entry and over by
// the time the lock directory has failed, which is the sequence a slow
// filesystem produces and a test cannot schedule.
func TestCancellationDuringLockSetupIsStillRefused(t *testing.T) {
	dir := t.TempDir()
	store := newTestStore(t, dir)
	require.NoError(t, os.WriteFile(store.lockDir(), []byte("not a directory"), 0o600))

	calls := 0
	cause := func() error {
		calls++
		if calls == 1 {
			return nil
		}
		return context.Canceled
	}

	// A nil done channel is never selected on here: the fall-through is
	// reached before any wait.
	release, err := store.lockFile(lockRequest{name: keyLockName("profile:bot"), what: "credential", cause: cause})
	release()
	assert.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 2, calls, "the fall-through never rechecked the caller's wait")
}

// TestCanceledLogoutRevokesNothing is that check where it matters most: a
// logout revokes the token server-side and then deletes it, and neither
// should happen for a command that was stopped.
func TestCanceledLogoutRevokesNothing(t *testing.T) {
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	dir := t.TempDir()
	store := newTestStore(t, dir)
	require.NoError(t, store.Save("profile:bot", &Credentials{
		AccessToken:   "live",
		RefreshToken:  "refresh-0",
		OAuthType:     oauthTypeBC5,
		Issuer:        srv.URL,
		TokenEndpoint: srv.URL + "/token",
	}))

	m := NewManager(&config.Config{BaseURL: srv.URL, ActiveProfile: "bot"}, srv.Client())
	m.SetStore(store)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := m.LogoutCredential(ctx, "profile:bot", "")
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Zero(t, requests, "a canceled logout revoked the token")

	// And again where the lock cannot be created at all, which falls
	// through unlocked for a live caller and must not for this one.
	require.NoError(t, os.RemoveAll(store.lockDir()))
	require.NoError(t, os.WriteFile(store.lockDir(), []byte("not a directory"), 0o600))
	_, err = m.LogoutCredential(ctx, "profile:bot", "")
	require.ErrorIs(t, err, context.Canceled)
	assert.Zero(t, requests, "a canceled logout revoked the token where locking was impossible")

	creds, loadErr := store.Load("profile:bot")
	require.NoError(t, loadErr, "a canceled logout deleted the credential")
	assert.Equal(t, "live", creds.AccessToken)
}

// TestCredentialLockReleasedByProcessDeathIsNotStale: a holder that dies
// takes its lock with it. The kernel drops an flock when the descriptor
// closes, process death included, so there is no stale lock to reap and no
// reaping logic to get wrong — which is why nothing here writes a PID into
// a lock file or ages one out.
func TestCredentialLockReleasedByProcessDeathIsNotStale(t *testing.T) {
	dir := t.TempDir()
	store := newTestStore(t, dir)

	// #nosec G204 -- the command is this test binary, at a fixed test name.
	holder := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^"+helperTest+"$", "-test.v=false")
	holder.Env = append(os.Environ(),
		helperModeEnv+"=hold",
		helperDirEnv+"="+dir,
		helperKeyEnv+"=bot",
		"BASECAMP_NO_KEYRING=1",
		"XDG_CONFIG_HOME="+dir,
	)
	stdout, err := holder.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, holder.Start())
	t.Cleanup(func() {
		_ = holder.Process.Kill()
		_ = holder.Wait()
	})

	// Wait for it to say it holds the lock.
	held := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		if scanner.Scan() {
			held <- scanner.Text()
		}
		close(held)
	}()
	select {
	case line, ok := <-held:
		require.True(t, ok, "the holder exited without taking the lock")
		require.Contains(t, line, "HELD=")
	case <-time.After(30 * time.Second):
		t.Fatal("the holder never took the lock")
	}

	// While it holds, the lock is genuinely held.
	restoreLockWait(t, 100*time.Millisecond)
	_, err = store.acquire(context.Background(), keyLockName("profile:bot"))
	require.Error(t, err, "the lock was not held while the holder was alive")

	require.NoError(t, holder.Process.Kill())
	_, _ = holder.Process.Wait()

	// The moment it dies the lock is free, with no cleanup in between.
	credentialLockWait = 10 * time.Second
	release, err := store.acquire(context.Background(), keyLockName("profile:bot"))
	require.NoError(t, err, "a dead holder's lock was still held")
	release()
}

// TestUnlockableStoreProceedsWithAWarning: a host where the lock cannot be
// created at all must keep working — unlocked is where every process was
// before this existed — and must say so once.
func TestUnlockableStoreProceedsWithAWarning(t *testing.T) {
	// A file where the lock directory has to go: MkdirAll cannot succeed.
	dir := t.TempDir()
	store := newTestStore(t, dir)
	require.NoError(t, os.WriteFile(store.lockDir(), []byte("not a directory"), 0o600))

	ran := false
	err := store.withKeyLock(context.Background(), "profile:bot", func() error {
		ran = true
		return nil
	})
	require.NoError(t, err)
	assert.True(t, ran, "the operation was refused instead of proceeding unlocked")
}

// restoreLockWait shortens the lock wait for one test and puts it back.
func restoreLockWait(t *testing.T, d time.Duration) {
	t.Helper()
	original := credentialLockWait
	credentialLockWait = d
	t.Cleanup(func() { credentialLockWait = original })
}

// TestKeyLockNamesAreDistinctAndFilenameSafe: credential keys are profile
// names and URLs, and the lock file each maps to has to be a filename that
// no other key shares.
func TestKeyLockNamesAreDistinctAndFilenameSafe(t *testing.T) {
	keys := []string{
		"profile:bot",
		"profile:BOT",
		"https://3.basecampapi.com",
		"http://3.basecamp.localhost:3001",
		"profile:" + strings.Repeat("x", 500),
	}
	seen := map[string]string{}
	for _, key := range keys {
		name := keyLockName(key)
		assert.NotContains(t, name, "/")
		assert.NotContains(t, name, ":")
		assert.LessOrEqual(t, len(name), 255)
		if other, dup := seen[name]; dup {
			t.Fatalf("keys %q and %q share the lock file %q", other, key, name)
		}
		seen[name] = key
	}
}

// TestStoreSaveKeepsOtherKeys pins the whole-store lock's job at the unit
// level: a Save must never be able to drop a key it is not writing.
func TestStoreSaveKeepsOtherKeys(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BASECAMP_NO_KEYRING", "1")

	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			key := "profile:p" + strconv.Itoa(i)
			// A Store of its own, as a separate process would have.
			own := NewStore(dir)
			assert.NoError(t, own.Save(key, &Credentials{AccessToken: "a" + strconv.Itoa(i)}))
		}()
	}
	wg.Wait()

	raw, err := os.ReadFile(filepath.Join(dir, "credentials.json"))
	require.NoError(t, err)
	var all map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &all))
	assert.Len(t, all, 8)
}

// WithCredential holds the key's lock while fn runs: a caller that decides
// on a credential and writes inside fn cannot have it replaced underneath.
func TestWithCredentialHoldsTheKeyLock(t *testing.T) {
	t.Setenv("BASECAMP_NO_KEYRING", "1")
	dir := t.TempDir()
	store := NewStore(dir)
	const key = "profile:agent"
	require.NoError(t, store.Save(key, &Credentials{AccessToken: "first", OAuthType: "agent"}))

	inside := make(chan struct{})
	replaced := make(chan error, 1)
	err := store.WithCredential(context.Background(), key, func(held HeldCredential) error {
		creds, heldKey, ok := Held(held)
		require.True(t, ok)
		assert.Equal(t, "first", creds.AccessToken)
		assert.Equal(t, key, heldKey)
		go func() {
			close(inside)
			replaced <- NewStore(dir).withKeyLock(context.Background(), key, func() error {
				return NewStore(dir).Save(key, &Credentials{AccessToken: "second", OAuthType: "agent"})
			})
		}()
		<-inside
		time.Sleep(200 * time.Millisecond) // let the other writer reach the lock
		// The other writer is waiting on the key lock, so what was read is
		// still what is stored.
		again, err := store.Load(key)
		require.NoError(t, err)
		assert.Equal(t, "first", again.AccessToken)
		return nil
	})
	require.NoError(t, err)
	require.NoError(t, <-replaced)

	after, err := store.Load(key)
	require.NoError(t, err)
	assert.Equal(t, "second", after.AccessToken, "the waiting writer lands once the section ends")
}

// A host that cannot lock at all is an error for WithCredential, never a
// warning and an unsynchronized run: its callers' guarantee is the lock.
func TestWithCredentialRefusesWhenTheHostCannotLock(t *testing.T) {
	t.Setenv("BASECAMP_NO_KEYRING", "1")
	dir := t.TempDir()
	store := NewStore(dir)
	const key = "profile:agent"
	require.NoError(t, store.Save(key, &Credentials{AccessToken: "first", OAuthType: "agent"}))
	// No lock file can be created where nothing may be written.
	locks := store.lockDir()
	require.NoError(t, os.MkdirAll(locks, 0o700))
	require.NoError(t, os.Chmod(locks, 0o500))
	t.Cleanup(func() { _ = os.Chmod(locks, 0o700) })

	ran := false
	err := store.WithCredential(context.Background(), key, func(HeldCredential) error {
		ran = true
		return nil
	})
	require.Error(t, err)
	assert.False(t, ran, "the section never runs unlocked")
	var apiErr *output.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, output.CodeLockUnavailable, apiErr.Code)
}

// The proof stops being valid when the lock is released, so a callback that
// keeps it cannot write with it afterwards.
func TestHeldCredentialIsInvalidAfterTheLockIsReleased(t *testing.T) {
	t.Setenv("BASECAMP_NO_KEYRING", "1")
	store := NewStore(t.TempDir())
	const key = "profile:agent"
	require.NoError(t, store.Save(key, &Credentials{AccessToken: "first", OAuthType: "agent"}))

	var kept HeldCredential
	require.NoError(t, store.WithCredential(context.Background(), key, func(held HeldCredential) error {
		kept = held
		creds, heldKey, ok := Held(held)
		require.True(t, ok)
		require.NotNil(t, creds)
		assert.Equal(t, key, heldKey)
		return nil
	}))

	creds, heldKey, ok := Held(kept)
	assert.False(t, ok, "the lock is gone")
	assert.Nil(t, creds)
	assert.Empty(t, heldKey)
}
