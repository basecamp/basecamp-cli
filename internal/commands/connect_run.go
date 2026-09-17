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

// connectStateHome is where connector state lives: $XDG_STATE_HOME, or
// ~/.local/state.
func connectStateHome() (string, error) {
	if dir := os.Getenv("XDG_STATE_HOME"); dir != "" && filepath.IsAbs(dir) {
		return dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state"), nil
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

func runConnect(cmd *cobra.Command, f *connectRunFlags) error {
	if runtime.GOOS == "windows" {
		return output.ErrUsage("basecamp connect runs on macOS and Linux only: it starts workers as process groups")
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
		sessions, err := ensurePrivateChain(stateDir, "sessions")
		if err != nil {
			return err
		}
		routes := map[int64]admission.Route{}
		for bucket, route := range file.Projects {
			routes[bucket] = route
		}
		worker, err := spawn.New(file.WorkerName(), spawn.Options{})
		if err != nil {
			return output.ErrUsage(err.Error())
		}
		dispatcher, err = connector.NewDispatcher(connector.DispatcherOptions{
			Ledger:       ledger,
			Driver:       worker,
			Routes:       func() map[int64]admission.Route { return routes },
			Concurrency:  file.Concurrency,
			Deadline:     time.Duration(file.Deadline),
			MCP:          connector.WorkerMCP{Command: exe, Profile: name, StateDir: stateDir},
			PrivateDir:   sessions,
			Replies:      connector.SDKReplies{Client: accountClient, AgentID: agentID},
			Lines:        lines,
			Logger:       logger,
			StillRunning: connector.DefaultStillRunning,
		})
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
			logger.Info("connector: shutting down", "signal", sig.String())
			cancel()
		case <-runCtx.Done():
		}
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
			if err != nil && runCtx.Err() == nil {
				errOnce.Do(func() { firstErr = fmt.Errorf("%s: %w", part, err) })
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
