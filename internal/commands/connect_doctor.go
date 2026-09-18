package commands

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/connector"
	"github.com/basecamp/basecamp-cli/internal/connector/driver/acp"
	"github.com/basecamp/basecamp-cli/internal/connector/setup"
	"github.com/basecamp/basecamp-cli/internal/output"
	"github.com/basecamp/basecamp-cli/internal/richtext"
)

// mcpHandshakeTimeout bounds doctor's MCP handshake. A var so a test can
// shorten it; production only reads it, and a test that changes it must not
// run in parallel.
var mcpHandshakeTimeout = 30 * time.Second

func newConnectDoctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check what the connector needs to run",
		Long: `Check the connector for a set-up profile: connect.json, the token, the agent's
identity, the stream ticket mint, the account feed, the ledger (its gaps, open
losses, hold, the worktrees it kept and messages waiting for a person), the
worker the driver runs — the worker's own CLI on PATH under the spawn driver,
the pinned ACP adapter in the connector's adapters directory under the acp
driver, and the adapter's own refusal of configuration on this machine that
the connector cannot switch off, run in the directory every routed project
would work in (its worktree, where a profile makes them) — and a
handshake with the agent's Basecamp MCP server, started with a worker's
environment (without the basecamp_connect domain, which only a dispatched
task's token opens).

It writes nothing to the connector's ledger and posts nothing to Basecamp.
Renewing the profile's own credential, which every command does when its token
is due, may still write the credential store.`,
		Example: `  basecamp connect doctor -P agent`,
		Args:    cobra.NoArgs,
		RunE:    runConnectDoctor,
	}
}

func runConnectDoctor(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	if os.Getenv("BASECAMP_TOKEN") != "" {
		return errEnvTokenShadows("doctor checks the agent its profile holds, and BASECAMP_TOKEN would override it")
	}
	p, err := loadConnectProfile(cmd)
	if err != nil {
		return err
	}
	checks := []setup.Check{{Name: "connect.json", Status: setup.StatusPass,
		Message: fmt.Sprintf("Agent person %d in account %s, driver %s, worker %s", p.file.Agent.PersonID, p.file.AccountID, p.file.Driver, p.file.WorkerName())}}
	checks = append(checks, driverChecks(p)...)

	agent, agentErr := verifiedConnectAgent(ctx, p)
	if agentErr != nil {
		checks = append(checks, setup.Check{Name: "Token and identity", Status: setup.StatusFail, Message: errorMessage(agentErr),
			Hint: "Reconnect the agent's profile, then run basecamp connect setup again."})
	} else {
		checks = append(checks,
			setup.Check{Name: "Token", Status: setup.StatusPass, Message: "The profile's credential yields a token"},
			setup.Check{Name: "Identity", Status: setup.StatusPass, Message: fmt.Sprintf("Person %d, as connect.json names", agent.personID)},
			setup.TicketCheck(ctx, agent.reader, agent.kind),
			feedCheck(ctx, agent.client.ForAccount(agent.account)),
		)
	}
	checks = append(checks, ledgerChecks(ctx, p)...)
	checks = append(checks, workerBinaryChecks(ctx, p.file)...)
	checks = append(checks, mcpHandshakeCheck(ctx, p.name))

	result := summarizeChecks(asDoctorChecks(checks))
	title := "Connector doctor for profile " + strconv.Quote(p.name)
	if p.app.Output.EffectiveFormat() == output.FormatStyled {
		renderChecksStyled(cmd.OutOrStdout(), title, result)
		if result.Failed > 0 {
			return doctorNotReady(checks)
		}
		return nil
	}
	if result.Failed > 0 {
		return doctorNotReady(checks)
	}
	return p.app.OK(result, output.WithSummary(result.Summary()))
}

func errorMessage(err error) string {
	var apiErr *output.Error
	if errors.As(err, &apiErr) {
		return apiErr.Message
	}
	return richtext.SanitizeSingleLine(err.Error())
}

func doctorNotReady(checks []setup.Check) error {
	report := &setup.Report{}
	report.Add(checks...)
	failures := report.Failed()
	msg := "The connector is not ready:"
	hint := ""
	for _, c := range failures {
		msg += " " + c.Name + ": " + c.Message + ";"
		if hint == "" {
			hint = c.Hint
		}
	}
	return &output.Error{Code: codeNotReady, Message: msg[:len(msg)-1], Hint: hint}
}

