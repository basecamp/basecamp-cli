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
	"github.com/basecamp/basecamp-cli/internal/richtext"
	"github.com/basecamp/basecamp-cli/internal/version"
)

// NewConnectCmd is the local agent connector's command group.
func NewConnectCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "connect",
		Short: "Set up a local agent connector for a Basecamp agent",
		Long: `Run a local agent connector: it listens to the account event feed as a
Basecamp agent, admits what a trusted person asks of that agent, and hands
the work to a local coding agent that replies in Basecamp as the agent.

Connect the agent to a profile first (basecamp auth agent connect -P <profile>),
then run setup on that profile: it records who may drive the agent, maps
projects to the directories their work runs in, and checks the connector is
ready.`,
	}
	cmd.AddCommand(newConnectSetupCmd())
	return cmd
}

// connectSetupFlags are setup's flags, as typed.
type connectSetupFlags struct {
	expectIdentity string

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
		Short: "Choose who may drive a connected agent, route projects, and check readiness",
		Long: `Set up the connector for the agent a profile holds: record the trusted
operator, write connect.json, and check what can be checked before the
connector runs.

Credential. Setup does not obtain one; connect the profile first, then run
setup against it:
  basecamp auth agent connect -P agent                  # an Agent person
  basecamp auth login -P bot --expect-identity <id>     # a bot user (v1)
On the bot-user path pass --expect-identity to setup as well, so it can prove
the login is the bot and not you; later runs remember it.

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
changed it or a directory above it. Where this CLI cannot verify that
(Windows), or where the filesystem holding the configuration directory
cannot lock, setup refuses rather than write a trust file it cannot vouch
for.

Every check runs before connect.json is written, and it is written only
when all of them pass. Exit status: usage for refused input, auth when the
profile's credential is missing, unreadable, cannot be proven, or is not the
agent it should be, not_ready when a readiness check failed. Setup never
stores, replaces or removes a credential; a token due for renewal is renewed
as by any command.

Run setup again to change any of it; what you do not pass is kept.

Examples:
  basecamp auth agent connect -P agent
  basecamp connect setup -P agent --operator-profile me --route 12345=~/Work/app
  basecamp connect setup -P agent --operator-profile me --trust allowlist --allow 111 --allow 222
  basecamp connect setup -P bot --operator-profile me --expect-identity 4242 --route 12345=~/Work/app
  basecamp connect setup -P agent --class 12345=internal --deadline 90m --worktrees`,
		Args: cobra.NoArgs,
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
	fl.StringVar(&f.operator, "operator", "", "Person id of the operator the agent follows")
	fl.StringVar(&f.operatorProfile, "operator-profile", "", "Profile whose identity is the operator")
	fl.StringVar(&f.trust, "trust", "", "Who may drive the agent: operator, allowlist or project")
	fl.StringArrayVar(&f.allow, "allow", nil, "Person id to trust besides the operator (repeatable; implies --trust allowlist)")
	fl.StringArrayVar(&f.routes, "route", nil, "Route a project to a directory: <project-id>=<dir> (repeatable)")
	fl.StringArrayVar(&f.unroute, "remove-route", nil, "Remove a project's route (repeatable)")
	fl.StringArrayVar(&f.classes, "class", nil, "Classify a routed project: <project-id>=<class>, or <project-id>= to clear it (repeatable)")
	fl.StringArrayVar(&f.watch, "watch-completions", nil, "Admit every trusted completion in a routed project (repeatable)")
	fl.StringArrayVar(&f.unwatch, "no-watch-completions", nil, "Stop watching a project's completions (repeatable)")
	fl.StringVar(&f.driver, "driver", "", "How workers are run: spawn or acp (default spawn)")
	fl.IntVar(&f.parallel, "concurrency", 0, fmt.Sprintf("Workers at once (default %d)", setup.DefaultConcurrency))
	fl.DurationVar(&f.deadline, "deadline", 0, fmt.Sprintf("Deadline per task (default %s)", setup.DefaultDeadline))
	fl.BoolVar(&f.worktrees, "worktrees", false, "Give each task its own git worktree")
	cmd.MarkFlagsMutuallyExclusive("operator", "operator-profile")

	return cmd
}

