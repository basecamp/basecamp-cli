package commands

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/auth"
	"github.com/basecamp/basecamp-cli/internal/config"
	"github.com/basecamp/basecamp-cli/internal/output"
	"github.com/basecamp/basecamp-cli/internal/tui"
)

// newAuthAgentCmd groups the commands that connect this computer to a
// Basecamp agent. Bare invocation shows help, as every command group does.
func newAuthAgentCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agent",
		Short: "Connect this computer to a Basecamp agent",
		Long: `Connect this computer to a Basecamp agent — an identity your Basecamp
administrator created — so commands run as that agent.

The connection hands this CLI the agent's own OAuth client, which mints the
access tokens it spends. Nobody pastes a credential: you approve the
connection in your browser and pick which agent this computer acts as.`,
	}
	cmd.AddCommand(newAuthAgentConnectCmd())
	return cmd
}

// newAuthAgentConnectCmd runs the agent-connection handshake and stores
// what it is given under the named profile.
func newAuthAgentConnectCmd() *cobra.Command {
	var deviceName string
	var softwareName string
	var scope string
	var noBrowser bool
	var local bool

	cmd := &cobra.Command{
		Use:   "connect",
		Short: "Connect this computer to a Basecamp agent",
		Long: `Connect this computer to a Basecamp agent and store its credential under
a profile.

The command asks Basecamp for a connection, prints a link and a one-time
code, and opens the link in your browser. Sign in, check the code matches,
pick which agent this computer acts as, and approve it. The credential is
the agent's own OAuth client: this CLI mints an access token from it
whenever it needs one, so there is no token to paste and none to renew by
hand.

The account comes from the agent you approve — pass --account only to
insist on one, and the connection is refused if the agent belongs to
another.

Examples:
  basecamp auth agent connect -P agent
  basecamp auth agent connect -P agent --scope read
  basecamp auth agent connect -P agent --device-name build-box --no-browser

Over SSH, in CI, or on a host with no display the browser is skipped and
the link is yours to open on any device; --local forces a launch anyway,
--no-browser skips it. Ctrl-C cancels the wait and stores nothing.

With --json, the ceremony is data for whatever runs it, and stdout carries
two JSON values. The first is one line, written as soon as Basecamp
answers the request:

  {"type":"verification","verification_uri":"…","user_code":"WDJB-MJHT","expires_at":"…","expires_in":600}

The second is the result envelope every command prints: the profile, its
account, the access approved and the agent's client id — or the error, if
the operator declined, the code expired, or nothing could be stored. The words meant
for a person go to stderr, and the browser opens as it would without
--json. Neither line ever carries the client secret or the device code.
The link and the code are for the person approving at this computer:
show them there, never anywhere else.

Disconnecting is done in Basecamp: disconnecting the agent there kills the
client secret this holds. Drop the local copy with ` + "`basecamp auth logout -P <profile>`" + `.`,
		Annotations: map[string]string{AnnotationProfileMayCreate: "true"},
		Args:        cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if app == nil {
				return fmt.Errorf("app not initialized")
			}

			if err := refuseAgentConnectOutputMode(app); err != nil {
				return err
			}
			if err := refuseNonInteractiveAgentConnect(); err != nil {
				return err
			}
			if os.Getenv("BASECAMP_TOKEN") != "" {
				return errEnvTokenShadows("BASECAMP_TOKEN is set")
			}
			if scope != "" && scope != "read" && scope != "full" {
				return output.ErrUsage("Invalid scope. Use 'read' or 'full'")
			}

			return connectAgentProfile(cmd, app, agentConnectFlags{
				deviceName: deviceName, softwareName: softwareName, scope: scope, noBrowser: noBrowser, local: local,
				machine: app.Flags.JSON || app.Flags.Agent,
			})
		},
	}

	cmd.Flags().StringVar(&deviceName, "device-name", "", "Name this computer on the approval page (default: this host's name)")
	cmd.Flags().StringVar(&softwareName, "software-name", "", "Name the software this CLI runs inside, shown as \"Connected to\"")
	cmd.Flags().StringVar(&scope, "scope", "", "OAuth scope to ask for: 'read' or 'full' (default full; the approval page can downgrade it)")
	cmd.Flags().BoolVar(&noBrowser, "no-browser", false, "Print the link instead of opening a browser")
	cmd.Flags().BoolVar(&local, "local", false, "Treat this as a local session: open the browser here even over SSH, in CI, or without a display")

	return cmd
}

