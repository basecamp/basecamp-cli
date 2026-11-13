package commands

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/connector/admission"
	"github.com/basecamp/basecamp-cli/internal/connector/runner"
	"github.com/basecamp/basecamp-cli/internal/output"
	"github.com/basecamp/basecamp-cli/internal/richtext"
	"github.com/basecamp/basecamp-cli/skills"
)

// The runner is the connector doing the work itself: started with
// --orchestrator, it gives each trusted request to a headless session of the
// coding agent named, which follows the basecamp-connect skill and hands the
// work to workers through the commands below. Without the flag the connector
// is what it was: it prints each request for the session that started it.

// connectRunnerFlags are the run's flags that turn the runner on.
type connectRunnerFlags struct {
	orchestrator       string
	orchestratorModel  string
	orchestratorEffort string
	worker             string
	workerModel        string
	workerEffort       string
	permissions        string
	// maxWorkers is --max-workers, the cap on workers running at once. It
	// remembers whether it was passed: 0 given takes a kept cap off.
	maxWorkers optionalInt
	// run starts the runner with what the profile's last start kept.
	run bool
}

func addConnectRunnerFlags(cmd *cobra.Command, f *connectRunnerFlags) {
	agents := strings.Join(runner.AdapterNames(), ", ")
	fl := cmd.Flags()
	fl.StringVar(&f.orchestrator, "orchestrator", "", "Do the work here instead of printing requests: the coding agent that acknowledges each request and hands it to a worker ("+agents+"; kept for the profile)")
	fl.StringVar(&f.orchestratorModel, "orchestrator-model", "", "The orchestrator's model (default: the coding agent's own; kept for the profile)")
	fl.StringVar(&f.orchestratorEffort, "orchestrator-effort", "", "The orchestrator's reasoning effort (default: the coding agent's own; kept for the profile)")
	fl.StringVar(&f.worker, "worker", "", "The coding agent that does each request's work ("+agents+"; default: the orchestrator's; kept for the profile)")
	fl.StringVar(&f.workerModel, "worker-model", "", "The workers' model (default: the coding agent's own; kept for the profile)")
	fl.StringVar(&f.workerEffort, "worker-effort", "", "The workers' reasoning effort (default: the coding agent's own; kept for the profile)")
	fl.StringVar(&f.permissions, "permissions", "", "What the coding agents may do on this computer: full (any command, without asking; the default) or guarded (each under its own guardrails: Claude Code's auto mode, Codex's sandbox with automatic review); kept for the profile")
	fl.Var(&f.maxWorkers, "max-workers", "Cap how many workers run at once, so a request past that waits its turn (default: no cap; 0 takes a kept cap off; kept for the profile)")
	fl.BoolVar(&f.run, "run", false, "Do the work here, with the orchestrator, worker, permissions and cap on workers this profile was last started with")
}

// requested reports whether any runner flag was given.
func (f connectRunnerFlags) requested() bool { return f != (connectRunnerFlags{}) }

// connectRunnerStart is what a run that does the work itself starts with.
type connectRunnerStart struct {
	// orchestrator is whole. worker is what the start and runner.json name
	// between them, which may leave the agent, the model or the effort to
	// the orchestrator's.
	orchestrator runner.Spec
	worker       runner.Spec
	permissions  runner.Permissions
	// maxWorkers is the cap on workers this start named, zero for none. It
	// is nil when the start named no cap, which leaves runner.json's.
	maxWorkers *int
}

