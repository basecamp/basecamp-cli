//go:build unix

package commands

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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
	"github.com/basecamp/basecamp-cli/internal/connector/driver/acp"
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

// doctor refuses what the run command refuses, and nothing the run command
// runs. Both the acp driver and worktrees are on the run command now, so
// neither is a failing check any more.
func TestConnectDoctorRefusesOnlyWhatTheRunCommandRefuses(t *testing.T) {
	file := setup.New("agent")
	assert.Empty(t, driverChecks(connectProfile{name: "agent", file: file}))

	acpFile := setup.New("agent")
	acpFile.Driver = setup.DriverACP
	assert.Empty(t, driverChecks(connectProfile{name: "agent", file: acpFile}),
		"the acp driver is a driver the connector starts on")

	worktrees := setup.New("agent")
	worktrees.Worktrees = true
	assert.Empty(t, driverChecks(connectProfile{name: "agent", file: worktrees}),
		"the connector starts with worktrees on")

	unknown := setup.New("agent")
	unknown.Driver = "someday"
	checks := driverChecks(connectProfile{name: "agent", file: unknown})
	require.Len(t, checks, 1)
	assert.Equal(t, "Driver", checks[0].Name)
	assert.Equal(t, setup.StatusFail, checks[0].Status)
}

// Copilot, on #748: on macOS the Platform check said the connector cannot run
// there and then named a capability macOS has, so the failure contradicted
// itself and sent a Mac reader after the wrong thing. The constraint that
// actually applies is #736's: the task token reaches a worker's MCP server
// over an inherited descriptor, and Linux alone seals the descriptors a
// process passes on. Reading a process's start time is the half macOS has.
//
// The check and the run command's refusal say the one reason, so a person
// cannot be told two different things about the same platform.
func TestConnectDoctorPlatformCheckNamesTheConstraintThatApplies(t *testing.T) {
	c := connectUnsupportedOSCheck("darwin")
	assert.Equal(t, "Platform", c.Name)
	assert.Equal(t, setup.StatusFail, c.Status)
	assert.Contains(t, c.Message, "darwin", "it names the platform it refuses")
	assert.Contains(t, c.Message, connectLinuxOnlyReason, "it gives the reason the run command gives")
	assert.NotContains(t, c.Message, "macOS",
		"a refusal must not name a capability the platform it refuses actually has")

	err := connectUnsupportedOSError("darwin")
	require.Error(t, err)
	assert.Contains(t, err.Error(), connectLinuxOnlyReason, "doctor and the run command give the one reason")
	assert.NotContains(t, err.Error(), "macOS")
}

// The acp driver runs a pinned adapter out of the connector's own npm
// prefix, never one on PATH: doctor resolves it the way the driver does, so
// a documented install passes and an unpinned build on PATH does not.
func TestConnectDoctorFindsTheACPAdapterWhereTheDriverDoes(t *testing.T) {
	data := t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)

	// A decoy on PATH, which is not what the acp driver would run.
	decoy := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(decoy, "claude-agent-acp"), []byte("#!/bin/sh\nexit 0\n"), 0o700))
	t.Setenv("PATH", decoy)

	file := setup.New("agent")
	file.Driver = setup.DriverACP
	checks := workerBinaryChecks(context.Background(), file)
	require.Len(t, checks, 1)
	assert.Equal(t, setup.StatusFail, checks[0].Status,
		"an unpinned executable that happens to be on PATH is not the pinned adapter")
	assert.Contains(t, checks[0].Hint, "make acp-adapters")

	// The adapters directory make acp-adapters writes.
	adapter, ok := acp.AdapterForWorker(setup.WorkerClaude)
	require.True(t, ok)
	prefix := filepath.Join(data, "basecamp", "acp-adapters")
	pkgDir := filepath.Join(prefix, "node_modules", filepath.FromSlash(adapter.Package))
	require.NoError(t, os.MkdirAll(pkgDir, 0o755))
	manifest := fmt.Sprintf(`{"name":%q,"version":%q}`, adapter.Package, adapter.Version)
	require.NoError(t, os.WriteFile(filepath.Join(pkgDir, "package.json"), []byte(manifest), 0o600))
	binDir := filepath.Join(prefix, "node_modules", ".bin")
	require.NoError(t, os.MkdirAll(binDir, 0o755))
	bin := filepath.Join(binDir, adapter.Name)
	require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o700))

	checks = workerBinaryChecks(context.Background(), file)
	require.Len(t, checks, 1)
	assert.Equal(t, setup.StatusPass, checks[0].Status, "the adapter make acp-adapters installed is the one doctor finds")
	assert.Contains(t, checks[0].Message, bin)
	assert.Contains(t, checks[0].Message, adapter.Version)

	// A version other than the pin is not the adapter the driver would run.
	require.NoError(t, os.WriteFile(filepath.Join(pkgDir, "package.json"),
		[]byte(fmt.Sprintf(`{"name":%q,"version":"0.0.1-not-the-pin"}`, adapter.Package)), 0o600))
	checks = workerBinaryChecks(context.Background(), file)
	require.Len(t, checks, 1)
	assert.Equal(t, setup.StatusFail, checks[0].Status, "an adapter off the pin is not ready")
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