func runConnectSetup(cmd *cobra.Command, app *appctx.App, f *connectSetupFlags) error {
	ctx := cmd.Context()

	name := app.Config.ActiveProfile
	if name == "" {
		return output.ErrUsageHint("Setup needs the agent's profile", "Pass -P/--profile <name>, a profile connected with `basecamp auth agent connect`.")
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
		if isLockBusy(err) {
			return errBusy(name, err)
		}
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
			"Nothing was changed. Fix or remove "+richtext.SanitizeSingleLine(path)+", then run setup again.")
	}
	if existing.Profile != name {
		return output.ErrUsage(fmt.Sprintf("%s names profile %q, not %q", path, existing.Profile, name))
	}

	// Everything refusable without the network is refused first.
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
	accountID, err := connectAccount(app, name)
	if err != nil {
		return err
	}
	if exists && !accountIDsEqual(existing.AccountID, accountID) {
		return output.ErrUsageHint(
			fmt.Sprintf("connect.json was set up in account %s, and profile %q is bound to account %s", existing.AccountID, name, accountID),
			"Nothing was changed. Remove "+richtext.SanitizeSingleLine(path)+" to set this profile up afresh.")
	}
	kind, err := connectCredentialKind(ctx, app)
	if err != nil {
		return err
	}
	// The credential's own disagreement with connect.json comes first: it is
	// the wrong credential whatever the flags say.
	if exists && kind != "" && existing.Agent.Kind != kind {
		return output.ErrAuth(fmt.Sprintf("connect.json was set up for a %s credential, and profile %q now holds a %s one; remove %s to set this profile up afresh",
			existing.Agent.Kind, name, kind, richtext.SanitizeSingleLine(path)))
	}
	if err := refuseCredentialConflicts(name, path, kind, expect, exists, existing); err != nil {
		return err
	}
	if kind == setup.KindBotUser && expect == 0 {
		expect = existing.Agent.IdentityID
	}

	var checks []setup.Check

	// Token: one snapshot of the credential and the token it yields, which
	// every read below spends. A credential another process stores under
	// the profile while setup runs is not what setup checked, so it is
	// compared again before anything is written.
	token, err := app.Auth.AccessToken(ctx)
	if err != nil {
		return output.ErrAuth(fmt.Sprintf("Profile %q holds a credential that does not produce a token: %s", name, setup.ErrorText(err)))
	}
	creds, err := app.Auth.GetStore().LoadContext(ctx, app.Auth.CredentialKey())
	if err != nil {
		return output.ErrAuth("The stored credential could not be read: " + setup.ErrorText(err))
	}
	if creds.AccessToken != token {
		return errCredentialChanged(name)
	}
	provider := &basecamp.StaticTokenProvider{Token: token}
	checks = append(checks, setup.Check{Name: "Token", Status: setup.StatusPass, Message: "The profile's credential yields a token"})

	// Identity.
	client := connectSDKClient(app, provider)
	reader := setup.SDKReader{Client: client.ForAccount(accountID)}
	me, err := reader.Me(ctx)
	if err != nil {
		return output.ErrAuth(fmt.Sprintf("Could not read who profile %q is in account %s: %s", name, accountID, setup.ErrorText(err)))
	}
	identityCheck, err := checkConnectIdentity(ctx, app, client, kind, creds.OAuthType, me, expect)
	if err != nil {
		return err
	}
	checks = append(checks, identityCheck)
	if exists && existing.Agent.PersonID != me.ID {
		return output.ErrAuth(fmt.Sprintf("connect.json was set up for agent person %d, and profile %q now authenticates as person %d; if this is a different agent on purpose, remove %s and run setup again",
			existing.Agent.PersonID, name, me.ID, richtext.SanitizeSingleLine(path)))
	}
	checks = append(checks, setup.ScopeCheck(creds.OAuthType, creds.Scope))

	// Trust, verified before anything is written: connect.json is the trust
	// anchor, and nobody in it is recorded unverified. People are read
	// through the operator's own credential when there is one, since
	// Basecamp refuses person reads to an Agent identity today.
	trust := setup.Trust{
		Allowlist:       next.Trust.AllowlistIDs,
		OperatorProfile: f.operatorProfile,
	}
	if exists {
		trust.Recorded = existing.Trust
	}
	people := setup.Reader(reader)
	if operatorMgr != nil {
		op, opReader, err := resolveOperatorProfile(ctx, operatorMgr, f.operatorProfile, accountID)
		if err != nil {
			return err
		}
		trust.Operator = op
		people = opReader
	} else {
		if operatorID == 0 {
			operatorID = existing.Trust.OperatorID
		}
		trust.Operator = setup.Person{ID: operatorID}
	}
	trustChecks := setup.VerifyTrust(ctx, people, trust, me.ID)
	for _, c := range trustChecks {
		if c.Status == setup.StatusFail {
			return output.ErrUsageHint("Trust was refused, and nothing was written: "+c.Name+": "+c.Message, c.Hint)
		}
	}
	checks = append(checks, trustChecks...)

	next.AccountID = accountID
	next.Agent = setup.Agent{PersonID: me.ID, Kind: kind}
	if kind == setup.KindBotUser {
		next.Agent.IdentityID = expect
	}
	next.Trust.OperatorID = trust.Operator.ID
	if err := next.Validate(); err != nil {
		return output.ErrUsage("connect.json was not written: " + err.Error())
	}

	// Every check runs before connect.json is written, and it is written
	// only when all of them pass: a setup that is not ready changes nothing.
	report := &setup.Report{
		Path:          path,
		Profile:       name,
		AccountID:     accountID,
		AgentPersonID: me.ID,
		AgentKind:     kind,
		OperatorID:    next.Trust.OperatorID,
		TrustMode:     string(next.Trust.Mode),
		Routes:        len(next.Projects),
	}
	report.Add(checks...)
	report.Add(setup.TicketCheck(ctx, reader, kind))
	report.Add(setup.RouteChecks(ctx, reader, next)...)

	w := cmd.OutOrStdout()
	styled := app.Output.EffectiveFormat() == output.FormatStyled
	title := "Connector setup for profile " + strconv.Quote(name)
	if len(report.Failed()) > 0 {
		if styled {
			renderChecksStyled(w, title, summarizeChecks(asDoctorChecks(report.Checks())))
		}
		return errConnectorNotReady(report)
	}

	// The write and the last look at the credential are one step under the
	// credential key's own lock, which every login, refresh and import
	// takes: no other command can replace the profile's credential between
	// the check and the write. What is written names the identity that
	// credential authenticates as, and connect.json's VerifyAgent is what
	// the connector re-checks at start-up, so a credential replaced later
	// stops it rather than making it act as the wrong agent.
	saveErr := app.Auth.GetStore().WithCredential(ctx, app.Auth.CredentialKey(), func(held auth.HeldCredential) error {
		stored, _, ok := auth.Held(held)
		if !ok || !sameCredential(creds, stored) {
			return errCredentialChanged(name)
		}
		if err := next.VerifyAgent(kind, me.ID, expect); err != nil {
			return output.ErrAuth(err.Error())
		}
		return setup.Save(held, path, next)
	})
	if saveErr != nil {
		return classifyWriteError(name, saveErr)
	}
	report.Written = true

	summary := summarizeChecks(asDoctorChecks(report.Checks()))
	if !report.Ready() {
		return errConnectorNotReady(report)
	}
	if styled {
		renderChecksStyled(w, title, summary)
		fmt.Fprintf(w, "  connect.json written: %s\n\n", richtext.SanitizeSingleLine(path))
		return nil
	}
	return app.OK(report, output.WithSummary("connect.json written; "+summary.Summary()))
}

