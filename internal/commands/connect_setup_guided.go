package commands

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/auth"
	"github.com/basecamp/basecamp-cli/internal/config"
	"github.com/basecamp/basecamp-cli/internal/connector"
	"github.com/basecamp/basecamp-cli/internal/connector/driver"
	"github.com/basecamp/basecamp-cli/internal/connector/setup"
	"github.com/basecamp/basecamp-cli/internal/output"
	"github.com/basecamp/basecamp-cli/internal/richtext"
	"github.com/basecamp/basecamp-cli/internal/tui"
)

// The guided setup. Run in a terminal with none of the flags that set
// policy, `connect setup` works out where this computer and its agent are
// and moves them forward: connect this computer to the agent, reconnect one
// Basecamp stopped accepting, set up connect.json (the agent's owner as the
// operator, its projects by name), rebuild a connect.json that cannot be
// read, and offer to keep the connector running. Running it again either
// takes the next step or says everything is set. With a policy flag, or
// with nobody at the terminal, setup is the scriptable command it always
// was.

// connectAgentProfileName is the profile the guided setup uses when none was
// named and the active one is not an agent's.
const connectAgentProfileName = "agent"

// connectSetupPolicyFlags are setup's flags that decide policy. Any of them
// makes a run the scriptable one.
var connectSetupPolicyFlags = []string{
	"expect-identity", "operator", "operator-profile", "trust", "allow",
	"serve", "unserve", "class", "watch-completions", "no-watch-completions",
	"driver", "worker", "concurrency", "deadline",
}

// connectSetupInteractive reports whether a person is at the terminal to
// answer. A variable so tests can say yes.
var connectSetupInteractive = setupCanRun

// connectSetupConnection is how the guided setup connects this computer: as
// `auth agent connect` does, naming the software on the approval page and in
// Adminland's "Connected to". A variable so tests can keep the browser shut.
var connectSetupConnection = agentConnectFlags{softwareName: "basecamp connect"}

// errNoProjectsYet is a new agent that is in no projects: the next step of a
// first setup, which happens in Basecamp, not a failure.
var errNoProjectsYet = errors.New("the agent isn't in any projects yet")

// connectSetupAsk asks the guided setup's questions. A variable so tests
// can answer them.
var connectSetupAsk connectSetupPrompter = tuiConnectSetupPrompter{}

type connectSetupPrompter interface {
	Confirm(question string, yes bool) (bool, error)
	Input(question string) (string, error)
}

type tuiConnectSetupPrompter struct{}

func (tuiConnectSetupPrompter) Confirm(question string, yes bool) (bool, error) {
	return tui.Confirm(question, yes)
}

func (tuiConnectSetupPrompter) Input(question string) (string, error) {
	return tui.Input(question, "")
}

// connectSetupGuided reports whether this run of setup is the guided one.
func connectSetupGuided(cmd *cobra.Command, app *appctx.App) bool {
	for _, name := range connectSetupPolicyFlags {
		if cmd.Flags().Changed(name) {
			return false
		}
	}
	return connectSetupInteractive(app)
}

func runGuidedConnectSetup(cmd *cobra.Command, app *appctx.App, f *connectSetupFlags) error {
	ctx := cmd.Context()
	w := cmd.OutOrStdout()
	r := output.NewRendererWithTheme(w, false, tui.ResolveTheme(tui.DetectDark()))

	if os.Getenv("BASECAMP_TOKEN") != "" {
		return errEnvTokenShadows("connect setup cannot check the agent while BASECAMP_TOKEN is set")
	}
	name, err := guidedSetupProfile(ctx, app)
	if err != nil {
		return err
	}
	if !isValidProfileName(name) {
		return output.ErrUsage(fmt.Sprintf("Invalid profile name %q: use only letters, numbers, hyphens, and underscores", name))
	}

	connected, err := ensureGuidedAgentConnection(cmd, app, name, r)
	if err != nil {
		return err
	}
	agent, err := readGuidedAgent(ctx, app, name)
	if err != nil {
		return err
	}
	if connected {
		fmt.Fprintln(w)
		fmt.Fprintln(w, r.Success.Render("✓ Connected this computer as "+richtext.SanitizeSingleLine(agent.Me.Name)))
	}

	path, err := setup.Path(config.GlobalConfigDir(), name)
	if err != nil {
		return output.ErrUsage(err.Error())
	}
	file, err := guidedConnectFile(ctx, w, r, path, agent)
	if err != nil {
		return err
	}
	worker := setup.New(name)
	if file != nil {
		worker = *file
	}
	if err := checkGuidedWorker(ctx, w, r, worker); err != nil {
		return err
	}
	if file == nil {
		err := setUpGuidedConnectFile(cmd, app, w, r, agent, f)
		if errors.Is(err, errNoProjectsYet) {
			renderNoProjectsYet(w, agent)
			return nil
		}
		if err != nil {
			return err
		}
		loaded, err := setup.Load(path)
		if err != nil {
			return output.ErrUsage("connect.json cannot be used: " + err.Error())
		}
		file = &loaded
	}

	renderGuidedSummary(w, r, agent, *file, connectorRunning(*file), name)
	return nil
}

