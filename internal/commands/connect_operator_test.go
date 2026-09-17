package commands

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	_ "modernc.org/sqlite"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp/eventfeed"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/connector"
	"github.com/basecamp/basecamp-cli/internal/connector/admission"
	"github.com/basecamp/basecamp-cli/internal/connector/setup"
	"github.com/basecamp/basecamp-cli/internal/output"
)

const operatorSecretContent = "please-look-secret-instruction"

// operatorFixture is a set-up "agent" profile with its state under a temp
// XDG_STATE_HOME.
type operatorFixture struct {
	s    *connectSetupServer
	file setup.File
}

func newOperatorFixture(t *testing.T) operatorFixture {
	t.Helper()
	s := startConnectSetupServer(t)
	firstSetup(t, s)
	state := t.TempDir()
	require.NoError(t, os.Chmod(state, 0o700))
	t.Setenv("XDG_STATE_HOME", state)
	file, err := setup.Load(connectSetupPath(t, "agent"))
	require.NoError(t, err)
	return operatorFixture{s: s, file: file}
}

// ledger creates the connector's (or the shadow's) ledger with an admitted
// record 1 and a blocked record 2, and returns it open.
func (f operatorFixture) ledger(t *testing.T, shadow bool) *connector.Ledger {
	t.Helper()
	dir, err := connectStateDir(f.file, shadow)
	require.NoError(t, err)
	l, err := connector.OpenLedger(filepath.Join(dir, connector.LedgerFile))
	require.NoError(t, err)
	ctx := context.Background()
	for _, id := range []int64{1, 2} {
		_, err := l.RecordSeen(ctx, eventfeed.Event{ID: id, Kind: "comment_created", EventType: "comment.created", Action: "created",
			CreatedAt: time.Now(), BucketID: setupProject, CreatorID: setupOperatorPerson, RecordingID: 77}, connector.LanePoll)
		require.NoError(t, err)
	}
	_, err = l.Admission().Commit(ctx, admission.Verdict{
		EventID: 1, EventType: "comment.created", BucketID: setupProject, RecordingID: 77, RequesterID: setupOperatorPerson,
		State: admission.StateAdmitted, Trigger: admission.TriggerMentioned, Acknowledge: true, ConversationKey: "recording:70",
		Reply: &admission.ReplyDestination{Kind: admission.ReplyComment, RecordingID: 70}, Routed: true, Route: "/work/app",
		RecordingURL: "https://app.basecamp.com/999/buckets/1/recordings/77",
		Snapshot:     &admission.Snapshot{Type: "Comment", Content: operatorSecretContent, UpdatedAt: time.Now()},
	})
	require.NoError(t, err)
	_, err = l.Admission().Commit(ctx, admission.Verdict{
		EventID: 2, EventType: "comment.created", BucketID: setupProject, RecordingID: 77, RequesterID: setupOperatorPerson,
		State: admission.StateBlocked, Reason: admission.ReasonUnroutable,
	})
	require.NoError(t, err)
	return l
}

func (f operatorFixture) run(t *testing.T, format output.Format, args ...string) (string, error) {
	t.Helper()
	app := newConnectSetupApp(t, f.s, "agent")
	var buf bytes.Buffer
	app.Output = output.New(output.Options{Format: format, Writer: &buf})
	cmd := NewConnectCmd()
	cmd.SetArgs(args)
	cmd.SetContext(appctx.WithApp(context.Background(), app))
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	err := cmd.Execute()
	return buf.String(), err
}

func usageError(t *testing.T, err error) *output.Error {
	t.Helper()
	var e *output.Error
	require.True(t, errors.As(err, &e), "an output error, got %v", err)
	return e
}