// feedCheck polls one page of the account feed at the present, as the
// connector's poll lane does. The page is dropped; its position is a resumable
// token and is never shown.
func feedCheck(ctx context.Context, account *basecamp.AccountClient) setup.Check {
	c := setup.Check{Name: "Account feed"}
	_, err := account.EventFeed().PollEvents(ctx, &basecamp.PollEventsOptions{Since: "now", ActorTypes: []string{"person"}})
	if err != nil {
		c.Status, c.Message = setup.StatusFail, "Polling the account feed failed: "+setup.ErrorText(err)
		c.Hint = "The account event feed has to be enabled for this account and the agent."
		return c
	}
	c.Status, c.Message = setup.StatusPass, "The agent can poll the account feed"
	return c
}

// ledgerChecks reports what the ledger records that a person should know:
// gaps with their epoch ids, losses, the hold, and lifecycle messages that
// wait for a decision. It reads the ledger read-only.
func ledgerChecks(ctx context.Context, p connectProfile) []setup.Check {
	dir, err := connectStatePath(p.file, false)
	if err != nil {
		return []setup.Check{{Name: "Ledger", Status: setup.StatusFail, Message: errorMessage(err)}}
	}
	ledger, err := connector.OpenLedgerReadOnly(ctx, filepath.Join(dir, connector.LedgerFile))
	if errors.Is(err, os.ErrNotExist) {
		return []setup.Check{{Name: "Ledger", Status: setup.StatusSkip, Message: "No ledger yet: the connector has not run"}}
	}
	if err != nil {
		return []setup.Check{{Name: "Ledger", Status: setup.StatusFail, Message: errorMessage(err)}}
	}
	defer func() { _ = ledger.Close() }()
	s, err := ledger.Status(ctx, ledger.RetainedWorktreeStatus)
	if err != nil {
		return []setup.Check{{Name: "Ledger", Status: setup.StatusFail, Message: errorMessage(err)}}
	}
	checks := []setup.Check{{Name: "Ledger", Status: setup.StatusPass, Message: fmt.Sprintf("Schema %d, private, readable", s.SchemaVersion)}}
	for _, g := range s.Gaps {
		msg := fmt.Sprintf("A %s gap was recorded at %s", g.Class, g.DetectedAt.UTC().Format(time.RFC3339))
		if g.EpochAfterID != nil {
			msg += fmt.Sprintf("; history at or below event %d is gone", *g.EpochAfterID)
		}
		checks = append(checks, setup.Check{Name: fmt.Sprintf("Gap %d", g.ID), Status: setup.StatusWarn, Message: msg})
	}
	if len(s.Losses) > 0 || s.Unrecovered > 0 {
		checks = append(checks, setup.Check{Name: "Losses", Status: setup.StatusWarn,
			Message: fmt.Sprintf("%d overflow losses open, %d event ids unrecovered", len(s.Losses), s.Unrecovered)})
	}
	if s.Hold != nil {
		checks = append(checks, setup.Check{Name: "Hold", Status: setup.StatusWarn,
			Message: fmt.Sprintf("Held since %s by %s: nothing dispatches or posts", s.Hold.HeldAt.UTC().Format(time.RFC3339), richtext.SanitizeSingleLine(s.Hold.HeldBy)),
			Hint:    "Review held records in basecamp connect status, then basecamp connect release -P " + shellQuote(p.name)})
	}
	if len(s.Indeterminate) > 0 {
		checks = append(checks, setup.Check{Name: "Lifecycle messages", Status: setup.StatusWarn,
			Message: fmt.Sprintf("%d messages may or may not have been posted and wait for a person", len(s.Indeterminate))})
	}
	switch {
	case !s.WorktreesKnown:
		checks = append(checks, setup.Check{Name: "Worktrees", Status: setup.StatusWarn,
			Message: "The worktrees the connector kept could not be listed: " + richtext.SanitizeSingleLine(s.WorktreesUnavailable),
			Hint:    "basecamp connect worktrees list -P " + shellQuote(p.name)})
	case len(s.Worktrees) > 0:
		checks = append(checks, setup.Check{Name: "Worktrees", Status: setup.StatusWarn,
			Message: fmt.Sprintf("%d worktree(s) kept for you to deal with; the connector removes none of its own accord", len(s.Worktrees)),
			Hint:    "basecamp connect worktrees list -P " + shellQuote(p.name) + ", then prune"})
	}
	return checks
}