// doctor is the other place a person looks. A waiting route warns: it is a
// machine a person must fix, but the connector is still trying it, so calling
// it a failure would exit non-zero on a connector that is running correctly.
func TestConnectDoctorWarnsAboutRoutesWaitingForAWorktree(t *testing.T) {
	ctx := context.Background()
	f := newOperatorFixture(t)
	l := f.ledger(t, false)
	first := time.Date(2026, 9, 18, 6, 0, 0, 0, time.UTC)
	require.NoError(t, l.RecordRouteWait(ctx, connector.RouteWait{
		Route: "/work/app", Failures: 14, Reason: "git rev-parse: detected dubious ownership in repository",
		FirstAt: first, LastAt: first.Add(6 * time.Hour), Until: first.Add(6*time.Hour + 30*time.Minute),
	}))
	require.NoError(t, l.RecordRouteWait(ctx, connector.RouteWait{
		Route: "/work/other", Failures: 2, FirstAt: first.Add(time.Hour), LastAt: first.Add(2 * time.Hour), Until: first.Add(3 * time.Hour),
	}))
	require.NoError(t, l.Close())

	byName := map[string]setup.Check{}
	for _, c := range ledgerChecks(ctx, connectProfile{name: "agent", file: f.file}) {
		byName[c.Name] = c
	}
	require.Contains(t, byName, "Waiting routes")
	assert.Equal(t, setup.StatusWarn, byName["Waiting routes"].Status, "a route waiting is not a route broken")
	assert.Contains(t, byName["Waiting routes"].Message, "/work/app: 14 failed attempts at a worktree since 2026-09-18T06:00:00Z")
	assert.Contains(t, byName["Waiting routes"].Message, "dubious ownership")
	assert.Contains(t, byName["Waiting routes"].Message, "and 1 other route(s)")
	assert.Contains(t, byName["Waiting routes"].Message, "the connector keeps trying and refuses nothing")
	assert.Contains(t, byName["Waiting routes"].Hint, "basecamp connect status -P agent")
}

