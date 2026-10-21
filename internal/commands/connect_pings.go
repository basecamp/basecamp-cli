package commands

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/auth"
	"github.com/basecamp/basecamp-cli/internal/connector"
	"github.com/basecamp/basecamp-cli/internal/connector/setup"
	"github.com/basecamp/basecamp-cli/internal/output"
	"github.com/basecamp/basecamp-cli/internal/richtext"
)

// Which connector answers an agent's Pings.
//
// Every connector for an agent sees all of the agent's Pings: a Ping is in no
// project, so serving projects can't divide them up the way it divides the
// rest. Each connector that admitted a Ping's line would answer it, so only
// one should, and that one is the connector started with --pings. It hears
// Pings and anything else outside a project; every other connector hears its
// projects alone.
//
// Running exactly one is the person's to arrange: the CLI can't see the
// connectors on other computers, and nothing in Basecamp chooses between
// them. What it can do is pick the likely one by default, keep the choice
// across restarts, and say so when it sees a second on this computer.

// The reasons a connector hears Pings or doesn't, as they are saved and shown
// in status --json.
const (
	// pingsBecauseChosen: --pings or --no-pings, on this start or an earlier
	// one.
	pingsBecauseChosen = "chosen"
	// pingsBecauseFirstConnection: the profile holds the agent's first
	// connection, whose secret is the agent client's own.
	pingsBecauseFirstConnection = "first_connection"
	// pingsBecauseAddedConnection: the profile holds a connection added after
	// the first, whose secret is its own.
	pingsBecauseAddedConnection = "added_connection"
	// pingsBecauseNotAnAgentConnection: the profile holds a person's login,
	// such as a bot user's, which hears Pings as every connector did before.
	pingsBecauseNotAnAgentConnection = "not_an_agent_connection"
	// pingsBecauseProject: --project narrowed the run to projects. Never
	// saved: it is this run's, not the profile's.
	pingsBecauseProject = "project"
)

// agentConnectionSecretPrefix starts the secret of every connection Basecamp
// adds to an agent after its first (Agent::Installation::SECRET_PREFIX in
// bc3). The first connection authenticates with the agent client's own
// secret, which doesn't.
const agentConnectionSecretPrefix = "bcac_"

// connectPings is whether a connector hears the agent's Pings, and why.
type connectPings struct {
	Hears   bool   `json:"hears"`
	Because string `json:"because"`
}

// resolveConnectPings decides whether a start hears Pings, and whether it
// saves that for the profile. In order:
//
//   - --pings or --no-pings, saved.
//   - --project: a narrowed run's feed brings no Ping, so it hears none, as
//     before. Nothing is saved; the profile's choice is the next un-narrowed
//     run's.
//   - the profile's saved choice.
//   - the default for its credential, saved, so a restart keeps it even
//     after the credential changes: a profile reconnected where Basecamp adds
//     connections holds an added connection afterwards, and the person who
//     had it answering Pings still does.
func resolveConnectPings(flag *bool, saved *connector.PingsChoice, narrowed bool, oauthType, secret string) (connectPings, bool) {
	switch {
	case flag != nil:
		return connectPings{Hears: *flag, Because: pingsBecauseChosen}, true
	case narrowed:
		return connectPings{Hears: false, Because: pingsBecauseProject}, false
	case saved != nil:
		return connectPings{Hears: saved.Hears, Because: saved.Because}, false
	}
	return connectPingsDefault(oauthType, secret), true
}

// connectPingsDefault is whether a profile that has chosen nothing hears
// Pings: an agent's first connection does, a connection added after it
// doesn't, and a person's login does, as before.
func connectPingsDefault(oauthType, secret string) connectPings {
	switch {
	case oauthType != "agent":
		return connectPings{Hears: true, Because: pingsBecauseNotAnAgentConnection}
	case strings.HasPrefix(secret, agentConnectionSecretPrefix):
		return connectPings{Hears: false, Because: pingsBecauseAddedConnection}
	}
	return connectPings{Hears: true, Because: pingsBecauseFirstConnection}
}

// Describe is why, in a person's words.
func (p connectPings) Describe() string {
	switch p.Because {
	case pingsBecauseChosen:
		if p.Hears {
			return "chosen with --pings"
		}
		return "chosen with --no-pings"
	case pingsBecauseFirstConnection:
		return "the agent's first connection"
	case pingsBecauseAddedConnection:
		return "a connection added after the agent's first"
	case pingsBecauseNotAnAgentConnection:
		return "a person's login, not an agent connection"
	case pingsBecauseProject:
		return "--project narrows this run to projects"
	}
	return "as saved for this profile"
}

