package auth

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
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
	results := runHelpers(t, processes, "token", dir, "agentbot")

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

	var wg sync.WaitGroup
	results := make([]helperResult, processes)
	for i, key := range keys {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = runHelper(t, "save", dir, key)
		}()
	}
	wg.Wait()

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

// runHelpers starts n children at once and collects what each reported.
// They are started from goroutines rather than in sequence so the window
// they contend over is as wide as the process start-up jitter allows.
func runHelpers(t *testing.T, n int, mode, dir, key string) []helperResult {
	t.Helper()
	results := make([]helperResult, n)
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = runHelper(t, mode, dir, key)
		}()
	}
	wg.Wait()
	return results
}

// runHelper runs one child process of this test binary.
func runHelper(t *testing.T, mode, dir, key string) helperResult {
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
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()

	value := ""
	for _, line := range strings.Split(string(out), "\n") {
		if _, rest, found := strings.Cut(line, "="); found {
			value = strings.TrimSpace(rest)
		}
	}
	return helperResult{value: value, stderr: stderr.String(), err: err}
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
