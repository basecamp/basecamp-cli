package commands

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/auth"
	"github.com/basecamp/basecamp-cli/internal/config"
	"github.com/basecamp/basecamp-cli/internal/connector/admission"
	"github.com/basecamp/basecamp-cli/internal/connector/setup"
	"github.com/basecamp/basecamp-cli/internal/output"
	"github.com/basecamp/basecamp-cli/internal/version"
)

// NewConnectCmd is the local agent connector's command group.
func NewConnectCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "connect",
		Short: "Run a local agent connector for a Basecamp agent",
		Long: `Run a local agent connector: it listens to the account event feed as a
Basecamp agent, admits what a trusted person asks of that agent, and hands
the work to a local coding agent that replies in Basecamp as the agent.

Start with setup, which connects the agent, records who may drive it, and
maps projects to the directories their work runs in.`,
	}
	cmd.AddCommand(newConnectSetupCmd())
	return cmd
}

// connectSetupFlags are setup's flags, as typed.
type connectSetupFlags struct {
	expectIdentity string
	noBrowser      bool
	deviceName     string

	operator        string
	operatorProfile string

	trust string
	allow []string

	routes    []string
	classes   []string
	watch     []string
	unwatch   []string
	unroute   []string
	driver    string
	parallel  int
	deadline  time.Duration
	worktrees bool
}

func newConnectSetupCmd() *cobra.Command {
	var f connectSetupFlags

	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Connect an agent, choose who may drive it, and route projects",
		Long: `Set up the connector for the agent held by a profile: connect the agent,
record the trusted operator, write connect.json, and check what can be
checked before the connector runs.

Credential. With no credential under the profile, setup runs the agent
connection (` + "`basecamp auth agent connect`" + `): approve it in your browser and
the agent's own OAuth client is stored under the profile. On the bot-user
path, pass --expect-identity with the bot's identity id instead, and setup
runs the device login and refuses any other identity. A profile that is
already connected is used as it is.

Operator. The person whose instructions the agent follows, keyed on Person
id. Name them by their own profile (--operator-profile, which proves who
they are), or by id (--operator), which the agent must be able to read —
Basecamp refuses that read to an Agent identity today, so on the agent
connection path use --operator-profile. With neither, setup keeps the operator
connect.json already has; on a first setup one of the two is required.

Trust. operator (default): the operator alone. allowlist: the operator and
the people passed with --allow. project: the operator and any non-client
member of the event's project. Assignments are the operator's alone in every
mode.

Routes. connect.json is the only authority for which directory a project's
work runs in: --route <project-id>=<dir>. A project with no route gets a
holding reply and no work. --watch-completions <project-id> makes the agent
hear every trusted completion in that project without being assigned.

connect.json is written owner-only and refused when anyone else could have
changed it or a directory above it. On Windows it must live under your user
profile directory.

Setup exits non-zero when a check fails, after writing connect.json.

Run setup again to change any of it; what you do not pass is kept.

Examples:
  basecamp connect setup -P agent --route 12345=~/Work/app
  basecamp connect setup -P agent --operator-profile me --trust allowlist --allow 111 --allow 222
  basecamp connect setup -P bot --expect-identity 4242 --route 12345=~/Work/app --watch-completions 12345
  basecamp connect setup -P agent --class 12345=internal --deadline 90m --worktrees`,
		Annotations: map[string]string{AnnotationProfileMayCreate: "true"},
		Args:        cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if app == nil {
				return fmt.Errorf("app not initialized")
			}
			return runConnectSetup(cmd, app, &f)
		},
	}

	fl := cmd.Flags()
	fl.StringVar(&f.expectIdentity, "expect-identity", "", "Bot-user path: the identity id the profile's login must authenticate as")
	fl.BoolVar(&f.noBrowser, "no-browser", false, "Print the approval link instead of opening a browser")
	fl.StringVar(&f.deviceName, "device-name", "", "Name this computer on the agent approval page (default: this host's name)")
	fl.StringVar(&f.operator, "operator", "", "Person id of the operator the agent follows")
	fl.StringVar(&f.operatorProfile, "operator-profile", "", "Profile whose identity is the operator")
	fl.StringVar(&f.trust, "trust", "", "Who may drive the agent: operator, allowlist or project")
	fl.StringArrayVar(&f.allow, "allow", nil, "Person id to trust besides the operator (repeatable; implies --trust allowlist)")
	fl.StringArrayVar(&f.routes, "route", nil, "Route a project to a directory: <project-id>=<dir> (repeatable)")
	fl.StringArrayVar(&f.unroute, "remove-route", nil, "Remove a project's route (repeatable)")
	fl.StringArrayVar(&f.classes, "class", nil, "Classify a routed project: <project-id>=<class> (repeatable)")
	fl.StringArrayVar(&f.watch, "watch-completions", nil, "Admit every trusted completion in a routed project (repeatable)")
	fl.StringArrayVar(&f.unwatch, "no-watch-completions", nil, "Stop watching a project's completions (repeatable)")
	fl.StringVar(&f.driver, "driver", "", "How workers are run: spawn or acp (default spawn)")
	fl.IntVar(&f.parallel, "concurrency", 0, fmt.Sprintf("Workers at once (default %d)", setup.DefaultConcurrency))
	fl.DurationVar(&f.deadline, "deadline", 0, fmt.Sprintf("Deadline per task (default %s)", setup.DefaultDeadline))
	fl.BoolVar(&f.worktrees, "worktrees", false, "Give each task its own git worktree")
	cmd.MarkFlagsMutuallyExclusive("operator", "operator-profile")

	return cmd
}