// agentConnectFlags are the choices an agent connection takes.
type agentConnectFlags struct {
	deviceName   string
	softwareName string
	scope        string
	noBrowser    bool
	local        bool

	// quiet leaves out what was stored, for the guided setup, which says
	// in one line of its own which agent this computer is now connected as.
	quiet bool

	// machine reads the ceremony out as data: the verification line and
	// the result envelope on stdout, the operator's words on stderr.
	machine bool
}

// refuseAgentConnectOutputMode lets the ceremony run under --json (and
// --agent, its data-only form), and refuses the output modes that cannot
// carry it. A --jq filter runs over one envelope, and this writes two
// lines; --quiet, --ids-only and --count would each throw the verification
// line away, and with it the only thing the operator needs.
//
// --agent is let through as --json is: it prints the result's data alone,
// after the same verification line.
func refuseAgentConnectOutputMode(app *appctx.App) error {
	if app.Flags.JQFilter != "" {
		return output.ErrJQNotSupported("the agent connection")
	}
	if app.Flags.Quiet || app.Flags.IDsOnly || app.Flags.Count {
		return output.ErrUsageHint("The agent connection cannot run under --quiet, --ids-only or --count",
			"Run it plainly for a person at a terminal, or with --json to read the link, the code and the result as JSON lines.")
	}
	return nil
}

// agentConnectVerification is the ceremony's first JSON line: what the
// operator needs to approve the connection, and nothing else.
type agentConnectVerification struct {
	Type            string `json:"type"`
	VerificationURI string `json:"verification_uri"`
	UserCode        string `json:"user_code"`
	ExpiresAt       string `json:"expires_at"`
	ExpiresIn       int    `json:"expires_in"`
}

// writeAgentConnectVerification writes the verification line, one JSON
// object terminated by a newline, so a reader can act on it while the
// ceremony is still waiting.
func writeAgentConnectVerification(w io.Writer, intake auth.AgentConnectIntake) {
	line := agentConnectVerification{
		Type:            "verification",
		VerificationURI: intake.VerificationURI,
		UserCode:        intake.UserCode,
		ExpiresAt:       intake.ExpiresAt.UTC().Format(time.RFC3339),
		ExpiresIn:       max(0, int(time.Until(intake.ExpiresAt).Round(time.Second).Seconds())),
	}
	// Encode cannot fail on a struct of strings and ints, and a write
	// that fails is a stdout that is gone, which the result line will
	// meet too.
	_ = json.NewEncoder(w).Encode(line)
}

