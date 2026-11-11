package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/connector/runner"
	"github.com/basecamp/basecamp-cli/internal/output"
	"github.com/basecamp/basecamp-cli/skills"
)

// The flags that turn the runner on are settled before the connector reads
// anything from Basecamp: a start that could never run a turn ends as a
// usage error, with nothing taken and nothing posted.
func TestConnectRefusesAnOrchestratorItCouldNotRun(t *testing.T) {
	f := newOperatorFixture(t)
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"--orchestrator", "gemini"}, "unknown adapter"},
		{[]string{"--worker", "codex"}, "No coding agent is named for the orchestrator"},
		{[]string{"--orchestrator-model", "opus"}, "No coding agent is named for the orchestrator"},
		{[]string{"--orchestrator", "claude", "--worker", "aider"}, "--worker: unknown adapter"},
		{[]string{"--orchestrator", "claude", "--orchestrator-model", "--evil"}, "is not a model name"},
		{[]string{"--orchestrator", "claude", "--shadow"}, "A shadow run hands nothing off"},
	} {
		_, err := f.run(t, output.FormatJSON, c.args...)
		require.Error(t, err, c.args)
		e := usageError(t, err)
		assert.Equal(t, output.CodeUsage, e.Code, c.args)
		assert.Contains(t, e.Message, c.want, c.args)
	}

	// An agent that is not installed is named, with the command it needs.
	t.Setenv("PATH", t.TempDir())
	_, err := f.run(t, output.FormatJSON, "--orchestrator", "claude")
	assert.Contains(t, usageError(t, err).Message, "the claude adapter needs the `claude` command")

	// runner.json can name the orchestrator's agent, and is refused when it
	// cannot be read as written.
	config := connectRunnerConfigPath(connectSetupPath(t, "agent"))
	require.NoError(t, os.WriteFile(config, []byte(`{"orchestrator": {"adapter": "codex"}}`), 0o600))
	_, err = f.run(t, output.FormatJSON, "--worker", "pi")
	assert.Contains(t, usageError(t, err).Message, "the codex adapter needs the `codex` command")
	require.NoError(t, os.WriteFile(config, []byte(`{"orchestrater": {}}`), 0o600))
	_, err = f.run(t, output.FormatJSON, "--orchestrator", "claude")
	assert.Contains(t, usageError(t, err).Message, "runner.json")
}

// An agent the runner started is told where the runner is, and its commands
// go there without reading connect.json: from inside a coding agent's
// sandbox that file's ownership cannot be checked. Another profile's
// commands are not sent there.
func TestAnAgentsCommandsReachItsRunnerWithoutReadingConnectJSON(t *testing.T) {
	f := newOperatorFixture(t)
	require.NoError(t, os.Remove(connectSetupPath(t, "agent")))
	_, err := f.run(t, output.FormatJSON, "workers", "list")
	assert.Contains(t, usageError(t, err).Message, "is not set up as a connector", "with nothing said of a runner, the profile is read")

	private := t.TempDir()
	require.NoError(t, os.Chmod(private, 0o700))
	t.Setenv(runner.SocketEnv, filepath.Join(private, "sock"))
	t.Setenv(runner.ProfileEnv, "agent")
	_, err = f.run(t, output.FormatJSON, "workers", "list")
	assert.Contains(t, usageError(t, err).Message, "No connector is running with an orchestrator", "the runner named is the one called, and nothing listens there")

	t.Setenv(runner.ProfileEnv, "someone-else")
	_, err = f.run(t, output.FormatJSON, "workers", "list")
	assert.Contains(t, usageError(t, err).Message, "is not set up as a connector")
}

func TestTheRunnersCommandsSayWhenNoRunnerIsRunning(t *testing.T) {
	f := newOperatorFixture(t)
	for _, args := range [][]string{{"workers", "list"}, {"stop"}, {"restart"}, {"say", "hello"}} {
		_, err := f.run(t, output.FormatJSON, args...)
		e := usageError(t, err)
		assert.Contains(t, e.Message, `No connector is running with an orchestrator for profile "agent"`, args)
		assert.Contains(t, e.Hint, "basecamp connect -P agent --orchestrator claude")
	}
}