// connectRunnerSettle is the start the flags ask for, over what runner.json
// kept from the last one: a flag wins, and what no flag names comes from the
// file. It is settled before anything is read from Basecamp. Nothing is kept
// here: keep does that, once the start has passed its other checks.
func connectRunnerSettle(f connectRunnerFlags, configPath string) (connectRunnerStart, error) {
	var start connectRunnerStart
	config, err := runner.LoadConfig(configPath)
	if err != nil {
		return start, output.ErrUsage(err.Error())
	}
	asked := runner.Spec{Adapter: f.orchestrator, Model: f.orchestratorModel, Effort: f.orchestratorEffort}
	if err := asked.Validate(true); err != nil {
		return start, output.ErrUsage("--orchestrator: " + err.Error())
	}
	start.orchestrator = asked.Over(config.Orchestrator)
	if start.orchestrator.Adapter == "" {
		hint := "Pass --orchestrator with one of " + strings.Join(runner.AdapterNames(), ", ") + ". It is kept for the profile, and --run starts with it from then on."
		if f.run {
			return start, output.ErrUsageHint("This profile has no orchestrator kept from an earlier start", hint)
		}
		return start, output.ErrUsageHint("No coding agent is named for the orchestrator", hint)
	}
	asked = runner.Spec{Adapter: f.worker, Model: f.workerModel, Effort: f.workerEffort}
	if err := asked.Validate(true); err != nil {
		return start, output.ErrUsage("--worker: " + err.Error())
	}
	start.worker = asked.Over(config.Worker)
	start.permissions = config.Mode()
	if f.permissions != "" {
		if start.permissions, err = runner.ParsePermissions(f.permissions); err != nil {
			return start, output.ErrUsage("--permissions: " + err.Error())
		}
	}
	if f.maxWorkers.set {
		if err := runner.CheckMaxWorkers(f.maxWorkers.value); err != nil {
			return start, output.ErrUsage("--max-workers: " + err.Error())
		}
		start.maxWorkers = &f.maxWorkers.value
	}
	// The agents every request needs are looked for now. One only a rule
	// names is found, or not, when a request first matches that rule.
	everyRequest := []runner.Spec{start.orchestrator, start.worker.Over(start.orchestrator)}
	if err := runner.CheckInstalled(nil, everyRequest...); err != nil {
		return start, output.ErrUsage(err.Error())
	}
	if err := runner.CheckPermissions(start.permissions, everyRequest...); err != nil {
		return start, output.ErrUsage(err.Error())
	}
	return start, nil
}

// keep writes the start into runner.json when it differs from what the file
// holds, so the next start is `--run`. The run calls it under the connector's
// lock, with the profile's identity verified: a start that is refused before
// then leaves what an earlier one kept as it was.
func (s connectRunnerStart) keep(configPath string) error {
	config, err := runner.LoadConfig(configPath)
	if err != nil {
		return output.ErrUsage(err.Error())
	}
	sameCap := s.maxWorkers == nil || *s.maxWorkers == config.MaxWorkers
	if s.orchestrator == config.Orchestrator && s.worker == config.Worker && s.permissions == config.Permissions && sameCap {
		return nil
	}
	if err := runner.SaveStart(configPath, s.orchestrator, s.worker, s.permissions, s.maxWorkers); err != nil {
		return output.ErrUsage("These settings could not be kept for the profile: " + err.Error())
	}
	return nil
}

// connectRunnerDir is the runner's own directory inside the connector's
// state directory.
func connectRunnerDir(stateDir string) string { return filepath.Join(stateDir, "runner") }

// connectRunnerConfigPath is runner.json, beside the profile's connect.json.
func connectRunnerConfigPath(connectJSON string) string {
	return filepath.Join(filepath.Dir(connectJSON), runner.ConfigFile)
}

// connectLogKeep is how large the kept copy of the connector's output may
// be when a run starts before it is started over.
const connectLogKeep = 32 << 20

// openConnectorLog opens the file a runner's connector keeps its output
// lines in, for its orchestrator to search the way the skill searches the
// output file of a connector it started.
func openConnectorLog(dir string) (*os.File, error) {
	path := filepath.Join(dir, "connector.ndjson")
	flags := os.O_CREATE | os.O_APPEND | os.O_WRONLY
	if info, err := os.Stat(path); err == nil && info.Size() > connectLogKeep {
		flags |= os.O_TRUNC
	}
	return os.OpenFile(path, flags, 0o600) //nolint:gosec // G304: the connector's own state directory
}