// connectSetupResult is what setup reports.
type connectSetupResult struct {
	Path          string        `json:"path"`
	Profile       string        `json:"profile"`
	AccountID     string        `json:"account_id"`
	AgentPersonID int64         `json:"agent_person_id"`
	AgentKind     string        `json:"agent_kind"`
	OperatorID    int64         `json:"operator_id"`
	TrustMode     string        `json:"trust_mode"`
	Routes        int           `json:"routes"`
	Ready         bool          `json:"ready"`
	Checks        *DoctorResult `json:"checks"`
	Written       bool          `json:"written"`
}

func runConnectSetup(cmd *cobra.Command, app *appctx.App, f *connectSetupFlags) error {
	ctx := cmd.Context()

	name := app.Config.ActiveProfile
	if name == "" {
		return output.ErrUsageHint("Setup needs the agent's profile", "Pass -P/--profile <name>; setup connects the agent under it if it holds no credential yet.")
	}
	if !isValidProfileName(name) {
		return output.ErrUsage(fmt.Sprintf("Invalid profile name %q: use only letters, numbers, hyphens, and underscores", name))
	}
	if os.Getenv("BASECAMP_TOKEN") != "" {
		return errEnvTokenShadows("connect setup cannot check the agent while BASECAMP_TOKEN is set")
	}

	changes, err := f.changes(cmd)
	if err != nil {
		return err
	}
	worktreesChanged(cmd, f, &changes)
	expect, err := parseExpectIdentity(f.expectIdentity)
	if err != nil {
		return err
	}
	operatorID, err := parsePositiveID("--operator", f.operator)
	if err != nil {
		return err
	}
	if f.operatorProfile == name {
		return output.ErrUsage("--operator-profile names the agent's own profile; the operator is a different person")
	}

	path, err := setup.Path(config.GlobalConfigDir(), name)
	if err != nil {
		return output.ErrUsage(err.Error())
	}
	// One setup per profile at a time: load, change and save are one step.
	unlock, err := setup.Lock(path)
	if err != nil {
		return output.ErrUsage(err.Error())
	}
	defer unlock()

	existing, err := setup.Load(path)
	exists := err == nil
	switch {
	case errors.Is(err, os.ErrNotExist):
		existing = setup.New(name)
	case err != nil:
		return output.ErrUsageHint("connect.json cannot be used: "+err.Error(),
			"Nothing was changed. Fix or remove "+path+", then run setup again.")
	}
	if existing.Profile != name {
		return output.ErrUsage(fmt.Sprintf("%s names profile %q, not %q", path, existing.Profile, name))
	}

	// Everything refusable without the network is refused before anyone is
	// sent to a browser.
	next, err := setup.Apply(existing, changes)
	if err != nil {
		return output.ErrUsage(err.Error())
	}
	if operatorID == 0 && f.operatorProfile == "" && existing.Trust.OperatorID == 0 {
		return output.ErrUsageHint("Setup needs to know who the operator is",
			"Pass --operator-profile <your profile> (or --operator <your person id>). The operator is the person the agent takes instructions from, and is never guessed.")
	}
	var operatorMgr *operatorProfile
	if f.operatorProfile != "" {
		if operatorMgr, err = operatorProfileManager(ctx, app, f.operatorProfile); err != nil {
			return err
		}
	}
	held, err := connectCredentialKind(ctx, app)
	if err != nil {
		return err
	}
	if err := refuseCredentialConflicts(app, name, path, held, expect, exists, existing); err != nil {
		return err
	}

	kind, err := ensureConnectCredential(cmd, app, name, held, expect)
	if err != nil {
		return err
	}
	if exists && existing.Agent.Kind != kind {
		return output.ErrUsageHint(
			fmt.Sprintf("connect.json was set up for a %s credential, and profile %q now holds a %s one", existing.Agent.Kind, name, kind),
			"Nothing was changed. Remove "+path+" to set this profile up afresh.")
	}
	if kind == setup.KindBotUser && expect == 0 {
		expect = existing.Agent.IdentityID
	}

	accountID := app.Config.AccountID
	if accountID == "" {
		if p := app.Config.Profiles[name]; p != nil {
			accountID = p.AccountID
		}
	}
	accountID, err = canonicalAccount(accountID)
	if err != nil {
		return output.ErrUsageHint(fmt.Sprintf("Profile %q has no usable account", name), "Bind it: basecamp profile set "+name+" account_id <id>, or pass --account.")
	}
	if exists && !accountIDsEqual(existing.AccountID, accountID) {
		return output.ErrUsageHint(
			fmt.Sprintf("connect.json was set up in account %s, and profile %q now addresses account %s", existing.AccountID, name, accountID),
			"Nothing was changed. Remove "+path+" to set this profile up afresh.")
	}

	var checks []setup.Check

	// Token.
	provider := &managerTokens{mgr: app.Auth}
	if _, err := app.Auth.AccessToken(ctx); err != nil {
		return output.ErrAuth(fmt.Sprintf("Profile %q holds a credential that does not produce a token: %v", name, err))
	}
	checks = append(checks, setup.Check{Name: "Token", Status: setup.StatusPass, Message: "The profile's credential yields a token"})

	// Identity.
	client := connectSDKClient(app, provider)
	reader := setup.SDKReader{Client: client.ForAccount(accountID)}
	me, err := reader.Me(ctx)
	if err != nil {
		return output.ErrAuth(fmt.Sprintf("Could not read who profile %q is in account %s: %v", name, accountID, err))
	}
	identityCheck, err := checkConnectIdentity(ctx, app, client, kind, me, expect)
	if err != nil {
		return err
	}
	checks = append(checks, identityCheck)
	if exists && existing.Agent.PersonID != me.ID {
		return output.ErrUsageHint(
			fmt.Sprintf("connect.json was set up for agent person %d, and profile %q now authenticates as person %d", existing.Agent.PersonID, name, me.ID),
			"Nothing was changed. If this is a different agent on purpose, remove "+path+" and run setup again.")
	}
	if scope := profileScope(app, name); scope == "read" {
		checks = append(checks, setup.Check{Name: "Scope", Status: setup.StatusFail,
			Message: "The credential is read-only: the agent could not reply or acknowledge",
			Hint:    "Reconnect with full access."})
	}

	// Operator, verified before anything is written: connect.json is the
	// trust anchor, and an operator that fails verification is not one to
	// record.
	var operatorCheck setup.Check
	switch {
	case operatorMgr != nil:
		op, err := resolveOperatorProfile(ctx, operatorMgr, f.operatorProfile, accountID)
		if err != nil {
			return err
		}
		operatorID = op.ID
		operatorCheck = setup.OperatorCheck(ctx, reader, op, me.ID, f.operatorProfile, false)
	default:
		if operatorID == 0 {
			operatorID = existing.Trust.OperatorID
		}
		recorded := exists && operatorID == existing.Trust.OperatorID
		operatorCheck = setup.OperatorCheck(ctx, reader, setup.Person{ID: operatorID}, me.ID, "", recorded)
	}
	if operatorID == me.ID {
		return output.ErrUsage(fmt.Sprintf("The operator (person %d) is the agent itself; the agent's own id never authorizes", operatorID))
	}
	if operatorCheck.Status == setup.StatusFail {
		return output.ErrUsageHint("The operator was refused, and nothing was written: "+operatorCheck.Message, operatorCheck.Hint)
	}
	checks = append(checks, operatorCheck)

	next.AccountID = accountID
	next.Agent = setup.Agent{PersonID: me.ID, Kind: kind}
	if kind == setup.KindBotUser {
		next.Agent.IdentityID = expect
	}
	next.Trust.OperatorID = operatorID
	if err := setup.Save(path, next); err != nil {
		if errors.Is(err, setup.ErrNotPrivate) {
			return output.ErrUsageHint("connect.json was not written: "+err.Error(), "Setup writes connect.json only where nobody else can change it.")
		}
		return output.ErrUsage("connect.json was not written: " + err.Error())
	}

	checks = append(checks, setup.TicketCheck(ctx, reader, kind))
	checks = append(checks, setup.RouteChecks(ctx, reader, next)...)

	result := &connectSetupResult{
		Path:          path,
		Profile:       name,
		AccountID:     accountID,
		AgentPersonID: me.ID,
		AgentKind:     kind,
		OperatorID:    operatorID,
		TrustMode:     string(next.Trust.Mode),
		Routes:        len(next.Projects),
		Checks:        summarizeChecks(asDoctorChecks(checks)),
	}
	result.Ready = result.Checks.Failed == 0

	if app.Output.EffectiveFormat() == output.FormatStyled {
		w := cmd.OutOrStdout()
		renderChecksStyled(w, "Connector setup for profile "+strconv.Quote(name), result.Checks)
		fmt.Fprintf(w, "  connect.json written: %s\n\n", path)
	}
	if !result.Ready {
		return errConnectorNotReady(path, checks)
	}
	if app.Output.EffectiveFormat() == output.FormatStyled {
		return nil
	}
	return app.OK(result, output.WithSummary("connect.json written; "+result.Checks.Summary()))
}