// doctor reads card 19's worktree ledger too: worktrees the connector kept
// are a person's to deal with, and doctor is where a person finds out.
func TestConnectDoctorReportsTheWorktreesTheConnectorKept(t *testing.T) {
	ctx := context.Background()
	f := newOperatorFixture(t)
	l := f.ledger(t, false)
	id, err := l.BeginWorktree(ctx, connector.Worktree{
		Path: "/w/one", WorkDir: "/w/one/app", Route: "app",
		Repository: "/repo", Branch: "basecamp-connect/1-a1b2c3", BaseCommit: "abc",
	})
	require.NoError(t, err)
	require.NoError(t, l.MoveWorktree(ctx, id, connector.WorktreeLive, connector.WorktreeCreating))
	require.NoError(t, l.RetainWorktree(ctx, id, connector.RetainedDirty, connector.WorktreeLive))
	require.NoError(t, l.Close())

	byName := map[string]setup.Check{}
	for _, c := range ledgerChecks(ctx, connectProfile{name: "agent", file: f.file}) {
		byName[c.Name] = c
	}
	require.Contains(t, byName, "Worktrees")
	assert.Equal(t, setup.StatusWarn, byName["Worktrees"].Status)
	assert.Contains(t, byName["Worktrees"].Message, "1 worktree(s) kept")
	assert.Contains(t, byName["Worktrees"].Hint, "basecamp connect worktrees list")
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
	for _, arg := range flag.Args() {
		if pidFile, ok := strings.CutPrefix(arg, "spawn-child="); ok {
			// A server that starts a descendant in its group and then hangs
			// without ever answering the handshake.
			child := exec.CommandContext(context.Background(), "/bin/sleep", "300")
			if err := child.Start(); err != nil {
				os.Exit(2)
			}
			_ = os.WriteFile(pidFile, []byte(strconv.Itoa(child.Process.Pid)), 0o600)
			select {}
		}
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

func TestConnectStatusOnAMissingShadowLedgerPointsAtTheShadowRun(t *testing.T) {
	f := newOperatorFixture(t)
	_, err := f.run(t, output.FormatJSON, "status", "--shadow")
	require.Error(t, err)
	assert.Contains(t, usageError(t, err).Hint, "--shadow")
}

// A token in the environment would decide a record's prerequisite as somebody
// other than the agent, so redispatch and doctor refuse before the ledger is
// touched.
func TestRedispatchAndDoctorRefuseAShadowingToken(t *testing.T) {
	f := newOperatorFixture(t)
	l := f.ledger(t, false)
	require.NoError(t, l.Close())
	t.Setenv("BASECAMP_TOKEN", "not-a-real-token")

	for _, args := range [][]string{{"redispatch", "2"}, {"doctor"}} {
		_, err := f.run(t, output.FormatJSON, args...)
		require.Error(t, err, args[0])
		assert.Contains(t, err.Error(), "BASECAMP_TOKEN", args[0])
	}
	dir, err := connectStatePath(f.file, false)
	require.NoError(t, err)
	db, err := sql.Open("sqlite", filepath.Join(dir, connector.LedgerFile))
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	var decisions int
	require.NoError(t, db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM decisions`).Scan(&decisions))
	assert.Zero(t, decisions, "nothing was decided")
}

// The decision commands' JSON is the CLI's snake_case, as status's is.
func TestTheDecisionCommandsSpeakSnakeCase(t *testing.T) {
	f := newOperatorFixture(t)
	l := f.ledger(t, false)
	_, err := l.SetHold(context.Background(), "local:tester", connector.HoldByOperator)
	require.NoError(t, err)
	require.NoError(t, l.Close())

	out, err := f.run(t, output.FormatJSON, "redispatch", "1")
	require.NoError(t, err, out)
	assert.Contains(t, out, `"event_id"`)
	assert.NotContains(t, out, `"EventID"`)

	out, err = f.run(t, output.FormatJSON, "release")
	require.NoError(t, err, out)
	assert.Contains(t, out, `"still_held"`)
	assert.NotContains(t, out, `"StillHeld"`)
}

// A route that could not take a worktree holds its records back, and until
// now that was visible only in the connector's log. Status is one of the two
// places a person looks.
func TestStatusReportsTheRoutesWaitingForAWorktree(t *testing.T) {
	ctx := context.Background()
	f := newOperatorFixture(t)
	l := f.ledger(t, false)
	first := time.Date(2026, 9, 18, 6, 0, 0, 0, time.UTC)
	require.NoError(t, l.RecordRouteWait(ctx, connector.RouteWait{
		Route: "/work/app", Failures: 14, Reason: "connector: route /work/app is not in a git repository: git rev-parse: exit status 128",
		FirstAt: first, LastAt: first.Add(6 * time.Hour), Until: time.Now().Add(20 * time.Minute),
	}))
	require.NoError(t, l.Close())

	styled, err := f.run(t, output.FormatStyled, "status")
	require.NoError(t, err, styled)
	assert.Contains(t, styled, "Waiting routes 1 (no worktree could be made; their records wait, and the connector keeps trying)")
	assert.Contains(t, styled, "/work/app  14 failures since 2026-09-18 06:00:00Z, next try ")
	assert.Contains(t, styled, "is not in a git repository")

	out, err := f.run(t, output.FormatJSON, "status")
	require.NoError(t, err, out)
	assert.Contains(t, out, `"waiting_routes"`)
	assert.Contains(t, out, `"failures": 14`)
	assert.Contains(t, out, "1 routes waiting for a worktree")
}

// A status with nothing waiting says so, rather than leaving a reader to
// guess: a route waiting forever and a route with no work on it looked
// identical, which is the whole complaint.
func TestStatusSaysNoRouteIsWaiting(t *testing.T) {
	var buf bytes.Buffer
	renderConnectStatus(&buf, connectStatusReport{Profile: "agent", Status: connector.Status{
		Queues: map[string]int{}, Blocked: map[string]int{},
	}})
	assert.Contains(t, buf.String(), "Waiting routes 0 ")
}

// Status reads card 19's worktree ledger: the worktrees the connector kept
// are what it reports, with why each is kept.
func TestStatusReportsTheWorktreesTheConnectorKept(t *testing.T) {
	ctx := context.Background()
	f := newOperatorFixture(t)
	l := f.ledger(t, false)
	id, err := l.BeginWorktree(ctx, connector.Worktree{
		Path: "/w/one", WorkDir: "/w/one/app", Route: "app",
		Repository: "/repo", Branch: "basecamp-connect/1-a1b2c3", BaseCommit: "abc",
	})
	require.NoError(t, err)
	require.NoError(t, l.MoveWorktree(ctx, id, connector.WorktreeLive, connector.WorktreeCreating))
	require.NoError(t, l.RetainWorktree(ctx, id, connector.RetainedDirty, connector.WorktreeLive))
	require.NoError(t, l.Close())

	styled, err := f.run(t, output.FormatStyled, "status")
	require.NoError(t, err, styled)
	assert.Contains(t, styled, "Worktrees      1 retained")
	assert.Contains(t, styled, "/w/one dirty")
	assert.NotContains(t, styled, "Worktrees      unavailable")

	out, err := f.run(t, output.FormatJSON, "status")
	require.NoError(t, err, out)
	assert.Contains(t, out, `"worktrees_known": true`)
	assert.Contains(t, out, `"reason": "dirty"`)
}

// A listing that could not be read is unavailable, never none: the
// distinction the nil lister carried is now what a failed listing carries.
func TestStatusSaysWorktreesAreUnavailableNotNone(t *testing.T) {
	var buf bytes.Buffer
	renderConnectStatus(&buf, connectStatusReport{Profile: "agent", Status: connector.Status{
		Queues: map[string]int{}, Blocked: map[string]int{},
		WorktreesUnavailable: "the worktrees table cannot be read",
	}})
	assert.Contains(t, buf.String(), "Worktrees      unavailable: the worktrees table cannot be read")
	assert.NotContains(t, buf.String(), "0 retained")
}

// preflightCheck is the row acpPreflightCheck adds to doctor's worker
// checks, found by the name a person reads rather than by position.
func preflightCheck(t *testing.T, checks []setup.Check) (setup.Check, bool) {
	t.Helper()
	for _, c := range checks {
		if strings.HasSuffix(c.Name, " preflight") {
			return c, true
		}
	}
	return setup.Check{}, false
}

// codexProfile is an acp/codex profile whose routes are dirs under one root,
// with HOME (and so ~/.codex) pointed at a home of its own: the machine
// state the Codex preflight reads, and nothing of the person running the
// test.
func codexProfile(t *testing.T, routes int) (setup.File, string, []string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "")
	file := setup.New("agent")
	file.Driver = setup.DriverACP
	file.Worker = setup.WorkerCodex
	paths := make([]string, 0, routes)
	for i := range routes {
		dir := filepath.Join(t.TempDir(), "repo")
		require.NoError(t, os.MkdirAll(dir, 0o755))
		file.Projects[int64(i+1)] = admission.Route{Path: dir}
		paths = append(paths, dir)
	}
	return file, home, paths
}

func writeCodexConfig(t *testing.T, dir, body string) string {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".codex"), 0o755))
	path := filepath.Join(dir, ".codex", "config.toml")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	return path
}

// The acp driver refuses a session before it starts anything when a Codex
// config layer declares MCP servers of its own, so every dispatch on such a
// machine is blocked with a notice on the card. Doctor runs that same
// refusal: a profile whose adapter is installed and on the pin is not ready
// if no session it would start could run.
func TestConnectDoctorRunsTheAdaptersPreflightAgainstEveryRoutedDirectory(t *testing.T) {
	file, home, _ := codexProfile(t, 2)

	checks := workerBinaryChecks(context.Background(), file)
	c, ok := preflightCheck(t, checks)
	require.True(t, ok, "the codex adapter's preflight is a check of its own")
	assert.Equal(t, setup.StatusPass, c.Status)
	assert.Contains(t, c.Message, "2 checked", "it says how many routed directories it ran in")

	// The user's own layer: shared by every route, and the one anybody who
	// uses Codex with MCP servers at all has.
	userConfig := writeCodexConfig(t, home, "[mcp_servers.linear]\ncommand = \"linear-mcp\"\n")

	c, ok = preflightCheck(t, workerBinaryChecks(context.Background(), file))
	require.True(t, ok)
	assert.Equal(t, setup.StatusFail, c.Status, "doctor never calls a profile ready that would not start")
	assert.Contains(t, c.Message, userConfig, "the person reading this has to know which file")
	assert.Contains(t, c.Message, "any routed directory", "one reason every route shares is reported once")
	assert.Equal(t, 1, strings.Count(c.Message, userConfig), "and named once, not once per route")
	assert.Contains(t, c.Hint, "CODEX_HOME", "and what to do about it")

	// And that is what the command exits with: the file is on the error a
	// person sees, not only in the styled table.
	err := doctorNotReady(append(append([]setup.Check{}, checks...), c))
	require.Error(t, err)
	assert.Contains(t, err.Error(), userConfig)
}

// One route can carry a project layer another has not, so the check runs in
// every routed directory and reports every route that would not start, not
// the first.
func TestConnectDoctorPreflightNamesEveryRouteThatWouldNotStart(t *testing.T) {
	file, _, paths := codexProfile(t, 3)
	writeCodexConfig(t, paths[1], "[mcp_servers.linear]\ncommand = \"linear-mcp\"\n")
	writeCodexConfig(t, paths[2], "[mcp_servers.other]\ncommand = \"other-mcp\"\n")

	c, ok := preflightCheck(t, workerBinaryChecks(context.Background(), file))
	require.True(t, ok)
	assert.Equal(t, setup.StatusFail, c.Status)
	assert.Contains(t, c.Message, filepath.Join(paths[1], ".codex", "config.toml"))
	assert.Contains(t, c.Message, filepath.Join(paths[2], ".codex", "config.toml"),
		"the second route's own layer is reported too, not only the first's")
	assert.NotContains(t, c.Message, "any routed directory", "the clean route is not called blocked")
	assert.NotContains(t, c.Message, filepath.Join(paths[0], ".codex"), "the route that would start is not named")
}

// With no route there is no directory a session would run in, and nothing
// dispatches anyway: the check says so rather than passing on a machine it
// never looked at.
func TestConnectDoctorPreflightSkipsWithNoRoute(t *testing.T) {
	file, _, _ := codexProfile(t, 0)
	c, ok := preflightCheck(t, workerBinaryChecks(context.Background(), file))
	require.True(t, ok)
	assert.Equal(t, setup.StatusSkip, c.Status)
	assert.Contains(t, c.Message, "No project is routed")
}

// claude-agent-acp has nothing on this machine to refuse a session over, so
// it gets no row: a check that does not exist must not report that it
// passed.
func TestConnectDoctorHasNoPreflightRowForAnAdapterWithoutOne(t *testing.T) {
	file := setup.New("agent")
	file.Driver = setup.DriverACP
	file.Worker = setup.WorkerClaude
	file.Projects[1] = admission.Route{Path: t.TempDir()}
	_, ok := preflightCheck(t, workerBinaryChecks(context.Background(), file))
	assert.False(t, ok)
}

// The spawn driver runs the worker's own CLI with the host's configuration
// switched off on the command line (codex --ignore-user-config, claude
// --setting-sources ""), so there is no machine-configuration refusal for
// doctor to run there.
func TestConnectDoctorHasNoPreflightRowUnderTheSpawnDriver(t *testing.T) {
	file := setup.New("agent")
	file.Driver = setup.DriverSpawn
	file.Worker = setup.WorkerCodex
	file.Projects[1] = admission.Route{Path: t.TempDir()}
	_, ok := preflightCheck(t, workerBinaryChecks(context.Background(), file))
	assert.False(t, ok)
}

// With one route there is no "every route" to speak of: the message names
// the directory, because a person with one route reads the path, not a
// quantifier over it.
func TestConnectDoctorPreflightNamesTheOnlyRoute(t *testing.T) {
	file, home, paths := codexProfile(t, 1)
	writeCodexConfig(t, home, "[mcp_servers.linear]\ncommand = \"linear-mcp\"\n")

	c, ok := preflightCheck(t, workerBinaryChecks(context.Background(), file))
	require.True(t, ok)
	assert.Equal(t, setup.StatusFail, c.Status)
	assert.Contains(t, c.Message, paths[0])
	assert.NotContains(t, c.Message, "any routed directory")
}

// codexWorktreeProfile is an acp/codex profile with worktrees on and one
// route inside a git repository, with its own HOME and state home: the
// machine state the Codex preflight reads, and nothing of the person
// running the test. It answers the route, the repository and the connector
// state directory the worktrees would go under.
func codexWorktreeProfile(t *testing.T) (setup.File, string, string, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	home := t.TempDir()
	state := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("CODEX_HOME", "")

	repo := filepath.Join(t.TempDir(), "repo")
	route := filepath.Join(repo, "app")
	require.NoError(t, os.MkdirAll(route, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(route, "README"), []byte("hi\n"), 0o600))
	testGit(t, repo, home, "init", "-q", "-b", "main")
	testGit(t, repo, home, "add", ".")
	testGit(t, repo, home, "commit", "-q", "-m", "init")

	file := setup.New("agent")
	file.Driver = setup.DriverACP
	file.Worker = setup.WorkerCodex
	file.Worktrees = true
	file.Projects[1] = admission.Route{Path: route}

	stateDir, err := connectStatePath(file, false)
	require.NoError(t, err)
	return file, route, repo, stateDir
}

func testGit(t *testing.T, dir, home string, args ...string) {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "git",
		append([]string{"-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	cmd.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), "GIT_CONFIG_NOSYSTEM=1"}
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
}

// With worktrees on the session never runs in the route: it runs in a
// worktree under the connector's state directory. A Codex config layer the
// route has but the repository does not track is in no worktree, so it
// blocks nothing and doctor must not refuse the profile over it.
func TestConnectDoctorPreflightDoesNotRefuseARouteFileNoWorktreeWouldHave(t *testing.T) {
	file, route, _, _ := codexWorktreeProfile(t)
	writeCodexConfig(t, route, "[mcp_servers.linear]\ncommand = \"linear-mcp\"\n")

	c, ok := preflightCheck(t, workerBinaryChecks(context.Background(), file))
	require.True(t, ok)
	assert.Equal(t, setup.StatusPass, c.Status,
		"an untracked config in the route is in no worktree, so no dispatch reads it")
}

// And a layer above the worktrees — under the connector's state directory,
// which is above every worktree and above no route — blocks every dispatch.
// Checking the route would miss it, which is this card's bug again.
func TestConnectDoctorPreflightReadsTheLayersAboveTheWorktree(t *testing.T) {
	file, _, _, stateDir := codexWorktreeProfile(t)
	require.NoError(t, os.MkdirAll(stateDir, 0o700))
	config := writeCodexConfig(t, stateDir, "[mcp_servers.linear]\ncommand = \"linear-mcp\"\n")

	c, ok := preflightCheck(t, workerBinaryChecks(context.Background(), file))
	require.True(t, ok)
	assert.Equal(t, setup.StatusFail, c.Status, "a layer above every worktree blocks every dispatch")
	assert.Contains(t, c.Message, config)
}

// A config the repository tracks is in every worktree made from it, so it
// does block every dispatch — and the message names the file where a person
// can change it, not a worktree nobody has made.
func TestConnectDoctorPreflightNamesTheRepositoryForACommittedConfig(t *testing.T) {
	file, route, repo, stateDir := codexWorktreeProfile(t)
	committed := writeCodexConfig(t, route, "[mcp_servers.linear]\ncommand = \"linear-mcp\"\n")
	testGit(t, repo, os.Getenv("HOME"), "add", ".")
	testGit(t, repo, os.Getenv("HOME"), "commit", "-q", "-m", "codex config")

	c, ok := preflightCheck(t, workerBinaryChecks(context.Background(), file))
	require.True(t, ok)
	assert.Equal(t, setup.StatusFail, c.Status)
	assert.Contains(t, c.Message, committed, "the file a person can go and change")
	assert.Contains(t, c.Message, "committed in the repository")
	assert.NotContains(t, c.Message, stateDir, "not a path inside a worktree that does not exist")
	assert.Contains(t, c.Hint, "CODEX_HOME", "renaming the file in the message does not lose what the refusal is")
}

// With worktrees on, a route that can take no worktree takes no task: every
// dispatch on it waits in a backoff nothing reports. Doctor says so instead
// of calling the profile ready.
func TestConnectDoctorPreflightRefusesARouteThatCouldTakeNoWorktree(t *testing.T) {
	file, route, _, _ := codexWorktreeProfile(t)
	outside := filepath.Join(t.TempDir(), "not-a-repo")
	require.NoError(t, os.MkdirAll(outside, 0o755))
	file.Projects[2] = admission.Route{Path: outside}

	c, ok := preflightCheck(t, workerBinaryChecks(context.Background(), file))
	require.True(t, ok)
	assert.Equal(t, setup.StatusFail, c.Status)
	assert.Contains(t, c.Message, outside)
	assert.Contains(t, c.Message, "would get a working directory")
	assert.NotContains(t, c.Message, route, "the route that would run is not named")
	assert.Contains(t, c.Hint, "without worktrees", "a route that cannot take a worktree is not fixed by editing a Codex config")
	assert.Contains(t, c.Message, "no worktree can be made on this route",
		"and it is the route itself, proved, not a read that failed this once")
}

// A config layer that cannot be read refuses every session too — nothing
// can say it declares no MCP server — but it is not a declaration, and
// telling a person to take mcp_servers out of a file they cannot read is
// not a remedy.
func TestConnectDoctorPreflightSaysWhatToDoAboutAnUnreadableConfig(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a file whatever its mode says")
	}
	file, home, _ := codexProfile(t, 1)
	config := writeCodexConfig(t, home, "model = \"gpt-5\"\n")
	require.NoError(t, os.Chmod(config, 0o000))
	t.Cleanup(func() { _ = os.Chmod(config, 0o600) })

	c, ok := preflightCheck(t, workerBinaryChecks(context.Background(), file))
	require.True(t, ok)
	assert.Equal(t, setup.StatusFail, c.Status, "what cannot be read cannot be vouched for")
	assert.Contains(t, c.Message, config)
	assert.Contains(t, c.Message, "cannot be read")
	assert.NotContains(t, c.Message, "declares MCP servers of its own",
		"a file nobody could read is not a file that declares them")
	assert.Contains(t, c.Hint, "readable")
	assert.NotContains(t, c.Hint, "Take the MCP servers out",
		"there are no MCP servers to take out of a file nothing has read")
}

// And one run names every layer a person has to change, not the first: the
// shared layers are read before a route's own, so stopping at the first
// would hide the route's until the shared one was fixed and doctor run
// again.
func TestConnectDoctorPreflightNamesEveryLayerInOneRun(t *testing.T) {
	file, home, paths := codexProfile(t, 1)
	user := writeCodexConfig(t, home, "[mcp_servers.linear]\ncommand = \"linear-mcp\"\n")
	project := writeCodexConfig(t, paths[0], "[mcp_servers.other]\ncommand = \"other-mcp\"\n")

	c, ok := preflightCheck(t, workerBinaryChecks(context.Background(), file))
	require.True(t, ok)
	assert.Equal(t, setup.StatusFail, c.Status)
	assert.Contains(t, c.Message, user)
	assert.Contains(t, c.Message, project, "the route's own layer is named in the same run as the shared one")
}

// A Codex config that is a symbolic link is a layer doctor does not model:
// git hands back the link's target text where the session would read the
// file it points at. What it must not do is read that text, find no
// mcp_servers in it, and call the profile ready — a layer nothing read is
// said as one, and the profile is not ready on the strength of it.
func TestConnectDoctorPreflightSaysWhenALayerCouldNotBeChecked(t *testing.T) {
	file, route, repo, _ := codexWorktreeProfile(t)
	require.NoError(t, os.WriteFile(filepath.Join(repo, "codex.toml"), []byte("[mcp_servers.linear]\ncommand = \"linear-mcp\"\n"), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(route, ".codex"), 0o755))
	require.NoError(t, os.Symlink("../../codex.toml", filepath.Join(route, ".codex", "config.toml")))
	testGit(t, repo, os.Getenv("HOME"), "add", ".")
	testGit(t, repo, os.Getenv("HOME"), "commit", "-q", "-m", "a linked codex config")

	c, ok := preflightCheck(t, workerBinaryChecks(context.Background(), file))
	require.True(t, ok)
	assert.Equal(t, setup.StatusFail, c.Status, "a layer nothing read is not a layer that passed")
	assert.Contains(t, c.Message, "could not check")
	assert.Contains(t, c.Message, filepath.Join(route, ".codex", "config.toml"), "the layer is named where a person can look at it")
	assert.Contains(t, c.Message, "a symbolic link", "and what is in the way is said")
	assert.Contains(t, c.Hint, "yourself")
	assert.Contains(t, c.Hint, "without worktrees")
}

// A route git could read as an option or a pattern is a route like any
// other: doctor must not report it as a configuration it cannot read.
func TestConnectDoctorPreflightReadsARouteNamedLikeAnOption(t *testing.T) {
	file, _, repo, _ := codexWorktreeProfile(t)
	dashed := filepath.Join(repo, "-app")
	require.NoError(t, os.MkdirAll(dashed, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dashed, "README"), []byte("hi\n"), 0o600))
	testGit(t, repo, os.Getenv("HOME"), "add", ".")
	testGit(t, repo, os.Getenv("HOME"), "commit", "-q", "-m", "a directory named like an option")
	file.Projects[2] = admission.Route{Path: dashed}

	c, ok := preflightCheck(t, workerBinaryChecks(context.Background(), file))
	require.True(t, ok)
	assert.Equal(t, setup.StatusPass, c.Status, "nothing in that repository refuses a session")
}

// A layer every route shares is reported once for all of them even when one
// route has a second reason of its own: they are grouped one refusal at a
// time, not by the whole of what a route was refused for.
func TestConnectDoctorPreflightGroupsEachRefusalOnItsOwn(t *testing.T) {
	file, home, paths := codexProfile(t, 2)
	user := writeCodexConfig(t, home, "[mcp_servers.linear]\ncommand = \"linear-mcp\"\n")
	project := writeCodexConfig(t, paths[1], "[mcp_servers.other]\ncommand = \"other-mcp\"\n")

	c, ok := preflightCheck(t, workerBinaryChecks(context.Background(), file))
	require.True(t, ok)
	assert.Equal(t, setup.StatusFail, c.Status)
	assert.Equal(t, 1, strings.Count(c.Message, user), "the shared layer is named once")
	assert.Contains(t, c.Message, "any routed directory", "and named as every route's")
	assert.Equal(t, 1, strings.Count(c.Message, project), "the second route's own layer is named too, once")
	assert.Contains(t, c.Message, "start in "+paths[1]+":", "and named as that route's alone")
	assert.NotContains(t, c.Message, "start in "+paths[0], "the route with only the shared reason is not named on its own")
}

// The hint carries what to do about every reason reported, most pressing
// first. The layers are read in a fixed order, so a file that cannot be read
// comes before a config that certainly declares MCP servers; the person has
// to be told about the declaration, and about both.
func TestConnectDoctorPreflightHintsAtWhatMostNeedsDoing(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a file whatever its mode says")
	}
	file, home, _ := codexProfile(t, 1)
	// config.toml is read before managed_config.toml, so the layer that
	// cannot be read is the one this would hint about by order alone.
	unreadable := writeCodexConfig(t, home, "model = \"gpt-5\"\n")
	declared := filepath.Join(home, ".codex", "managed_config.toml")
	require.NoError(t, os.WriteFile(declared, []byte("[mcp_servers.linear]\ncommand = \"linear-mcp\"\n"), 0o600))
	require.NoError(t, os.Chmod(unreadable, 0o000))
	t.Cleanup(func() { _ = os.Chmod(unreadable, 0o600) })

	c, ok := preflightCheck(t, workerBinaryChecks(context.Background(), file))
	require.True(t, ok)
	assert.Equal(t, setup.StatusFail, c.Status)
	assert.Contains(t, c.Message, declared, "both layers are reported")
	assert.Contains(t, c.Message, unreadable)
	assert.Contains(t, c.Hint, "Take the MCP servers out",
		"the declaration is certainly there, and is what to do about it first")
	assert.Contains(t, c.Hint, "readable by the user", "and the other layer still has its own remedy")
	assert.Less(t, strings.Index(c.Hint, "Take the MCP servers out"), strings.Index(c.Hint, "readable by the user"),
		"most pressing first, not first in the order the layers are read")
}

// A route the repository does not track is a directory no worktree would
// hold, so a session there would have nowhere to start. Doctor says so
// rather than passing over it — the route exists on disk, and every check
// that only looks at the disk is satisfied by it.
func TestConnectDoctorPreflightRefusesARouteNoWorktreeWouldHold(t *testing.T) {
	file, route, repo, _ := codexWorktreeProfile(t)

	// The tracked route the repository has is ready, and stays ready.
	c, ok := preflightCheck(t, workerBinaryChecks(context.Background(), file))
	require.True(t, ok)
	require.Equal(t, setup.StatusPass, c.Status, "a route the repository tracks is in every worktree of it")
	assert.Contains(t, c.Message, "would start")

	untracked := filepath.Join(repo, "scratch")
	require.NoError(t, os.MkdirAll(untracked, 0o755))
	file.Projects[2] = admission.Route{Path: untracked}

	c, ok = preflightCheck(t, workerBinaryChecks(context.Background(), file))
	require.True(t, ok)
	assert.Equal(t, setup.StatusFail, c.Status, "doctor never calls a connector ready that would not start")
	assert.Contains(t, c.Message, untracked)
	assert.Contains(t, c.Message, "no worktree of it would hold the route")
	assert.Contains(t, c.Hint, "Commit it", "and what to do about it")
	assert.NotContains(t, c.Message, "start in "+route, "the route that would run is not named")
}
