package commands

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/basecamp/basecamp-cli/internal/appctx"
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
// mode: connect.json has it on now (fileSaysOn, read fresh at each launch, so
// turning it off needs no restart), and the connector was started trusting
// its operator alone. Trust is read once, at start, so a connector started
// admitting other people never runs their work with a shell, whatever
// connect.json says since.
func dangerousLaunches(startedWith admission.TrustMode, fileSaysOn func() bool) func() bool {
	return func() bool {
		return startedWith == admission.TrustOperator && fileSaysOn()
	}
}