// guidedSetupProfile is the profile the guided setup works on: the one named
// on the command line or in the environment, else the active profile when it
// holds an agent, else the agent profile, created if need be by connecting.
func guidedSetupProfile(ctx context.Context, app *appctx.App) (string, error) {
	name := app.Config.ActiveProfile
	if app.Flags.Profile != "" || os.Getenv("BASECAMP_PROFILE") != "" {
		return name, nil
	}
	if name != "" {
		kind, err := credentialKindOf(ctx, app.Auth)
		if err != nil {
			return "", err
		}
		if kind == setup.KindAgent {
			return name, nil
		}
	}
	if _, ok := app.Config.Profiles[connectAgentProfileName]; ok {
		if err := app.Config.ApplyProfile(connectAgentProfileName); err != nil {
			return "", err
		}
		// As root does after applying a profile: what this invocation named,
		// in its environment and flags, outranks the profile's own values,
		// so an --account the profile isn't bound to is refused rather than
		// replaced.
		if err := config.LoadFromEnv(app.Config); err != nil {
			return "", err
		}
		config.ApplyOverrides(app.Config, config.FlagOverrides{
			Account:  app.Flags.Account,
			Project:  app.Flags.Project,
			Todolist: app.Flags.Todolist,
			CacheDir: app.Flags.CacheDir,
		})
	} else {
		app.Config.ActiveProfile = connectAgentProfileName
	}
	return connectAgentProfileName, nil
}

// ensureGuidedAgentConnection leaves the profile holding a credential for an
// agent that Basecamp accepts: connecting this computer when it holds none,
// and offering to reconnect when Basecamp no longer takes the one it holds.
// It reports whether it connected.
func ensureGuidedAgentConnection(cmd *cobra.Command, app *appctx.App, name string, r *output.Renderer) (bool, error) {
	ctx := cmd.Context()
	w := cmd.OutOrStdout()

	kind := ""
	if _, ok := app.Config.Profiles[name]; ok {
		var err error
		if kind, err = connectCredentialKind(ctx, app); err != nil {
			return false, err
		}
	}
	switch kind {
	case "":
		fmt.Fprintln(w, "First, connect this computer to your agent in Basecamp.")
		fmt.Fprintln(w)
		return true, connectGuidedAgent(cmd, app)
	case setup.KindBotUser:
		return false, output.ErrUsageHint(fmt.Sprintf("Profile %q holds a person's login, not an agent's", name),
			"Set up the agent under a profile of its own: basecamp connect setup -P "+connectAgentProfileName)
	}

	refused, err := agentCredentialRefused(ctx, app, name)
	if err != nil || !refused {
		return false, err
	}
	fmt.Fprintln(w, r.Warning.Render("This computer's connection to your agent was disconnected in Basecamp, or it was connected on another computer."))
	reconnect, err := connectSetupAsk.Confirm("Connect this computer to it again?", true)
	if err != nil {
		return false, err
	}
	if !reconnect {
		return false, output.ErrAuth("This computer is not connected to the agent")
	}
	fmt.Fprintln(w)
	return true, connectGuidedAgent(cmd, app)
}

// connectGuidedAgent connects this computer, leaving out what was stored:
// the guided setup says which agent it connected in a line of its own.
func connectGuidedAgent(cmd *cobra.Command, app *appctx.App) error {
	flags := connectSetupConnection
	flags.quiet = true
	return connectAgentProfile(cmd, app, flags)
}

// agentCredentialRefused reports whether Basecamp refuses the profile's
// agent credential: the token endpoint refusing its secret, or the account
// refusing a token it minted before.
func agentCredentialRefused(ctx context.Context, app *appctx.App, name string) (bool, error) {
	token, err := app.Auth.AccessToken(ctx)
	if errors.Is(err, auth.ErrAgentCredentialRefused) {
		return true, nil
	}
	if err != nil {
		return false, output.ErrAuth(fmt.Sprintf("Profile %q holds a credential that does not produce a token: %s", name, setup.ErrorText(err)))
	}
	accountID, err := connectAccount(app, name)
	if err != nil {
		return false, err
	}
	client := connectSDKClient(app, &basecamp.StaticTokenProvider{Token: token}).ForAccount(accountID)
	_, err = client.People().Me(ctx)
	var apiErr *basecamp.Error
	if errors.As(err, &apiErr) && apiErr.HTTPStatus == 401 {
		return true, nil
	}
	return false, nil
}

