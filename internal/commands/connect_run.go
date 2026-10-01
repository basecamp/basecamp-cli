package commands

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp/eventfeed"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/auth"
	"github.com/basecamp/basecamp-cli/internal/config"
	"github.com/basecamp/basecamp-cli/internal/connector"
	"github.com/basecamp/basecamp-cli/internal/connector/admission"
	"github.com/basecamp/basecamp-cli/internal/connector/ndjson"
	"github.com/basecamp/basecamp-cli/internal/connector/setup"
	"github.com/basecamp/basecamp-cli/internal/output"
	"github.com/basecamp/basecamp-cli/internal/richtext"
)

// connectRunFlags are the run's flags.
type connectRunFlags struct {
	projects []string
	shadow   bool
	since    int64
	hold     bool
}

func addConnectRunFlags(cmd *cobra.Command, f *connectRunFlags) {
	fl := cmd.Flags()
	// --project shadows the global flag of the same name and keeps its type,
	// so the flag reads the same everywhere; here it may be repeated.
	fl.Var((*repeatedString)(&f.projects), "project", "Only hear events in this project id (repeatable; default every project the agent can see)")
	fl.BoolVar(&f.shadow, "shadow", false, "Admit and log in an isolated state directory; dispatch and post nothing")
	fl.Int64Var(&f.since, "since", 0, "Enter the feed just after this event id, whatever the ledger holds")
	fl.BoolVar(&f.hold, "hold", false, "Set the durable hold: intake and admission run, nothing is dispatched or posted until the hold is released, and earlier records wait for review")
}

// connectStateHome is the directory holding the connector's state root, from
// connector.StateRoot so the connector and the worker's MCP server agree on
// one place.
func connectStateHome() (string, error) {
	root, err := connector.StateRoot()
	if err != nil {
		return "", err
	}
	// StateRoot is <home>/basecamp/connect; the chain is created from its
	// grandparent so each directory is made owner-only.
	return filepath.Dir(filepath.Dir(root)), nil
}

// ensurePrivateChain creates each missing directory from root down to dir
// owner-only, and refuses any that someone else could change.
func ensurePrivateChain(root string, parts ...string) (string, error) {
	dir := root
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", err
	}
	for _, p := range parts {
		dir = filepath.Join(dir, p)
		if err := setup.EnsurePrivateDir(dir); err != nil {
			return "", err
		}
	}
	return dir, nil
}

// connectStateDir is the connector's state directory for a set-up profile,
// created owner-only: $XDG_STATE_HOME/basecamp/connect/<account>-<agent>, or
// connect-shadow for a shadow run. Everything that reads the connector's
// state (status) resolves it here.
func connectStateDir(file setup.File, shadow bool) (string, error) {
	stateHome, err := connectStateHome()
	if err != nil {
		return "", err
	}
	return ensurePrivateChain(stateHome, connectStateParts(file, shadow)...)
}

// connectStatePath is connectStateDir's path, created nothing: for commands
// that only read the connector's state, and must not make a directory to do
// it.
func connectStatePath(file setup.File, shadow bool) (string, error) {
	stateHome, err := connectStateHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(append([]string{stateHome}, connectStateParts(file, shadow)...)...), nil
}

func connectStateParts(file setup.File, shadow bool) []string {
	group := "connect"
	if shadow {
		// An isolated ledger, lock and checkpoint: a shadow never shares a
		// position or a record with the connector it watches beside.
		group = "connect-shadow"
	}
	return []string{"basecamp", group, connector.StateDirName(file.AccountID, file.Agent.PersonID)}
}