// codeNotReady is the error code for a setup that wrote connect.json but
// whose checks failed: the connector would not run.
const codeNotReady = "not_ready"

// errConnectorNotReady reports failed checks as the command's error, so a
// script or an agent reading the exit status sees what a person sees.
func errConnectorNotReady(path string, checks []setup.Check) error {
	var failed []string
	hint := ""
	for _, c := range checks {
		if c.Status != setup.StatusFail {
			continue
		}
		failed = append(failed, c.Name+": "+c.Message)
		if hint == "" {
			hint = c.Hint
		}
	}
	if hint == "" {
		hint = "Fix what failed and run setup again."
	}
	return &output.Error{
		Code:    codeNotReady,
		Message: fmt.Sprintf("connect.json was written to %s, but the connector is not ready. %s", path, strings.Join(failed, "; ")),
		Hint:    hint,
	}
}

// canonicalAccount is the account id as a number spells it, the spelling
// the connector's instance lock uses.
func canonicalAccount(raw string) (string, error) {
	if err := requireNumericAccount(raw); err != nil {
		return "", err
	}
	n, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || n == 0 {
		return "", output.ErrUsage(fmt.Sprintf("Invalid account ID %q", raw))
	}
	return strconv.FormatUint(n, 10), nil
}

// changes turns setup's flags into the changes connect.json takes.
func (f *connectSetupFlags) changes(cmd *cobra.Command) (setup.Changes, error) {
	var ch setup.Changes
	if f.trust != "" {
		ch.Trust = admission.TrustMode(f.trust)
		switch ch.Trust {
		case admission.TrustOperator, admission.TrustAllowlist, admission.TrustProject:
		default:
			return ch, output.ErrUsage(fmt.Sprintf("Invalid --trust %q: use operator, allowlist or project", f.trust))
		}
	}
	for _, raw := range f.allow {
		for part := range strings.SplitSeq(raw, ",") {
			id, err := parsePositiveID("--allow", strings.TrimSpace(part))
			if err != nil || id == 0 {
				return ch, output.ErrUsage(fmt.Sprintf("Invalid --allow %q: expected a Person id", raw))
			}
			ch.Allow = append(ch.Allow, id)
		}
	}

	var err error
	if ch.Routes, err = parseProjectPairs("--route", f.routes); err != nil {
		return ch, err
	}
	if ch.Classes, err = parseProjectPairs("--class", f.classes); err != nil {
		return ch, err
	}
	for _, list := range []struct {
		flag string
		raw  []string
		on   bool
	}{{"--watch-completions", f.watch, true}, {"--no-watch-completions", f.unwatch, false}} {
		for _, raw := range list.raw {
			id, err := parsePositiveID(list.flag, raw)
			if err != nil || id == 0 {
				return ch, output.ErrUsage(fmt.Sprintf("Invalid %s %q: expected a project id", list.flag, raw))
			}
			if ch.WatchCompletions == nil {
				ch.WatchCompletions = map[int64]bool{}
			}
			if prev, seen := ch.WatchCompletions[id]; seen && prev != list.on {
				return ch, output.ErrUsage(fmt.Sprintf("Project %d is given both --watch-completions and --no-watch-completions", id))
			}
			ch.WatchCompletions[id] = list.on
		}
	}
	for _, raw := range f.unroute {
		id, err := parsePositiveID("--remove-route", raw)
		if err != nil || id == 0 {
			return ch, output.ErrUsage(fmt.Sprintf("Invalid --remove-route %q: expected a project id", raw))
		}
		ch.Remove = append(ch.Remove, id)
	}

	switch f.driver {
	case "", setup.DriverSpawn, setup.DriverACP:
		ch.Driver = f.driver
	default:
		return ch, output.ErrUsage(fmt.Sprintf("Invalid --driver %q: use spawn or acp", f.driver))
	}
	// A typed zero is out of range, not a request for the default: the flags
	// are read as typed, not as their zero values.
	if cmd.Flags().Changed("concurrency") {
		if f.parallel < 1 || f.parallel > setup.MaxConcurrency {
			return ch, output.ErrUsage(fmt.Sprintf("Invalid --concurrency %d: use 1 to %d", f.parallel, setup.MaxConcurrency))
		}
		ch.Concurrency = f.parallel
	}
	if cmd.Flags().Changed("deadline") {
		if f.deadline < setup.MinDeadline || f.deadline > setup.MaxDeadline {
			return ch, output.ErrUsage(fmt.Sprintf("Invalid --deadline %s: use %s to %s", f.deadline, setup.MinDeadline, setup.MaxDeadline))
		}
		ch.Deadline = f.deadline
	}
	return ch, nil
}