// classifyWriteError puts the last step's failures in the command's exit
// contract: a credential that is gone or unreadable is auth, another
// process holding the profile's credential or setup lock is busy, a
// connect.json nobody else may change is usage.
func classifyWriteError(name string, err error) error {
	var apiErr *output.Error
	profile := shellQuote(name)
	switch {
	case errors.Is(err, setup.ErrSetupRunning):
		return errBusy(name, err)
	case errors.As(err, &apiErr):
		// Already in the contract: the credential store reports contention
		// as a retryable rate_limit, and setup's own refusals are typed.
		return err
	case errors.Is(err, auth.ErrNoCredential):
		return &output.Error{Code: output.CodeAuth,
			Message: fmt.Sprintf("Profile %q's credential was removed while setup was checking it, so nothing was written", name),
			Hint:    "Connect the agent again: basecamp auth agent connect -P " + profile}
	case errors.Is(err, auth.ErrInvalidCredentials):
		return &output.Error{Code: output.CodeAuth,
			Message: fmt.Sprintf("Profile %q's stored credential could not be read, so nothing was written", name),
			Hint:    "Connect the agent again: basecamp auth agent connect -P " + profile}
	case errors.Is(err, setup.ErrNotPrivate):
		return output.ErrUsageHint("connect.json was not written: "+err.Error(), "Setup writes connect.json only where nobody else can change it.")
	}
	return output.ErrUsage("connect.json was not written: " + err.Error())
}