func runConnect(cmd *cobra.Command, f *connectRunFlags) error {
	if !connectSupportedOS(runtime.GOOS) {
		return connectUnsupportedOSError(runtime.GOOS)
	}
	app := appctx.FromContext(cmd.Context())
	ctx := cmd.Context()

	name := app.Config.ActiveProfile
	if name == "" {
		return output.ErrUsageHint("The connector needs the agent's profile", "Pass -P/--profile <name>, a profile set up with `basecamp connect setup`.")
	}
	if !isValidProfileName(name) {
		return output.ErrUsage(fmt.Sprintf("Invalid profile name %q", name))
	}
	if os.Getenv("BASECAMP_TOKEN") != "" {
		return errEnvTokenShadows("the connector acts only as the agent its profile holds, and BASECAMP_TOKEN would override it")
	}
	buckets, err := parseProjectIDs(f.projects)
	if err != nil {
		return err
	}
	// Before the account is read or the feed is touched: a flag that cannot
	// mean anything is a mistake to say so about, not one to act around.
	since, err := connectSinceOverride(f.since)
	if err != nil {
		return err
	}

	path, err := setup.Path(config.GlobalConfigDir(), name)
	if err != nil {
		return output.ErrUsage(err.Error())
	}
	file, err := setup.Load(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return output.ErrUsageHint(fmt.Sprintf("Profile %q is not set up as a connector", name), "Run: basecamp connect setup -P "+shellQuote(name))
	case err != nil:
		return output.ErrUsage("connect.json cannot be used: " + err.Error())
	}

	account, err := connectAccount(app, name)
	if err != nil {
		return err
	}
	if !accountIDsEqual(account, file.AccountID) {
		return output.ErrUsage(fmt.Sprintf("connect.json was set up in account %s, and profile %q is bound to account %s", file.AccountID, name, account))
	}
	kind, err := connectCredentialKind(ctx, app)
	if err != nil {
		return err
	}
	if kind == "" {
		return output.ErrAuth(fmt.Sprintf("Profile %q holds no credential", name))
	}
	creds, err := app.Auth.GetStore().LoadContext(ctx, app.Auth.CredentialKey())
	if err != nil {
		return output.ErrAuth("The stored credential could not be read: " + setup.ErrorText(err))
	}
	// The lock first, under the agent connect.json names: a second
	// connector for an agent already running says so now, not after
	// waiting out a rate limit for a token it would never use. VerifyAgent
	// below holds the credential to that same person.
	stateDir, err := connectStateDir(file, f.shadow)
	if err != nil {
		return output.ErrUsage("The connector's state directory cannot be used: " + err.Error())
	}
	lock, err := connector.AcquireInstanceLock(stateDir, account, file.Agent.PersonID, time.Now())
	if err != nil {
		if errors.Is(err, connector.ErrAlreadyRunning) {
			return &output.Error{Code: output.CodeLockUnavailable, Message: err.Error()}
		}
		return err
	}
	defer func() { _ = lock.Release() }()

	logger := slog.New(slog.NewTextHandler(cmd.ErrOrStderr(), nil))
	tokens := &managerTokens{mgr: app.Auth}
	// Every read below needs a token first. One the running feed would
	// wait for is waited for here too, rather than ending the start.
	startSignals, stopStartSignals := connector.NotifyShutdown()
	err = awaitConnectToken(ctx, tokens, connectStartWait{Log: func(line string) { logger.Warn(line) }, Signals: startSignals})
	stopStartSignals()
	select {
	case sig := <-startSignals:
		// Delivered after the wait had finished, before the stop took.
		return connectStoppedBySignal(sig)
	default:
	}
	if err != nil {
		return connectStartFailure(ctx, kind, name, err)
	}
	client := connectSDKClient(app, tokens)
	accountClient := client.ForAccount(account)
	me, err := (setup.SDKReader{Client: accountClient}).Me(ctx)
	if err != nil {
		return connectStartFailure(ctx, kind, name, err)
	}
	if _, err := checkConnectIdentity(ctx, app, client, kind, creds.OAuthType, me, file.Agent.IdentityID); err != nil {
		return err
	}
	if err := file.VerifyAgent(kind, me.ID, file.Agent.IdentityID); err != nil {
		return output.ErrAuth(err.Error())
	}
	agentID := me.ID

	policy, err := file.Policy(agentID)
	if err != nil {
		return output.ErrUsage(err.Error())
	}
	policy.Buckets = buckets

	ledger, err := connector.OpenLedger(filepath.Join(stateDir, connector.LedgerFile))
	if err != nil {
		return err
	}
	defer func() { _ = ledger.Close() }()

	if f.hold {
		// Before intake starts: nothing this run admits may dispatch ahead of
		// the marker.
		held, err := ledger.SetHold(ctx, operatorName(), connector.HoldByOperator)
		if err != nil {
			return err
		}
		logger.Info("connector: held", "generation", held.Hold.Generation, "tagged_for_review", held.Tagged, "held", held.Held)
	}
	if hold, ok, err := ledger.HoldMarker(ctx); err != nil {
		return err
	} else if ok {
		logger.Warn("connector: the hold stands; nothing is dispatched or posted until `basecamp connect release`",
			"since", hold.HeldAt, "by", richtext.SanitizeSingleLine(hold.HeldBy))
	}
	lines := ndjson.NewWriter(cmd.OutOrStdout())

	queue, err := connector.NewQueue(connector.DefaultBacklogWarn, connector.DefaultBacklogPause)
	if err != nil {
		return err
	}
	live, err := eventfeed.NewLive(&basecamp.Config{BaseURL: app.Config.BaseURL}, &feedTokens{*tokens}, account, eventfeed.AccountLane, connectSDKOptions()...)
	if err != nil {
		return err
	}
	intakeOpts := connector.LiveOptions(live)
	intakeOpts.AccountID = account
	intakeOpts.ConsumerNamespace = connectConsumerNamespace(agentID, f.shadow)
	intakeOpts.Filters = eventfeed.Filters{Buckets: buckets, ExcludePerformers: []int64{agentID}, ActorTypes: []string{"person"}}
	intakeOpts.SinceEventID = since
	intakeOpts.Ledger = ledger
	intakeOpts.Queue = queue
	intakeOpts.Lines = lines
	intakeOpts.Logger = logger
	intakeOpts.Membership = connector.SDKMembership{Client: accountClient}
	intake, err := connector.New(intakeOpts)
	if err != nil {
		return err
	}

	// connect.json's served projects as they are now, for admission and the
	// handoff alike: both reload from the same place and share one cache, so
	// a setup change reaches the next decision without a restart. A reading
	// is at most connectServedTTL old — see TestConnectServedTTLStaysSmall.
	served := newConnectServed(path, file, logger)

	reads := admission.NewSDKReads(&basecamp.Config{BaseURL: app.Config.BaseURL}, tokens, account, connectSDKOptions()...)
	admitter, err := admission.NewAdmitter(policy, reads, admission.WithServed(served.Current))
	if err != nil {
		return output.ErrUsage(err.Error())
	}

	signals, stopSignals := connector.NotifyShutdown()
	defer stopSignals()
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		received os.Signal
		mu       sync.Mutex
	)
	go func() {
		select {
		case sig := <-signals:
			mu.Lock()
			received = sig
			mu.Unlock()
			logger.Info("connector: shutting down", "signal", sig.String())
			cancel()
		case <-runCtx.Done():
			return
		}
		// A second signal is a person who has waited long enough: the
		// settlement each live attempt is in the middle of may be waiting on
		// Basecamp, and this leaves it for the next start to recover rather
		// than making them wait.
		sig := <-signals
		logger.Error("connector: stopping now; live attempts are left for the next start to settle", "signal", sig.String())
		os.Exit(connector.ExitCodeForSignal(sig))
	}()

	stopState, stopDetail := connector.ConnectionStopped, ""
	defer func() {
		// Whatever ended the run, status says it is not running any more.
		_ = ledger.NoteConnection(context.WithoutCancel(ctx), stopState, stopDetail)
	}()
	logger.Info("connector: running", connectRunningAttrs(name, account, agentID, f.shadow, len(file.Projects), buckets, stateDir)...)

	var (
		wg       sync.WaitGroup
		errOnce  sync.Once
		firstErr error
	)
	runPart := func(part string, fn func(context.Context) error) {
		wg.Go(func() {
			err := fn(runCtx)
			if runCtx.Err() == nil {
				// Whether it failed or simply returned, this part has stopped
				// while the rest were still running: the connector is not
				// doing its job, and must not exit as though it were.
				errOnce.Do(func() {
					if err == nil {
						err = errors.New("stopped on its own")
					}
					firstErr = fmt.Errorf("%s: %w", part, err)
				})
			}
			// One part ending ends the connector: intake without admission,
			// or dispatch without intake, is a connector silently doing half
			// its job.
			cancel()
		})
	}
	if err := ledger.NoteConnection(ctx, connector.ConnectionRunning, ""); err != nil {
		logger.Warn("connector: could not record that it runs, for status", "error", err)
	}
	runPart("intake", intake.Run)
	runPart("admission", func(ctx context.Context) error {
		return connector.RunAdmission(ctx, connector.AdmissionOptions{Ledger: ledger, Queue: queue, Admitter: admitter, Lines: lines, Logger: logger})
	})
	if !f.shadow {
		// Each trusted request goes to stdout for the session that started
		// the connector, and the connector is done with it.
		started := time.Now()
		runPart("handoff", func(ctx context.Context) error {
			return connector.RunHandoff(ctx, connector.HandoffOptions{
				Ledger: ledger, Served: served.Current, Buckets: buckets, AgentID: agentID,
				Lines: lines, Logger: logger, Started: started,
			})
		})
	}
	wg.Wait()

	mu.Lock()
	sig := received
	mu.Unlock()
	switch {
	case sig != nil:
		return connectStoppedBySignal(sig)
	case firstErr != nil:
		var err error
		refused := func() bool { return confirmAgentRefused(context.WithoutCancel(ctx), app, kind, name) }
		stopState, stopDetail, err = connectorStoppedBy(firstErr, me.Name, name, refused)
		if err != firstErr { //nolint:errorlint // identity: was firstErr put in the person's words
			logger.Error("connector: Basecamp refused the agent's credential", "error", firstErr)
		}
		return err
	case ctx.Err() != nil:
		return ctx.Err()
	}
	return nil
}

