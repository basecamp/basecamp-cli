package commands

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/config"
	"github.com/basecamp/basecamp-cli/internal/connector"
	"github.com/basecamp/basecamp-cli/internal/connector/admission"
	"github.com/basecamp/basecamp-cli/internal/connector/driver"
	"github.com/basecamp/basecamp-cli/internal/connector/setup"
	"github.com/basecamp/basecamp-cli/internal/output"
	"github.com/basecamp/basecamp-cli/internal/richtext"
)

// The operator's commands on a connector's ledger: status, redispatch,
// discard, release, shadow promote and import. doctor is in connect_doctor.go.
// Each resolves the connector from the profile's connect.json, locally.

// connectProfile is a set-up profile, read without the network.
type connectProfile struct {
	app  *appctx.App
	name string
	file setup.File
}

func loadConnectProfile(cmd *cobra.Command) (connectProfile, error) {
	app := appctx.FromContext(cmd.Context())
	if app == nil {
		return connectProfile{}, errors.New("app not initialized")
	}
	name := app.Config.ActiveProfile
	if name == "" {
		return connectProfile{}, output.ErrUsageHint("This needs the agent's profile", "Pass -P/--profile <name>, a profile set up with `basecamp connect setup`.")
	}
	if !isValidProfileName(name) {
		return connectProfile{}, output.ErrUsage(fmt.Sprintf("Invalid profile name %q", name))
	}
	path, err := setup.Path(config.GlobalConfigDir(), name)
	if err != nil {
		return connectProfile{}, output.ErrUsage(err.Error())
	}
	file, err := setup.Load(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return connectProfile{}, output.ErrUsageHint(fmt.Sprintf("Profile %q is not set up as a connector", name), "Run: basecamp connect setup -P "+shellQuote(name))
	case err != nil:
		return connectProfile{}, output.ErrUsage("connect.json cannot be used: " + err.Error())
	}
	return connectProfile{app: app, name: name, file: file}, nil
}

// operatorName is who a decision is recorded as: the local user who ran it.
func operatorName() string {
	name := ""
	if u, err := user.Current(); err == nil {
		name = u.Username
	}
	if name == "" {
		name = os.Getenv("USER")
	}
	if name == "" {
		name = "unknown"
	}
	return "local:" + richtext.SanitizeSingleLine(name)
}

func parseEventIDArg(raw string) (int64, error) {
	id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || id <= 0 {
		return 0, output.ErrUsage(fmt.Sprintf("Invalid event id %q: expected a positive number", raw))
	}
	return id, nil
}

// openConnectLedger opens the connector's ledger for a decision. It must
// already exist: a decision is about records the connector wrote. The returned
// func releases whatever the open holds, and is never nil.
//
// requireStopped takes the instance lock for the whole command, for the work
// that cannot run beside a connector (an import). Otherwise the lock is taken
// only when the ledger is older than this build, because opening it then
// migrates it, and a connector running on the older schema must not have its
// triggers replaced underneath it. Either way the lock is the authority: the
// metadata beside it is written best-effort and says nothing on its own.
func openConnectLedger(ctx context.Context, p connectProfile, requireStopped bool) (*connector.Ledger, func(), error) {
	done := func() {}
	dir, err := connectStatePath(p.file, false)
	if err != nil {
		return nil, done, err
	}
	path := filepath.Join(dir, connector.LedgerFile)
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return nil, done, output.ErrUsageHint(fmt.Sprintf("Profile %q's connector has no ledger yet", p.name), "Run the connector first: basecamp connect -P "+shellQuote(p.name))
	}
	// Opening for a decision migrates the ledger, and a connector running on
	// an older binary's schema must not have its triggers replaced under it.
	// The instance lock is what says no connector is running — the metadata
	// beside it is diagnostic — so it is taken before the migration and held
	// until the ledger is closed.
	outOfDate := false
	if reader, err := connector.OpenLedgerReadOnly(ctx, path); err == nil {
		_ = reader.Close()
	} else if errors.Is(err, connector.ErrLedgerOutOfDate) {
		outOfDate = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, done, err
	}
	if requireStopped || outOfDate {
		lock, lockErr := connector.AcquireInstanceLock(dir, p.file.AccountID, p.file.Agent.PersonID, time.Now())
		switch {
		case errors.Is(lockErr, connector.ErrAlreadyRunning) && outOfDate:
			return nil, done, output.ErrUsageHint(
				"The connector is running on a ledger older than this build, and this command would migrate it underneath it: "+lockErr.Error(),
				"Stop the connector, run this command, and start it again.")
		case errors.Is(lockErr, connector.ErrAlreadyRunning):
			return nil, done, &output.Error{Code: output.CodeLockUnavailable, Message: lockErr.Error(),
				Hint: "Stop the connector before this command."}
		case lockErr != nil:
			return nil, done, lockErr
		}
		done = func() { _ = lock.Release() }
	}
	ledger, err := connector.OpenLedger(path) //nolint:contextcheck // OpenLedger migrates on its own context
	if err != nil {
		done()
		return nil, func() {}, err
	}
	// A verdict a redispatch writes calls for the lifecycle messages a running
	// connector's verdict would: the same intents, which the connector's outbox
	// sends.
	ledger.SetHooks(connector.LifecycleHooks(ledger, connector.LifecycleOptions{}))
	return ledger, func() {
		_ = ledger.Close()
		done()
	}, nil
}

