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
}

func addConnectRunFlags(cmd *cobra.Command, f *connectRunFlags) {
	fl := cmd.Flags()
	// --project shadows the global flag of the same name and keeps its type,
	// so the flag reads the same everywhere; here it may be repeated.
	fl.Var((*repeatedString)(&f.projects), "project", "Only hear events in this project id (repeatable; default every project the agent can see)")
	fl.BoolVar(&f.shadow, "shadow", false, "Admit and log in an isolated state directory; dispatch and post nothing")
	fl.Int64Var(&f.since, "since", 0, "Enter the feed just after this event id, whatever the ledger holds")
	fl.StringVar(&f.driver, "driver", "", "Override connect.json's driver (spawn)")
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
// state (worktrees prune, status) resolves it here.
func connectStateDir(file setup.File, shadow bool) (string, error) {
	stateHome, err := connectStateHome()
	if err != nil {
		return "", err
	}
	group := "connect"
	if shadow {
		// An isolated ledger, lock and checkpoint: a shadow never shares a
		// position or a record with the connector it watches beside.
		group = "connect-shadow"
	}
	return ensurePrivateChain(stateHome, "basecamp", group, connector.StateDirName(file.AccountID, file.Agent.PersonID))
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

func runConnect(cmd *cobra.Command, f *connectRunFlags) error {
	if !connectSupportedOS(runtime.GOOS) {
		return output.ErrUsage("basecamp connect runs on macOS and Linux only: it ends a crashed connector's workers by process group and start time, which only those two can read")
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
	if file.Worktrees && !f.shadow {
		// Refused rather than ignored: workers would share the route's
		// checkout while connect.json says each task gets its own.
		return output.ErrUsage("connect.json asks for worktrees, which this basecamp does not support yet; run setup with --worktrees=false")
	}
	driverName := file.Driver
	if f.driver != "" {
		driverName = f.driver
	}
	if !f.shadow && driverName != setup.DriverSpawn {
		return output.ErrUsage(fmt.Sprintf("driver %q is not available yet; use %q", driverName, setup.DriverSpawn))
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
	intakeOpts.ConsumerNamespace = "basecamp-connect-" + strconv.FormatInt(agentID, 10)
	intakeOpts.Filters = eventfeed.Filters{Buckets: buckets, ExcludePerformers: []int64{agentID}, ActorTypes: []string{"person"}}
	intakeOpts.SinceEventID = f.since
	intakeOpts.Ledger = ledger
	intakeOpts.Queue = queue
	intakeOpts.Lines = lines
	intakeOpts.Logger = logger
	intakeOpts.Membership = connector.SDKMembership{Client: accountClient}
	intake, err := connector.New(intakeOpts)
	if err != nil {
		return err
	}

	reads := admission.NewSDKReads(&basecamp.Config{BaseURL: app.Config.BaseURL}, tokens, account, connectSDKOptions()...)
	admitter, err := admission.NewAdmitter(policy, reads)
	if err != nil {
		return output.ErrUsage(err.Error())
	}

	var dispatcher *connector.Dispatcher
	if !f.shadow {
		exe, err := os.Executable()
		if err != nil {
			return fmt.Errorf("locate this binary for the worker's MCP server: %w", err)
		}
		sessions, err := connectSessionsDir(file)
		if err != nil {
			return err
		}
		routes := newConnectRoutes(path, file, logger)
		worker, err := spawn.New(file.WorkerName(), spawn.Options{})
		if err != nil {
			return output.ErrUsage(err.Error())
		}
		// Built with worktrees off too, so the ones made while they were on
		// are still settled and recovered.
		worktreesRoot, err := ensurePrivateChain(stateDir, connectWorktreesDir)
		if err != nil {
			return err
		}
		workspaces, err := connector.NewWorktrees(connector.WorktreesOptions{Ledger: ledger, Root: worktreesRoot, Logger: logger, Off: !file.Worktrees})
		if err != nil {
			return err
		}
		options := connectDispatcherOptions(connectDispatch{
			File: file, Buckets: buckets, Ledger: ledger, Driver: worker, Routes: routes.Current,
			Profile: name, Executable: exe, StateDir: stateDir, SessionsDir: sessions,
			Replies: connector.SDKReplies{Client: accountClient, AgentID: agentID},
			Lines:   lines, Logger: logger,
		})
		options.Workspaces = workspaces
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
	runPart("intake", intake.Run)
	runPart("admission", func(ctx context.Context) error {
		return connector.RunAdmission(ctx, connector.AdmissionOptions{Ledger: ledger, Queue: queue, Admitter: admitter, Lines: lines, Logger: logger})
	})
	if dispatcher != nil {
		runPart("dispatch", dispatcher.Run)
	}
	wg.Wait()

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

// connectSupportedOS is where the connector runs: the platforms whose
// process start times the driver can read, so a recorded worker group is
// never signaled after its pid was reused.
func connectSupportedOS(goos string) bool {
	return goos == "linux" || goos == "darwin"
}

// connectRoutes is connect.json's routes as they are now, not as they were at
// start: a route removed by `connect setup --unroute` stops authorizing
// dispatch without a restart. A file that no longer loads, or that now names
// another agent or account, authorizes nothing.
type connectRoutes struct {
	path     string
	agent    setup.Agent
	account  string
	log      *slog.Logger
	now      func() time.Time
	mu       sync.Mutex
	loadedAt time.Time
	routes   map[int64]admission.Route
	failing  bool
}

// connectRoutesTTL is how long a read of connect.json is reused.
const connectRoutesTTL = 2 * time.Second

func newConnectRoutes(path string, file setup.File, log *slog.Logger) *connectRoutes {
	return &connectRoutes{path: path, agent: file.Agent, account: file.AccountID, log: log, now: time.Now}
}

// Current returns a copy of the routes connect.json approves now.
func (r *connectRoutes) Current() map[int64]admission.Route {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.routes == nil || r.now().Sub(r.loadedAt) >= connectRoutesTTL {
		r.reload()
	}
	out := make(map[int64]admission.Route, len(r.routes))
	for k, v := range r.routes {
		out[k] = v
	}
	return out
}

func (r *connectRoutes) reload() {
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
		r.routes = map[int64]admission.Route{}
		return
	}
	if r.failing {
		r.log.Info("connector: connect.json is usable again")
	}
	r.failing = false
	r.routes = make(map[int64]admission.Route, len(file.Projects))
	for bucket, route := range file.Projects {
		r.routes[bucket] = route
	}
}

// connectDispatch is what the run knows when it builds the dispatcher.
type connectDispatch struct {
	File    setup.File
	Buckets []int64
	Ledger  *connector.Ledger
	Driver  driver.Driver
	Routes  func() map[int64]admission.Route

	Profile     string
	Executable  string
	StateDir    string
	SessionsDir string

	Replies connector.ReplyLister
	Lines   *ndjson.Writer
	Logger  *slog.Logger
}

// connectDispatcherOptions is the dispatcher the run starts: connect.json's
// concurrency and deadline, the projects this run hears, and the worker's own
// MCP server. Built here so what the command wires is what a test can read.
func connectDispatcherOptions(d connectDispatch) connector.DispatcherOptions {
	return connector.DispatcherOptions{
		Ledger:       d.Ledger,
		Driver:       d.Driver,
		Routes:       d.Routes,
		Concurrency:  d.File.Concurrency,
		Deadline:     time.Duration(d.File.Deadline),
		Buckets:      d.Buckets,
		MCP:          connector.WorkerMCP{Command: d.Executable, Profile: d.Profile, StateDir: d.StateDir},
		PrivateDir:   d.SessionsDir,
		Replies:      d.Replies,
		Lines:        d.Lines,
		Logger:       d.Logger,
		StillRunning: connector.DefaultStillRunning,
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