// worktreesChanged wires --worktrees, which is only a change when typed.
func worktreesChanged(cmd *cobra.Command, f *connectSetupFlags, ch *setup.Changes) {
	if cmd.Flags().Changed("worktrees") {
		v := f.worktrees
		ch.Worktrees = &v
	}
}

// parseProjectPairs parses repeatable <project-id>=<value> flags.
func parseProjectPairs(flag string, raw []string) (map[int64]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	out := make(map[int64]string, len(raw))
	for _, pair := range raw {
		idText, value, ok := strings.Cut(pair, "=")
		id, err := parsePositiveID(flag, strings.TrimSpace(idText))
		if !ok || err != nil || id == 0 || value == "" {
			return nil, output.ErrUsage(fmt.Sprintf("Invalid %s %q: expected <project-id>=<value>", flag, pair))
		}
		if _, dup := out[id]; dup {
			return nil, output.ErrUsage(fmt.Sprintf("%s names project %d twice", flag, id))
		}
		out[id] = value
	}
	return out, nil
}

// parsePositiveID parses a numeric id flag: 0 when absent.
func parsePositiveID(flag, raw string) (int64, error) {
	if raw == "" {
		return 0, nil
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, output.ErrUsage(fmt.Sprintf("Invalid %s %q: expected a numeric id", flag, raw))
	}
	return id, nil
}