func decisionError(err error) error {
	if errors.Is(err, connector.ErrDecisionRefused) || errors.Is(err, connector.ErrNoSuchRecord) {
		msg := strings.TrimPrefix(err.Error(), "connector: ")
		return output.ErrUsage(strings.TrimSuffix(msg, ": "+connector.ErrDecisionRefused.Error()))
	}
	return err
}

// --- status ---------------------------------------------------------------

func newConnectStatusCmd() *cobra.Command {
	var shadow bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show what the connector heard, holds and ran",
		Long: `Show the connector's ledger: whether it is running, the hold, the feed
position (whether one is held, never the position), the last poll-served id,
gaps and losses, queue depths, live tasks, retained worktrees, lifecycle
messages waiting for a person, held records, and the last 20 dispatches with
their outcomes.

It reads the ledger read-only and takes no lock, so it works while the
connector runs. It shows no content and no token.`,
		Example: `  basecamp connect status -P agent
  basecamp connect status -P agent --shadow --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runConnectStatus(cmd, shadow)
		},
	}
	cmd.Flags().BoolVar(&shadow, "shadow", false, "Show the shadow run's ledger")
	return cmd
}

// connectStatusReport is status's output.
type connectStatusReport struct {
	Profile string           `json:"profile"`
	Shadow  bool             `json:"shadow"`
	Running *connectRunning  `json:"running,omitempty"`
	Status  connector.Status `json:"status"`
}

// connectRunning is what the instance lock's holder wrote. Alive says a
// process with that pid exists now; after a crash the file stays behind, and
// the pid may since belong to another process.
type connectRunning struct {
	PID       int    `json:"pid"`
	StartedAt string `json:"started_at"`
	Alive     bool   `json:"alive"`
}

func runConnectStatus(cmd *cobra.Command, shadow bool) error {
	p, err := loadConnectProfile(cmd)
	if err != nil {
		return err
	}
	dir, err := connectStatePath(p.file, shadow)
	if err != nil {
		return err
	}
	ledger, err := connector.OpenLedgerReadOnly(cmd.Context(), filepath.Join(dir, connector.LedgerFile))
	if errors.Is(err, os.ErrNotExist) {
		return output.ErrUsageHint(fmt.Sprintf("Profile %q's connector has no ledger yet", p.name), "Run the connector first: basecamp connect -P "+shellQuote(p.name))
	}
	if err != nil {
		return err
	}
	defer func() { _ = ledger.Close() }()
	status, err := ledger.Status(cmd.Context(), nil)
	if err != nil {
		return err
	}
	for i, t := range status.Tasks {
		status.Tasks[i].Worker = recordedWorkerState(t)
	}
	report := connectStatusReport{Profile: p.name, Shadow: shadow, Status: status}
	if holder, ok := connector.InstanceHolder(dir, p.file.AccountID, p.file.Agent.PersonID); ok {
		report.Running = &connectRunning{PID: holder.PID, StartedAt: holder.StartedAt, Alive: processAlive(holder.PID)}
	}
	if p.app.Output.EffectiveFormat() == output.FormatStyled {
		renderConnectStatus(cmd.OutOrStdout(), report)
		return nil
	}
	return p.app.OK(report, output.WithSummary(connectStatusSummary(report)))
}

func connectStatusSummary(r connectStatusReport) string {
	parts := []string{}
	if r.Status.Hold != nil {
		parts = append(parts, "held")
	}
	parts = append(parts,
		fmt.Sprintf("%d live tasks", len(r.Status.Tasks)),
		fmt.Sprintf("%d held records", len(r.Status.Held)),
		fmt.Sprintf("%d indeterminate messages", len(r.Status.Indeterminate)))
	return strings.Join(parts, ", ")
}

func renderConnectStatus(w io.Writer, r connectStatusReport) {
	s := r.Status
	clean := richtext.SanitizeSingleLine
	stamp := func(t time.Time) string { return t.UTC().Format("2006-01-02 15:04:05Z") }
	title := "Connector status for profile " + strconv.Quote(r.name())
	if r.Shadow {
		title += " (shadow)"
	}
	fmt.Fprintf(w, "%s\n\n", title)

	switch {
	case r.Running != nil && r.Running.Alive:
		fmt.Fprintf(w, "  Running        pid %d since %s (as its lock file says)\n", r.Running.PID, clean(r.Running.StartedAt))
	default:
		fmt.Fprintf(w, "  Running        no\n")
	}
	if s.Connection != nil {
		fmt.Fprintf(w, "  Last run       %s at %s", clean(s.Connection.State), stamp(s.Connection.ChangedAt))
		if s.Connection.Detail != "" {
			fmt.Fprintf(w, " (%s)", clean(s.Connection.Detail))
		}
		fmt.Fprintln(w)
	}
	if s.Hold != nil {
		fmt.Fprintf(w, "  Hold           set by %s at %s (%s, generation %d): nothing dispatches or posts until release\n",
			clean(s.Hold.HeldBy), stamp(s.Hold.HeldAt), clean(s.Hold.Cause), s.Hold.Generation)
	} else {
		fmt.Fprintf(w, "  Hold           none\n")
	}
	for _, pos := range s.Positions {
		held := "no position"
		if pos.HasPosition {
			held = "position held"
		}
		fmt.Fprintf(w, "  Feed           %s; last poll-served id %d; updated %s\n", held, pos.LastPollServedID, stamp(pos.UpdatedAt))
	}
	for _, g := range s.Gaps {
		epoch := ""
		if g.EpochAfterID != nil {
			epoch = fmt.Sprintf(", epoch after %d", *g.EpochAfterID)
		}
		fmt.Fprintf(w, "  Gap            %s at %s%s\n", clean(g.Class), stamp(g.DetectedAt), epoch)
	}
	for _, l := range s.Losses {
		fmt.Fprintf(w, "  Loss           %d dropped, %d still missing, window ends %s\n", l.Dropped, l.Missing, stamp(l.DeadlineAt))
	}
	if s.Unrecovered > 0 {
		fmt.Fprintf(w, "  Unrecovered    %d event ids\n", s.Unrecovered)
	}

	fmt.Fprintf(w, "\n  Queues        ")
	for _, state := range []string{"seen", "admitted", "queued", "blocked", "dispatched", "held"} {
		fmt.Fprintf(w, " %s %d", state, s.Queues[state])
	}
	fmt.Fprintln(w)
	for reason, n := range s.Blocked {
		fmt.Fprintf(w, "  Blocked        %s: %d\n", clean(reason), n)
	}
	if s.Review > 0 || s.AuthorizedBlocked > 0 || s.RedispatchPending > 0 {
		fmt.Fprintf(w, "  Review         %d tagged, %d authorized and blocked, %d redispatches waiting for their task\n", s.Review, s.AuthorizedBlocked, s.RedispatchPending)
	}

	fmt.Fprintf(w, "\n  Live tasks     %d\n", len(s.Tasks))
	for _, t := range s.Tasks {
		fmt.Fprintf(w, "    task %d  %s  %s  pid %d (%s)  since %s  events %v  in %s\n", t.TaskID, clean(t.AttemptID), clean(t.State), t.PID, clean(t.Worker), stamp(t.LaunchedAt), t.EventIDs, clean(t.WorkDir))
	}
	if !s.WorktreesKnown {
		fmt.Fprintf(w, "  Worktrees      not tracked by this build\n")
	} else {
		fmt.Fprintf(w, "  Worktrees      %d retained\n", len(s.Worktrees))
		for _, wt := range s.Worktrees {
			fmt.Fprintf(w, "    %s %s\n", clean(wt.Path), clean(wt.Reason))
		}
	}
	fmt.Fprintf(w, "  Indeterminate  %d lifecycle messages wait for a person\n", len(s.Indeterminate))
	for _, in := range s.Indeterminate {
		fmt.Fprintf(w, "    intent %d  %s  event %d  %s on %d\n", in.ID, clean(in.Kind), in.EventID, clean(in.MessageKind), in.RecordingID)
	}
	fmt.Fprintf(w, "  Held records   %d (redispatch or discard each)\n", len(s.Held))
	for _, h := range s.Held {
		fmt.Fprintf(w, "    event %d  %s  %s  %s\n", h.EventID, clean(h.EventType), clean(h.Trigger), clean(h.RecordingURL))
	}

	fmt.Fprintf(w, "\n  Last dispatches\n")
	if len(s.Dispatches) == 0 {
		fmt.Fprintf(w, "    none\n")
	}
	for _, d := range s.Dispatches {
		outcomes := make([]string, 0, len(d.Events))
		for _, e := range d.Events {
			o := e.Outcome
			if o == "" {
				o = e.Delivery
			}
			if e.Withdrawn {
				o = "withdrawn"
			}
			outcomes = append(outcomes, fmt.Sprintf("%d:%s", e.EventID, clean(o)))
		}
		fmt.Fprintf(w, "    %s  task %d  %s %s  %s\n", stamp(d.LaunchedAt), d.TaskID, clean(d.State), clean(d.StopReason), strings.Join(outcomes, " "))
	}
	fmt.Fprintln(w)
}

func (r connectStatusReport) name() string { return r.Profile }

// --- redispatch -----------------------------------------------------------

func newConnectRedispatchCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "redispatch <event_id>",
		Short: "Authorize a record to run again, or for the first time",
		Long: `Authorize a record the connector will not run on its own.

Accepted for a completed record whose outcome is unknown or failed, every
blocked record, and a held one. Refused for a success, a discarded record, and
anything live. The replaced task's token is retired, its worker is stopped,
and who authorized it is recorded.

A completed or held record is admitted at once (a completed one whose task is
still running, when that task ends). A blocked record keeps its state and
runs what blocked it again — the read, the events lookup, the route check —
and is admitted the moment that succeeds; if it blocks again, the record stays
blocked with the authorization, and redispatch runs it again. While the hold
stands the record is authorized and nothing launches until release.

It works on the ledger's transactions, so it is safe while the connector runs;
the running connector dispatches what it admits.`,
		Example: `  basecamp connect redispatch -P agent 9876543210`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runConnectRedispatch(cmd, args[0])
		},
	}
}

// connectRedispatchReport is redispatch's output.
type connectRedispatchReport struct {
	connector.RedispatchResult
	// WorkerStopped says the replaced worker's recorded process group was
	// signaled.
	WorkerStopped bool `json:"worker_stopped,omitempty"`
	// WorkerState is what became of it: stopped, gone, held (its group still
	// runs and was not proven this task's to signal), unverified, or
	// not_recorded (the attempt had no worker process recorded yet).
	WorkerState string `json:"worker_state,omitempty"`
	WorkerNote  string `json:"worker_note,omitempty"`
	// Verdict is what running the prerequisite again decided.
	Verdict      string `json:"verdict,omitempty"`
	VerdictNote  string `json:"verdict_reason,omitempty"`
	RerunSkipped string `json:"rerun_skipped,omitempty"`
}

func runConnectRedispatch(cmd *cobra.Command, raw string) error {
	ctx := cmd.Context()
	id, err := parseEventIDArg(raw)
	if err != nil {
		return err
	}
	p, err := loadConnectProfile(cmd)
	if err != nil {
		return err
	}
	ledger, done, err := openConnectLedger(cmd.Context(), p, false)
	if err != nil {
		return err
	}
	defer done()

	res, err := ledger.Redispatch(ctx, id, operatorName())
	if err != nil {
		return decisionError(err)
	}
	report := connectRedispatchReport{RedispatchResult: res}
	if res.Worker != nil {
		stop := stopReplacedWorker(driver.Process{
			PID: res.Worker.Process.PID, PGID: res.Worker.Process.PGID, StartedAt: res.Worker.Process.StartedAt,
		}, driver.DefaultGrace)
		report.WorkerStopped, report.WorkerState, report.WorkerNote = stop.signaled, stop.state, stop.note
	}
	if res.Rerun {
		verdict, reason, err := rerunPrerequisite(ctx, p, ledger, id)
		if err != nil {
			report.RerunSkipped = err.Error()
		} else {
			report.Verdict, report.VerdictNote = verdict, reason
		}
	}
	return p.app.OK(report, output.WithSummary(redispatchSummary(report)))
}

func redispatchSummary(r connectRedispatchReport) string {
	var s string
	switch {
	case r.Pending:
		s = fmt.Sprintf("Event %d authorized; admitted when its task %d ends", r.EventID, r.SupersededTaskID)
	case r.Admitted:
		s = fmt.Sprintf("Event %d admitted", r.EventID)
	case r.Verdict != "":
		s = fmt.Sprintf("Event %d authorized; its prerequisite ran again: %s", r.EventID, r.Verdict)
		if r.VerdictNote != "" {
			s += " (" + r.VerdictNote + ")"
		}
	case r.RerunSkipped != "":
		s = fmt.Sprintf("Event %d authorized and still blocked; its prerequisite did not run (%s). Run redispatch again to retry it", r.EventID, r.RerunSkipped)
	default:
		s = fmt.Sprintf("Event %d authorized", r.EventID)
	}
	if r.Held {
		s += "; the hold stands, so nothing launches until release"
	}
	return s
}

// rerunPrerequisite decides a blocked record again, as the agent, exactly as
// the connector's admission would: the verdict is revision-guarded, so a
// running connector deciding it at the same time is not a second verdict.
func rerunPrerequisite(ctx context.Context, p connectProfile, ledger *connector.Ledger, id int64) (string, string, error) {
	agent, err := verifiedConnectAgent(ctx, p)
	if err != nil {
		return "", "", err
	}
	policy, err := p.file.Policy(agent.personID)
	if err != nil {
		return "", "", err
	}
	reads := admission.NewSDKReads(&basecamp.Config{BaseURL: p.app.Config.BaseURL}, agent.tokens, agent.account, connectSDKOptions()...)
	admitter, err := admission.NewAdmitter(policy, reads)
	if err != nil {
		return "", "", err
	}
	records := ledger.Admission()
	ev, ok, err := records.LoadUndecided(ctx, id)
	if err != nil {
		return "", "", err
	}
	if !ok {
		return "", "", errors.New("the record is no longer blocked; something else decided it")
	}
	v, err := admitter.Decide(ctx, ev)
	if err != nil {
		return "", "", err
	}
	v, err = admission.NewCommitter(records).Commit(ctx, v)
	if errors.Is(err, admission.ErrAlreadyDecided) {
		return "", "", errors.New("the running connector decided it first")
	}
	if err != nil {
		return "", "", err
	}
	return string(v.State), string(v.Reason), nil
}

// connectAgent is the agent a profile's credential proved to be, checked
// against connect.json.
type connectAgent struct {
	account  string
	personID int64
	tokens   basecamp.TokenProvider
	client   *basecamp.Client
	reader   setup.SDKReader
	kind     string
}

func verifiedConnectAgent(ctx context.Context, p connectProfile) (connectAgent, error) {
	app := p.app
	if os.Getenv("BASECAMP_TOKEN") != "" {
		return connectAgent{}, errEnvTokenShadows("the connector acts only as the agent its profile holds, and BASECAMP_TOKEN would override it")
	}
	account, err := connectAccount(app, p.name)
	if err != nil {
		return connectAgent{}, err
	}
	if !accountIDsEqual(account, p.file.AccountID) {
		return connectAgent{}, output.ErrUsage(fmt.Sprintf("connect.json was set up in account %s, and profile %q is bound to account %s", p.file.AccountID, p.name, account))
	}
	kind, err := connectCredentialKind(ctx, app)
	if err != nil {
		return connectAgent{}, err
	}
	if kind == "" {
		return connectAgent{}, output.ErrAuth(fmt.Sprintf("Profile %q holds no credential", p.name))
	}
	creds, err := app.Auth.GetStore().LoadContext(ctx, app.Auth.CredentialKey())
	if err != nil {
		return connectAgent{}, output.ErrAuth("The stored credential could not be read: " + setup.ErrorText(err))
	}
	tokens := &managerTokens{mgr: app.Auth}
	client := connectSDKClient(app, tokens)
	reader := setup.SDKReader{Client: client.ForAccount(account)}
	me, err := reader.Me(ctx)
	if err != nil {
		return connectAgent{}, output.ErrAuth(fmt.Sprintf("Could not read who profile %q is: %s", p.name, setup.ErrorText(err)))
	}
	if _, err := checkConnectIdentity(ctx, app, client, kind, creds.OAuthType, me, p.file.Agent.IdentityID); err != nil {
		return connectAgent{}, err
	}
	if err := p.file.VerifyAgent(kind, me.ID, p.file.Agent.IdentityID); err != nil {
		return connectAgent{}, output.ErrAuth(err.Error())
	}
	return connectAgent{account: account, personID: me.ID, tokens: tokens, client: client, reader: reader, kind: kind}, nil
}

// --- discard --------------------------------------------------------------

func newConnectDiscardCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "discard <event_id>",
		Short: "Close a held, blocked or unknown record without running it",
		Long: `Close a record without running it, as discarded(by_operator), and record who
decided. Accepted for a held record, a blocked one, and a completed one whose
outcome is unknown. A lifecycle message still pending for it is not sent.`,
		Example: `  basecamp connect discard -P agent 9876543210`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseEventIDArg(args[0])
			if err != nil {
				return err
			}
			p, err := loadConnectProfile(cmd)
			if err != nil {
				return err
			}
			ledger, done, err := openConnectLedger(cmd.Context(), p, false)
			if err != nil {
				return err
			}
			defer done()
			res, err := ledger.Discard(cmd.Context(), id, operatorName())
			if err != nil {
				return decisionError(err)
			}
			summary := fmt.Sprintf("Event %d discarded", id)
			if res.Already {
				summary = fmt.Sprintf("Event %d was already discarded by a person", id)
			}
			return p.app.OK(res, output.WithSummary(summary))
		},
	}
}

// --- release --------------------------------------------------------------

func newConnectReleaseCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "release",
		Short: "Clear the hold: dispatch and posting resume",
		Long: `Clear the durable hold that basecamp connect --hold or shadow promote set.
Records a person authorized, and records that arrived after the hold, dispatch;
held records stay held until each is redispatched or discarded.`,
		Example: `  basecamp connect release -P agent`,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := loadConnectProfile(cmd)
			if err != nil {
				return err
			}
			ledger, done, err := openConnectLedger(cmd.Context(), p, false)
			if err != nil {
				return err
			}
			defer done()
			res, err := ledger.Release(cmd.Context(), operatorName())
			if err != nil {
				return err
			}
			summary := "No hold stood"
			if res.Released {
				summary = fmt.Sprintf("Released; %d held records stay held", res.StillHeld)
			}
			return p.app.OK(res, output.WithSummary(summary))
		},
	}
}

// --- shadow promote -------------------------------------------------------

func newConnectShadowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "shadow",
		Short: "Work with a shadow run's state",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "promote",
		Short: "Make the shadow ledger the connector's, held",
		Long: `Turn the shadow run's ledger into the connector's under the hold.

Both the shadow connector and the connector must be stopped: promote takes
both instance locks. In one transaction it sets the hold and tags every
non-terminal shadow record for review, then moves the ledger into the
connector's state directory. A crash at any point leaves either the untouched
shadow or a held ledger; run promote again to finish.

Start the connector afterwards: intake continues from the promoted position,
nothing dispatches until basecamp connect release, and held records wait for
redispatch or discard.`,
		Example: `  basecamp connect shadow promote -P agent`,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := loadConnectProfile(cmd)
			if err != nil {
				return err
			}
			shadowDir, err := connectStatePath(p.file, true)
			if err != nil {
				return err
			}
			stateHome, err := connectStateHome()
			if err != nil {
				return err
			}
			parent, err := ensurePrivateChain(stateHome, "basecamp", "connect")
			if err != nil {
				return output.ErrUsage("The connector's state directory cannot be used: " + err.Error())
			}
			res, err := connector.PromoteShadow(cmd.Context(), connector.PromoteOptions{
				ShadowDir: shadowDir,
				StateDir:  filepath.Join(parent, connector.StateDirName(p.file.AccountID, p.file.Agent.PersonID)),
				AccountID: p.file.AccountID,
				AgentID:   p.file.Agent.PersonID,
				By:        operatorName(),
			})
			switch {
			case errors.Is(err, connector.ErrAlreadyRunning):
				return &output.Error{Code: output.CodeLockUnavailable, Message: err.Error(),
					Hint: "Stop the shadow connector and the connector first; promote never stops a process itself."}
			case errors.Is(err, connector.ErrNoShadowLedger), errors.Is(err, connector.ErrLedgerExists):
				return output.ErrUsage(strings.TrimPrefix(err.Error(), "connector: "))
			case err != nil:
				return err
			}
			summary := fmt.Sprintf("Promoted under the hold: %d records tagged for review, %d held", res.Tagged, res.Held)
			if res.Already {
				summary = "Already promoted; the ledger is held"
			}
			return p.app.OK(res, output.WithSummary(summary))
		},
	})
	return cmd
}

// --- import ---------------------------------------------------------------

// maxReconciliationBytes bounds a reconciliation file.
const maxReconciliationBytes = 16 << 20

func newConnectImportCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "import <file>",
		Short: "Apply a cutover reconciliation file to the ledger",
		Long: `Apply a reconciliation file in one transaction: a tombstone for every entry
decided done, and the review tag on every other non-terminal record, each
keeping its state and blocking reason. A file with an entry that cannot be
applied changes nothing. The connector must be stopped.

The file is JSON:
  {"version": 1, "entries": [{"event_id": 123, "decision": "done"},
                             {"event_id": 456, "decision": "held"}]}`,
		Example: `  basecamp connect import -P agent reconciliation.json`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := loadConnectProfile(cmd)
			if err != nil {
				return err
			}
			data, err := readReconciliation(args[0])
			if err != nil {
				return err
			}
			r, err := connector.ParseReconciliation(data)
			if err != nil {
				return output.ErrUsage(strings.TrimPrefix(err.Error(), "connector: "))
			}
			dir, err := connectStatePath(p.file, false)
			if err != nil {
				return err
			}
			if _, err := os.Lstat(filepath.Join(dir, connector.LedgerFile)); errors.Is(err, os.ErrNotExist) {
				return output.ErrUsageHint(fmt.Sprintf("Profile %q's connector has no ledger yet", p.name), "Promote the shadow first: basecamp connect shadow promote -P "+shellQuote(p.name))
			}
			// The connector must be stopped: the open takes the instance lock
			// and holds it for the import.
			ledger, done, err := openConnectLedger(cmd.Context(), p, true)
			if err != nil {
				return err
			}
			defer done()
			res, err := ledger.Import(cmd.Context(), r, operatorName())
			if err != nil {
				return decisionError(err)
			}
			return p.app.OK(res, output.WithSummary(fmt.Sprintf("Imported %d entries: %d tombstoned, %d tombstones added, %d records tagged for review",
				len(r.Entries), res.Tombstoned, res.Inserted, res.Tagged)))
		},
	}
}

func readReconciliation(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, output.ErrUsage(fmt.Sprintf("Cannot read %s: %v", richtext.SanitizeSingleLine(path), err))
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxReconciliationBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxReconciliationBytes {
		return nil, output.ErrUsage("The reconciliation file is larger than 16 MB")
	}
	return data, nil
}