// newConnectRunner builds the run's runner.
func newConnectRunner(profile, stateDir, connectJSON string, start connectRunnerStart, log *os.File, startLine string,
	summaries admission.SummaryReader, onHold connectOnHoldReader, held func(context.Context) (bool, error), stop func(restart bool), logger *slog.Logger) (*runner.Runner, error) {
	skill, err := skills.Connect.ReadFile("basecamp-connect/SKILL.md")
	if err != nil {
		return nil, err
	}
	subagent, err := skills.Connect.ReadFile("basecamp-connect/subagent.md")
	if err != nil {
		return nil, err
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	return runner.New(runner.Options{
		Profile: profile, Dir: connectRunnerDir(stateDir),
		Orchestrator: start.orchestrator, Worker: start.worker, MaxWorkers: start.maxWorkers, ConfigPath: connectRunnerConfigPath(connectJSON),
		Skill: string(skill), Subagent: string(subagent),
		ConnectorLog: log.Name(), Executable: executable, StartLine: startLine,
		Recordings: connectRecordingsAbove(summaries, onHold, logger), Held: held, Stop: stop, Logger: logger,
	})
}

// Recording types of a card table's parts.
const (
	// kanbanColumnType is a column.
	kanbanColumnType = "Kanban::Column"
	// kanbanOnHoldType is a column's On Hold section, which holds cards and
	// is not a column: its id is its own, and it is reached through the
	// column it belongs to.
	kanbanOnHoldType = "Kanban::OnHold"
)

// connectOnHoldReader finds the column an On Hold section belongs to and
// that column's card table, in a project. It answers nothing when it cannot.
type connectOnHoldReader func(ctx context.Context, project, section int64) []int64

// connectOnHoldAbove reads a project's card tables for the column whose On
// Hold section is the one asked for. No read gives a section's column from
// the section's own id, so the card tables are looked through.
func connectOnHoldAbove(client *basecamp.AccountClient, logger *slog.Logger) connectOnHoldReader {
	// Said whenever the column is not found, as the walk's other failures
	// are: a rule that then does not match should not fail to in silence.
	const unresolved = "connector: the column an On Hold section belongs to could not be found; a runner.json rule about its column or card table does not match the request"
	return func(ctx context.Context, project, section int64) []int64 {
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		found, err := client.Projects().Get(ctx, project)
		if err != nil || found == nil {
			logger.Warn(unresolved, "project", project, "section", section, "error", err)
			return nil
		}
		for _, tool := range found.Dock {
			if tool.Name != "kanban_board" {
				continue
			}
			table, err := client.CardTables().Get(ctx, tool.ID)
			if err != nil || table == nil {
				logger.Warn("connector: a card table could not be read while looking for an On Hold section's column", "card_table", tool.ID, "error", err)
				continue
			}
			for _, column := range table.Lists {
				if column.OnHold != nil && column.OnHold.ID == section {
					return []int64{column.ID, table.ID}
				}
			}
		}
		logger.Warn(unresolved, "project", project, "section", section)
		return nil
	}
}

// connectRecordingsAbove lists a request's recording and what it sits under,
// nearest first, by reading each one's parent as the agent. It is asked only
// when runner.json has a rule about a card table, a to-do list or another
// recording. A read that fails ends the walk where it stands: the rule then
// does not match, and the log says the walk fell short.
func connectRecordingsAbove(summaries admission.SummaryReader, onHold connectOnHoldReader, logger *slog.Logger) func(ctx context.Context, project, recordingID int64, recordingType string) []int64 {
	return func(ctx context.Context, project, recordingID int64, recordingType string) []int64 {
		ids := []int64{recordingID}
		ref := basecamp.RecordingRef{BucketID: project, RecordingID: recordingID, RecordingType: recordingType}
		for range 8 {
			readCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			summary, err := summaries.Summarize(readCtx, ref)
			cancel()
			if err != nil && strings.HasPrefix(ref.RecordingType, "Kanban::") && ref.RecordingType != kanbanColumnType {
				// A card table's triage, "not now" and done columns have
				// types of their own, which the typed read does not know.
				// Each is read the way any column is.
				ref.RecordingType = kanbanColumnType
				readCtx, cancel = context.WithTimeout(ctx, 15*time.Second)
				summary, err = summaries.Summarize(readCtx, ref)
				cancel()
			}
			if err != nil {
				logger.Warn("connector: what a request sits under could not be read past here; a runner.json rule about a container further up does not match it",
					"recording_id", ref.RecordingID, "error", err)
				break
			}
			if summary == nil || summary.Parent == nil || summary.Parent.ID <= 0 {
				break
			}
			ids = append(ids, summary.Parent.ID)
			if summary.Parent.Type == kanbanOnHoldType {
				// A card on hold sits in its column's On Hold section. The
				// column and the card table above it end the walk.
				if onHold != nil {
					ids = append(ids, onHold(ctx, project, summary.Parent.ID)...)
				}
				break
			}
			ref = basecamp.RecordingRef{BucketID: project, RecordingID: summary.Parent.ID, RecordingType: summary.Parent.Type}
		}
		return ids
	}
}

// errConnectRestart is the run asking to be started again: `basecamp connect
// restart` reached its runner. Everything the run held is released by the
// time it is returned.
var errConnectRestart = errors.New("connect: restart")

// --- the commands an agent, or the person, runs against a running runner ---

// callConnectRunner makes one call on the profile's running runner.
//
// An agent that runner started is told where its socket is
// (runner.SocketEnv), and its commands go straight there. Anyone else's find
// it through the profile's connect.json.
func callConnectRunner(cmd *cobra.Command, req runner.Request, timeout time.Duration) (connectProfile, runner.Response, error) {
	var (
		p      connectProfile
		socket string
	)
	if app := appctx.FromContext(cmd.Context()); app != nil && os.Getenv(runner.SocketEnv) != "" && app.Config.ActiveProfile != "" &&
		os.Getenv(runner.ProfileEnv) == app.Config.ActiveProfile {
		p, socket = connectProfile{app: app, name: app.Config.ActiveProfile}, os.Getenv(runner.SocketEnv)
	} else {
		var err error
		if p, err = loadConnectProfile(cmd); err != nil {
			return p, runner.Response{}, err
		}
		stateDir, err := connectStatePath(p.name, p.file, false)
		if err != nil {
			return p, runner.Response{}, output.ErrUsage("The connector's state directory cannot be used: " + err.Error())
		}
		socket = runner.SocketPath(connectRunnerDir(stateDir))
	}
	resp, err := runner.Call(cmd.Context(), socket, req, timeout)
	if errors.Is(err, runner.ErrNotRunning) {
		return p, resp, output.ErrUsageHint(fmt.Sprintf("No connector is running with an orchestrator for profile %q", p.name),
			"Start one: basecamp connect -P "+richtext.ShellQuote(p.name)+" --orchestrator claude")
	}
	if err != nil {
		return p, resp, output.ErrUsage(err.Error())
	}
	return p, resp, nil
}

// maxConnectStdin bounds what a command reads from standard input: a handoff
// carries a project's AGENTS.md, and nothing larger.
const maxConnectStdin = 2 << 20

// readConnectStdin reads what a command was given on standard input.
func readConnectStdin(cmd *cobra.Command, what string) (string, error) {
	data, err := io.ReadAll(io.LimitReader(cmd.InOrStdin(), maxConnectStdin+1))
	if err != nil {
		return "", err
	}
	if len(data) > maxConnectStdin {
		// Refused whole: a handoff cut short would reach the worker looking
		// complete.
		return "", output.ErrUsage(fmt.Sprintf("Standard input is larger than %d MiB: %s", maxConnectStdin>>20, what))
	}
	text := strings.TrimSpace(string(data))
	if text == "" {
		return "", output.ErrUsage("Nothing was given on standard input: " + what)
	}
	return text, nil
}

func newConnectDispatchCmd() *cobra.Command {
	var (
		req    runner.DispatchRequest
		noRepo bool
	)
	cmd := &cobra.Command{
		Use:   "dispatch",
		Short: "Hand a request to a background worker",
		Long: `Start a worker for a request a running connector handed off, and return at
once with the worker's id. It is the orchestrator's command: the connector
runs it for the basecamp-connect skill's "dispatch one background subagent".

Everything the worker needs is read from standard input. The worker is a
session of its own, started in --repo (or in a scratch directory with
--no-repo), given the skill's worker instructions and that handoff. When it
finishes, its report reaches the orchestrator as a message.

Which coding agent, model and effort the worker runs on comes from how the
connector was started and from runner.json's rules. --adapter, --model and
--effort override both for this one worker. A second dispatch for an event
that already has a worker returns that worker; --again starts another.

A request on a thread an earlier worker handled (the same card, to-do,
message or chat) goes to that worker, continued in its own session, when the
work is in the same repo and on the same coding agent. It waits its turn if
that worker is still busy. A session that can no longer be opened is replaced
by a new one.`,
		Example: `  basecamp connect dispatch -P agent --event 123 --repo ~/code/app < handoff.md
  basecamp connect dispatch -P agent --event 124 --no-repo --adapter codex --effort high < handoff.md`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			handoff, err := readConnectStdin(cmd, "the handoff, everything the worker needs")
			if err != nil {
				return err
			}
			req.Handoff, req.NoRepo = handoff, noRepo
			// Long enough for the reads that find what the request sits
			// under, each of which has its own bound: a call that gave up
			// first would report a failure for a worker that then starts.
			p, resp, err := callConnectRunner(cmd, runner.Request{Op: runner.OpDispatch, Dispatch: req}, 5*time.Minute)
			if err != nil {
				return err
			}
			summary := fmt.Sprintf("Dispatched worker %s on %s", resp.Worker.ID, connectWorkerAgent(*resp.Worker))
			switch {
			case resp.Existing:
				summary = fmt.Sprintf("Event %d already has worker %s (%s); pass --again to start another", req.EventID, resp.Worker.ID, resp.Worker.State)
			case resp.Worker.Requests > 1:
				summary = fmt.Sprintf("Given to worker %s, which handled this thread before and continues in its session, on %s", resp.Worker.ID, connectWorkerAgent(*resp.Worker))
			}
			return p.app.OK(map[string]any{"worker": resp.Worker, "existing": resp.Existing}, output.WithSummary(summary))
		},
	}
	fl := cmd.Flags()
	fl.Int64Var(&req.EventID, "event", 0, "The request's event_id (omit for work the owner asked for at this computer)")
	fl.StringVar(&req.Repo, "repo", "", "The local repo the worker starts in")
	fl.BoolVar(&noRepo, "no-repo", false, "The work needs no repo: start the worker in a scratch directory")
	fl.StringVar(&req.Spec.Adapter, "adapter", "", "Run this worker on this coding agent ("+strings.Join(runner.AdapterNames(), ", ")+")")
	fl.StringVar(&req.Spec.Model, "model", "", "Run this worker on this model")
	fl.StringVar(&req.Spec.Effort, "effort", "", "Run this worker at this reasoning effort")
	fl.BoolVar(&req.Again, "again", false, "Start a new worker for an event that already has one")
	return cmd
}