// refuseCredentialConflicts refuses, before any credential step runs, the
// combinations that step would only be refused after: a bot-user login for
// a profile that does not exist, --expect-identity against an Agent's
// connect.json or credential, and a person's login with nothing pinning it.
func refuseCredentialConflicts(app *appctx.App, name, path, held string, expect int64, exists bool, existing setup.File) error {
	switch {
	case expect != 0 && held == setup.KindAgent:
		return output.ErrUsage("--expect-identity is for the bot-user path, and profile " + strconv.Quote(name) + " holds an Agent's credential, which has no identity")
	case expect != 0 && exists && existing.Agent.Kind == setup.KindAgent:
		return output.ErrUsageHint("--expect-identity is for the bot-user path, and connect.json was set up for an Agent",
			"Nothing was changed. Remove "+path+" to set this profile up afresh.")
	case held == "" && expect != 0 && app.Config.Profiles[name] == nil:
		return output.ErrUsageHint(fmt.Sprintf("Profile %q does not exist, and the bot-user login needs it", name),
			fmt.Sprintf("Create it with `basecamp profile create %s` (signing in as the bot), then run setup again.", name))
	case held == "" && exists && existing.Agent.Kind == setup.KindBotUser && expect == 0:
		return output.ErrUsageHint(fmt.Sprintf("Profile %q holds no credential, and connect.json was set up for a bot user", name),
			fmt.Sprintf("Log the bot in again: basecamp connect setup -P %s --expect-identity %d", name, existing.Agent.IdentityID))
	case held == setup.KindBotUser && expect == 0 && existing.Agent.IdentityID == 0:
		return output.ErrUsageHint(fmt.Sprintf("Profile %q holds a person's login, not an Agent's credential", name),
			"On the bot-user path pass --expect-identity <the bot's identity id>, so setup can prove this login is the bot and not you.")
	}
	return nil
}