// guidedAgent is what the guided setup knows about the agent it sets up.
type guidedAgent struct {
	Profile   string
	AccountID string
	Me        setup.Person
	Owner     setup.Person
	HasOwner  bool
	Projects  []guidedProject
	Listed    bool // whether Basecamp listed the agent's projects
	client    *basecamp.AccountClient
	projectsE error
}

type guidedProject struct {
	ID   int64
	Name string
}

// readGuidedAgent reads the agent the profile holds: who it is, who it works
// for, and which projects it is in.
func readGuidedAgent(ctx context.Context, app *appctx.App, name string) (guidedAgent, error) {
	accountID, err := connectAccount(app, name)
	if err != nil {
		return guidedAgent{}, err
	}
	client := connectSDKClient(app, &managerTokens{mgr: app.Auth}).ForAccount(accountID)
	me, err := setup.SDKReader{Client: client}.Me(ctx)
	if err != nil {
		return guidedAgent{}, output.ErrAuth(fmt.Sprintf("Could not read who profile %q is in account %s: %s", name, accountID, setup.ErrorText(err)))
	}
	agent := guidedAgent{Profile: name, AccountID: accountID, Me: me, client: client}
	if agent.Owner, agent.HasOwner, err = readAgentOwner(ctx, client, setup.KindAgent); err != nil {
		return guidedAgent{}, output.ErrAuth(fmt.Sprintf("Could not read who agent %q works for: %s", me.Name, setup.ErrorText(err)))
	}
	agent.Projects, agent.projectsE = listAgentProjects(ctx, client)
	agent.Listed = agent.projectsE == nil
	return agent, nil
}

// readAgentOwner is who a personal agent works for, as Basecamp names them in
// the agent's own profile (`boss`). ok is false for an agent that works for no
// one in particular, for a bot user's login, and for a Basecamp that does not
// say.
//
// The SDK's Person has no owner yet, so the profile is read through the
// account client's plain GET, the one `basecamp api get` makes, and only the
// one field is taken from it.
func readAgentOwner(ctx context.Context, client *basecamp.AccountClient, kind string) (setup.Person, bool, error) {
	if kind != setup.KindAgent {
		return setup.Person{}, false, nil
	}
	resp, err := client.Get(ctx, "/my/profile.json")
	if err != nil {
		return setup.Person{}, false, err
	}
	var profile struct {
		Boss *struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		} `json:"boss"`
	}
	if err := resp.UnmarshalData(&profile); err != nil {
		return setup.Person{}, false, err
	}
	if profile.Boss == nil || profile.Boss.ID <= 0 {
		return setup.Person{}, false, nil
	}
	return setup.Person{ID: profile.Boss.ID, Name: profile.Boss.Name, PersonableType: "User"}, true, nil
}

// errOperatorRequired refuses a setup that names no operator for an agent
// Basecamp gives no owner.
func errOperatorRequired() error {
	return output.ErrUsageHint("Setup needs to know who the operator is",
		"Pass --operator-profile <your profile> (or --operator <your person id>). The operator is the person the agent takes instructions from; only a personal agent's owner is taken without asking.")
}

// listAgentProjects is every active project the agent is in.
func listAgentProjects(ctx context.Context, client *basecamp.AccountClient) ([]guidedProject, error) {
	result, err := client.Projects().List(ctx, nil)
	if err != nil {
		return nil, err
	}
	projects := make([]guidedProject, 0, len(result.Projects))
	for _, p := range result.Projects {
		projects = append(projects, guidedProject{ID: p.ID, Name: p.Name})
	}
	return projects, nil
}

