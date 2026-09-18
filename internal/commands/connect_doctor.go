package commands

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
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
driver — and a handshake with the agent's Basecamp MCP server, started with a
worker's environment (without the basecamp_connect domain, which only a
dispatched task's token opens).

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
	checks = append(checks, workerBinaryChecks(p.file)...)
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
func workerBinaryChecks(file setup.File) []setup.Check {
	if file.Driver == setup.DriverACP {
		return []setup.Check{acpAdapterCheck(file.WorkerName())}
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