// connectorStoppedBy is what the connector exits with when one of its parts
// stopped it with err, and the connection state status keeps for that. The
// stop people meet — the agent was disconnected in Basecamp, or connected on
// another computer — is said in their words; anything else is returned as it
// is. The feed's authorization_failed counts forbidden answers too, so it is
// called a disconnect only when refused, asking Basecamp, confirms it; a
// refused token renewal is already Basecamp's answer.
func connectorStoppedBy(err error, agent, profile string, refused func() bool) (state, detail string, exit error) {
	disconnected := errors.Is(err, auth.ErrAgentCredentialRefused) || (feedAuthorizationFailed(err) && refused())
	if !disconnected {
		return connector.ConnectionStopped, "", err
	}
	e := errAgentDisconnected(agent, profile)
	return connector.ConnectionDisconnected, e.Message, e
}

// connectStartFailure is what a start that could not learn who it is exits
// with. Stopped by a signal, or because ctx itself ended, it says so as it
// is; a request's own timeout is not the connector stopping.
// Disconnected while the connector wasn't running, it is said the way a
// running connector says it, with no name, since reading it failed.
// Anything else is a failure to read who the profile is.
func connectStartFailure(ctx context.Context, kind, profile string, err error) error {
	var e *output.Error
	switch {
	case ctx.Err() != nil,
		errors.As(err, &e) && (e.Code == output.CodeTerminated || e.Code == output.CodeInterrupted):
		return err
	case agentDisconnectedAtStart(kind, err):
		return errAgentDisconnected("", profile)
	}
	return output.ErrAuth(fmt.Sprintf("Could not read who profile %q is: %s", profile, setup.ErrorText(err)))
}

