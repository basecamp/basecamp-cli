//go:build unix

package commands

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/connector"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// A second connector for an agent that is already running says so at once,
// even while the token endpoint is rate-limiting it: the lock is checked
// before the start waits for a token, not after.
func TestASecondConnectorIsRefusedBeforeItWaitsForAToken(t *testing.T) {
	f := newOperatorFixture(t)
	dir, err := connectStateDir(f.file, false)
	require.NoError(t, err)
	lock, err := connector.AcquireInstanceLock(dir, f.file.AccountID, f.file.Agent.PersonID, time.Now())
	require.NoError(t, err)
	t.Cleanup(func() { _ = lock.Release() })

	app := newConnectSetupApp(t, f.s, "agent")
	creds, err := app.Auth.GetStore().Load(app.Auth.CredentialKey())
	require.NoError(t, err)
	creds.ExpiresAt = time.Now().Add(-time.Minute).Unix()
	require.NoError(t, app.Auth.GetStore().Save(app.Auth.CredentialKey(), creds))
	f.s.mu.Lock()
	f.s.rateLimitMints, f.s.mints = true, 0
	f.s.mu.Unlock()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var buf bytes.Buffer
	app.Output = output.New(output.Options{Format: output.FormatJSON, Writer: &buf})
	cmd := NewConnectCmd()
	cmd.SetArgs(nil)
	cmd.SetContext(appctx.WithApp(ctx, app))
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	done := make(chan error, 1)
	go func() { done <- cmd.Execute() }()

	select {
	case err := <-done:
		refusal := usageError(t, err)
		assert.Equal(t, output.CodeLockUnavailable, refusal.Code, buf.String())
		assert.Contains(t, refusal.Message, fmt.Sprintf("held by pid %d", os.Getpid()), "the refusal names the holder")
		assert.Contains(t, refusal.Hint, "One connector serves every repo")
	case <-time.After(10 * time.Second):
		cancel()
		<-done
		t.Fatalf("a second connector waited for a token instead of refusing: %s", buf.String())
	}
	f.s.mu.Lock()
	defer f.s.mu.Unlock()
	assert.Zero(t, f.s.mints, "nothing was minted for a connector that cannot run")
}

// A start waiting for its token does not hold the agent's lock: until the
// credential is proven to be that agent, the lock is only looked at, so a
// profile whose credential is some other agent's cannot keep the real
// connector out while it waits.
func TestAStartWaitingForItsTokenHoldsNoLock(t *testing.T) {
	f := newOperatorFixture(t)
	app := newConnectSetupApp(t, f.s, "agent")
	creds, err := app.Auth.GetStore().Load(app.Auth.CredentialKey())
	require.NoError(t, err)
	creds.ExpiresAt = time.Now().Add(-time.Minute).Unix()
	require.NoError(t, app.Auth.GetStore().Save(app.Auth.CredentialKey(), creds))
	f.s.mu.Lock()
	f.s.rateLimitMints = true
	f.s.mu.Unlock()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var out lockedBuffer
	app.Output = output.New(output.Options{Format: output.FormatJSON, Writer: &out})
	cmd := NewConnectCmd()
	cmd.SetArgs(nil)
	cmd.SetContext(appctx.WithApp(ctx, app))
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	done := make(chan error, 1)
	go func() { done <- cmd.Execute() }()

	require.Eventually(t, func() bool { return strings.Contains(out.String(), "could not get the agent's token yet") },
		10*time.Second, 20*time.Millisecond, "the start never waited: %s", out.String())
	dir, err := connectStateDir(f.file, false)
	require.NoError(t, err)
	lock, err := connector.AcquireInstanceLock(dir, f.file.AccountID, f.file.Agent.PersonID, time.Now())
	require.NoError(t, err, "a waiting start held the agent's lock")
	require.NoError(t, lock.Release())

	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the waiting start did not stop when canceled")
	}
}
