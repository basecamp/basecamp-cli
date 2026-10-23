package commands

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/auth"
	"github.com/basecamp/basecamp-cli/internal/config"
	"github.com/basecamp/basecamp-cli/internal/connector/setup"
	"github.com/basecamp/basecamp-cli/internal/output"
	"github.com/basecamp/basecamp-cli/internal/stdinarg"
	"github.com/basecamp/basecamp-cli/internal/tui"
)

// Connect as: `auth login` connects this computer either as the person's
// agent or as the person. At a terminal, against Basecamp's own server, it
// asks, the agent first; --as answers ahead of time. Everywhere else it
// connects the person, exactly as it did before there was a choice, because
// shipped scripts run it that way. The agent is connected by the same
// ceremony `auth agent connect` runs: the device flow on the first-party
// client can only ever hand back a person's token, so the choice is made
// here, before either flow starts, and never on the wire.

// The answers to "connect this computer as".
const (
	connectAsAgent = "agent"
	connectAsMe    = "me"
)

// loginAsks reports whether a person is at this terminal to answer the
// login's questions: stdin and stderr are terminals (huh reads one and draws
// to the other), nobody said otherwise with BASECAMP_NONINTERACTIVE, and no
// machine output mode, flag or configured, is reading what the login says.
// A variable so tests can say yes.
var loginAsks = func(app *appctx.App) bool {
	return !config.NonInteractiveEnv() && !app.IsMachineOutput() && stdinarg.InteractivePrompt()
}

// loginAsk asks the login's questions. A variable so tests can answer them.
var loginAsk loginPrompter = tuiLoginPrompter{}

type loginPrompter interface {
	// ConnectAs asks who this computer connects as: connectAsAgent or
	// connectAsMe. Enter alone answers connectAsAgent.
	ConnectAs() (string, error)
	Confirm(question string, yes bool) (bool, error)
}

type tuiLoginPrompter struct{}

// connectAsOptions are the answers, in the order asked. A select opens on
// its first option, so the agent goes first: Enter alone takes it.
var connectAsOptions = []tui.SelectOption{
	{Value: connectAsAgent, Label: "Your agent (recommended)"},
	{Value: connectAsMe, Label: "Yourself"},
}

func (tuiLoginPrompter) ConnectAs() (string, error) {
	return tui.Select("Connect this computer as", connectAsOptions)
}

func (tuiLoginPrompter) Confirm(question string, yes bool) (bool, error) {
	return tui.Confirm(question, yes)
}

// parseConnectAs validates --as. Empty means it wasn't given.
func parseConnectAs(raw string) (string, error) {
	switch raw {
	case "", connectAsAgent, connectAsMe:
		return raw, nil
	}
	return "", output.ErrUsage(fmt.Sprintf("Invalid --as %q: use 'agent' (your Basecamp agent) or 'me' (yourself)", raw))
}

// browserLogin is what an OAuth login was asked for: who it connects as,
// and how the flow reaches a browser.
type browserLogin struct {
	as         string
	scope      string
	noBrowser  bool
	remote     bool
	local      bool
	deviceCode bool
	loginHint  string
	expect     int64

	// provider is the authorization server, once something has discovered
	// it; nil until then.
	provider *auth.Provider
}

// runBrowserLogin runs the OAuth login: the agent connection for --as
// agent, and otherwise the person's login, which asks first when it can.
func runBrowserLogin(cmd *cobra.Command, app *appctx.App, l browserLogin) error {
	if l.as == connectAsAgent {
		if l.loginHint != "" {
			return output.ErrUsageHint("--login-hint only applies when connecting as yourself",
				"Drop --login-hint: you approve an agent connection in a browser already signed in to Basecamp. Or connect as yourself with --as me.")
		}
		if l.expect != 0 {
			return output.ErrUsage("--expect-identity cannot be checked for an agent connection: an agent is not a person")
		}
		return runLoginAsAgent(cmd, app, l)
	}
	return runLoginAsYourself(cmd, app, l)
}

