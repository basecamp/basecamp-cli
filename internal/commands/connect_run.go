package commands

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp/eventfeed"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/config"
	"github.com/basecamp/basecamp-cli/internal/connector"
	"github.com/basecamp/basecamp-cli/internal/connector/admission"
	"github.com/basecamp/basecamp-cli/internal/connector/driver"
	"github.com/basecamp/basecamp-cli/internal/connector/driver/acp"
	"github.com/basecamp/basecamp-cli/internal/connector/driver/spawn"
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
	driver   string
	adapters string
	hold     bool
}

func addConnectRunFlags(cmd *cobra.Command, f *connectRunFlags) {
	fl := cmd.Flags()
	// --project shadows the global flag of the same name and keeps its type,
	// so the flag reads the same everywhere; here it may be repeated.
	fl.Var((*repeatedString)(&f.projects), "project", "Only hear events in this project id (repeatable; default every project the agent can see)")
	fl.BoolVar(&f.shadow, "shadow", false, "Admit and log in an isolated state directory; dispatch and post nothing")
	fl.Int64Var(&f.since, "since", 0, "Enter the feed just after this event id, whatever the ledger holds")
	fl.StringVar(&f.driver, "driver", "", "Override connect.json's driver (spawn or acp)")
	fl.StringVar(&f.adapters, "acp-adapters", "", "Where the pinned ACP adapters are installed, for --driver acp (default $XDG_DATA_HOME/basecamp/acp-adapters)")
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

// connectSessionsDir is where a session's short-lived files go — the MCP
// configuration, and the socket that hands over a task token. Never
// under the state directory or a working directory, which outlive the session
// and which other tools read: under $XDG_RUNTIME_DIR, the per-user,
// memory-backed directory made for exactly this, or /tmp where there is none.
// Not the platform's temporary directory: on macOS that path is too long for
// a unix socket inside it. Owner-only, and swept when the connector starts.
func connectSessionsDir(file setup.File) (string, error) {
	dir := connectSessionsPath(file)
	if err := setup.EnsurePrivateDir(dir); err != nil {
		return "", fmt.Errorf("the connector's session directory cannot be used: %w", err)
	}
	return dir, nil
}

// connectSessionsPath is where a run's session directories go, without making
// anything: the per-user runtime directory, which is short and cleared when
// the user logs out, and /tmp where there is none.
func connectSessionsPath(file setup.File) string {
	base := os.Getenv("XDG_RUNTIME_DIR")
	if info, err := os.Stat(base); base == "" || !filepath.IsAbs(base) || err != nil || !info.IsDir() {
		base = "/tmp"
	}
	return filepath.Join(base, "bcc-"+connector.StateDirName(file.AccountID, file.Agent.PersonID))
}

// connectShutdownFlush bounds how long a stopping connector spends posting
// the completion notices of the attempts it stopped. What it cannot post in
// time stays pending in the outbox and goes out on the next start.
const connectShutdownFlush = 15 * time.Second

// connectStartBound bounds how long a starting connector spends settling the
// lifecycle messages a previous process left, before intake and dispatch run.
const connectStartBound = 2 * time.Minute

// connectDriver is the driver connect.json (or --driver) names for its
// worker. The acp driver runs the worker's pinned ACP adapter, found where it
// was installed; nothing is downloaded here.
func connectDriver(name, worker, adaptersDir string) (driver.Driver, error) {
	if name != setup.DriverACP {
		return spawn.New(worker, spawn.Options{})
	}
	if adaptersDir != "" && !filepath.IsAbs(adaptersDir) {
		abs, err := filepath.Abs(adaptersDir)
		if err != nil {
			return nil, err
		}
		adaptersDir = abs
	}
	return acp.ForWorker(worker, adaptersDir, nil)
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
	driverName := file.Driver
	if f.driver != "" {
		driverName = f.driver
	}
	if !f.shadow && driverName != setup.DriverSpawn && driverName != setup.DriverACP {
		return output.ErrUsage(fmt.Sprintf("driver %q is not %q or %q", driverName, setup.DriverSpawn, setup.DriverACP))
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
	tokens := &managerTokens{mgr: app.Auth}
	client := connectSDKClient(app, tokens)
	accountClient := client.ForAccount(account)
	me, err := (setup.SDKReader{Client: accountClient}).Me(ctx)
	if err != nil {
		return output.ErrAuth(fmt.Sprintf("Could not read who profile %q is: %s", name, setup.ErrorText(err)))
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

	stateDir, err := connectStateDir(file, f.shadow)
	if err != nil {
		return output.ErrUsage("The connector's state directory cannot be used: " + err.Error())
	}
	lock, err := connector.AcquireInstanceLock(stateDir, account, agentID, time.Now())
	if err != nil {
		if errors.Is(err, connector.ErrAlreadyRunning) {
			return &output.Error{Code: output.CodeLockUnavailable, Message: err.Error()}
		}
		return err
	}
	defer func() { _ = lock.Release() }()

	ledger, err := connector.OpenLedger(filepath.Join(stateDir, connector.LedgerFile))
	if err != nil {
		return err
	}
	defer func() { _ = ledger.Close() }()

	logger := slog.New(slog.NewTextHandler(cmd.ErrOrStderr(), nil))
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
	live, err := eventfeed.NewLive(&basecamp.Config{BaseURL: app.Config.BaseURL}, tokens, account, eventfeed.AccountLane, connectSDKOptions()...)
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

	// connect.json's served projects as they are now, for admission and for
	// dispatch alike. What one reader buys is that neither half reads the
	// startup file any more: both reload from the same place, share one
	// cache, and treat a failed read the same way. Admission deciding
	// against the file as it was at startup while the dispatcher read the
	// current one is what left an unserved project's events admitted and
	// never started — no work and no holding reply — and a newly served
	// project's blocked until a restart (Copilot on #765).
	//
	// It is not a shared snapshot, and the sentence above is not saying it
	// is. Admission and dispatch call Current independently, and the cache
	// can expire between the two calls, so a setup change landing in that
	// gap is seen by one and not the other (Copilot on #765). The
	// difference from the bug this replaced is that the disagreement is
	// bounded: one decision against a served set at most connectServedTTL
	// old, where the startup file never caught up at all. That bound is
	// asserted, not merely described — see TestConnectServedTTLStaysSmall.
	//
	// One place does hold a snapshot across a whole decision, and it is the
	// one where a stale reading would start work: served.Authorize reads
	// connect.json under `connect setup`'s own lock and keeps the lock until
	// the launch has committed. Admission keeps the cache: it decides once
	// per event, a file lock per event is not a thing to put on that path,
	// and the cost of it being a reading rather than a lock is a record
	// admitted into a project that has just stopped being served — which
	// dispatch then refuses and reportStranded names.
	served := newConnectServed(path, file, logger)

	reads := admission.NewSDKReads(&basecamp.Config{BaseURL: app.Config.BaseURL}, tokens, account, connectSDKOptions()...)
	admitter, err := admission.NewAdmitter(policy, reads, admission.WithServed(served.Current))
	if err != nil {
		return output.ErrUsage(err.Error())
	}

	var (
		dispatcher *connector.Dispatcher
		outbox     *connector.Outbox
	)
	if !f.shadow {
		// Lifecycle messages: the hooks write each intent in its transition's
		// transaction, so they are installed before anything transitions. A
		// shadow run installs none: it posts nothing, and a shadow ledger
		// promoted later must carry nothing to send.
		ledger.SetHooks(connector.LifecycleHooks(ledger, connector.LifecycleOptions{}))
		poster, err := connector.NewBasecampPoster(accountClient, agentID)
		if err != nil {
			return err
		}
		outbox, err = connector.NewOutbox(connector.OutboxOptions{Ledger: ledger, Poster: poster, Paused: ledger.Held, Lines: lines, Logger: logger})
		if err != nil {
			return err
		}
		exe, err := os.Executable()
		if err != nil {
			return fmt.Errorf("locate this binary for the worker's MCP server: %w", err)
		}
		sessions, err := connectSessionsDir(file)
		if err != nil {
			return err
		}
		worker, err := connectDriver(driverName, file.WorkerName(), f.adapters)
		if err != nil {
			return output.ErrUsage(err.Error())
		}
		options := connectDispatcherOptions(connectDispatch{
			File: file, Buckets: buckets, Ledger: ledger, Driver: worker,
			Served: served.Current, Authorize: served.Authorize,
			Profile: name, Executable: exe, StateDir: stateDir, SessionsDir: sessions,
			// Replies are listed with their words, so the connector's own
			// notices are left out even before their receipts are known, and
			// no reply is ever adopted from one. That is the whole filter:
			// an id-only predicate beside it would ask the ledger again for
			// every reply, outside the adoption budget, for nothing.
			Replies: connector.LifecycleFilteredReplies{Lister: poster, Ledger: ledger},
			Lines:   lines, Logger: logger,
		})
		dispatcher, err = connector.NewDispatcher(options)
		if err != nil {
			return err
		}
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
			logger.Info("connector: shutting down; workers are being canceled and settled", "signal", sig.String())
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

	defer func() {
		// Whatever ended the run, status says it is not running any more.
		_ = ledger.NoteConnection(context.WithoutCancel(ctx), connector.ConnectionStopped, "")
	}()
	logger.Info("connector: running", "profile", richtext.SanitizeSingleLine(name), "account", account,
		"agent_person_id", agentID, "shadow", f.shadow, "projects", len(buckets), "state", richtext.SanitizeSingleLine(stateDir))

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
	if outbox != nil {
		// On start, before anything transitions: settle what a previous
		// process left sending and send what is due, so no stale notice
		// waits behind new work. Bounded, so a slow Basecamp delays the
		// connector's start rather than stopping it; what is left, Run
		// carries on with. A ledger that cannot settle an intent stops the
		// start.
		startCtx, stopStart := context.WithTimeout(runCtx, connectStartBound)
		err := outbox.Start(startCtx)
		stopStart()
		if err != nil && runCtx.Err() == nil {
			return err
		}
	}
	if err := ledger.NoteConnection(ctx, connector.ConnectionRunning, ""); err != nil {
		logger.Warn("connector: could not record that it runs, for status", "error", err)
	}
	runPart("intake", intake.Run)
	runPart("admission", func(ctx context.Context) error {
		return connector.RunAdmission(ctx, connector.AdmissionOptions{Ledger: ledger, Queue: queue, Admitter: admitter, Lines: lines, Logger: logger})
	})
	if dispatcher != nil {
		runPart("dispatch", dispatcher.Run)
	}
	if outbox != nil {
		runPart("outbox", outbox.Run)
	}
	wg.Wait()
	if outbox != nil {
		// The dispatcher has settled every attempt it stopped; their
		// completion notices go out now, within a bound.
		flushCtx, stopFlush := context.WithTimeout(context.WithoutCancel(ctx), connectShutdownFlush)
		if err := outbox.Flush(flushCtx); err != nil {
			logger.Warn("connector: posting lifecycle messages on the way out", "error", err)
		}
		stopFlush()
	}

	mu.Lock()
	sig := received
	mu.Unlock()
	switch {
	case sig == os.Interrupt || sig == syscall.SIGINT:
		return output.ErrInterrupted("connector interrupted")
	case sig == syscall.SIGTERM:
		return output.ErrTerminated("connector terminated")
	case firstErr != nil:
		return firstErr
	case ctx.Err() != nil:
		return ctx.Err()
	}
	return nil
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

// connectSupportedOS is where the connector runs: Linux, and for now only
// Linux.
//
// Two things have to hold, and macOS has only one of them. The driver must
// be able to read process start times, so a recorded worker group is never
// signaled after its pid was reused — macOS can. And the task token has to
// reach the worker's MCP server, which it does on an inherited descriptor:
// `connect worker-mcp` execs `basecamp mcp --connect-token-fd`, and that
// hand-over is accepted only where the descriptors this process inherited
// are sealed against everything it starts, which is Linux alone
// (mcp_token_linux.go, and #736, which gated it deliberately). On macOS
// every non-shadow dispatch would start a worker whose Basecamp tools fail
// at the handshake, so the connector says so here rather than at the far
// end of each task.
func connectSupportedOS(goos string) bool {
	return goos == "linux"
}

// connectLinuxOnlyReason is why, in one place: the run command's refusal and
// doctor's Platform check say the same thing, so a person who meets one and
// then the other is not told two different stories about their machine.
const connectLinuxOnlyReason = "the task token reaches a worker's MCP server over an inherited descriptor, and Linux is the only platform that seals the descriptors a process inherits"

// connectUnsupportedOSError is the run command's refusal on a platform the
// connector does not run on.
func connectUnsupportedOSError(goos string) error {
	return output.ErrUsage(fmt.Sprintf("basecamp connect runs on Linux only, not %s: %s", goos, connectLinuxOnlyReason))
}

// connectServed is connect.json's served projects as they are now, not as
// they were at start: a project removed by `connect setup --unserve` stops
// authorizing dispatch without a restart. A file that no longer loads, or
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
	// lockFailing is failing's counterpart for the lock: a host that cannot
	// take it says so once rather than once per launch.
	lockFailing bool
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
//
// A caller that must not have an unserve land between its reading and what
// that reading authorizes wants Authorize, not this.
func (r *connectServed) Current() (map[int64]admission.Project, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// A failure is cached for the TTL exactly as an answer is. Reloading on
	// every call while the file is broken would read it once per event in
	// admission and once per tick in dispatch (Copilot on #765); the answer
	// would not change, and the log line is written once either way.
	if (r.projects == nil && r.err == nil) || r.now().Sub(r.loadedAt) >= connectServedTTL {
		r.reload()
	}
	return r.answer()
}

// Authorize reads connect.json under the per-profile setup lock — the lock
// `connect setup` holds across its load, change and save — and hands back
// the release for the caller to hold until the commit that reading
// authorizes has landed. Between the two an unserve cannot complete, so the
// launch is decided against the file as it will still be when the task
// exists.
//
// The read is fresh: the TTL is a reuse policy for callers that are only
// choosing, and reusing a reading here would reintroduce exactly the gap the
// lock is taken to close. It refreshes the cache on its way through, so the
// next Current cannot be older than this read.
//
// A lock another command holds is connector.ErrPolicyBusy: this authorizes
// nothing, and the dispatcher gives up its pass rather than waiting out a
// `connect setup`'s network checks. A host that cannot lock at all
// authorizes nothing either — the guarantee here is the lock, as it is in
// setup.
func (r *connectServed) Authorize() (map[int64]admission.Project, func(), error) {
	unlock, err := setup.TryLock(r.path)
	if err != nil {
		if errors.Is(err, setup.ErrSetupRunning) {
			return nil, nil, fmt.Errorf("%w: %w", connector.ErrPolicyBusy, err)
		}
		r.logLockFailure(err)
		return nil, nil, fmt.Errorf("%w: %w", connector.ErrPolicyUnreadable, err)
	}
	r.mu.Lock()
	r.lockFailing = false
	r.reload()
	projects, err := r.answer()
	r.mu.Unlock()
	if err != nil {
		unlock()
		// reload has already said why, once. This only tells the dispatcher
		// which kind of nothing it is being handed.
		return nil, nil, fmt.Errorf("%w: %w", connector.ErrPolicyUnreadable, err)
	}
	return projects, unlock, nil
}

// logLockFailure says a lock could not be taken, once per spell of failing.
func (r *connectServed) logLockFailure(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.lockFailing {
		r.log.Error("connector: dispatching nothing until connect.json can be locked", "error", err)
	}
	r.lockFailing = true
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
			r.log.Error("connector: dispatching nothing until connect.json is usable again", "error", err)
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

// connectDispatch is what the run knows when it builds the dispatcher.
type connectDispatch struct {
	File    setup.File
	Buckets []int64
	Ledger  *connector.Ledger
	Driver  driver.Driver
	Served  func() (map[int64]admission.Project, error)
	// Authorize is Served read under connect.json's lock, with the release
	// the dispatcher holds across a launch's commit.
	Authorize func() (map[int64]admission.Project, func(), error)

	Profile     string
	Executable  string
	StateDir    string
	SessionsDir string

	Replies            connector.ReplyLister
	IsLifecycleMessage func(id int64) bool
	Lines              *ndjson.Writer
	Logger             *slog.Logger
}

// connectDispatcherOptions is the dispatcher the run starts: connect.json's
// concurrency and deadline, the projects this run hears, and the worker's own
// MCP server. Built here so what the command wires is what a test can read.
func connectDispatcherOptions(d connectDispatch) connector.DispatcherOptions {
	return connector.DispatcherOptions{
		Ledger:             d.Ledger,
		Driver:             d.Driver,
		Served:             d.Served,
		Authorize:          d.Authorize,
		Concurrency:        d.File.Concurrency,
		Deadline:           time.Duration(d.File.Deadline),
		Buckets:            d.Buckets,
		MCP:                connector.WorkerMCP{Command: d.Executable, Profile: d.Profile, StateDir: d.StateDir},
		PrivateDir:         d.SessionsDir,
		Replies:            d.Replies,
		IsLifecycleMessage: d.IsLifecycleMessage,
		Lines:              d.Lines,
		Logger:             d.Logger,
		StillRunning:       connector.DefaultStillRunning,
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