// connectPingsPeer is another connector on this computer, for the same agent
// in the same account, that hears its Pings.
type connectPingsPeer struct {
	// Profile is its profile, or empty for a connector an older build runs on
	// state from before profiles had their own.
	Profile string `json:"profile"`
	PID     int    `json:"pid"`
	// Older says an older build started it, which doesn't say whether it
	// hears Pings. It does unless it was given --project.
	Older bool `json:"older,omitempty"`
}

// Words is the peer for a log line or a check.
func (p connectPingsPeer) Words() string {
	who := "an older connector"
	if p.Profile != "" {
		who = `profile "` + richtext.SanitizeSingleLine(p.Profile) + `"`
	}
	return who
}

// connectPingsPeers lists the connectors running on this computer for the
// agent connect.json names, under profiles other than this one, that hear
// its Pings or may. It reads what each wrote beside its instance lock and
// takes no lock: a look must not make a starting connector find its own
// lock held. A shadow answers nothing, so only the connectors' own state is
// read.
func connectPingsPeers(profile string, file setup.File) []connectPingsPeer {
	stateHome, err := connectStateHome()
	if err != nil {
		return nil
	}
	root := filepath.Join(stateHome, "basecamp", "connect")
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	prefix := strings.TrimSuffix(connector.StateDirName(file.AccountID, file.Agent.PersonID, ""), "-")
	legacy := connector.LegacyStateDirName(file.AccountID, file.Agent.PersonID)
	// This profile's own state is its profile directory, or, until a run of
	// this build adopts it, the legacy one status and doctor read as its own.
	own := map[string]bool{connector.StateDirName(file.AccountID, file.Agent.PersonID, profile): true}
	if dir, err := connectStatePath(profile, file, false); err == nil {
		own[filepath.Base(dir)] = true
	}
	var peers []connectPingsPeer
	for _, entry := range entries {
		name := entry.Name()
		other, isProfile := strings.CutPrefix(name, prefix+"-")
		if own[name] || !entry.IsDir() || (name != legacy && (!isProfile || !isValidProfileName(other))) {
			continue
		}
		holder, ok := connector.InstanceHolder(filepath.Join(root, name), file.AccountID, file.Agent.PersonID)
		if !ok || !holderStillRuns(holder) || (holder.Pings != nil && !*holder.Pings) {
			continue
		}
		peer := connectPingsPeer{PID: holder.PID, Older: holder.Pings == nil}
		if name != legacy {
			peer.Profile = other
		}
		peers = append(peers, peer)
	}
	slices.SortFunc(peers, func(a, b connectPingsPeer) int { return strings.Compare(a.Profile, b.Profile) })
	return peers
}

// connectProfilePings is whether the profile's connector hears Pings: the
// one running now, as its lock says (running true), else what a start with
// neither flag nor --project would decide, from the saved choice or the
// default for the credential the profile holds. It reads only this computer.
// It reports false when it can't say, and a saved choice that can't be read
// as the error the next start would refuse with.
func connectProfilePings(ctx context.Context, app *appctx.App, profile string, file setup.File) (pings connectPings, running, ok bool, err error) {
	if live, ok := connectRunningPings(profile, file); ok {
		return live, true, true, nil
	}
	next, ok, err := connectNextPings(ctx, app, profile, file)
	return next, false, ok, err
}

// connectRunningPings is whether the profile's connector running now hears
// Pings, as it said beside its lock; false when none runs, or an older build
// runs it and doesn't say.
func connectRunningPings(profile string, file setup.File) (connectPings, bool) {
	dir, err := connectStatePath(profile, file, false)
	if err != nil {
		return connectPings{}, false
	}
	holder, ok := connector.InstanceHolder(dir, file.AccountID, file.Agent.PersonID)
	if !ok || holder.Pings == nil || !holderStillRuns(holder) {
		return connectPings{}, false
	}
	return connectPings{Hears: *holder.Pings, Because: holder.PingsBecause}, true
}