// guidedConnectFile is the connect.json this profile already has, nil when
// the guided setup has to make one: there is none, or the person chose to
// set up afresh over one that cannot be used or names another agent.
func guidedConnectFile(ctx context.Context, w io.Writer, r *output.Renderer, path string, agent guidedAgent) (*setup.File, error) {
	existing, err := setup.Load(path)
	var problem string
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil, nil
	case err != nil:
		problem = "Your connector's settings (connect.json) can't be used: " + setup.ErrorText(err)
	case existing.Profile != agent.Profile, !accountIDsEqual(existing.AccountID, agent.AccountID), existing.Agent.PersonID != agent.Me.ID:
		problem = "Your connector's settings (connect.json) are for a different agent than the one this computer is connected to."
	default:
		return &existing, nil
	}

	fmt.Fprintln(w, r.Warning.Render(richtext.SanitizeSingleLine(problem)))
	afresh, err := connectSetupAsk.Confirm("Set them up again? The old file is kept beside the new one.", true)
	if err != nil {
		return nil, err
	}
	if !afresh {
		return nil, output.ErrUsageHint("connect.json was left as it is",
			"Nothing was changed. Run setup again to set it up afresh, or fix "+richtext.SanitizeSingleLine(path)+" by hand.")
	}
	unlock, err := setup.Lock(ctx, path)
	if err != nil {
		return nil, classifyLockError(agent.Profile, err)
	}
	defer unlock()
	aside := path + ".broken-" + time.Now().UTC().Format("20060102T150405Z")
	if err := os.Rename(path, aside); err != nil {
		return nil, fmt.Errorf("could not move %s aside: %w", richtext.SanitizeSingleLine(path), err)
	}
	fmt.Fprintln(w, r.Muted.Render("Moved the old file to "+richtext.SanitizeSingleLine(aside)))
	fmt.Fprintln(w)
	return nil, nil
}

// setUpGuidedConnectFile writes connect.json through setup itself: the
// owner as the operator (setup's own default), and the projects the person
// picks by name.
//
// The owner is "you": only a personal agent's owner can connect a computer
// to it, so the person who approved this computer, and who is running setup,
// is the owner.
func setUpGuidedConnectFile(cmd *cobra.Command, app *appctx.App, w io.Writer, r *output.Renderer, agent guidedAgent, f *connectSetupFlags) error {
	if agent.HasOwner {
		fmt.Fprintln(w, r.Success.Render("✓ "+richtext.SanitizeSingleLine(agent.Owner.Name)+" (you) is the only person who can give it work."))
	} else {
		return errOperatorRequired()
	}

	serve, err := chooseGuidedProjects(w, agent)
	if err != nil {
		return err
	}
	fmt.Fprintln(w)

	setupFlags := *f
	setupFlags.guided = true
	for _, p := range serve {
		setupFlags.serve = append(setupFlags.serve, strconv.FormatInt(p.ID, 10))
	}
	return runConnectSetup(cmd, app, &setupFlags)
}

// chooseGuidedProjects asks which of the agent's projects it serves,
// defaulting to all of them.
func chooseGuidedProjects(w io.Writer, agent guidedAgent) ([]guidedProject, error) {
	name := richtext.SanitizeSingleLine(agent.Me.Name)
	if !agent.Listed {
		return nil, output.ErrUsageHint(fmt.Sprintf("Could not list %s's projects: %s", name, setup.ErrorText(agent.projectsE)),
			"Serve a project by its id: basecamp connect setup -P "+richtext.ShellQuote(agent.Profile)+" --serve <project-id>")
	}
	if len(agent.Projects) == 0 {
		return nil, errNoProjectsYet
	}

	names := make([]string, len(agent.Projects))
	for i, p := range agent.Projects {
		names[i] = richtext.SanitizeSingleLine(p.Name)
	}
	question := fmt.Sprintf("Work in all %d of %s's projects? %s", len(names), name, strings.Join(names, ", "))
	if len(names) == 1 {
		question = fmt.Sprintf("Work in %s's project, %s?", name, names[0])
	}
	all, err := connectSetupAsk.Confirm(question, true)
	if err != nil {
		return nil, err
	}
	if all {
		return agent.Projects, nil
	}

	fmt.Fprintln(w, "Which projects should it work in?")
	for i, n := range names {
		fmt.Fprintf(w, "  %d. %s\n", i+1, n)
	}
	answer, err := connectSetupAsk.Input("Numbers, separated by commas")
	if err != nil {
		return nil, err
	}
	var chosen []guidedProject
	for part := range strings.SplitSeq(answer, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		n, err := strconv.Atoi(part)
		if err != nil || n < 1 || n > len(agent.Projects) {
			return nil, output.ErrUsage(fmt.Sprintf("%q is not a number from the list", part))
		}
		chosen = append(chosen, agent.Projects[n-1])
	}
	if len(chosen) == 0 {
		return nil, output.ErrUsage("No projects were chosen, so nothing was set up")
	}
	return chosen, nil
}