// isLockBusy reports a failure that is another process holding a lock.
// The credential store reports its own contention as a retryable
// rate_limit error, which classifyWriteError passes through untouched.
func isLockBusy(err error) bool {
	return errors.Is(err, setup.ErrSetupRunning)
}

func errBusy(name string, err error) error {
	return &output.Error{Code: output.CodeBusy,
		Message: fmt.Sprintf("Another command is working on profile %q right now, so nothing was changed: %s", name, setup.ErrorText(err)),
		Hint:    "Nothing is wrong with the profile. Run setup again when it has finished."}
}

// sameCredential reports whether two loads of a profile's credential are the
// same credential with the same token: what setup checked is what is stored.
func sameCredential(a, b *auth.Credentials) bool {
	return a != nil && b != nil &&
		a.OAuthType == b.OAuthType && a.ClientID == b.ClientID && a.Scope == b.Scope &&
		a.AccessToken == b.AccessToken && a.RefreshToken == b.RefreshToken
}

func errCredentialChanged(name string) error {
	return &output.Error{Code: output.CodeAuth,
		Message: fmt.Sprintf("Profile %q's credential changed while setup was checking it, so nothing was written", name),
		Hint:    "Another command stored or renewed a credential under the profile. Run setup again to check the one it holds now."}
}

// codeNotReady is the error code for a setup whose checks failed: the
// connector would not run, and connect.json was not written.
const codeNotReady = "not_ready"