// agentDisconnectedAtStart reports whether the connector's first read of who
// it is failed because Basecamp refused an Agent's credential: a token
// renewal it refused, or a token minted before the disconnect that it no
// longer takes (disconnecting doesn't revoke tokens, so one can still be
// cached). A bot user's login keeps its own error.
func agentDisconnectedAtStart(kind string, err error) bool {
	if kind != setup.KindAgent || err == nil {
		return false
	}
	var apiErr *basecamp.Error
	return errors.Is(err, auth.ErrAgentCredentialRefused) ||
		(errors.As(err, &apiErr) && apiErr.HTTPStatus == http.StatusUnauthorized)
}

// confirmAgentRefused asks Basecamp whether it still takes the profile's
// Agent credential. Only an Agent is asked: a bot user whose login was
// revoked keeps its own error and remedy.
func confirmAgentRefused(ctx context.Context, app *appctx.App, kind, name string) bool {
	if kind != setup.KindAgent {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	refused, _ := agentCredentialRefused(ctx, app, name)
	return refused
}

// feedAuthorizationFailed reports whether err is the event feed ending on
// repeated authorization failures.
func feedAuthorizationFailed(err error) bool {
	var terminal *eventfeed.TerminalError
	return errors.As(err, &terminal) && terminal.Reason == eventfeed.ReasonAuthorizationFailed
}

// errAgentDisconnected says the connector stopped because its agent was
// disconnected, in the words guided setup uses for the same thing, and how to
// reconnect.
func errAgentDisconnected(agent, profile string) *output.Error {
	who := richtext.SanitizeSingleLine(agent)
	if who == "" {
		who = "Your agent"
	}
	e := output.ErrAuth(who + " was disconnected in Basecamp, or connected on another computer")
	e.Hint = "Reconnect it: basecamp connect setup -P " + richtext.ShellQuote(profile)
	return e
}

// connectSinceOverride is the feed position --since asks for. Zero is the
// default and means "resume from the ledger"; intake takes only a positive
// value as an override (connector.Options.SinceEventID), so a negative one
// would be accepted here and then quietly ignored there — the run would
// resume from the ledger while the person who typed it believes they moved
// the position.
func connectSinceOverride(since int64) (int64, error) {
	if since < 0 {
		return 0, output.ErrUsage("--since takes the event id to enter the feed just after; the default, 0, resumes from the ledger")
	}
	return since, nil
}

// connectConsumerNamespace names a run's checkpoint lineage. A shadow run
// gets its own: it has its own state directory, ledger, lock and checkpoint
// already (connectStateDir), and intake's contract is that two connectors in
// one account never share a lineage (connector.Options.ConsumerNamespace) —
// a shadow running beside the connector it watches is two.
func connectConsumerNamespace(agentID int64, shadow bool) string {
	name := "basecamp-connect-" + strconv.FormatInt(agentID, 10)
	if shadow {
		return name + "-shadow"
	}
	return name
}

// connectSupportedOS is where the connector runs: Linux and macOS, the
// platforms it has been tested on.
func connectSupportedOS(goos string) bool {
	return goos == "linux" || goos == "darwin"
}

// connectSupportedOSReason is why, in one place: the run command's refusal and
// doctor's Platform check say the same thing, so a person who meets one and
// then the other is not told two different stories about their machine.
const connectSupportedOSReason = "it has only been tested there"

// connectUnsupportedOSError is the refusal on a platform the connector does
// not run on, given by the run command and by `service install`, which would
// otherwise write a unit that supervises a process that cannot start.
//
// It opens on the connector rather than on "basecamp connect ..." because a
// refusal is prose, not a command to run, and a hint that begins like a
// command is read as one — by a person, and by TestHintCommandsResolve.
func connectUnsupportedOSError(goos string) error {
	return output.ErrUsage(fmt.Sprintf("The connector runs on Linux and macOS only, not %s: %s", goos, connectSupportedOSReason))
}

// connectServed is connect.json's served projects as they are now, not as
// they were at start: a project removed by `connect setup --unserve` stops
// being handed off without a restart. A file that no longer loads, or
// that now names another agent or account, authorizes nothing.
type connectServed struct {
	path     string
	agent    setup.Agent
	account  string
	log      *slog.Logger
	now      func() time.Time
	mu       sync.Mutex
	loadedAt time.Time
	projects map[int64]admission.Project
	// err is why the last reload could not answer. It is kept apart from an
	// empty map on purpose: "the operator serves no projects" and "nothing
	// could read the file" are different answers, and only the first is
	// safe to tell a person on a card (Copilot on #765).
	err     error
	failing bool
}

// connectServedTTL is how long a read of connect.json is reused.
const connectServedTTL = 2 * time.Second

func newConnectServed(path string, file setup.File, log *slog.Logger) *connectServed {
	return &connectServed{path: path, agent: file.Agent, account: file.AccountID, log: log, now: time.Now}
}

// Current returns a copy of the projects connect.json serves as of the last
// read, or the reason it could not be read. "As of the last read" is the
// honest span: a reading is reused for connectServedTTL, and no lock is
// taken, so a `connect setup --unserve` can complete between any read and
// whatever the caller goes on to do with it. Dispatch treats an error as authorizing
// nothing; admission holds the record as a configuration error rather than
// answering that the project is not served.
func (r *connectServed) Current() (map[int64]admission.Project, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// A failure is cached for the TTL exactly as an answer is. Reloading on
	// every call while the file is broken would read it once per event in
	// admission and once per tick in the handoff (Copilot on #765); the answer
	// would not change, and the log line is written once either way.
	if (r.projects == nil && r.err == nil) || r.now().Sub(r.loadedAt) >= connectServedTTL {
		r.reload()
	}
	return r.answer()
}

// answer is the reader's answer from whatever the last reload left: a copy of
// the served projects, or the reason there is none. r.mu is held.
func (r *connectServed) answer() (map[int64]admission.Project, error) {
	if r.err != nil {
		return nil, r.err
	}
	out := make(map[int64]admission.Project, len(r.projects))
	for k, v := range r.projects {
		out[k] = v
	}
	return out, nil
}

func (r *connectServed) reload() {
	r.loadedAt = r.now()
	file, err := setup.Load(r.path)
	switch {
	case err != nil:
		err = fmt.Errorf("connect.json cannot be read: %w", err)
	case file.Agent != r.agent || file.AccountID != r.account:
		err = errors.New("connect.json now names another agent or account")
	}
	if err != nil {
		if !r.failing {
			r.log.Error("connector: handing off nothing until connect.json is usable again", "error", err)
		}
		r.failing = true
		r.projects, r.err = nil, err
		return
	}
	if r.failing {
		r.log.Info("connector: connect.json is usable again")
	}
	r.failing = false
	r.err = nil
	r.projects = make(map[int64]admission.Project, len(file.Projects))
	for bucket, project := range file.Projects {
		r.projects[bucket] = project
	}
}

func parseProjectIDs(raw []string) ([]int64, error) {
	var out []int64
	for _, r := range raw {
		id, err := parsePositiveID("--project", r)
		if err != nil {
			return nil, err
		}
		if id == 0 {
			return nil, output.ErrUsage("Invalid --project \"\": expected a numeric id")
		}
		if !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return out, nil
}

// repeatedString is a string flag that may be given more than once, or as a
// comma-separated list.
type repeatedString []string

func (r *repeatedString) String() string { return strings.Join(*r, ",") }

func (r *repeatedString) Set(v string) error {
	for _, part := range strings.Split(v, ",") {
		*r = append(*r, strings.TrimSpace(part))
	}
	return nil
}

func (r *repeatedString) Type() string { return "string" }

// connectRunningAttrs is what the connector logs as it starts. It says how
// many projects the agent serves, and, only when --project narrowed the run,
// which ones this run is limited to: a bare count of the narrowing read as
// "serving nothing" when there was none.
func connectRunningAttrs(profile, account string, agentID int64, shadow bool, served int, only []int64, stateDir string) []any {
	attrs := []any{"profile", richtext.SanitizeSingleLine(profile), "account", account,
		"agent_person_id", agentID, "shadow", shadow, "served_projects", served}
	if len(only) > 0 {
		attrs = append(attrs, "only_projects", only)
	}
	return append(attrs, "state", richtext.SanitizeSingleLine(stateDir))
}