// The commands an orchestrator runs reach the runner of the connector
// running for the profile: a dispatch starts a worker on the agent the
// rules choose, the list shows it, and stop asks the connector to stop.
func TestTheRunnersCommandsReachTheRunningRunner(t *testing.T) {
	f := newOperatorFixture(t)
	// A stand-in for Claude Code: it reads its prompt and answers in
	// stream-json, the session it was given confirmed.
	bin := t.TempDir()
	script := "#!/bin/sh\ncat > /dev/null\n" +
		`echo '{"type":"system","subtype":"init","session_id":"11111111-1111-4111-8111-111111111111"}'` + "\n" +
		`echo '{"type":"result","subtype":"success","is_error":false,"result":"all quiet"}'` + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(bin, "claude"), []byte(script), 0o700)) //nolint:gosec // G306: a test's executable

	stateDir, err := connectStateDir("agent", f.file, false)
	require.NoError(t, err)
	skill, err := skills.Connect.ReadFile("basecamp-connect/SKILL.md")
	require.NoError(t, err)
	subagent, err := skills.Connect.ReadFile("basecamp-connect/subagent.md")
	require.NoError(t, err)
	stopped := make(chan bool, 1)
	work, err := runner.New(runner.Options{
		Profile: "agent", Dir: connectRunnerDir(stateDir),
		Orchestrator: runner.Spec{Adapter: "claude"}, Worker: runner.Spec{Effort: "low"},
		ConfigPath: connectRunnerConfigPath(connectSetupPath(t, "agent")),
		Skill:      string(skill), Subagent: string(subagent),
		LookPath: func(name string) (string, error) { return filepath.Join(bin, name), nil },
		Stop:     func(restart bool) { stopped <- restart },
	})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- work.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	<-work.Ready()

	run := func(stdin string, args ...string) map[string]any {
		t.Helper()
		app := newConnectSetupApp(t, f.s, "agent")
		var buf bytes.Buffer
		app.Output = output.New(output.Options{Format: output.FormatJSON, Writer: &buf})
		cmd := NewConnectCmd()
		cmd.SetArgs(args)
		cmd.SetContext(appctx.WithApp(context.Background(), app))
		cmd.SetIn(strings.NewReader(stdin))
		cmd.SetOut(&buf)
		cmd.SetErr(&buf)
		cmd.SilenceErrors, cmd.SilenceUsage = true, true
		require.NoError(t, cmd.Execute(), buf.String())
		var envelope struct {
			Data    map[string]any `json:"data"`
			Summary string         `json:"summary"`
		}
		require.NoError(t, json.Unmarshal(buf.Bytes(), &envelope), buf.String())
		envelope.Data["summary"] = envelope.Summary
		return envelope.Data
	}

	// The orchestrator's skill is the shipped one, with the mechanics that
	// name these very commands in front of it.
	doc, err := os.ReadFile(filepath.Join(connectRunnerDir(stateDir), "orchestrator.md"))
	require.NoError(t, err)
	assert.Contains(t, string(doc), "basecamp connect dispatch -P agent --event <event_id> --repo '<path>'")
	assert.Contains(t, string(doc), "# /basecamp-connect: drive your agent from Basecamp")
	assert.NotContains(t, string(doc), "triggers:")

	repo := t.TempDir()
	require.NoError(t, os.WriteFile(connectRunnerConfigPath(connectSetupPath(t, "agent")),
		[]byte(`{"rules": [{"repo": "`+repo+`", "worker": {"model": "opus"}}]}`), 0o600))
	dispatched := run("Everything the worker needs.", "dispatch", "--repo", repo)
	worker := dispatched["worker"].(map[string]any)
	assert.Equal(t, "claude", worker["adapter"])
	assert.Equal(t, "opus", worker["model"])
	assert.Equal(t, "low", worker["effort"])
	assert.Equal(t, "rule 1 (repo "+repo+")", worker["matched"])
	assert.Equal(t, "Dispatched worker w1 on claude · opus · low", dispatched["summary"])

	// Polled here, on the test's own goroutine, where a failed call fails
	// the test rather than a timeout hiding it.
	finished := false
	for deadline := time.Now().Add(20 * time.Second); !finished && time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		workers, _ := run("", "workers", "list")["workers"].([]any)
		if len(workers) == 1 {
			worker, _ := workers[0].(map[string]any)
			finished = worker["state"] == "done" && worker["report"] == "all quiet"
		}
	}
	require.True(t, finished, "the worker finished and reported")

	assert.Equal(t, "all quiet", run("", "say", "anything", "to", "report?")["reply"])
	assert.Equal(t, "all quiet", run("anything to report?", "say")["reply"])
	sent := run("One more thing.", "workers", "send", "w1")
	assert.Equal(t, "w1", sent["id"])
	assert.Equal(t, "w1", run("", "workers", "stop", "w1")["id"])

	run("", "restart")
	assert.True(t, <-stopped, "restart asks the connector to stop and start again")
	run("", "stop")
	assert.False(t, <-stopped)

	// A dispatch with nothing to tell the worker is refused before it is sent.
	app := newConnectSetupApp(t, f.s, "agent")
	cmd := NewConnectCmd()
	cmd.SetArgs([]string{"dispatch", "--no-repo"})
	cmd.SetContext(appctx.WithApp(context.Background(), app))
	cmd.SetIn(strings.NewReader("  \n"))
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	assert.Contains(t, usageError(t, cmd.Execute()).Message, "Nothing was given on standard input")
}