// connectAgentProfile runs the agent-connection handshake for the active
// profile and says what it stored. Both `auth agent connect` and the guided
// `connect setup` run it, so a person sees one connection either way.
func connectAgentProfile(cmd *cobra.Command, app *appctx.App, f agentConnectFlags) error {
	target, err := resolveAgentConnectProfile(app)
	if err != nil {
		return err
	}

	w := cmd.OutOrStdout()
	// Under --json stdout is the data, so the operator's half of the
	// ceremony — the link, the code, the wait line — goes to stderr.
	human := w
	var onIntake func(auth.AgentConnectIntake)
	if f.machine {
		human = cmd.ErrOrStderr()
		onIntake = func(intake auth.AgentConnectIntake) { writeAgentConnectVerification(w, intake) }
	}
	r := output.NewRendererWithTheme(human, false, tui.ResolveTheme(tui.DetectDark()))

	var isDefault bool
	ctx, stop := loginContext(cmd)
	result, err := app.Auth.ConnectAgent(ctx, auth.AgentConnectOptions{
		DeviceName:   connectDeviceName(f.deviceName),
		SoftwareName: f.softwareName,
		Scope:        f.scope,
		NoBrowser:    f.noBrowser,
		Local:        f.local,
		Logger:       func(msg string) { fmt.Fprintln(human, msg) },
		Progress:     human,
		OnIntake:     onIntake,
		BeforeStore: func(conn *auth.AgentConnection) error {
			registered, commitErr := target.commit(app, conn)
			isDefault = registered
			return commitErr
		},
	})
	err = loginOutcome(ctx, err, human, r)
	stop()
	if err != nil || f.quiet {
		return err
	}

	if f.machine {
		data := map[string]any{
			"profile":         target.name,
			"account_id":      result.AccountID,
			"base_url":        app.Config.BaseURL,
			"source":          "agent_connection",
			"oauth_type":      result.OAuthType,
			"scope":           result.Scope,
			"client_id":       result.ClientID,
			"profile_created": target.existing == nil,
		}
		if isDefault {
			data["default"] = true
		}
		return app.OK(data, output.WithSummary(fmt.Sprintf("Connected profile %q to a Basecamp agent", target.name)))
	}

	fmt.Fprintln(w)
	fmt.Fprintln(w, r.Success.Render(fmt.Sprintf("Connected profile %q to a Basecamp agent", target.name)))
	fmt.Fprintln(w, r.Muted.Render(fmt.Sprintf("Profile: %s · Account: %s · Access: %s · Token: minted on demand, no refresh token",
		target.name, result.AccountID, result.Scope)))
	if target.existing == nil {
		line := fmt.Sprintf("Created profile %q for account %s", target.name, result.AccountID)
		if isDefault {
			line += " (default)"
		}
		fmt.Fprintln(w, r.Muted.Render(line))
	}
	fmt.Fprintln(w, r.Muted.Render(fmt.Sprintf("Check it any time: basecamp auth status -P %s", target.name)))
	return nil
}

// connectDeviceName is what the approval page calls this computer: what
// the operator named, or this host's own name. A hostname that cannot be
// read leaves it empty, and the flow refuses with the flag to pass — the
// intake requires the name, and inventing one ("unknown host") would put a
// row nobody can recognize on the operator's connections list.
func connectDeviceName(named string) string {
	if named != "" {
		return named
	}
	host, err := os.Hostname()
	if err != nil {
		return ""
	}
	return host
}

// refuseNonInteractiveAgentConnect is the environment gate: the ceremony
// waits on a person opening a link, signing in, and picking the agent, so
// BASECAMP_NONINTERACTIVE — which says nobody is at this terminal — has to
// refuse it. There is no --device-code equivalent here: the approval page
// IS the device-code half, and it still needs someone signed in.
func refuseNonInteractiveAgentConnect() error {
	if !config.NonInteractiveEnv() {
		return nil
	}
	return output.ErrUsageHint("An agent connection cannot run under BASECAMP_NONINTERACTIVE",
		"Approving a connection needs a person signed in to Basecamp. Unset BASECAMP_NONINTERACTIVE, or store an agent credential headlessly: "+
			"`... | basecamp auth login --with-client-credentials --client-id <id> -P <profile> --account <id>`.")
}

// agentConnectTarget is the profile a connection stores its credential
// under. It is the headless-login profile target with one difference that
// changes the order everything happens in: the ACCOUNT IS THE SERVER'S TO
// NAME. The operator picks an agent on the approval page, and the agent
// belongs to whichever account it belongs to — so the entry cannot be
// written, or even fully decided, until the poll has answered.
//
// What can be decided first still is: the profile's name, its binding, and
// whether the global config file could take the entry at all. Those are
// the refusals that would otherwise arrive after an operator had approved
// something.
type agentConnectTarget struct {
	name string

	// existing is the configured entry, nil when the connection creates
	// the profile.
	existing *config.ProfileConfig

	// asserted is the account this invocation named on the command line or
	// in the environment, "" when it named none. A connection to an agent
	// in another account is refused rather than quietly stored.
	asserted string
}