func TestConnectStatusReadsWithoutWritingAndShowsNoContent(t *testing.T) {
	f := newOperatorFixture(t)

	_, err := f.run(t, output.FormatJSON, "status")
	require.Error(t, err)
	assert.Equal(t, output.CodeUsage, usageError(t, err).Code)
	state, _ := connectStateHome()
	_, statErr := os.Lstat(filepath.Join(state, "basecamp"))
	assert.ErrorIs(t, statErr, os.ErrNotExist, "status on no ledger creates nothing")

	l := f.ledger(t, false)
	_, err = l.SetHold(context.Background(), "local:tester", connector.HoldByOperator)
	require.NoError(t, err)
	require.NoError(t, l.Close())

	out, err := f.run(t, output.FormatJSON, "status")
	require.NoError(t, err, out)
	assert.NotContains(t, out, operatorSecretContent)
	var envelope struct {
		Data connectStatusReport `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &envelope), out)
	require.NotNil(t, envelope.Data.Status.Hold)
	require.Len(t, envelope.Data.Status.Held, 1)
	assert.Equal(t, int64(1), envelope.Data.Status.Held[0].EventID)

	styled, err := f.run(t, output.FormatStyled, "status")
	require.NoError(t, err)
	assert.Contains(t, styled, "Held records   1")
	assert.NotContains(t, styled, operatorSecretContent)
}

func TestConnectRedispatchDiscardAndRelease(t *testing.T) {
	f := newOperatorFixture(t)
	l := f.ledger(t, false)
	ctx := context.Background()
	_, err := l.SetHold(ctx, "local:tester", connector.HoldByOperator)
	require.NoError(t, err)
	require.NoError(t, l.Close())

	out, err := f.run(t, output.FormatJSON, "redispatch", "1")
	require.NoError(t, err, out)
	assert.Contains(t, out, "admitted")
	assert.Contains(t, out, "nothing launches until release")

	out, err = f.run(t, output.FormatJSON, "redispatch", "1")
	require.Error(t, err, out)
	assert.Equal(t, output.CodeUsage, usageError(t, err).Code, "an admitted record is live")

	out, err = f.run(t, output.FormatJSON, "discard", "2")
	require.NoError(t, err, out)
	out, err = f.run(t, output.FormatJSON, "redispatch", "2")
	require.Error(t, err, out)

	_, err = f.run(t, output.FormatJSON, "redispatch", "nope")
	require.Error(t, err)

	out, err = f.run(t, output.FormatJSON, "release")
	require.NoError(t, err, out)
	assert.Contains(t, out, "Released")

	dir, err := connectStatePath(f.file, false)
	require.NoError(t, err)
	l, err = connector.OpenLedgerReadOnly(context.Background(), filepath.Join(dir, connector.LedgerFile))
	require.NoError(t, err)
	defer func() { _ = l.Close() }()
	held, err := l.Held(ctx)
	require.NoError(t, err)
	assert.False(t, held)
	one, _, err := l.Get(ctx, 1)
	require.NoError(t, err)
	assert.Equal(t, connector.StateAdmitted, one.State)
	two, _, err := l.Get(ctx, 2)
	require.NoError(t, err)
	assert.Equal(t, connector.StateDiscarded, two.State)
	assert.Equal(t, connector.ReasonByOperator, two.Reason)
}

func TestConnectShadowPromoteAndImport(t *testing.T) {
	f := newOperatorFixture(t)
	require.NoError(t, f.ledger(t, true).Close())

	out, err := f.run(t, output.FormatJSON, "import", filepath.Join(t.TempDir(), "missing.json"))
	require.Error(t, err, out)

	out, err = f.run(t, output.FormatJSON, "shadow", "promote")
	require.NoError(t, err, out)
	assert.Contains(t, out, "Promoted under the hold")
	out, err = f.run(t, output.FormatJSON, "shadow", "promote")
	require.NoError(t, err, out)
	assert.Contains(t, out, "Already promoted")

	bad := filepath.Join(t.TempDir(), "bad.json")
	require.NoError(t, os.WriteFile(bad, []byte(`{"version":1,"entries":[{"event_id":2,"decision":"maybe"}]}`), 0o600))
	_, err = f.run(t, output.FormatJSON, "import", bad)
	require.Error(t, err)
	assert.Equal(t, output.CodeUsage, usageError(t, err).Code)

	dir, err := connectStatePath(f.file, false)
	require.NoError(t, err)
	lock, err := connector.AcquireInstanceLock(dir, f.file.AccountID, f.file.Agent.PersonID, time.Now())
	require.NoError(t, err)
	good := filepath.Join(t.TempDir(), "good.json")
	require.NoError(t, os.WriteFile(good, []byte(`{"version":1,"entries":[{"event_id":2,"decision":"done"}]}`), 0o600))
	_, err = f.run(t, output.FormatJSON, "import", good)
	require.Error(t, err, "a running connector refuses the import")
	assert.Equal(t, output.CodeLockUnavailable, usageError(t, err).Code)
	require.NoError(t, lock.Release())

	out, err = f.run(t, output.FormatJSON, "import", good)
	require.NoError(t, err, out)
	l, err := connector.OpenLedgerReadOnly(context.Background(), filepath.Join(dir, connector.LedgerFile))
	require.NoError(t, err)
	defer func() { _ = l.Close() }()
	two, _, err := l.Get(context.Background(), 2)
	require.NoError(t, err)
	assert.Equal(t, connector.StateDiscarded, two.State)
	one, _, err := l.Get(context.Background(), 1)
	require.NoError(t, err)
	assert.Equal(t, connector.StateHeld, one.State)
}

func TestConnectHoldFlagIsOnTheRunCommand(t *testing.T) {
	cmd := NewConnectCmd()
	require.NoError(t, cmd.Flags().Parse([]string{"--hold"}))
	v, err := cmd.Flags().GetBool("hold")
	require.NoError(t, err)
	assert.True(t, v)
}

func TestConnectDoctorWorkerBinaries(t *testing.T) {
	file := setup.New("agent")
	assert.Equal(t, []string{"claude"}, workerBinaries(file))
	assert.Empty(t, driverChecks(connectProfile{name: "agent", file: file}))
	file.Driver = setup.DriverACP
	assert.Equal(t, []string{"claude-agent-acp"}, workerBinaries(file))
	checks := driverChecks(connectProfile{name: "agent", file: file})
	require.Len(t, checks, 1)
	assert.Equal(t, setup.StatusFail, checks[0].Status, "a driver the run command refuses is not ready")
}

func TestConnectDoctorReportsLedgerGapsAndTheHold(t *testing.T) {
	f := newOperatorFixture(t)
	l := f.ledger(t, false)
	epoch := int64(500)
	_, err := l.RecordGap(context.Background(), connector.Gap{DetectedAt: time.Now(), Class: connector.GapEpoch, EpochAfterID: &epoch, EntryClass: connector.EntryPresent})
	require.NoError(t, err)
	_, err = l.SetHold(context.Background(), "local:tester", connector.HoldByOperator)
	require.NoError(t, err)
	require.NoError(t, l.Close())

	checks := ledgerChecks(context.Background(), connectProfile{name: "agent", file: f.file})
	byName := map[string]setup.Check{}
	for _, c := range checks {
		byName[c.Name] = c
	}
	assert.Equal(t, setup.StatusPass, byName["Ledger"].Status)
	assert.Contains(t, byName["Gap 1"].Message, "500")
	assert.Equal(t, setup.StatusWarn, byName["Hold"].Status)
}

// fakeMCPServerArg marks a test binary run as the doctor's MCP server.
const fakeMCPServerArg = "fake-basecamp-mcp"

// TestFakeMCPServer is not a test: doctor's handshake starts it. It lists one
// tool when its environment is the allowlist, and a second when a variable
// the allowlist excludes reached it.
func TestFakeMCPServer(t *testing.T) {
	if !strings.Contains(strings.Join(flag.Args(), " "), fakeMCPServerArg) {
		t.Skip("started by the doctor's handshake test")
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "fake", Version: "0"}, nil)
	type none struct{}
	handler := func(context.Context, *mcp.CallToolRequest, none) (*mcp.CallToolResult, none, error) {
		return &mcp.CallToolResult{}, none{}, nil
	}
	mcp.AddTool(server, &mcp.Tool{Name: "ok"}, handler)
	if os.Getenv("CONNECT_DOCTOR_LEAK_CHECK") != "" {
		mcp.AddTool(server, &mcp.Tool{Name: "leaked"}, handler)
	}
	_ = server.Run(context.Background(), &mcp.StdioTransport{})
	os.Exit(0)
}

func TestConnectDoctorMCPHandshakeRunsTheServerWithAnAllowlistedEnvironment(t *testing.T) {
	t.Setenv("CONNECT_DOCTOR_LEAK_CHECK", "not-a-real-secret")
	orig := mcpServerCommand
	mcpServerCommand = func(string) (string, []string, error) {
		return os.Args[0], []string{"-test.run=^TestFakeMCPServer$", "--", fakeMCPServerArg}, nil
	}
	t.Cleanup(func() { mcpServerCommand = orig })

	c := mcpHandshakeCheck(context.Background(), "agent")
	assert.Equal(t, setup.StatusPass, c.Status, c.Message)
	assert.Contains(t, c.Message, "1 tools", "only the allowlisted environment reached the server")
}

func TestOperatorCommandsDoNotMigrateUnderARunningConnector(t *testing.T) {
	f := newOperatorFixture(t)
	require.NoError(t, f.ledger(t, false).Close())
	dir, err := connectStatePath(f.file, false)
	require.NoError(t, err)

	// A ledger an older binary wrote: its last migration is not recorded.
	db, err := sql.Open("sqlite", filepath.Join(dir, connector.LedgerFile))
	require.NoError(t, err)
	_, err = db.ExecContext(context.Background(), `DELETE FROM schema_migrations WHERE version = (SELECT MAX(version) FROM schema_migrations)`)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	lock, err := connector.AcquireInstanceLock(dir, f.file.AccountID, f.file.Agent.PersonID, time.Now())
	require.NoError(t, err)
	defer func() { _ = lock.Release() }()

	_, err = f.run(t, output.FormatJSON, "release")
	require.Error(t, err)
	assert.Contains(t, usageError(t, err).Message, "older than this build")
}

// The migration guard is the lock itself, not the metadata beside it: a
// connector whose lock file carries nothing still stops the migration.
func TestTheMigrationGuardIsTheLockNotItsMetadata(t *testing.T) {
	f := newOperatorFixture(t)
	require.NoError(t, f.ledger(t, false).Close())
	dir, err := connectStatePath(f.file, false)
	require.NoError(t, err)
	db, err := sql.Open("sqlite", filepath.Join(dir, connector.LedgerFile))
	require.NoError(t, err)
	_, err = db.ExecContext(context.Background(), `DELETE FROM schema_migrations WHERE version = (SELECT MAX(version) FROM schema_migrations)`)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	lock, err := connector.AcquireInstanceLock(dir, f.file.AccountID, f.file.Agent.PersonID, time.Now())
	require.NoError(t, err)
	defer func() { _ = lock.Release() }()
	require.NoError(t, os.Remove(lock.Path()+".json"), "the holder's metadata is best-effort and may be missing")

	_, err = f.run(t, output.FormatJSON, "release")
	require.Error(t, err)
	assert.Contains(t, usageError(t, err).Message, "older than this build")
}

// Import takes the instance lock itself, so it does not refuse its own hold on
// a ledger older than this build — the cutover's whole reason to run.
func TestImportRunsOnALedgerOlderThanTheBuild(t *testing.T) {
	f := newOperatorFixture(t)
	require.NoError(t, f.ledger(t, false).Close())
	dir, err := connectStatePath(f.file, false)
	require.NoError(t, err)
	db, err := sql.Open("sqlite", filepath.Join(dir, connector.LedgerFile))
	require.NoError(t, err)
	_, err = db.ExecContext(context.Background(), `DELETE FROM schema_migrations WHERE version = (SELECT MAX(version) FROM schema_migrations)`)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	file := filepath.Join(t.TempDir(), "reconciliation.json")
	require.NoError(t, os.WriteFile(file, []byte(`{"version":1,"entries":[{"event_id":2,"decision":"done"}]}`), 0o600))
	// The fabricated ledger cannot actually migrate (its tables are already
	// there), so the migration's own error is the end of this run. What
	// matters is what it is not: import must never refuse itself over the
	// lock it holds.
	_, err = f.run(t, output.FormatJSON, "import", file)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "already holds this account")
	assert.NotContains(t, err.Error(), "Stop the connector")
}