// ensureConnectCredential makes sure the profile holds the agent's
// credential, running the command that stores one when it does not, and
// reports which kind it holds. Setup never handles a secret itself: the
// ceremony is `basecamp auth agent connect`, the bot-user login is
// `basecamp auth login --expect-identity`, run as they run on their own.
func ensureConnectCredential(cmd *cobra.Command, app *appctx.App, name, held string, expect int64) (string, error) {
	if held != "" {
		return held, nil
	}
	flags := map[string]string{}
	if noBrowser, _ := cmd.Flags().GetBool("no-browser"); noBrowser {
		flags["no-browser"] = "true"
	}
	if expect != 0 {
		flags["expect-identity"] = strconv.FormatInt(expect, 10)
		if err := runChildCommand(cmd, buildLoginCmd("login"), flags); err != nil {
			return "", err
		}
	} else {
		if deviceName, _ := cmd.Flags().GetString("device-name"); deviceName != "" {
			flags["device-name"] = deviceName
		}
		if err := runChildCommand(cmd, newAuthAgentConnectCmd(), flags); err != nil {
			return "", err
		}
	}

	kind, err := connectCredentialKind(cmd.Context(), app)
	if err != nil {
		return "", err
	}
	if kind == "" {
		return "", output.ErrAuth(fmt.Sprintf("Profile %q still holds no credential", name))
	}
	return kind, nil
}