// resolveAgentConnectProfile works out which profile the connection writes
// to WITHOUT writing anything, and refuses everything refusable before the
// operator is sent to their browser.
func resolveAgentConnectProfile(app *appctx.App) (*agentConnectTarget, error) {
	name := app.Config.ActiveProfile
	if name == "" {
		return nil, output.ErrUsageHint("An agent connection stores the credential under a named profile",
			"Pass -P/--profile <name>. The account comes from the agent you approve, so --account is not needed.")
	}
	if !isValidProfileName(name) {
		return nil, output.ErrUsage(fmt.Sprintf("Invalid profile name %q: use only letters, numbers, hyphens, and underscores", name))
	}

	target := &agentConnectTarget{name: name, existing: app.Config.Profiles[name]}
	if target.existing != nil {
		if err := requireProfileBinding(app, name, target.existing); err != nil {
			return nil, err
		}
	}
	if accountGivenExplicitly(app) {
		if err := requireNumericAccount(app.Config.AccountID); err != nil {
			return nil, err
		}
		target.asserted = app.Config.AccountID
	}

	// Registering or binding rewrites the global config file. Prove it can
	// be before an operator approves anything — and, when the entry exists
	// without an account, that the account written there takes effect.
	if target.existing == nil || target.existing.AccountID == "" {
		if err := globalConfigTakesProfiles(); err != nil {
			return nil, err
		}
	}
	if target.existing != nil && target.existing.AccountID == "" {
		if blocker := globalBindingBlocker(app.Config, name); blocker != "" {
			return nil, output.ErrUsageHint(fmt.Sprintf("Profile %q has no account, and connecting cannot bind one", name),
				blocker+", then rerun the connection.")
		}
	}
	return target, nil
}

// commit writes the profile entry the approved connection needs, and
// reports whether the profile is the default one.
//
// It runs between the mint that proved the credential and the write that
// stores it: the entry goes in first because an entry without a credential
// is a visible, harmless state, where a stored client secret under a
// profile nothing registered is an orphan nobody will find again. An error
// here aborts the connection and stores nothing.
func (t *agentConnectTarget) commit(app *appctx.App, conn *auth.AgentConnection) (bool, error) {
	if t.asserted != "" && !accountIDsEqual(t.asserted, conn.AccountID) {
		return false, output.ErrUsageHint(
			fmt.Sprintf("The approved agent belongs to account %s, not the %s this command named", conn.AccountID, t.asserted),
			"Nothing was stored. Rerun without --account to take the agent's own account, or approve an agent in that account.")
	}

	isDefault := app.Config.DefaultProfile == t.name
	switch {
	case t.existing == nil:
		entry := &config.ProfileConfig{BaseURL: app.Config.BaseURL, AccountID: conn.AccountID, Scope: conn.Scope}
		registered, err := registerProfile(t.name, entry)
		if err != nil {
			return false, err
		}
		isDefault = registered
		if app.Config.Profiles == nil {
			app.Config.Profiles = make(map[string]*config.ProfileConfig)
		}
		app.Config.Profiles[t.name] = entry
	case t.existing.AccountID == "":
		if err := bindProfileAccount(t.name, conn.AccountID); err != nil {
			return false, err
		}
		t.existing.AccountID = conn.AccountID
	case !accountIDsEqual(t.existing.AccountID, conn.AccountID):
		return false, output.ErrUsageHint(
			fmt.Sprintf("Profile %q is bound to account %s%s, and the approved agent belongs to account %s", t.name, t.existing.AccountID, boundIn(app.Config, t.name), conn.AccountID),
			"Nothing was stored. Connect the agent under a profile of its own: -P <name>.")
	}
	return isDefault, nil
}