// connectWorkerAgent is the agent a worker runs on, as a person reads it.
func connectWorkerAgent(w runner.WorkerView) string {
	return runner.Spec{Adapter: w.Adapter, Model: w.Model, Effort: w.Effort}.String()
}

func newConnectWorkersCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "workers",
		Short: "See and steer a running connector's workers",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List the workers and what each runs on",
		Long: `List the workers of the connector running for this profile: each one's
request, state, the coding agent, model and effort it runs on, the runner.json
rule that chose them, and its report once it has finished. It also says what
the orchestrator runs on and how many messages are waiting for it.`,
		Example: `  basecamp connect workers list -P agent`,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, resp, err := callConnectRunner(cmd, runner.Request{Op: runner.OpWorkers}, time.Minute)
			if err != nil {
				return err
			}
			workers := resp.Workers
			if workers == nil {
				workers = []runner.WorkerView{}
			}
			summary := fmt.Sprintf("%d worker(s): %d running, %d queued; orchestrator on %s", len(workers), resp.Status.Running, resp.Status.Queued, resp.Status.Orchestrator)
			if resp.Status.MaxWorkers > 0 {
				summary += fmt.Sprintf("; at most %d at once", resp.Status.MaxWorkers)
			}
			if resp.Status.ConfigError != "" {
				summary += "; no agent starts until runner.json is fixed: " + resp.Status.ConfigError
			}
			return p.app.OK(map[string]any{"orchestrator": resp.Status, "workers": workers}, output.WithSummary(summary))
		},
	}, &cobra.Command{
		Use:   "send <worker-id>",
		Short: "Tell a worker something more about its request",
		Long: `Give a worker a further message, read from standard input. A worker that is
running reads it after its current turn; one that has finished is continued
in the same session, and reports again when it is done.`,
		Example: `  echo 'Use the staging database, not production.' | basecamp connect workers send w12 -P agent`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			message, err := readConnectStdin(cmd, "the message for the worker")
			if err != nil {
				return err
			}
			p, resp, err := callConnectRunner(cmd, runner.Request{Op: runner.OpSend, Worker: args[0], Message: message}, time.Minute)
			if err != nil {
				return err
			}
			return p.app.OK(resp.Worker, output.WithSummary(fmt.Sprintf("Worker %s will read it (%s)", resp.Worker.ID, resp.Worker.State)))
		},
	}, &cobra.Command{
		Use:     "stop <worker-id>",
		Short:   "Stop a worker",
		Long:    `Stop a worker: its running turn ends now and nothing more is sent to it. What it already posted or pushed stays.`,
		Example: `  basecamp connect workers stop w12 -P agent`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, resp, err := callConnectRunner(cmd, runner.Request{Op: runner.OpStopWorker, Worker: args[0]}, time.Minute)
			if err != nil {
				return err
			}
			return p.app.OK(resp.Worker, output.WithSummary(fmt.Sprintf("Worker %s is %s", resp.Worker.ID, resp.Worker.State)))
		},
	})
	return cmd
}

func newConnectSayCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "say [message]",
		Short: "Talk to a running connector's orchestrator",
		Long: `Give the orchestrator of the connector running for this profile a message
from you, the person at this computer, and print its answer. It is how you
ask it what you would ask a session running the basecamp-connect skill:
which repo a project's work goes in, to serve another project, to stop.

The message is the arguments, or standard input when there are none. Your
word here is the owner's word. The command waits for the orchestrator's turn,
which comes after any request it is already handling.`,
		Example: `  basecamp connect say -P agent "Work for the Redesign project goes in ~/code/redesign"
  basecamp connect say -P agent "what are the workers doing?"`,
		RunE: func(cmd *cobra.Command, args []string) error {
			message := strings.TrimSpace(strings.Join(args, " "))
			if message == "" {
				var err error
				if message, err = readConnectStdin(cmd, "the message for the orchestrator"); err != nil {
					return err
				}
			}
			p, resp, err := callConnectRunner(cmd, runner.Request{Op: runner.OpSay, Message: message}, 0)
			if err != nil {
				return err
			}
			return p.app.OK(map[string]any{"reply": resp.Reply}, output.WithSummary(resp.Reply))
		},
	}
}

func newConnectStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Stop a running connector that does the work itself",
		Long: `Stop the connector running for this profile with an orchestrator. Workers
in the middle of a request are stopped with it and continued, in their own
sessions, when it is started again. Requests made while it is stopped are not
picked up later.`,
		Example: `  basecamp connect stop -P agent`,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, _, err := callConnectRunner(cmd, runner.Request{Op: runner.OpStop}, time.Minute)
			if err != nil {
				return err
			}
			return p.app.OK(map[string]any{"stopping": true}, output.WithSummary("The connector is stopping"))
		},
	}
}

func newConnectRestartCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "restart",
		Short: "Restart a running connector that does the work itself",
		Long: `Stop the connector running for this profile with an orchestrator and start
it again with the arguments it was started with, so a setup change takes
effect. Workers in the middle of a request are continued in their own
sessions once it is back.`,
		Example: `  basecamp connect restart -P agent`,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, _, err := callConnectRunner(cmd, runner.Request{Op: runner.OpStop, Restart: true}, time.Minute)
			if err != nil {
				return err
			}
			return p.app.OK(map[string]any{"restarting": true}, output.WithSummary("The connector is restarting"))
		},
	}
}