// connectCredentialKind is the kind of credential the active profile holds,
// "" for none. A store that cannot be read is an error, not "none": setup
// would otherwise run a new connection over a credential it merely failed
// to load.
func connectCredentialKind(ctx context.Context, app *appctx.App) (string, error) {
	return credentialKindOf(ctx, app.Auth)
}

func credentialKindOf(ctx context.Context, mgr *auth.Manager) (string, error) {
	creds, err := mgr.GetStore().LoadContext(ctx, mgr.CredentialKey())
	switch {
	case errors.Is(err, auth.ErrNoCredential):
		return "", nil
	case err != nil:
		return "", output.ErrAuth(fmt.Sprintf("The stored credential could not be read: %v", err))
	case creds.OAuthType == "agent":
		return setup.KindAgent, nil
	default:
		return setup.KindBotUser, nil
	}
}

// runChildCommand runs another command's RunE in this command's context and
// output, with the given flags set, so setup reuses a command whole instead
// of reimplementing it.
func runChildCommand(parent, child *cobra.Command, flags map[string]string) error {
	for flag, value := range flags {
		if err := child.Flags().Set(flag, value); err != nil {
			return err
		}
	}
	child.SetContext(parent.Context())
	child.SetOut(parent.OutOrStdout())
	child.SetErr(parent.ErrOrStderr())
	return child.RunE(child, nil)
}

// checkConnectIdentity proves the profile is the agent it is meant to be.
// An Agent credential must read back as an Agent person. A bot user's login
// must be the identity --expect-identity pinned, and must not be an Agent.
func checkConnectIdentity(ctx context.Context, app *appctx.App, client *basecamp.Client, kind string, me setup.Person, expect int64) (setup.Check, error) {
	c := setup.Check{Name: "Identity", Status: setup.StatusPass}
	switch kind {
	case setup.KindAgent:
		if me.PersonableType != setup.PersonableAgent {
			return c, output.ErrAuth(fmt.Sprintf("The agent credential authenticates as person %d, whose type is %q, not an Agent", me.ID, me.PersonableType))
		}
		c.Message = fmt.Sprintf("Agent person %d", me.ID)
	default:
		if me.PersonableType == setup.PersonableAgent {
			return c, output.ErrAuth(fmt.Sprintf("Person %d is an Agent, but the profile holds a person's login", me.ID))
		}
		endpoint, err := app.Auth.AuthorizationEndpoint(ctx)
		if err != nil {
			return c, err
		}
		info, err := client.Authorization().GetInfo(ctx, &basecamp.GetInfoOptions{Endpoint: endpoint, FilterProduct: "bc3"})
		if err != nil {
			return c, output.ErrAuth(fmt.Sprintf("Could not read the login's identity: %v", err))
		}
		if info.Identity.ID != expect {
			return c, output.ErrAuth(fmt.Sprintf("The profile's login is identity %d, not the %d --expect-identity names; nothing was written", info.Identity.ID, expect))
		}
		c.Message = fmt.Sprintf("Bot user person %d (identity %d)", me.ID, expect)
	}
	return c, nil
}