// checkGuidedWorker starts the worker as the connector would, before
// anything is written or anyone is told the agent is set up: an agent whose
// AI cannot start fails every request it is given, and the person who
// mentioned it is the last to be able to fix that.
func checkGuidedWorker(ctx context.Context, w io.Writer, r *output.Renderer, file setup.File) error {
	p, ok := connectWorkerPreflight(ctx, file)
	if !ok {
		return nil
	}
	for _, c := range p.Checks {
		if c.Status == driver.PreflightWarn {
			fmt.Fprintln(w, r.Warning.Render(richtext.SanitizeSingleLine(c.Message)))
		}
	}
	failed, bad := p.Failed()
	if !bad {
		named := p.Product
		if p.Version != "" {
			named += " " + p.Version
		}
		fmt.Fprintln(w, r.Success.Render("✓ "+richtext.SanitizeSingleLine(named)+" is ready"))
		return nil
	}
	hint := strings.TrimSpace(failed.Hint + " Then run this again: basecamp connect setup")
	return output.ErrUsageHint(richtext.SanitizeSingleLine(failed.Message), richtext.SanitizeSingleLine(hint))
}

// connectorRunning reports whether a connector for this setup is running:
// the instance lock's holder, as `connect status` reads it, is alive.
func connectorRunning(file setup.File) bool {
	dir, err := connectStatePath(file, false)
	if err != nil {
		return false
	}
	holder, ok := connector.InstanceHolder(dir, file.AccountID, file.Agent.PersonID)
	return ok && processPresence(holder.PID) == pidPresent
}

// renderGuidedChecks is a passing setup in the guided one's words: any
// warnings, by name, then that everything checks out.
func renderGuidedChecks(w io.Writer, checks []setup.Check) {
	r := output.NewRendererWithTheme(w, false, tui.ResolveTheme(tui.DetectDark()))
	warned := false
	for _, c := range checks {
		if c.Status == setup.StatusWarn {
			fmt.Fprintln(w, r.Warning.Render(richtext.SanitizeSingleLine(c.Name+": "+c.Message)))
			warned = true
		}
	}
	if warned {
		fmt.Fprintln(w, r.Success.Render("✓ Everything else checks out"))
	} else {
		fmt.Fprintln(w, r.Success.Render("✓ Everything checks out"))
	}
}

// renderNoProjectsYet is the step after connecting an agent that is in no
// projects: adding it to some, in Basecamp.
func renderNoProjectsYet(w io.Writer, agent guidedAgent) {
	name := richtext.SanitizeSingleLine(agent.Me.Name)
	fmt.Fprintln(w)
	fmt.Fprintf(w, "%s isn't in any projects yet, so there's nothing for it to work on.\n", name)
	fmt.Fprintf(w, "Next: add it to the projects it should work in (in Basecamp, Adminland → Manage agents → %s → Edit), then run `basecamp connect setup` again.\n", name)
}

func renderGuidedSummary(w io.Writer, r *output.Renderer, agent guidedAgent, file setup.File, running bool, name string) {
	names := make(map[int64]string, len(agent.Projects))
	for _, p := range agent.Projects {
		names[p.ID] = richtext.SanitizeSingleLine(p.Name)
	}
	served := make([]string, 0, len(file.Projects))
	for id := range file.Projects {
		if n, ok := names[id]; ok {
			served = append(served, n)
		} else {
			served = append(served, "project "+strconv.FormatInt(id, 10))
		}
	}
	slices.Sort(served)

	agentName := richtext.SanitizeSingleLine(agent.Me.Name)
	fmt.Fprintln(w)
	fmt.Fprintln(w, r.Success.Render(fmt.Sprintf("%s is set up on this computer", agentName)))
	if agent.HasOwner {
		fmt.Fprintf(w, "  Works for: %s\n", richtext.SanitizeSingleLine(agent.Owner.Name))
	}
	fmt.Fprintf(w, "  Works in:  %s\n", strings.Join(served, ", "))
	if running {
		fmt.Fprintln(w, "  Running:   yes")
		fmt.Fprintf(w, "\nMention %s in one of those projects to try it.\n", agentName)
	} else {
		// No background service yet: it would work in the home directory and
		// is untested on real machines. The agent works, and may change files
		// without asking, in the folder it is started in.
		fmt.Fprintln(w, "  Running:   no")
		fmt.Fprintln(w)
		fmt.Fprintln(w, "To start it, run this in the folder it should work in, and leave it running.")
		fmt.Fprintln(w, "It can change files in that folder without asking.")
		fmt.Fprintln(w, "  basecamp connect -P "+richtext.ShellQuote(name))
		fmt.Fprintf(w, "\nOnce it's running, mention %s in one of those projects to try it.\n", agentName)
	}
}