// runLoginAsYourself signs the person in: the device flow against
// Basecamp's own server, the Launchpad flow otherwise. When nothing has
// answered who this computer connects as and a person can, it asks first,
// once discovery has found a server that can connect an agent.
func runLoginAsYourself(cmd *cobra.Command, app *appctx.App, l browserLogin) error {
	if err := refuseMachineOutputLogin(app, "the login command"); err != nil {
		return err
	}
	if err := refuseNonInteractiveLogin(l.deviceCode); err != nil {
		return err
	}
	if l.expect != 0 && os.Getenv("BASECAMP_TOKEN") != "" {
		return errEnvTokenShadows("--expect-identity cannot be checked while BASECAMP_TOKEN is set")
	}
	// A person's login needs its profile to exist already; an agent
	// connection creates the one it is given. So a profile that doesn't
	// exist yet is refused here only when nothing will be asked, and
	// otherwise once the answer is yourself.
	missing := ""
	if name := app.Config.ActiveProfile; name != "" {
		existing, ok := app.Config.Profiles[name]
		switch {
		case !ok:
			missing = name
		default:
			if err := requireProfileBinding(app, name, existing); err != nil {
				return err
			}
		}
	}
	asks := shouldAskConnectAs(cmd.Context(), app, l)
	if missing != "" && !asks {
		return errLoginProfileMissing(missing)
	}

	remote := l.remote || l.deviceCode

	w := cmd.OutOrStdout()
	r := output.NewRendererWithTheme(w, false, tui.ResolveTheme(tui.DetectDark()))
	logger := func(msg string) { fmt.Fprintln(w, msg) }

	if app.Config.ActiveProfile != "" {
		fmt.Fprintln(w, r.Summary.Render(fmt.Sprintf("Starting authentication for profile %q...", app.Config.ActiveProfile)))
	} else {
		fmt.Fprintln(w, r.Summary.Render("Starting Basecamp authentication..."))
	}

	if asks {
		provider, err := app.Auth.DiscoverProvider(cmd.Context(), logger)
		if err != nil {
			return err
		}
		l.provider = provider
		if provider.Basecamp() {
			choice, err := loginAsk.ConnectAs()
			if err != nil {
				return loginPromptOutcome(err, w, r)
			}
			if choice == connectAsAgent {
				l.as = connectAsAgent
				return runLoginAsAgent(cmd, app, l)
			}
		}
	}
	if missing != "" {
		return errLoginProfileMissing(missing)
	}

	// With an expectation the login is assertive: the token is
	// checked before it is stored, and a mismatch stores nothing.
	// Without one the identity line stays informational.
	verifier := &loginVerifier{app: app, expectIdentity: l.expect, account: app.Config.AccountID, strict: l.expect != 0}
	ctx, stop := loginContext(cmd)
	result, err := app.Auth.Login(ctx, auth.LoginOptions{
		Scope:     l.scope,
		NoBrowser: l.noBrowser,
		Remote:    remote,
		Local:     l.local,
		LoginHint: l.loginHint,
		Provider:  l.provider,
		Logger:    logger,
		Progress:  w,
		Verify:    verifier.verify,
	})
	err = loginOutcome(ctx, err, w, r)
	stop()
	if err != nil {
		return err
	}

	fmt.Fprintln(w)
	fmt.Fprintln(w, r.Success.Render("Authentication successful!"))

	if result.Scope != "" {
		fmt.Fprintln(w, r.Muted.Render(fmt.Sprintf("Access: %s", result.Scope)))
	}

	if who := verifier.who; who != nil {
		if who.PersonID != 0 {
			_ = app.Auth.SetUserIdentity(cmd.Context(), strconv.FormatInt(who.PersonID, 10), who.Email)
		}
		fmt.Fprintln(w, r.Data.Render("Logged in as: "+who.label()))
	}

	printAgentNudge(w, r)

	return nil
}

// errLoginProfileMissing refuses a person's login under a profile that
// doesn't exist: it has no base URL or account of its own to sign in to.
func errLoginProfileMissing(name string) error {
	return output.ErrUsageHint(fmt.Sprintf("Profile %q does not exist", name),
		fmt.Sprintf("Create it with `basecamp profile create %s`, or import a token: `... | basecamp auth login --with-token -P %s --account <id>`.", name, name))
}

// shouldAskConnectAs reports whether the login asks who this computer
// connects as. Only when nothing answered it already: no --as, no flag that
// only means something for a person (--login-hint, --expect-identity), and a
// person at the terminal to answer. And not when the login is putting a
// person's own login back — the profile, or the top-level credential, holds
// one already — since that is signing back in, not a new connection; a
// credential that can't be read counts as one, so the login goes on as it
// always did.
func shouldAskConnectAs(ctx context.Context, app *appctx.App, l browserLogin) bool {
	if l.as != "" || l.loginHint != "" || l.expect != 0 || !loginAsks(app) {
		return false
	}
	kind, err := credentialKindOf(ctx, app.Auth)
	return err == nil && kind != setup.KindBotUser
}

