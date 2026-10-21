package commands

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/connector"
	"github.com/basecamp/basecamp-cli/internal/connector/setup"
	"github.com/basecamp/basecamp-cli/internal/output"
	"github.com/basecamp/basecamp-cli/internal/richtext"
)

func newConnectDoctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check what the connector needs to run",
		Long: `Check the connector for a set-up profile: connect.json, the token, the agent's
identity, the stream ticket mint, the account feed, whether it hears the
agent's Pings (and whether another connector on this computer does too), and
the ledger (its gaps, open losses, hold, and lifecycle messages an older
connector left waiting for a person).

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
		Message: fmt.Sprintf("Agent person %d in account %s", p.file.Agent.PersonID, p.file.AccountID)}}
	if !connectSupportedOS(runtime.GOOS) {
		checks = append(checks, connectUnsupportedOSCheck(runtime.GOOS))
	}

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
	checks = append(checks, pingsCheck(ctx, p))
	checks = append(checks, ledgerChecks(ctx, p)...)

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
	dir, err := connectStatePath(p.name, p.file, false)
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
	s, err := ledger.Status(ctx)
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
			Hint:    "Review held records in basecamp connect status, then basecamp connect release -P " + richtext.ShellQuote(p.name)})
	}
	if len(s.Indeterminate) > 0 {
		checks = append(checks, setup.Check{Name: "Lifecycle messages", Status: setup.StatusWarn,
			Message: fmt.Sprintf("%d messages may or may not have been posted and wait for a person", len(s.Indeterminate))})
	}
	return checks
}

// connectUnsupportedOSCheck is the Platform check on a GOOS the connector
// does not run on (connectSupportedOS).
func connectUnsupportedOSCheck(goos string) setup.Check {
	return setup.Check{Name: "Platform", Status: setup.StatusFail,
		Message: fmt.Sprintf("The connector does not run on %s: %s", goos, connectSupportedOSReason),
		Hint:    "Run the connector on Linux or macOS; the rest of the CLI runs here."}
}

// pingsCheck says whether the profile's connector hears the agent's Pings —
// the one running now, as its lock says, else a start with neither --pings,
// --no-pings nor --project — and warns when two connectors on this computer
// answer them. Only one should; the ones on other computers can't be seen
// from here. The saved choice is read either way: a running connector
// doesn't make an unreadable one the next start's to discover.
func pingsCheck(ctx context.Context, p connectProfile) setup.Check {
	c := setup.Check{Name: "Pings"}
	v := readConnectPingsView(ctx, p.app, p.name, p.file)
	if v.SavedErr != nil {
		c.Status, c.Message = setup.StatusFail, "Whether it hears the agent's Pings was saved, and can't be read, so the next start without a flag refuses: "+errorMessage(v.SavedErr)
		c.Hint = v.Remedy(p.name)
		return c
	}
	hears, known := v.Hears()
	if !known {
		c.Status, c.Message = setup.StatusSkip, "Whether it hears the agent's Pings can't be told without the profile's credential"
		return c
	}
	// A running connector is described by its lock; Next may be unknown
	// then, with no saved choice and no credential to read a default from.
	who, why := "The connector running now", v.Running.Describe()
	if !v.Running.Running {
		who, why = "It", v.Next.Describe()
	}
	switch {
	case !hears:
		c.Message = who + " doesn't hear the agent's Pings: " + why
	case v.Running.Running && v.Running.Pings == nil:
		c.Message = who + " may hear the agent's Pings: " + why
	default:
		c.Message = who + " hears the agent's Pings: " + why
	}
	// What a restart without flags or --project would do, when it differs.
	if v.Running.Running && v.Next != nil && v.Next.Hears != hears {
		if v.Next.Hears {
			c.Message += "; restarted without --project, it would hear them (" + v.Next.Describe() + ")"
		} else {
			c.Message += "; restarted, it would hear none (" + v.Next.Describe() + ")"
		}
	}
	switch {
	case v.Twice() && hears && len(v.Others) == 1:
		c.Status = setup.StatusWarn
		c.Message += ", and so does " + peerList(v.Others) + " on this computer: each Ping is answered twice"
	case v.Twice() && hears:
		c.Status = setup.StatusWarn
		c.Message += ", and so do " + peerList(v.Others) + " on this computer: each Ping is answered more than once"
	case v.Twice():
		c.Status = setup.StatusWarn
		c.Message += ", and " + peerList(v.Others) + " on this computer all do: each Ping is answered more than once"
	case len(v.Others) == 1:
		c.Status = setup.StatusPass
		c.Message += "; " + peerList(v.Others) + " answers them on this computer"
		return c
	case !hears:
		c.Status = setup.StatusPass
		c.Message += ". Only one connector should answer them; start this one with --pings if it's that one"
		return c
	default:
		c.Status = setup.StatusPass
		return c
	}
	c.Hint = "Only one connector should answer Pings. Start all but one of them with --no-pings: basecamp connect -P " + richtext.ShellQuote(p.name) + " --no-pings"
	for _, peer := range v.Others {
		if peer.Older {
			c.Hint = "Only one connector should answer Pings, and an older build can't be told: upgrade it, or start this one with --no-pings: basecamp connect -P " + richtext.ShellQuote(p.name) + " --no-pings"
		}
	}
	return c
}