// operatorProfileManager checks, without the network, that the operator's
// profile can name the operator: it exists, is on the agent's Basecamp, and
// holds a person's credential.
func operatorProfileManager(ctx context.Context, app *appctx.App, profile string) (*operatorProfile, error) {
	cfg, err := config.Load(config.FlagOverrides{})
	if err != nil {
		return nil, err
	}
	if _, ok := cfg.Profiles[profile]; !ok {
		return nil, output.ErrUsage(fmt.Sprintf("Operator profile %q does not exist", profile))
	}
	if err := cfg.ApplyProfile(profile); err != nil {
		return nil, err
	}
	if config.NormalizeBaseURL(cfg.BaseURL) != config.NormalizeBaseURL(app.Config.BaseURL) {
		return nil, output.ErrUsage(fmt.Sprintf("Operator profile %q is on %s, and the agent is on %s", profile, cfg.BaseURL, app.Config.BaseURL))
	}
	mgr := auth.NewManager(cfg, nil)
	if store := app.Auth.GetStore(); store != nil {
		mgr.SetStore(store)
	}
	switch kind, err := credentialKindOf(ctx, mgr); {
	case err != nil:
		return nil, err
	case kind == "":
		return nil, output.ErrUsageHint(fmt.Sprintf("Operator profile %q holds no credential", profile), "Log in: basecamp auth login -P "+profile)
	case kind == setup.KindAgent:
		return nil, output.ErrUsage(fmt.Sprintf("Operator profile %q holds an Agent's credential; an operator is a person", profile))
	}
	return &operatorProfile{mgr: mgr, baseURL: cfg.BaseURL}, nil
}

// operatorProfile is the operator's own credential, checked and ready to read.
type operatorProfile struct {
	mgr     *auth.Manager
	baseURL string
}

// resolveOperatorProfile reads the operator's person in the agent's account
// through the operator's own credential, which proves who they are in a way
// a typed id cannot.
func resolveOperatorProfile(ctx context.Context, op *operatorProfile, profile, accountID string) (setup.Person, error) {
	client := connectSDKClientFor(op.baseURL, &managerTokens{mgr: op.mgr})
	me, err := setup.SDKReader{Client: client.ForAccount(accountID)}.Me(ctx)
	if err != nil {
		return setup.Person{}, output.ErrAuth(fmt.Sprintf("Could not read who operator profile %q is in account %s: %v", profile, accountID, err))
	}
	if me.ID <= 0 {
		return setup.Person{}, output.ErrAuth(fmt.Sprintf("Operator profile %q reported no person id", profile))
	}
	return me, nil
}

func profileScope(app *appctx.App, name string) string {
	if p := app.Config.Profiles[name]; p != nil && p.Scope != "" {
		return p.Scope
	}
	return app.Config.Scope
}

func asDoctorChecks(in []setup.Check) []Check {
	out := make([]Check, len(in))
	for i, c := range in {
		out[i] = Check(c)
	}
	return out
}

// managerTokens is a TokenProvider over an auth manager.
type managerTokens struct{ mgr *auth.Manager }

func (t *managerTokens) AccessToken(ctx context.Context) (string, error) {
	return t.mgr.AccessToken(ctx)
}

// connectSDKClient is the client setup reads through: no request hooks, no
// debug logger and no cache. The hooks and logger print request URLs, and
// the feed's URLs carry positions and tickets; -v on setup must not be a way
// to print either.
func connectSDKClient(app *appctx.App, tokens basecamp.TokenProvider) *basecamp.Client {
	return connectSDKClientFor(app.Config.BaseURL, tokens)
}

func connectSDKClientFor(baseURL string, tokens basecamp.TokenProvider) *basecamp.Client {
	return basecamp.NewClient(&basecamp.Config{BaseURL: baseURL}, tokens,
		basecamp.WithTransport(http.DefaultTransport),
		basecamp.WithUserAgent(version.UserAgent()+" "+basecamp.DefaultUserAgent),
	)
}