// connectNextPings is whether a start with neither flag nor --project would
// hear Pings: the profile's saved choice, else the default for its
// credential.
func connectNextPings(ctx context.Context, app *appctx.App, profile string, file setup.File) (connectPings, bool, error) {
	dir, err := connectStatePath(profile, file, false)
	if err != nil {
		return connectPings{}, false, err
	}
	saved, ok, err := connector.LoadPingsChoice(dir)
	switch {
	case err != nil:
		return connectPings{}, false, err
	case ok:
		return connectPings{Hears: saved.Hears, Because: saved.Because}, true, nil
	}
	creds, err := app.Auth.GetStore().LoadContext(ctx, app.Auth.CredentialKey())
	if err != nil {
		return connectPings{}, false, nil //nolint:nilerr // the token checks report an unreadable credential
	}
	return connectPingsDefault(creds.OAuthType, creds.ClientSecret), true, nil
}

// connectPingsFlag is the start's --pings or --no-pings, nil when neither was
// given. A flag given false is the other choice: --pings=false is
// --no-pings. It refuses both at once, and hearing Pings on a run given
// --project, whose feed brings no Ping to hear.
func connectPingsFlag(f *connectRunFlags, narrowed bool, profile string) (*bool, error) {
	var hears bool
	switch {
	case f.pingsSet && f.noPingsSet:
		return nil, output.ErrUsage("--pings and --no-pings are one choice: pass one of them")
	case f.pingsSet:
		hears = f.pings
	case f.noPingsSet:
		hears = !f.noPings
	default:
		return nil, nil
	}
	if hears && narrowed {
		return nil, output.ErrUsageHint("--pings can't be used with --project: a run given --project hears no Pings",
			"To answer Pings here, serve this connector's projects with basecamp connect setup -P "+richtext.ShellQuote(profile)+" --serve <project-id>, and start it without --project.")
	}
	return &hears, nil
}

// connectRunPings decides whether this start hears Pings, from its flag, the
// profile's saved choice and its credential, and whether it saves the answer.
// It takes the run's instance lock as proof it is held: a start of the same
// profile that saved another choice while this one waited must not be
// overruled by a choice read before. The choice is the profile's, kept in its
// connector's state directory: a shadow reads it, so it admits what the
// connector it watches beside would, and never saves one. A saved choice that
// cannot be read refuses the start unless the start doesn't need it: guessing
// either way answers Pings twice or not at all.
func connectRunPings(held *connector.InstanceLock, profile string, file setup.File, shadow bool, flag *bool, narrowed bool, creds *auth.Credentials) (connectPings, bool, error) {
	if held == nil {
		return connectPings{}, false, errors.New("whether the connector hears Pings is decided only under its instance lock")
	}
	dir, err := connectStatePath(profile, file, false)
	if err != nil {
		return connectPings{}, false, output.ErrUsage("The connector's state directory cannot be used: " + err.Error())
	}
	var saved *connector.PingsChoice
	choice, ok, err := connector.LoadPingsChoice(dir)
	switch {
	case err != nil && flag == nil && !narrowed:
		return connectPings{}, false, output.ErrUsageHint("Whether this connector hears Pings was saved, and can't be read: "+setup.ErrorText(err),
			"Choose again: basecamp connect -P "+richtext.ShellQuote(profile)+" --pings, or --no-pings to leave them to another connector.")
	case ok:
		saved = &choice
	}
	pings, save := resolveConnectPings(flag, saved, narrowed, creds.OAuthType, creds.ClientSecret)
	return pings, save && !shadow, nil
}

// warnConnectPingsPeers says, as a connector that hears Pings starts, that
// another connector on this computer hears the same agent's Pings, so each is
// answered twice. One that hears none says what it leaves to whom.
func warnConnectPingsPeers(logger *slog.Logger, pings connectPings, profile string, file setup.File) {
	if !pings.Hears {
		logger.Info("connector: hearing no Pings, and leaving them to the one connector that answers them; start with --pings to answer them here",
			"pings_because", pings.Describe())
		return
	}
	for _, peer := range connectPingsPeers(profile, file) {
		msg := "connector: another connector on this computer hears this agent's Pings too, so each is answered twice; only one connector should answer Pings: restart one of them with --no-pings"
		if peer.Older {
			msg = "connector: an older connector on this computer may hear this agent's Pings too, so each would be answered twice; only one connector should answer Pings: upgrade it, or restart this one with --no-pings"
		}
		logger.Warn(msg, "profile", richtext.SanitizeSingleLine(peer.Profile), "pid", peer.PID)
	}
}
