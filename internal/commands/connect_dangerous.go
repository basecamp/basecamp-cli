package commands

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/connector"
	"github.com/basecamp/basecamp-cli/internal/connector/admission"
	"github.com/basecamp/basecamp-cli/internal/connector/setup"
	"github.com/basecamp/basecamp-cli/internal/output"
	"github.com/basecamp/basecamp-cli/internal/richtext"
)

// confirmDangerous asks the person at the terminal to turn dangerous mode on,
// after saying what it means. Only a typed "yes" turns it on, and a run with
// nobody at a terminal — a script, or an AI running the command — is refused
// outright: the agent must never be able to give itself this.
func confirmDangerous(w io.Writer, app *appctx.App, name string) error {
	again := "basecamp connect setup -P " + richtext.ShellQuote(name) + " --dangerous"
	if !connectSetupInteractive(app) {
		return output.ErrUsageHint("Dangerous mode needs you at a terminal to turn it on, so nothing was changed",
			"Run it yourself, in a terminal: "+again+". A script or an AI can't turn it on.")
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, setup.DangerousRisk)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "While it's on, only you can give your agent work, and setup, status and doctor say it's on.")
	fmt.Fprintln(w, "Turn it off any time: basecamp connect setup -P "+richtext.ShellQuote(name)+" --dangerous=false")
	fmt.Fprintln(w)
	answer, err := connectSetupAsk.Input("Type yes to turn on dangerous mode")
	if err != nil {
		return err
	}
	if !strings.EqualFold(strings.TrimSpace(answer), "yes") {
		return output.ErrUsage("Dangerous mode was left off, so nothing was changed")
	}
	return nil
}

// refusedChange puts a change setup.Apply refused in the command's words.
// Dangerous mode refused because other people can give the agent work comes
// with the reasons, printed before the error, and the one command that
// makes the change possible.
func refusedChange(w io.Writer, name string, err error) error {
	profile := richtext.ShellQuote(name)
	switch {
	case errors.Is(err, setup.ErrDangerousShared):
		fmt.Fprintln(w, setup.DangerousSharedExplainer)
		fmt.Fprintln(w)
		return output.ErrUsageHint("Dangerous mode is only for an agent that you alone can give work to, and other people can give yours work, so nothing was changed",
			"To use it, make the agent yours alone in the same run: basecamp connect setup -P "+profile+" --trust operator --dangerous")
	case errors.Is(err, setup.ErrDangerousOperatorChange):
		return output.ErrUsageHint("Dangerous mode is on, so the person who can give your agent work can't change, and nothing was changed",
			"To change the operator, turn dangerous mode off in the same run: basecamp connect setup -P "+profile+" --dangerous=false and the operator you want")
	case errors.Is(err, setup.ErrDangerousWhileShared):
		fmt.Fprintln(w, setup.DangerousSharedExplainer)
		fmt.Fprintln(w)
		return output.ErrUsageHint("Dangerous mode is on, so other people can't be allowed to give your agent work yet, and nothing was changed",
			"To share it, turn dangerous mode off in the same run: basecamp connect setup -P "+profile+" --dangerous=false and the trust you want")
	}
	return output.ErrUsage(err.Error())
}

// dangerousModeCheck is the line setup and doctor show while dangerous mode
// is on.
func dangerousModeCheck(name string) setup.Check {
	return setup.Check{
		Name:    "Dangerous mode",
		Status:  setup.StatusWarn,
		Message: "On: the agent can run any command on this computer, as you, without asking",
		Hint:    "Turn it off: basecamp connect setup -P " + richtext.ShellQuote(name) + " --dangerous=false",
	}
}

// dangerousLaunches is whether the connector's next launch runs in dangerous
// mode: this run may use it at all (allowed, decided once at start by
// dangerousAllowedThisRun), and connect.json has it on now (fileSaysOn, read
// fresh at each launch, so turning it off needs no restart).
func dangerousLaunches(allowed bool, fileSaysOn func() bool) func() bool {
	return func() bool {
		return allowed && fileSaysOn()
	}
}

// dangerousAllowedThisRun decides once, at start, whether this run may use
// dangerous mode: only when it trusts its operator alone, and nothing anyone
// else asked for is still waiting. Trust is read once, so a run started
// admitting other people never runs their work with a shell; and a request
// admitted in an earlier run, when others could give the agent work, is
// never run with one either, whoever restarted the connector since.
func dangerousAllowedThisRun(ctx context.Context, ledger *connector.Ledger, file setup.File) (bool, error) {
	if !file.Dangerous || file.Trust.Mode != admission.TrustOperator {
		return false, nil
	}
	others, err := ledger.UnfinishedFromOthers(ctx, file.Trust.OperatorID)
	if err != nil {
		return false, err
	}
	return others == 0, nil
}

// noteDangerousChange tells the person what a change to dangerous mode means
// for a connector that is running now: turning it on takes a restart to be
// sure of, and turning it off leaves a task already running to finish as it
// started.
func noteDangerousChange(w io.Writer, before, after setup.File, name string) {
	if before.Dangerous == after.Dangerous || !connectorRunning(after) {
		return
	}
	restart := "stop it (Ctrl-C) and start it again: basecamp connect -P " + richtext.ShellQuote(name)
	if after.Dangerous {
		fmt.Fprintln(w, "Your connector is running. Restart it for dangerous mode to take effect: "+restart)
		return
	}
	fmt.Fprintln(w, "Your connector is running. A task it already started keeps running as it started, with dangerous mode, until it finishes, and gets nothing more. To stop it now, "+restart)
}