// workerBinaries are the executables the spawn driver runs for the
// configured worker. The acp driver runs a pinned adapter instead, which
// acpAdapterCheck names and locates.
func workerBinaries(file setup.File) []string {
	return []string{file.WorkerName()}
}

// workerBinaryChecks looks for the worker where the driver that runs it
// looks. The spawn driver runs the worker's own CLI, which is on PATH. The
// acp driver runs a pinned adapter out of the connector's own npm prefix
// (`make acp-adapters`), which is not on PATH and is not meant to be: it is
// found and version-checked with acp.Locate, the driver's own locator, so
// doctor passes the adapter the connector would start and no other. PATH
// would both fail a correct install and pass an unpinned build that happens
// to be on it.
func workerBinaryChecks(ctx context.Context, file setup.File) []setup.Check {
	if file.Driver == setup.DriverACP {
		checks := []setup.Check{acpAdapterCheck(file.WorkerName())}
		if c, ok := acpPreflightCheck(ctx, file); ok {
			checks = append(checks, c)
		}
		return checks
	}
	bins := workerBinaries(file)
	checks := make([]setup.Check, 0, len(bins))
	for _, bin := range bins {
		c := setup.Check{Name: "Worker " + bin}
		path, err := exec.LookPath(bin)
		if err != nil {
			c.Status, c.Message = setup.StatusFail, fmt.Sprintf("%s is not on PATH", bin)
			c.Hint = "Install it, or put it on the PATH the connector starts with."
		} else {
			c.Status, c.Message = setup.StatusPass, richtext.SanitizeSingleLine(path)
		}
		checks = append(checks, c)
	}
	return checks
}

// acpAdapterCheck resolves the configured worker's pinned adapter where the
// acp driver would, in the default adapters directory. A connector started
// with --acp-adapters elsewhere is not what this checks; doctor has no such
// flag, and the message says where it looked.
func acpAdapterCheck(worker string) setup.Check {
	a, ok := acp.AdapterForWorker(worker)
	if !ok {
		return setup.Check{Name: "Worker " + worker, Status: setup.StatusFail,
			Message: fmt.Sprintf("The acp driver has no adapter for worker %q", worker),
			Hint:    "basecamp connect setup --driver spawn, or pick a worker the acp driver runs."}
	}
	c := setup.Check{Name: "Adapter " + a.Name}
	dir, err := acp.DefaultAdaptersDir(nil)
	if err != nil {
		c.Status, c.Message = setup.StatusFail, errorMessage(err)
		c.Hint = "Set XDG_DATA_HOME or HOME to an absolute path, then run make acp-adapters."
		return c
	}
	bin, err := acp.Locate(dir, a)
	if err != nil {
		c.Status, c.Message = setup.StatusFail, errorMessage(err)
		c.Hint = "Install the pinned adapters: make acp-adapters"
		return c
	}
	c.Status = setup.StatusPass
	c.Message = fmt.Sprintf("%s@%s at %s", a.Package, a.Version, richtext.SanitizeSingleLine(bin))
	return c
}