// errConnectorNotReady reports a report that is not ready as the command's
// error, so a script or an agent reading the exit status sees what a person
// sees: every check that failed, by name.
func errConnectorNotReady(report *setup.Report) error {
	failures := report.Failed()
	failed := make([]string, 0, len(failures))
	hint := ""
	for _, c := range failures {
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
		Message: fmt.Sprintf("The connector is not ready, so %s was not written. %s", richtext.SanitizeSingleLine(report.Path), strings.Join(failed, "; ")),
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
	if ch.Classes, err = parseProjectPairsAllowEmpty("--class", f.classes); err != nil {
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
	return parseProjectPairsWith(flag, raw, false)
}

// parseProjectPairsAllowEmpty is parseProjectPairs where <project-id>= (an
// empty value) is meaningful: it clears the setting.
func parseProjectPairsAllowEmpty(flag string, raw []string) (map[int64]string, error) {
	return parseProjectPairsWith(flag, raw, true)
}

func parseProjectPairsWith(flag string, raw []string, allowEmpty bool) (map[int64]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	out := make(map[int64]string, len(raw))
	for _, pair := range raw {
		idText, value, ok := strings.Cut(pair, "=")
		id, err := parsePositiveID(flag, strings.TrimSpace(idText))
		if !ok || err != nil || id == 0 || (value == "" && !allowEmpty) {
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

// refuseCredentialConflicts refuses a profile setup cannot use as it is:
// one with no credential (setup does not obtain one), --expect-identity
// against an Agent's credential or connect.json, and a person's login with
// nothing pinning it to the bot.
func refuseCredentialConflicts(name, path, held string, expect int64, exists bool, existing setup.File) error {
	profile := shellQuote(name)
	switch {
	// What connect.json already says comes first: a remediation that the
	// existing file would refuse anyway is no remediation.
	case expect != 0 && exists && existing.Agent.Kind == setup.KindAgent:
		return output.ErrUsageHint("--expect-identity is for the bot-user path, and connect.json was set up for an Agent",
			fmt.Sprintf("Connect the agent again (basecamp auth agent connect -P %s) and drop --expect-identity, or remove %s to set this profile up as a bot user.",
				profile, richtext.SanitizeSingleLine(path)))
	case held == "" && (expect != 0 || (exists && existing.Agent.Kind == setup.KindBotUser)):
		identity := expect
		if identity == 0 {
			identity = existing.Agent.IdentityID
		}
		return &output.Error{Code: output.CodeAuth,
			Message: fmt.Sprintf("Profile %q holds no credential", name),
			Hint:    fmt.Sprintf("Log the bot in first: basecamp auth login -P %s --expect-identity %d, then run setup again.", profile, identity)}
	case held == "":
		return &output.Error{Code: output.CodeAuth,
			Message: fmt.Sprintf("Profile %q holds no credential", name),
			Hint:    fmt.Sprintf("Connect the agent first: basecamp auth agent connect -P %s, then run setup again.", profile)}
	case expect != 0 && held == setup.KindAgent:
		return output.ErrUsageHint("--expect-identity is for the bot-user path, and profile "+strconv.Quote(name)+" holds an Agent's credential, which has no identity",
			fmt.Sprintf("Drop --expect-identity, or log a bot in under another profile: basecamp auth login -P <bot-profile> --expect-identity %d.", expect))
	case held == setup.KindBotUser && expect == 0 && existing.Agent.IdentityID == 0:
		return output.ErrUsageHint(fmt.Sprintf("Profile %q holds a person's login, not an Agent's credential", name),
			"On the bot-user path pass --expect-identity <the bot's identity id>, so setup can prove this login is the bot and not you.")
	}
	return nil
}

// connectAccount is the account setup works in: the one the agent's
// profile is bound to, which is where its credential was connected. It is
// never a config-wide default, and an --account or BASECAMP_ACCOUNT_ID that
// names another account is refused rather than preferred.
func connectAccount(app *appctx.App, name string) (string, error) {
	p := app.Config.Profiles[name]
	if p == nil {
		return "", output.ErrUsageHint(fmt.Sprintf("Profile %q does not exist", name),
			"Connect the agent first: basecamp auth agent connect -P "+shellQuote(name))
	}
	bound, err := canonicalAccount(p.AccountID)
	if err != nil {
		return "", output.ErrUsageHint(fmt.Sprintf("Profile %q is not bound to an account", name),
			"Bind it: basecamp profile set "+shellQuote(name)+" account_id <id>")
	}
	if accountGivenExplicitly(app) && !accountIDsEqual(app.Config.AccountID, bound) {
		return "", output.ErrUsageHint(
			fmt.Sprintf("Profile %q is bound to account %s, and this command named account %s", name, bound, app.Config.AccountID),
			"Setup works in the profile's own account. Drop --account (or BASECAMP_ACCOUNT_ID).")
	}
	return bound, nil
}

// connectCredentialKind is the kind of credential the active profile holds,
// "" for none. A store that cannot be read is an error, not "none".
func connectCredentialKind(ctx context.Context, app *appctx.App) (string, error) {
	return credentialKindOf(ctx, app.Auth)
}

func credentialKindOf(ctx context.Context, mgr *auth.Manager) (string, error) {
	creds, err := mgr.GetStore().LoadContext(ctx, mgr.CredentialKey())
	switch {
	case errors.Is(err, auth.ErrNoCredential):
		return "", nil
	case err != nil:
		return "", output.ErrAuth("The stored credential could not be read: " + setup.ErrorText(err))
	case creds.OAuthType == "agent":
		return setup.KindAgent, nil
	default:
		return setup.KindBotUser, nil
	}
}

// checkConnectIdentity proves the profile is the agent it is meant to be.
// An Agent credential must read back as an Agent person. A bot user's login
// must be the identity --expect-identity pinned, and must not be an Agent.
func checkConnectIdentity(ctx context.Context, app *appctx.App, client *basecamp.Client, kind, oauthType string, me setup.Person, expect int64) (setup.Check, error) {
	c := setup.Check{Name: "Identity", Status: setup.StatusPass}
	if me.ID <= 0 {
		return c, output.ErrAuth(fmt.Sprintf("The profile's credential read back no person id (%d); it cannot be the agent", me.ID))
	}
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
		endpoint, err := app.Auth.AuthorizationEndpointFor(oauthType)
		if err != nil {
			return c, output.ErrAuth("Could not locate the login's identity endpoint: " + setup.ErrorText(err))
		}
		info, err := client.Authorization().GetInfo(ctx, &basecamp.GetInfoOptions{Endpoint: endpoint, FilterProduct: "bc3"})
		if err != nil {
			return c, output.ErrAuth(fmt.Sprintf("Could not read the login's identity: %s", setup.ErrorText(err)))
		}
		if info.Identity.ID != expect {
			return c, output.ErrAuth(fmt.Sprintf("The profile's login is identity %d, not the %d --expect-identity names; nothing was written", info.Identity.ID, expect))
		}
		c.Message = fmt.Sprintf("Bot user person %d (identity %d)", me.ID, expect)
	}
	return c, nil
}

// profileConfig is the configuration a named profile runs under, in the
// CLI's own precedence: environment over profile over file over defaults.
// It is root's chain for the active profile (ApplyProfile, then LoadFromEnv
// re-applied over it), minus this invocation's flags, which name the agent's
// run and not the operator's profile.
func profileConfig(profile string) (*config.Config, error) {
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
	if err := config.LoadFromEnv(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

// operatorProfileManager checks, without the network, that the operator's
// profile can name the operator: it exists, is on the agent's Basecamp, and
// holds a person's credential.
func operatorProfileManager(ctx context.Context, app *appctx.App, profile string) (*operatorProfile, error) {
	cfg, err := profileConfig(profile)
	if err != nil {
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
		return nil, output.ErrUsageHint(fmt.Sprintf("Operator profile %q holds no credential", profile), "Log in: basecamp auth login -P "+shellQuote(profile))
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
func resolveOperatorProfile(ctx context.Context, op *operatorProfile, profile, accountID string) (setup.Person, setup.Reader, error) {
	client := connectSDKClientFor(op.baseURL, &managerTokens{mgr: op.mgr})
	reader := setup.SDKReader{Client: client.ForAccount(accountID)}
	me, err := reader.Me(ctx)
	if err != nil {
		return setup.Person{}, nil, output.ErrAuth(fmt.Sprintf("Could not read who operator profile %q is in account %s: %s", profile, accountID, setup.ErrorText(err)))
	}
	if me.ID <= 0 {
		return setup.Person{}, nil, output.ErrAuth(fmt.Sprintf("Operator profile %q reported no person id", profile))
	}
	return me, reader, nil
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
	return basecamp.NewClient(&basecamp.Config{BaseURL: baseURL}, tokens, connectSDKOptions()...)
}

// connectSDKOptions are the only options setup's clients get: a transport
// and a user agent, never hooks or a logger.
func connectSDKOptions() []basecamp.ClientOption {
	return []basecamp.ClientOption{
		basecamp.WithTransport(http.DefaultTransport),
		basecamp.WithUserAgent(version.UserAgent() + " " + basecamp.DefaultUserAgent),
	}
}