// What a start names is kept for the profile, so the next start is --run:
// a flag wins over what was kept, and what no flag names comes from it.
func TestWhatAStartNamesIsKeptAndRunStartsWithIt(t *testing.T) {
	bin := t.TempDir()
	for _, name := range runner.AdapterNames() {
		require.NoError(t, os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"), 0o700)) //nolint:gosec // G306: a test's executable
	}
	t.Setenv("PATH", bin)
	path := filepath.Join(t.TempDir(), runner.ConfigFile)

	_, err := connectRunnerSettle(connectRunnerFlags{run: true}, path)
	e := usageError(t, err)
	assert.Contains(t, e.Message, "This profile has no orchestrator kept from an earlier start")
	assert.Contains(t, e.Hint, "--run starts with it from then on")
	assert.NoFileExists(t, path, "a start that is refused keeps nothing")

	first, err := connectRunnerSettle(connectRunnerFlags{orchestrator: "claude", orchestratorModel: "sonnet", worker: "codex", workerEffort: "high", permissions: "guarded"}, path)
	require.NoError(t, err)
	assert.NoFileExists(t, path, "settling a start keeps nothing: the run keeps it once its other checks have passed")
	require.NoError(t, first.keep(path))
	assert.Equal(t, connectRunnerStart{
		orchestrator: runner.Spec{Adapter: "claude", Model: "sonnet"},
		worker:       runner.Spec{Adapter: "codex", Effort: "high"},
		permissions:  runner.PermissionsGuarded,
	}, first)

	again, err := connectRunnerSettle(connectRunnerFlags{run: true}, path)
	require.NoError(t, err)
	assert.Equal(t, first, again)

	// One flag changes that one thing, and the change is kept too.
	changed, err := connectRunnerSettle(connectRunnerFlags{workerEffort: "low"}, path)
	require.NoError(t, err)
	require.NoError(t, changed.keep(path))
	assert.Equal(t, runner.Spec{Adapter: "codex", Effort: "low"}, changed.worker)
	assert.Equal(t, first.orchestrator, changed.orchestrator)
	kept, err := runner.LoadConfig(path)
	require.NoError(t, err)
	assert.Equal(t, "low", kept.Worker.Effort)
	assert.Equal(t, runner.PermissionsGuarded, kept.Mode())

	// Guarded refuses a coding agent with no guardrails, before anything is
	// kept, and an unknown value is not a setting.
	_, err = connectRunnerSettle(connectRunnerFlags{worker: "pi"}, path)
	assert.Contains(t, usageError(t, err).Message, "pi has no guardrails of its own")
	_, err = connectRunnerSettle(connectRunnerFlags{permissions: "limited"}, path)
	assert.Contains(t, usageError(t, err).Message, `--permissions: permissions must be "full" or "guarded"`)
	kept, err = runner.LoadConfig(path)
	require.NoError(t, err)
	assert.Equal(t, "codex", kept.Worker.Adapter)

	full, err := connectRunnerSettle(connectRunnerFlags{worker: "pi", permissions: "full"}, path)
	require.NoError(t, err)
	assert.Equal(t, runner.PermissionsFull, full.permissions)
	assert.Equal(t, "pi", full.worker.Adapter)
	assert.Empty(t, full.worker.Model, "a model belongs to its coding agent: changing the agent does not carry it across")
}

// A handoff longer than a command reads is refused whole: cut short, it
// would reach the worker looking complete.
func TestStandardInputPastItsLimitIsRefusedNotCut(t *testing.T) {
	cmd := newConnectDispatchCmd()
	cmd.SetIn(strings.NewReader(strings.Repeat("x", maxConnectStdin+1)))
	_, err := readConnectStdin(cmd, "the handoff")
	assert.Contains(t, usageError(t, err).Message, "Standard input is larger than 2 MiB")
	cmd.SetIn(strings.NewReader(strings.Repeat("x", maxConnectStdin)))
	text, err := readConnectStdin(cmd, "the handoff")
	require.NoError(t, err)
	assert.Len(t, text, maxConnectStdin)
}

// summariesByID answers the typed read from a table, and refuses a type it
// was not given, as the SDK refuses one it has no read for.
type summariesByID map[int64]struct {
	kind   string
	parent *basecamp.Parent
}

func (s summariesByID) Summarize(_ context.Context, ref basecamp.RecordingRef) (*basecamp.RecordingSummary, error) {
	found, ok := s[ref.RecordingID]
	if !ok || found.kind != ref.RecordingType {
		return nil, fmt.Errorf("no typed read for recording type: %q", ref.RecordingType)
	}
	return &basecamp.RecordingSummary{ID: ref.RecordingID, Type: found.kind, Parent: found.parent}, nil
}

// What a request sits under is walked parent by parent. A card table's
// triage, "not now" and done columns have types of their own and are read as
// columns. A card on hold sits in its column's On Hold section, whose column
// and card table are found from the project's card tables.
func TestWhatARequestSitsUnderReachesTheCardTableThroughEveryKindOfColumn(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	summaries := summariesByID{
		1: {kind: "Comment", parent: &basecamp.Parent{ID: 2, Type: "Kanban::Card"}},
		2: {kind: "Kanban::Card", parent: &basecamp.Parent{ID: 3, Type: "Kanban::Triage"}},
		3: {kind: "Kanban::Column", parent: &basecamp.Parent{ID: 4, Type: "Kanban::Board"}},
		4: {kind: "Kanban::Board"},
		// And a card on hold.
		10: {kind: "Kanban::Card", parent: &basecamp.Parent{ID: 11, Type: "Kanban::OnHold"}},
	}
	onHold := func(_ context.Context, project, section int64) []int64 {
		assert.Equal(t, int64(456), project)
		assert.Equal(t, int64(11), section)
		return []int64{12, 4}
	}
	above := connectRecordingsAbove(summaries, onHold, logger)
	assert.Equal(t, []int64{1, 2, 3, 4}, above(context.Background(), 456, 1, "Comment"))
	assert.Equal(t, []int64{10, 11, 12, 4}, above(context.Background(), 456, 10, "Kanban::Card"), "the section, its column, and the card table")

	// With nothing to say whose section it is, the walk ends at the section.
	assert.Equal(t, []int64{10, 11}, connectRecordingsAbove(summaries, nil, logger)(context.Background(), 456, 10, "Kanban::Card"))
	// A read that fails ends the walk where it stands.
	assert.Equal(t, []int64{99}, above(context.Background(), 456, 99, "Comment"))
}