// acpPreflightCheck runs the adapter's own preflight — the refusal the acp
// driver makes before it starts anything — for the directories a dispatch
// would run in. It is the check a resolved adapter does not make: the
// preflight reads configuration on this machine the connector cannot switch
// off (a Codex config layer that declares MCP servers, which codex-acp would
// load into the session beside the connector's), so a profile whose adapter
// is installed and on the pin can still have every record refused with
// ErrUnusable the moment it is dispatched. There is no second check: doctor
// refuses what the run command refuses.
//
// Every routed directory, not the first, and every distinct reason rather
// than the first: the preflight walks per-directory layers as well as the
// machine's, so one route can carry a .codex/config.toml another has not,
// and a person fixing this wants the whole list out of one run. It costs a
// handful of file reads per route. The layers every route shares — the
// user's and the system's — fail identically, so an identical reason is
// reported once, for all of them.
//
// What it runs against is the directory a dispatch would give the session,
// which is the route only while worktrees are off. With them on the session
// works in a worktree under the connector's state directory, and the layers
// there are not the route's: a .codex/config.toml the route has but the
// repository does not track is in no worktree, one above the route is not
// above the worktree, and one above the state directory is above every
// worktree and above no route. So the worktree is planned — the same
// decision Prepare makes, making nothing — and the preflight runs in the
// planned working directory, reading the files inside it from the commit
// the checkout would be made at. A route that could take no worktree at all
// is a route no task could run in, and fails here rather than at the first
// dispatch.
//
// The second return is false when there is nothing to run: an adapter with
// no preflight (claude-agent-acp) gets no row, rather than a row saying a
// check that does not exist passed.
func acpPreflightCheck(ctx context.Context, file setup.File) (setup.Check, bool) {
	a, ok := acp.AdapterForWorker(file.WorkerName())
	if !ok || a.Preflight == nil {
		return setup.Check{}, false
	}
	c := setup.Check{Name: "Adapter " + a.Name + " preflight"}
	ids := make([]int64, 0, len(file.Projects))
	for id := range file.Projects {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	if len(ids) == 0 {
		c.Status = setup.StatusSkip
		c.Message = "No project is routed, so there is no directory a session would run in"
		return c, true
	}

	planner, plannerErr := preflightPlanner(file)
	type failure struct {
		reason string
		routes []string
		hint   string
	}
	var failures []*failure
	seen := map[string]*failure{}
	for _, id := range ids {
		path := file.Projects[id].Path
		err := plannerErr
		if err == nil {
			err = preflightRoute(ctx, a, planner, path)
		}
		if err == nil {
			continue
		}
		reason := preflightReason(err)
		f, ok := seen[reason]
		if !ok {
			f = &failure{reason: reason, hint: preflightHint(err)}
			seen[reason] = f
			failures = append(failures, f)
		}
		f.routes = append(f.routes, richtext.SanitizeSingleLine(path))
	}
	if len(failures) == 0 {
		c.Status = setup.StatusPass
		c.Message = fmt.Sprintf("%s would start in %s (%d checked)", a.Name, preflightWhere(file, "every routed directory", "the worktree every routed project would get"), len(ids))
		return c, true
	}

	c.Status = setup.StatusFail
	parts := make([]string, 0, len(failures))
	for i, f := range failures {
		where := strings.Join(f.routes, ", ")
		if len(ids) > 1 && len(f.routes) == len(ids) {
			// Every route, one reason: the user's or the system's layer,
			// which no route escapes by being somewhere else.
			where = preflightWhere(file, "any routed directory", "any routed project's worktree")
		}
		lead := "no session would start in"
		if i == 0 {
			lead = "No session would start in"
		}
		parts = append(parts, fmt.Sprintf("%s %s: %s", lead, where, f.reason))
		if c.Hint == "" {
			c.Hint = f.hint
		}
	}
	c.Message = strings.Join(parts, "; ")
	return c, true
}

// preflightHint is what to do about one refusal.
func preflightHint(err error) string {
	switch {
	case errors.Is(err, errNoWorkDir):
		return "With worktrees on, a route has to be a directory in a git repository with a commit: route the project elsewhere, or run the profile without worktrees."
	case errors.Is(err, acp.ErrForeignMCPConfig):
		// A declaration first: where a session is refused for both, the
		// declaration is the one that is certainly there.
		return "Take the MCP servers out of that file (a key an escape hides counts), or start the connector with CODEX_HOME set to a Codex home that declares none."
	case errors.Is(err, acp.ErrConfigUnreadable):
		return "Make that file readable by the user the connector runs as, or remove it: while it cannot be read, nothing can tell whether it declares MCP servers."
	default:
		return "The adapter refuses configuration on this machine that the connector cannot switch off; change it, then run doctor again."
	}
}

// preflightWhere names the directory the check ran in: the route itself, or
// the worktree a task on it would be given. Saying "routed directory" while
// checking a worktree would be the same silence this check exists to end.
func preflightWhere(file setup.File, route, worktree string) string {
	if file.Worktrees {
		return worktree
	}
	return route
}

// preflightPlanner plans the worktrees a profile with worktrees on would
// make: nil when they are off, and the session works in the route itself.
func preflightPlanner(file setup.File) (*connector.Worktrees, error) {
	if !file.Worktrees {
		return nil, nil //nolint:nilnil // no planner and no error is "the route is the working directory"
	}
	stateDir, err := connectStatePath(file, false)
	if err != nil {
		return nil, err
	}
	// The root the run command gives Worktrees, without making it: doctor
	// writes nothing, and a root that does not exist yet holds no
	// configuration either.
	return connector.PlanWorktrees(connector.WorktreesOptions{Root: filepath.Join(stateDir, connectWorktreesDir)})
}

// preflightWorktreeName stands in for the name Prepare gives a task's
// worktree. Any name is the same directory as far as configuration goes:
// what a task's own name would add is one directory level that does not
// exist until the worktree is made, and whose files are the repository's.
const preflightWorktreeName = "doctor"

// preflightRoute runs the preflight for one route, in the directory a
// dispatch on it would use.
func preflightRoute(ctx context.Context, a acp.Adapter, planner *connector.Worktrees, route string) error {
	if planner == nil {
		return acp.Preflight(a, route, nil, nil)
	}
	plan, err := planner.Plan(ctx, route, preflightWorktreeName)
	if err != nil {
		// A route that can take no worktree takes no task either: every
		// dispatch on it waits in a backoff nothing reports.
		return fmt.Errorf("%w: %w", errNoWorkDir, err)
	}
	read := func(name string) ([]byte, error) { return plan.ReadFile(ctx, name) }
	err = acp.Preflight(a, plan.Dir, nil, read)
	if err == nil {
		return nil
	}
	// A file inside the planned worktree is the repository's, at the commit
	// the worktree would be made from: say where a person can go and change
	// it, not where a directory nobody has made yet would have held it. The
	// error keeps its chain, so what it is stays the same as what it says.
	if named := strings.ReplaceAll(err.Error(), plan.Worktree, plan.Repository); named != err.Error() {
		return inRepositoryError{err: err, msg: named + " (committed in the repository, so every worktree of it has the file)"}
	}
	return err
}

// errNoWorkDir is a route no task could be given a working directory in.
var errNoWorkDir = errors.New("no task on it would get a working directory")

// inRepositoryError is a refusal over a file in a planned worktree, said as the
// repository's file. It is the same error: only its words move.
type inRepositoryError struct {
	err error
	msg string
}

func (e inRepositoryError) Error() string { return e.msg }
func (e inRepositoryError) Unwrap() error { return e.err }

// preflightReason is a preflight's refusal as a person reads it: the driver
// package's own prefix off the front, because the check already names the
// adapter, and the rest as it is — it names the file, which is the whole
// answer to what to change.
func preflightReason(err error) string {
	return strings.TrimPrefix(errorMessage(err), "acp: ")
}

// connectUnsupportedOSCheck is the Platform check on a GOOS the connector
// does not run on. It gives the constraint that actually applies
// (connectSupportedOS, and #736): the task token reaches a worker's MCP
// server over an inherited descriptor, and Linux alone seals the descriptors
// a process passes on. Reading a process's start time is the half macOS has,
// so naming that here told a Mac reader the connector could run there and
// then refused it anyway.
func connectUnsupportedOSCheck(goos string) setup.Check {
	return setup.Check{Name: "Platform", Status: setup.StatusFail,
		Message: fmt.Sprintf("The connector does not run on %s: %s", goos, connectLinuxOnlyReason),
		Hint:    "Run the connector on Linux; the rest of the CLI runs here."}
}

// driverChecks refuses what the run command refuses: doctor never calls a
// connector ready that would not start.
func driverChecks(p connectProfile) []setup.Check {
	var checks []setup.Check
	if !connectSupportedOS(runtime.GOOS) {
		checks = append(checks, connectUnsupportedOSCheck(runtime.GOOS))
	}
	if p.file.Driver != setup.DriverSpawn && p.file.Driver != setup.DriverACP {
		checks = append(checks, setup.Check{Name: "Driver", Status: setup.StatusFail,
			Message: fmt.Sprintf("Driver %q is not %q or %q, and the connector refuses to start on it", p.file.Driver, setup.DriverSpawn, setup.DriverACP),
			Hint:    "basecamp connect setup -P " + shellQuote(p.name) + " --driver spawn"})
	}
	return checks
}
