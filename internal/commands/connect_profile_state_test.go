//go:build unix

package commands

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp/eventfeed"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/connector"
	"github.com/basecamp/basecamp-cli/internal/connector/setup"
	"github.com/basecamp/basecamp-cli/internal/output"
)

type runningConnector struct {
	out    *lockedBuffer
	done   chan error
	cancel context.CancelFunc
}

func startProfileConnector(t *testing.T, s *connectSetupServer, profile string) runningConnector {
	t.Helper()
	app := newConnectSetupApp(t, s, profile)
	ctx, cancel := context.WithCancel(t.Context())
	out := &lockedBuffer{}
	app.Output = output.New(output.Options{Format: output.FormatJSON, Writer: out})
	cmd := NewConnectCmd()
	cmd.SetArgs(nil)
	cmd.SetContext(appctx.WithApp(ctx, app))
	cmd.SetOut(out)
	cmd.SetErr(out)
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	r := runningConnector{out: out, done: make(chan error, 1), cancel: cancel}
	go func() { r.done <- cmd.Execute() }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-r.done:
		case <-time.After(10 * time.Second):
			t.Errorf("the connector for %s did not stop when canceled", profile)
		}
	})
	return r
}

func (r runningConnector) awaitRunning(t *testing.T) {
	t.Helper()
	deadline := time.After(15 * time.Second)
	for !strings.Contains(r.out.String(), "connector: running") {
		select {
		case err := <-r.done:
			r.done <- err
			t.Fatalf("the connector exited before it ran: %v\n%s", err, r.out.String())
		case <-deadline:
			t.Fatalf("the connector never ran: %s", r.out.String())
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func (r runningConnector) awaitExit(t *testing.T) error {
	t.Helper()
	select {
	case err := <-r.done:
		r.done <- err
		return err
	case <-time.After(15 * time.Second):
		t.Fatalf("the connector kept running: %s", r.out.String())
		return nil
	}
}

func setUpSecondProfile(t *testing.T, s *connectSetupServer, profile string) setup.File {
	t.Helper()
	out, err := runAgentConnect(t, newConnectSetupApp(t, s, profile))
	require.NoError(t, err, out)
	out, err = runConnectSetupCmd(t, newConnectSetupApp(t, s, profile), "--operator", fmt.Sprint(setupOperatorPerson), serveArg())
	require.NoError(t, err, out)
	file, err := setup.Load(connectSetupPath(t, profile))
	require.NoError(t, err)
	return file
}

func connectStateRoot(t *testing.T) string {
	t.Helper()
	root, err := connector.StateRoot()
	require.NoError(t, err)
	return root
}

func connectRunKey(t *testing.T, s *connectSetupServer, file setup.File) eventfeed.CheckpointKey {
	t.Helper()
	origin, err := eventfeed.CanonicalOrigin(s.srv.URL)
	require.NoError(t, err)
	filters := eventfeed.Filters{ExcludePerformers: []int64{file.Agent.PersonID}, ActorTypes: []string{"person"}}
	return eventfeed.CheckpointKey{Origin: origin, AccountID: file.AccountID,
		ConsumerNamespace: connectConsumerNamespace(file.Agent.PersonID, false), FilterKey: filters.FilterKey()}
}

const legacyPosition = "legacy-position-41"

func seedLegacyLedger(t *testing.T, s *connectSetupServer, file setup.File) string {
	t.Helper()
	stateHome, err := connectStateHome()
	require.NoError(t, err)
	legacy, err := ensurePrivateChain(stateHome, "basecamp", "connect", connector.LegacyStateDirName(file.AccountID, file.Agent.PersonID))
	require.NoError(t, err)
	l, err := connector.OpenLedger(filepath.Join(legacy, connector.LedgerFile))
	require.NoError(t, err)
	ctx := context.Background()
	_, err = l.RecordSeen(ctx, eventfeed.Event{ID: 41, Kind: "comment_created", EventType: "comment.created", Action: "created",
		CreatedAt: time.Now(), BucketID: setupProject, CreatorID: setupOperatorPerson, RecordingID: 77}, connector.LanePoll)
	require.NoError(t, err)
	require.NoError(t, l.Save(ctx, connectRunKey(t, s, file), legacyPosition))
	require.NoError(t, l.Close())
	return legacy
}

func assertLegacyLedgerKept(t *testing.T, dir string, s *connectSetupServer, file setup.File) {
	t.Helper()
	l, err := connector.OpenLedgerReadOnly(context.Background(), filepath.Join(dir, connector.LedgerFile))
	require.NoError(t, err)
	defer func() { _ = l.Close() }()
	position, ok, err := l.Load(context.Background(), connectRunKey(t, s, file))
	require.NoError(t, err)
	require.True(t, ok, "the cursor came with the directory")
	assert.Equal(t, legacyPosition, position)
	_, found, err := l.Get(context.Background(), 41)
	require.NoError(t, err)
	assert.True(t, found, "the ledger's records came with it")
}

func TestTwoProfilesForOneAgentEachRunAConnector(t *testing.T) {
	f := newOperatorFixture(t)
	other := setUpSecondProfile(t, f.s, "agent-b")
	require.Equal(t, f.file.Agent.PersonID, other.Agent.PersonID, "both profiles are the same agent")
	require.Equal(t, f.file.AccountID, other.AccountID)

	a := startProfileConnector(t, f.s, "agent")
	a.awaitRunning(t)
	b := startProfileConnector(t, f.s, "agent-b")
	b.awaitRunning(t)

	root := connectStateRoot(t)
	for _, profile := range []string{"agent", "agent-b"} {
		dir := filepath.Join(root, connector.StateDirName(f.file.AccountID, f.file.Agent.PersonID, profile))
		_, err := os.Lstat(filepath.Join(dir, connector.LedgerFile))
		require.NoError(t, err, "%s runs on a ledger of its own", profile)
		holder, ok := connector.InstanceHolder(dir, f.file.AccountID, f.file.Agent.PersonID)
		require.True(t, ok, "%s holds the lock in its own state directory", profile)
		assert.Equal(t, os.Getpid(), holder.PID)
	}

	again := startProfileConnector(t, f.s, "agent")
	err := again.awaitExit(t)
	require.Error(t, err)
	assert.Equal(t, output.CodeLockUnavailable, usageError(t, err).Code, again.out.String())
	assert.Contains(t, err.Error(), "already runs for this profile")
}

func TestARunAdoptsTheLegacyStateDirectoryWithItsCursor(t *testing.T) {
	f := newOperatorFixture(t)
	root := connectStateRoot(t)
	legacy := seedLegacyLedger(t, f.s, f.file)

	r := startProfileConnector(t, f.s, "agent")
	r.awaitRunning(t)
	require.Eventually(t, func() bool { return strings.Contains(r.out.String(), "resuming the feed from the stored position") },
		10*time.Second, 20*time.Millisecond, "the run did not resume from the old cursor: %s", r.out.String())

	dir := filepath.Join(root, connector.StateDirName(f.file.AccountID, f.file.Agent.PersonID, "agent"))
	assert.Contains(t, r.out.String(), "state="+dir)
	_, err := os.Lstat(legacy)
	assert.ErrorIs(t, err, os.ErrNotExist, "the old directory moved rather than being copied")
	r.cancel()
	_ = r.awaitExit(t)

	assertLegacyLedgerKept(t, dir, f.s, f.file)
}

func TestAStartRefusesWhileAnOlderConnectorHoldsTheLegacyDirectory(t *testing.T) {
	f := newOperatorFixture(t)
	root := connectStateRoot(t)
	legacy := seedLegacyLedger(t, f.s, f.file)
	lock, err := connector.AcquireInstanceLock(legacy, f.file.AccountID, f.file.Agent.PersonID, time.Now())
	require.NoError(t, err)
	t.Cleanup(func() { _ = lock.Release() })

	r := startProfileConnector(t, f.s, "agent")
	err = r.awaitExit(t)
	require.Error(t, err)
	assert.Equal(t, output.CodeLockUnavailable, usageError(t, err).Code, r.out.String())
	_, err = os.Lstat(filepath.Join(legacy, connector.LedgerFile))
	require.NoError(t, err, "a running older connector keeps its directory")
	_, err = os.Lstat(filepath.Join(root, connector.StateDirName(f.file.AccountID, f.file.Agent.PersonID, "agent")))
	assert.ErrorIs(t, err, os.ErrNotExist)
}

func TestOperatorCommandsReadTheLegacyDirectoryUntilARunAdoptsIt(t *testing.T) {
	f := newOperatorFixture(t)
	root := connectStateRoot(t)
	legacy := seedLegacyLedger(t, f.s, f.file)

	dir, err := connectStatePath("agent", f.file, false)
	require.NoError(t, err)
	assert.Equal(t, legacy, dir)
	out, err := f.run(t, output.FormatJSON, "status")
	require.NoError(t, err, out)
	assert.Contains(t, out, `"profile": "agent"`)
	_, err = os.Lstat(filepath.Join(root, connector.StateDirName(f.file.AccountID, f.file.Agent.PersonID, "agent")))
	assert.ErrorIs(t, err, os.ErrNotExist, "reading the state moves nothing")

	adopted, err := connectStateDir("agent", f.file, false)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(root, connector.StateDirName(f.file.AccountID, f.file.Agent.PersonID, "agent")), adopted)
	dir, err = connectStatePath("agent", f.file, false)
	require.NoError(t, err)
	assert.Equal(t, adopted, dir)

	other, err := connectStatePath("agent-b", f.file, false)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(root, connector.StateDirName(f.file.AccountID, f.file.Agent.PersonID, "agent-b")), other,
		"once adopted, the old directory is no other profile's")
}

func TestAProfileThatCannotProveItIsTheAgentLeavesTheLegacyDirectory(t *testing.T) {
	f := newOperatorFixture(t)
	legacy := seedLegacyLedger(t, f.s, f.file)
	app := newConnectSetupApp(t, f.s, "agent")
	creds, err := app.Auth.GetStore().Load(app.Auth.CredentialKey())
	require.NoError(t, err)
	creds.ExpiresAt = time.Now().Add(-time.Minute).Unix()
	require.NoError(t, app.Auth.GetStore().Save(app.Auth.CredentialKey(), creds))
	f.s.mu.Lock()
	f.s.refuseSecret = true
	f.s.mu.Unlock()

	r := startProfileConnector(t, f.s, "agent")
	err = r.awaitExit(t)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "disconnected", r.out.String())
	_, err = os.Lstat(filepath.Join(legacy, connector.LedgerFile))
	require.NoError(t, err, "the old directory waits for the profile that is the agent")
	_, err = os.Lstat(filepath.Join(connectStateRoot(t), connector.StateDirName(f.file.AccountID, f.file.Agent.PersonID, "agent")))
	assert.ErrorIs(t, err, os.ErrNotExist)
}