// runLoginAsAgent connects this computer as the person's agent, by the
// ceremony `auth agent connect` runs, under the profile loginAgentProfile
// picks. When Basecamp connects no agent — declined, or not available to
// this person — it offers to connect the person instead, if one is at the
// terminal to say yes; otherwise it fails and says how.
func runLoginAsAgent(cmd *cobra.Command, app *appctx.App, l browserLogin) error {
	if err := refuseAgentConnectOutputMode(app); err != nil {
		return err
	}
	if err := refuseNonInteractiveAgentConnect(); err != nil {
		return err
	}
	if os.Getenv("BASECAMP_TOKEN") != "" {
		return errEnvTokenShadows("BASECAMP_TOKEN is set")
	}
	if l.scope != "" && l.scope != "read" && l.scope != "full" {
		return output.ErrUsage("Invalid scope. Use 'read' or 'full'")
	}

	machine := app.Flags.JSON || app.Flags.Agent
	human := cmd.OutOrStdout()
	if machine {
		human = cmd.ErrOrStderr()
	}
	r := output.NewRendererWithTheme(human, false, tui.ResolveTheme(tui.DetectDark()))

	// Choosing the agent may move the login onto another profile; if
	// Basecamp then connects none and the person signs in instead, that
	// login must land where it would have without the detour.
	before := *app.Config
	before.Sources = maps.Clone(app.Config.Sources)

	name, reconnects, err := loginAgentProfile(cmd.Context(), app)
	if err != nil {
		return err
	}
	if reconnects {
		fmt.Fprintln(human, r.Muted.Render(fmt.Sprintf("Profile %q already holds an agent connection. Approving replaces the credential stored here.", name)))
	}

	// Asking meant discovering first, for the base URL the login started
	// on, and that also fixed the address policy of this process's OAuth
	// client to it. A profile bound to another Basecamp needs both of its
	// own, so it is refused before anything is requested, with the command
	// that starts on it. Without the question nothing was discovered yet,
	// and the ceremony discovers for the profile's own Basecamp.
	if l.provider != nil && config.NormalizeBaseURL(app.Config.BaseURL) != config.NormalizeBaseURL(before.BaseURL) {
		return output.ErrUsageHint(
			fmt.Sprintf("Profile %q is on %s, not %s where this login started", name, app.Config.BaseURL, before.BaseURL),
			"Connect your agent there: basecamp auth login --as agent -P "+name)
	}

	err = connectAgentProfile(cmd, app, agentConnectFlags{
		scope: l.scope,
		// The ceremony always prints a link and a code, so the flags that
		// ask a login for that are the ceremony's --no-browser.
		noBrowser:  l.noBrowser || l.remote || l.deviceCode,
		local:      l.local,
		machine:    machine,
		provider:   l.provider,
		sayDefault: true,
	})
	if !errors.Is(err, auth.ErrNoAgentConnection) {
		return err
	}

	refusal := noAgentLoginError(err)
	if !loginAsks(app) {
		return refusal
	}
	fmt.Fprintln(human)
	fmt.Fprintln(human, r.Warning.Render(refusal.Message+"."))
	yes, err := loginAsk.Confirm("Connect as yourself instead?", true)
	if err != nil {
		return loginPromptOutcome(err, human, r)
	}
	if !yes {
		return refusal
	}
	fmt.Fprintln(human)
	*app.Config = before
	l.as = connectAsMe
	return runLoginAsYourself(cmd, app, l)
}

// loginAgentProfile is the profile the login connects the agent under, as
// the guided setup picks it: the one named with -P or BASECAMP_PROFILE,
// else the active profile when it holds an agent, else the one called
// "agent", created by connecting if need be. It reports whether the profile
// holds an agent's credential already, which connecting replaces. A profile
// holding a person's login is never turned into an agent's: that would sign
// the person out of it, quietly.
func loginAgentProfile(ctx context.Context, app *appctx.App) (string, bool, error) {
	name, err := guidedSetupProfile(ctx, app)
	if err != nil {
		return "", false, err
	}
	kind, err := credentialKindOf(ctx, app.Auth)
	if err != nil {
		return "", false, err
	}
	if kind == setup.KindBotUser {
		return "", false, output.ErrUsageHint(fmt.Sprintf("Profile %q holds a person's login, not an agent's", name),
			"Connect your agent under a profile of its own: basecamp auth login --as agent -P <name>.")
	}
	return name, kind == setup.KindAgent, nil
}

// noAgentLoginError is what the login says when Basecamp connected no
// agent: in its own words, since the ceremony's remedy is to run the
// connect command again, and from here the other answer is yourself. The
// ceremony's refusal stays the cause.
func noAgentLoginError(err error) *output.Error {
	refusal := output.ErrAuth("No agent was connected: the connection was declined, or agents aren't available to you yet")
	refusal.Hint = "Connect as yourself instead: basecamp auth login --as me"
	refusal.Cause = err
	return refusal
}

// loginPromptOutcome turns a question the person didn't answer into the
// error the root renders. Escape or Ctrl-C at a question is a canceled
// login, said as loginOutcome says it.
func loginPromptOutcome(err error, w io.Writer, r *output.Renderer) error {
	if !errors.Is(err, tui.ErrCanceled) {
		return err
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, r.Muted.Render("Login canceled. Nothing was stored."))
	return output.ErrInterrupted("login canceled")
}
