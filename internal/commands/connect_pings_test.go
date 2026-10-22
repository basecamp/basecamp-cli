//go:build unix

package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/auth"
	"github.com/basecamp/basecamp-cli/internal/connector"
	"github.com/basecamp/basecamp-cli/internal/connector/setup"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// addedConnectionSecret is shaped as Basecamp shapes an added connection's
// secret, and is not one.
const addedConnectionSecret = "bcac_" + "1111111111111111111111111111111111111111111"

// pingsWarning is the start's warning that another connector on this
// computer hears the agent's Pings too.
const pingsWarning = "only one connector should answer Pings"

// asAddedConnection gives a profile the credential of a connection added
// after the agent's first: the same agent, with a secret of its own.
func asAddedConnection(t *testing.T, s *connectSetupServer, profile string) {
	t.Helper()
	app := newConnectSetupApp(t, s, profile)
	key := auth.ProfileCredentialKey(profile)
	creds, err := app.Auth.GetStore().Load(key)
	require.NoError(t, err)
	require.Equal(t, "agent", creds.OAuthType)
	creds.ClientSecret = addedConnectionSecret
	require.NoError(t, app.Auth.GetStore().Save(key, creds))
}

func savedPingsChoice(t *testing.T, f operatorFixture, profile string) (connector.PingsChoice, bool) {
	t.Helper()
	dir := filepath.Join(connectStateRoot(t), connector.StateDirName(f.file.AccountID, f.file.Agent.PersonID, profile))
	choice, ok, err := connector.LoadPingsChoice(dir)
	require.NoError(t, err)
	return choice, ok
}

func holderPings(t *testing.T, f operatorFixture, profile string) *bool {
	t.Helper()
	dir := filepath.Join(connectStateRoot(t), connector.StateDirName(f.file.AccountID, f.file.Agent.PersonID, profile))
	holder, ok := connector.InstanceHolder(dir, f.file.AccountID, f.file.Agent.PersonID)
	require.True(t, ok, "%s's connector holds its lock", profile)
	return holder.Pings
}

func (r runningConnector) stop(t *testing.T) {
	t.Helper()
	r.cancel()
	_ = r.awaitExit(t)
}

// The agent's first connection hears its Pings, as every connector did
// before, and its first start saves that, beside the lock too.
func TestAnAgentsFirstConnectionHearsPings(t *testing.T) {
	f := newOperatorFixture(t)

	r := startProfileConnector(t, f.s, "agent")
	r.awaitRunning(t)

	assert.Contains(t, r.out.String(), "pings=true")
	choice, ok := savedPingsChoice(t, f, "agent")
	require.True(t, ok, "the first start saves what it found")
	assert.True(t, choice.Hears)
	assert.Equal(t, pingsBecauseFirstConnection, choice.Because)
	assert.False(t, choice.At.IsZero())
	hears := holderPings(t, f, "agent")
	require.NotNil(t, hears)
	assert.True(t, *hears)
	assert.NotContains(t, r.out.String(), pingsWarning, "nobody else hears them")
}

// A connection added after the first hears no Pings until started with
// --pings, and a restart keeps whatever was chosen last.
func TestAnAddedConnectionHearsPingsOnlyWhenStartedWithThem(t *testing.T) {
	f := newOperatorFixture(t)
	setUpSecondProfile(t, f.s, "laptop")
	asAddedConnection(t, f.s, "laptop")

	for _, step := range []struct {
		args    []string
		hears   bool
		because string
	}{
		{nil, false, pingsBecauseAddedConnection},
		{[]string{"--pings"}, true, pingsBecauseChosen},
		{nil, true, pingsBecauseChosen},
		{[]string{"--no-pings"}, false, pingsBecauseChosen},
		{nil, false, pingsBecauseChosen},
	} {
		r := startProfileConnector(t, f.s, "laptop", step.args...)
		r.awaitRunning(t)
		assert.Contains(t, r.out.String(), fmt.Sprintf("pings=%t", step.hears), "started with %v", step.args)
		choice, ok := savedPingsChoice(t, f, "laptop")
		require.True(t, ok)
		assert.Equal(t, step.hears, choice.Hears, "started with %v", step.args)
		assert.Equal(t, step.because, choice.Because, "started with %v", step.args)
		hears := holderPings(t, f, "laptop")
		require.NotNil(t, hears)
		assert.Equal(t, step.hears, *hears)
		r.stop(t)
	}
}

// A profile that first started on a connection added after the agent's
// first leaves Pings to another. Reconnected where the agent has no live
// first connection, it is handed that one, and hears them again: doctor
// says so before it starts, and the start saves it.
func TestAnAddedConnectionThatBecomesTheFirstHearsPings(t *testing.T) {
	f := newOperatorFixture(t)
	setUpSecondProfile(t, f.s, "laptop")
	asAddedConnection(t, f.s, "laptop")
	r := startProfileConnector(t, f.s, "laptop")
	r.awaitRunning(t)
	r.stop(t)
	choice, ok := savedPingsChoice(t, f, "laptop")
	require.True(t, ok)
	require.Equal(t, pingsBecauseAddedConnection, choice.Because)

	out, err := runAgentConnect(t, newConnectSetupApp(t, f.s, "laptop"))
	require.NoError(t, err, out)
	assert.Contains(t, f.doctorStyledFor(t, "laptop"), "It hears the agent's Pings: the agent's first connection", "what the next start does")

	r = startProfileConnector(t, f.s, "laptop")
	r.awaitRunning(t)
	assert.Contains(t, r.out.String(), "pings=true")
	choice, ok = savedPingsChoice(t, f, "laptop")
	require.True(t, ok)
	assert.True(t, choice.Hears)
	assert.Equal(t, pingsBecauseFirstConnection, choice.Because)
}

// A reconnect where Basecamp adds connections moves a profile off the
// agent's first connection, which stays live beside the one added. A profile
// that held it keeps answering Pings, even one that has never started on
// this build, as one 0.16.0 ran hasn't: the reconnect saves first what its
// next start would have saved, once the old credential still mints. One a
// newer approval replaced held the first connection no longer and answered
// nothing, so the profile leaves Pings to the one that does; when it can't be
// told, the reconnect says how to keep them here. A choice made with a flag
// stays, and a reconnect that hands over the first connection saves nothing.
func TestAReconnectOffTheFirstConnectionKeepsItAnsweringPings(t *testing.T) {
	for name, tc := range map[string]struct {
		args    []string // the profile's last start before the reconnect
		answer  int      // Basecamp's answer to a mint with the old secret
		secret  string   // the secret the reconnect hands over
		saved   *connectPings
		hears   bool
		because string
		says    string
	}{
		"its first connection is live": {
			secret: addedConnectionSecret, saved: &connectPings{Hears: true, Because: pingsBecauseFirstConnection},
			hears: true, because: pingsBecauseFirstConnection, says: "keeps answering the agent's Pings",
		},
		"a newer approval replaced it": {
			answer: http.StatusUnauthorized, secret: addedConnectionSecret,
			because: pingsBecauseAddedConnection,
		},
		"whether it still works can't be told": {
			answer: http.StatusServiceUnavailable, secret: addedConnectionSecret,
			because: pingsBecauseAddedConnection, says: "basecamp connect -P agent --pings",
		},
		"it was started with --no-pings": {
			args: []string{"--no-pings"}, secret: addedConnectionSecret, saved: &connectPings{Hears: false, Because: pingsBecauseChosen},
			because: pingsBecauseChosen,
		},
		"the reconnect hands over the first connection": {
			hears: true, because: pingsBecauseFirstConnection,
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newOperatorFixture(t)
			r := startProfileConnector(t, f.s, "agent", tc.args...)
			r.awaitRunning(t)
			r.stop(t)
			if tc.args == nil {
				dir, err := connectStatePath("agent", f.file, false)
				require.NoError(t, err)
				require.NoError(t, os.Remove(filepath.Join(dir, connector.PingsFile)), "as a profile 0.16.0 ran has none")
			}

			f.s.mu.Lock()
			f.s.secretAnswers = map[string]int{fakeConnectSecret: tc.answer}
			f.s.connectionSecret = tc.secret
			f.s.mu.Unlock()
			out, err := runAgentConnect(t, newConnectSetupApp(t, f.s, "agent"))
			require.NoError(t, err, out)
			if tc.says != "" {
				assert.Contains(t, out, tc.says)
			} else {
				assert.NotContains(t, out, "Pings")
			}
			choice, ok := savedPingsChoice(t, f, "agent")
			if tc.saved == nil {
				assert.False(t, ok, "the reconnect saves nothing")
			} else {
				require.True(t, ok, "the reconnect saved the choice")
				assert.Equal(t, *tc.saved, connectPings{Hears: choice.Hears, Because: choice.Because})
			}

			r = startProfileConnector(t, f.s, "agent")
			r.awaitRunning(t)
			assert.Contains(t, r.out.String(), fmt.Sprintf("pings=%t", tc.hears))
			choice, ok = savedPingsChoice(t, f, "agent")
			require.True(t, ok)
			assert.Equal(t, tc.hears, choice.Hears)
			assert.Equal(t, tc.because, choice.Because)
		})
	}
}

// A reconnect keeps a profile answering Pings only when Basecamp added the
// connection beside the old one, for the same agent, and only over a choice
// that still follows the credential: one saved while the approval waited,
// with a flag, stays. A save that fails doesn't fail the connection, whose
// handover can't be had twice: it says how to keep answering them here
// instead.
func TestKeepingPingsTakesAConnectionAddedForTheSameAgent(t *testing.T) {
	state := t.TempDir()
	require.NoError(t, os.Chmod(state, 0o700))
	t.Setenv("XDG_STATE_HOME", state)
	file := setup.File{AccountID: "999", Agent: setup.Agent{PersonID: setupAgentPerson}}
	carry := &connectPingsCarry{profile: "agent", file: file, clientID: "agent-client", live: true}
	added := &auth.AgentConnection{ClientID: "agent-client", ClientSecret: addedConnectionSecret}

	for name, conn := range map[string]auth.AgentConnection{
		"the first connection again": {ClientID: "agent-client", ClientSecret: fakeConnectSecret},
		"another agent's":            {ClientID: "another-client", ClientSecret: addedConnectionSecret},
	} {
		assert.Equal(t, connectPingsKept{}, carry.keep(&conn), name)
	}
	var none *connectPingsCarry
	assert.Equal(t, connectPingsKept{}, none.keep(added))

	dir, err := connectStateDir("agent", file, false)
	require.NoError(t, err)
	chosen := connector.PingsChoice{Hears: false, Because: pingsBecauseChosen, At: time.Now().UTC()}
	require.NoError(t, connector.SavePingsChoice(dir, chosen))
	assert.Equal(t, connectPingsKept{}, carry.keep(added))
	choice, ok, err := connector.LoadPingsChoice(dir)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, pingsBecauseChosen, choice.Because, "a --no-pings saved while the approval waited stays")

	notADir := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(notADir, nil, 0o600))
	t.Setenv("XDG_STATE_HOME", notADir)
	kept := carry.keep(added)
	require.Error(t, kept.unsaved)
	words := kept.Words("agent")
	assert.Contains(t, words, "couldn't be saved")
	assert.Contains(t, words, "basecamp connect -P agent --pings")
}

// A run given --project hears no Ping, as before, and leaves the profile's
// saved choice for the next run that isn't narrowed. --pings can't make a
// narrowed run hear one: its feed brings none, so it is refused rather than
// taken.
func TestARunGivenProjectsHearsNoPings(t *testing.T) {
	f := newOperatorFixture(t)
	r := startProfileConnector(t, f.s, "agent")
	r.awaitRunning(t)
	r.stop(t)

	r = startProfileConnector(t, f.s, "agent", "--project", fmt.Sprint(setupProject))
	r.awaitRunning(t)
	assert.Contains(t, r.out.String(), "pings=false")
	hears := holderPings(t, f, "agent")
	require.NotNil(t, hears)
	assert.False(t, *hears)
	choice, ok := savedPingsChoice(t, f, "agent")
	require.True(t, ok)
	assert.True(t, choice.Hears, "the saved choice is the next un-narrowed run's")
	assert.Equal(t, pingsBecauseFirstConnection, choice.Because)
	r.stop(t)

	_, err := f.run(t, output.FormatJSON, "--pings", "--project", fmt.Sprint(setupProject))
	require.Error(t, err)
	assert.Equal(t, output.CodeUsage, usageError(t, err).Code)
	assert.Contains(t, err.Error(), "--project")
}

func TestPingsAndNoPingsAreOneChoice(t *testing.T) {
	f := newOperatorFixture(t)
	_, err := f.run(t, output.FormatJSON, "--pings", "--no-pings")
	require.Error(t, err)
	assert.Equal(t, output.CodeUsage, usageError(t, err).Code)
	_, ok := savedPingsChoice(t, f, "agent")
	assert.False(t, ok, "a refused start saves nothing")
}

// Two connectors on this computer that both hear the agent's Pings answer
// each one twice. The second to start says so, naming the first; one that
// doesn't hear them, and a shadow, which answers nothing, say nothing.
func TestASecondConnectorThatHearsPingsHereWarns(t *testing.T) {
	f := newOperatorFixture(t)
	// A second profile on the same agent's first connection: Basecamp that
	// cannot add connections replaces the first one's secret, and the CLI
	// can't tell from here.
	setUpSecondProfile(t, f.s, "laptop")

	first := startProfileConnector(t, f.s, "agent")
	first.awaitRunning(t)

	second := startProfileConnector(t, f.s, "laptop")
	second.awaitRunning(t)
	warning := lineContaining(second.out.String(), pingsWarning)
	require.NotEmpty(t, warning, "the second warned before it ran: %s", second.out.String())
	assert.Contains(t, warning, "level=WARN")
	assert.Contains(t, warning, "profile=agent", "it names the other")
	assert.NotContains(t, first.out.String(), pingsWarning, "the first saw nobody")
	second.stop(t)

	quiet := startProfileConnector(t, f.s, "laptop", "--no-pings")
	quiet.awaitRunning(t)
	assert.NotContains(t, quiet.out.String(), pingsWarning)
	quiet.stop(t)

	shadow := startProfileConnector(t, f.s, "laptop", "--shadow", "--pings")
	shadow.awaitRunning(t)
	assert.NotContains(t, shadow.out.String(), pingsWarning, "a shadow answers nothing")
	shadow.stop(t)

	first.stop(t)
	again := startProfileConnector(t, f.s, "laptop", "--pings")
	again.awaitRunning(t)
	assert.NotContains(t, again.out.String(), pingsWarning, "and a stopped connector is nobody")
}

// status and doctor say whether the profile's connector hears Pings, why,
// and who else on this computer does.
func TestStatusAndDoctorSayWhoHearsPings(t *testing.T) {
	f := newOperatorFixture(t)
	setUpSecondProfile(t, f.s, "laptop")
	a := startProfileConnector(t, f.s, "agent")
	a.awaitRunning(t)
	b := startProfileConnector(t, f.s, "laptop")
	b.awaitRunning(t)

	out, err := f.run(t, output.FormatJSON, "status")
	require.NoError(t, err, out)
	var envelope struct {
		Data struct {
			LockHolder *struct {
				Pings *bool `json:"pings"`
			} `json:"lock_holder"`
			Pings struct {
				Saved *struct {
					Hears   bool   `json:"hears"`
					Because string `json:"because"`
				} `json:"saved"`
				Others []struct {
					Profile string `json:"profile"`
					PID     int    `json:"pid"`
				} `json:"others"`
			} `json:"pings"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &envelope), out)
	require.NotNil(t, envelope.Data.LockHolder)
	require.NotNil(t, envelope.Data.LockHolder.Pings)
	assert.True(t, *envelope.Data.LockHolder.Pings, "the running connector hears them")
	require.NotNil(t, envelope.Data.Pings.Saved)
	assert.True(t, envelope.Data.Pings.Saved.Hears)
	assert.Equal(t, pingsBecauseFirstConnection, envelope.Data.Pings.Saved.Because)
	require.Len(t, envelope.Data.Pings.Others, 1)
	assert.Equal(t, "laptop", envelope.Data.Pings.Others[0].Profile)

	styled, err := f.run(t, output.FormatStyled, "status")
	require.NoError(t, err)
	assert.Contains(t, styled, "Pings          heard: the agent's first connection")
	assert.Contains(t, styled, `profile "laptop"`)

	doctor := f.doctorStyled(t)
	assert.Contains(t, doctor, "Pings")
	assert.Contains(t, doctor, `profile "laptop"`)
	assert.Contains(t, doctor, "--no-pings")

	b.stop(t)
	doctor = f.doctorStyled(t)
	assert.Contains(t, doctor, "The connector running now hears the agent's Pings: the agent's first connection")
	assert.NotContains(t, doctor, `profile "laptop"`)

	a.stop(t)
	doctor = f.doctorStyled(t)
	assert.Contains(t, doctor, "It hears the agent's Pings: the agent's first connection", "with none running, what the next start does")
}

// A connector running with --project hears no Pings whatever the profile's
// saved choice says, and doctor, status and guided setup say what it does
// now, not what the next un-narrowed start would: a narrowed run beside the
// one that answers Pings is no second answerer.
func TestDiagnosticsSayWhatTheRunningConnectorDoes(t *testing.T) {
	f := newOperatorFixture(t)
	setUpSecondProfile(t, f.s, "laptop")
	r := startProfileConnector(t, f.s, "agent")
	r.awaitRunning(t)
	r.stop(t)

	narrowed := startProfileConnector(t, f.s, "agent", "--project", fmt.Sprint(setupProject))
	narrowed.awaitRunning(t)
	other := startProfileConnector(t, f.s, "laptop")
	other.awaitRunning(t)

	doctor := f.doctorStyled(t)
	assert.Contains(t, doctor, "The connector running now doesn't hear the agent's Pings: --project narrows this run to projects")
	assert.Contains(t, doctor, "restarted without --project, it would hear them (the agent's first connection)")
	assert.NotContains(t, doctor, "answered twice", "the one answering Pings is the only one")

	styled, err := f.run(t, output.FormatStyled, "status")
	require.NoError(t, err)
	assert.Contains(t, styled, "Pings          heard: the agent's first connection", "the saved choice")
	assert.Contains(t, styled, "the connector running now hears none: --project narrows this run to projects")

	assert.False(t, guidedPings(context.Background(), newConnectSetupApp(t, f.s, "agent"), "agent", f.file).hears,
		"guided setup doesn't suggest pinging a connector that can't hear it")
}

// --pings=false is --no-pings, and --no-pings=false is --pings: a flag given
// false is a choice, never the flag's absence.
func TestAPingsFlagGivenFalseIsTheOtherChoice(t *testing.T) {
	f := newOperatorFixture(t)
	for _, step := range []struct {
		arg   string
		hears bool
	}{
		{"--pings=false", false},
		{"--no-pings=false", true},
	} {
		r := startProfileConnector(t, f.s, "agent", step.arg)
		r.awaitRunning(t)
		assert.Contains(t, r.out.String(), fmt.Sprintf("pings=%t", step.hears), step.arg)
		choice, ok := savedPingsChoice(t, f, "agent")
		require.True(t, ok)
		assert.Equal(t, step.hears, choice.Hears, step.arg)
		assert.Equal(t, pingsBecauseChosen, choice.Because, step.arg)
		r.stop(t)
	}

	_, err := f.run(t, output.FormatJSON, "--no-pings=false", "--project", fmt.Sprint(setupProject))
	require.Error(t, err, "hearing Pings is refused on a narrowed run however it is spelled")
	assert.Contains(t, err.Error(), "--project")
	_, err = f.run(t, output.FormatJSON, "--pings=false", "--no-pings")
	require.Error(t, err, "two flags are two choices, even when they agree")
}

// A profile whose state is still in the legacy directory reads that state as
// its own, so an older build's connector running on it is this profile's,
// not a second one; to another profile of the agent it is a second one.
func TestAProfilesLegacyConnectorIsNotItsOwnPeer(t *testing.T) {
	f := newOperatorFixture(t)
	setUpSecondProfile(t, f.s, "laptop")
	stateHome, err := connectStateHome()
	require.NoError(t, err)
	legacy, err := ensurePrivateChain(stateHome, "basecamp", "connect", connector.LegacyStateDirName(f.file.AccountID, f.file.Agent.PersonID))
	require.NoError(t, err)
	lock, err := connector.AcquireInstanceLock(legacy, f.file.AccountID, f.file.Agent.PersonID, time.Now().Add(-time.Minute))
	require.NoError(t, err)
	t.Cleanup(func() { _ = lock.Release() })
	older, err := json.Marshal(map[string]any{"pid": os.Getpid(), "started_at": time.Now().Add(time.Minute).UTC().Format(time.RFC3339),
		"account_id": f.file.AccountID, "agent_person_id": f.file.Agent.PersonID})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(lock.Path()+".json", older, 0o600), "as an older build writes it: nothing about Pings")
	_, err = ensurePrivateChain(stateHome, "basecamp", "connect", connector.StateDirName(f.file.AccountID, f.file.Agent.PersonID, "laptop"))
	require.NoError(t, err)

	assert.Empty(t, connectPingsPeers("agent", f.file), "the legacy state is agent's own until a run adopts it")
	peers := connectPingsPeers("laptop", f.file)
	require.Len(t, peers, 1)
	assert.True(t, peers[0].Older)
	assert.Empty(t, peers[0].Profile)
}

// doctorStyled is doctor's styled report. The fake Basecamp serves no
// account feed, so doctor fails that check; the report is printed first.
func (f operatorFixture) doctorStyled(t *testing.T) string {
	t.Helper()
	return f.doctorStyledFor(t, "agent")
}

func (f operatorFixture) doctorStyledFor(t *testing.T, profile string) string {
	t.Helper()
	app := newConnectSetupApp(t, f.s, profile)
	var buf bytes.Buffer
	app.Output = output.New(output.Options{Format: output.FormatStyled, Writer: &buf})
	cmd := NewConnectCmd()
	cmd.SetArgs([]string{"doctor"})
	cmd.SetContext(appctx.WithApp(context.Background(), app))
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	_ = cmd.Execute()
	return buf.String()
}

func lineContaining(out, needle string) string {
	for line := range strings.SplitSeq(out, "\n") {
		if strings.Contains(line, needle) {
			return line
		}
	}
	return ""
}

// Guided setup tells a person whether this computer will answer the agent's
// Pings. A connection added after the first leaves them to the first, so
// "ping it" is no way to try it here.
func TestGuidedSetupOnAnAddedConnectionSaysWhoAnswersPings(t *testing.T) {
	s := startConnectSetupServer(t)
	ownedByTheOperator(s)
	s.agentProjects = projectsNamed(setupProject)
	s.connectionSecret = addedConnectionSecret
	guided(t, &scriptedPrompter{confirms: []bool{true}})

	out, err := runConnectSetupCmd(t, bareSetupApp(t, s, "agent"))
	require.NoError(t, err, out)

	assert.Contains(t, out, "Once it's running, mention Marie Chef in one of those projects to try it.")
	assert.NotContains(t, out, "or ping it")
	assert.Contains(t, out, "only one computer should answer")
	assert.Contains(t, out, "basecamp connect -P agent --pings")
}

// A shadow admits what the connector it watches beside would: it reads the
// profile's choice, and never saves one of its own.
func TestAShadowHearsPingsAsItsConnectorWould(t *testing.T) {
	f := newOperatorFixture(t)
	r := startProfileConnector(t, f.s, "agent", "--no-pings")
	r.awaitRunning(t)
	r.stop(t)

	shadow := startProfileConnector(t, f.s, "agent", "--shadow")
	shadow.awaitRunning(t)
	assert.Contains(t, shadow.out.String(), "pings=false", "the profile's choice")
	shadow.stop(t)

	shadow = startProfileConnector(t, f.s, "agent", "--shadow", "--pings")
	shadow.awaitRunning(t)
	assert.Contains(t, shadow.out.String(), "pings=true", "a flag is the shadow run's own")
	shadow.stop(t)

	choice, ok := savedPingsChoice(t, f, "agent")
	require.True(t, ok)
	assert.False(t, choice.Hears, "a shadow's flag doesn't change the profile's choice")
	shadowDir, err := connectStatePath("agent", f.file, true)
	require.NoError(t, err)
	_, saved, err := connector.LoadPingsChoice(shadowDir)
	require.NoError(t, err)
	assert.False(t, saved, "and a shadow saves none of its own")

	styled, err := f.run(t, output.FormatStyled, "status", "--shadow")
	require.NoError(t, err)
	assert.Contains(t, styled, "Pings          not heard: chosen with --no-pings", "status --shadow shows the choice the shadow reads")
}

// A running connector doesn't hide a saved choice the next start can't read:
// doctor reads it either way, and fails the check.
func TestDoctorReadsTheSavedChoiceWhileAConnectorRuns(t *testing.T) {
	f := newOperatorFixture(t)
	r := startProfileConnector(t, f.s, "agent", "--project", fmt.Sprint(setupProject))
	r.awaitRunning(t)
	dir, err := connectStatePath("agent", f.file, false)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, connector.PingsFile), []byte("{hears"), 0o600))

	doctor := f.doctorStyled(t)
	assert.Contains(t, doctor, "can't be read, so the next start without a flag refuses")
	assert.Contains(t, doctor, "basecamp connect -P agent --pings")
}

// A connector an older build runs on this profile's state doesn't say
// whether it hears Pings, and hears them unless given --project: it is
// reported as one that may, whatever choice this build saved, which the
// older build ignores.
func TestAnOlderBuildsRunningConnectorMayHearPings(t *testing.T) {
	f := newOperatorFixture(t)
	setUpSecondProfile(t, f.s, "laptop")
	r := startProfileConnector(t, f.s, "agent", "--no-pings")
	r.awaitRunning(t)
	r.stop(t)

	dir, err := connectStatePath("agent", f.file, false)
	require.NoError(t, err)
	lock, err := connector.AcquireInstanceLock(dir, f.file.AccountID, f.file.Agent.PersonID, time.Now())
	require.NoError(t, err)
	t.Cleanup(func() { _ = lock.Release() })
	older, err := json.Marshal(map[string]any{"pid": os.Getpid(), "started_at": time.Now().Add(time.Minute).UTC().Format(time.RFC3339),
		"account_id": f.file.AccountID, "agent_person_id": f.file.Agent.PersonID})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(lock.Path()+".json", older, 0o600), "as an older build writes it: nothing about Pings")
	other := startProfileConnector(t, f.s, "laptop")
	other.awaitRunning(t)

	doctor := f.doctorStyled(t)
	assert.Contains(t, doctor, "The connector running now may hear the agent's Pings: an older build runs it")
	assert.Contains(t, doctor, `and so does profile "laptop"`, "and a second one beside it is warned about")
	assert.NotContains(t, doctor, "doesn't hear the agent's Pings", "the saved --no-pings is not what's running")

	styled, err := f.run(t, output.FormatStyled, "status")
	require.NoError(t, err)
	assert.Contains(t, styled, "the connector running now may hear them: an older build runs it")
	assert.True(t, guidedPings(context.Background(), newConnectSetupApp(t, f.s, "agent"), "agent", f.file).hears)
}

// The one connector that answers the agent's Pings, beside one that leaves
// them to it, is the arrangement wanted: status and doctor name the
// answerer, and warn about nothing.
func TestOneAnswererBesideAConnectorThatLeavesPingsIsNoConflict(t *testing.T) {
	f := newOperatorFixture(t)
	setUpSecondProfile(t, f.s, "laptop")
	a := startProfileConnector(t, f.s, "agent", "--no-pings")
	a.awaitRunning(t)
	b := startProfileConnector(t, f.s, "laptop")
	b.awaitRunning(t)

	styled, err := f.run(t, output.FormatStyled, "status")
	require.NoError(t, err)
	assert.Contains(t, styled, `answered on this computer by profile "laptop"`)
	assert.NotContains(t, styled, "only one connector should answer them")

	doctor := f.doctorStyled(t)
	assert.Contains(t, doctor, `The connector running now doesn't hear the agent's Pings: chosen with --no-pings; profile "laptop"`)
	assert.NotContains(t, doctor, "answered twice")
}

// A saved choice that can't be read, or that isn't this user's own file,
// is recovered by removing it and choosing again: a flag alone can't write
// over a file it won't trust. Every place that meets it says so, with its
// path.
func TestAnUnusableSavedChoiceSaysHowToRecover(t *testing.T) {
	f := newOperatorFixture(t)
	r := startProfileConnector(t, f.s, "agent")
	r.awaitRunning(t)
	r.stop(t)
	dir, err := connectStatePath("agent", f.file, false)
	require.NoError(t, err)
	path := filepath.Join(dir, connector.PingsFile)
	require.NoError(t, os.Remove(path))
	require.NoError(t, os.Symlink(filepath.Join(t.TempDir(), "elsewhere.json"), path))

	for _, args := range [][]string{nil, {"--no-pings"}} {
		_, err := f.run(t, output.FormatJSON, args...)
		require.Error(t, err, "started with %v", args)
		e := usageError(t, err)
		assert.Equal(t, output.CodeUsage, e.Code)
		assert.Contains(t, e.Hint, "Remove "+path, "started with %v", args)
	}
	g := guidedPings(context.Background(), newConnectSetupApp(t, f.s, "agent"), "agent", f.file)
	assert.False(t, g.hears, "guided setup doesn't advertise Pings a start would refuse")
	assert.Contains(t, g.remedy, "Remove "+path)
	styled, err := f.run(t, output.FormatStyled, "status")
	require.NoError(t, err)
	assert.Contains(t, styled, "Remove "+path)
	assert.Contains(t, f.doctorStyled(t), "Remove "+path)

	require.NoError(t, os.Remove(path))
	again := startProfileConnector(t, f.s, "agent", "--no-pings")
	again.awaitRunning(t)
	choice, ok := savedPingsChoice(t, f, "agent")
	require.True(t, ok)
	assert.False(t, choice.Hears, "and once it's gone, the flag chooses")
}

// guidedSummary is guided setup's closing summary for the agent profile, as
// runGuidedConnectSetup renders it from what this computer says now: for
// connect.json as set up, or with its projects taken out.
func (f operatorFixture) guidedSummary(t *testing.T, serving bool) string {
	t.Helper()
	file := f.file
	if !serving {
		file.Projects = nil
	}
	agent := guidedAgent{Me: setup.Person{Name: "Marie Chef"}, Projects: []guidedProject{{ID: setupProject, Name: "Connector"}}}
	pings := guidedPings(context.Background(), newConnectSetupApp(t, f.s, "agent"), "agent", file)
	var buf bytes.Buffer
	renderGuidedSummary(&buf, output.NewRenderer(&buf, false), agent, file, connectorRunning("agent", file), pings, "agent")
	return buf.String()
}

// Guided setup says how to recover a saved choice it can't read whether or
// not a connector runs, and whatever the running one hears: the next start
// refuses either way. It suggests a ping only where the profile's connector
// hears them, which a running one still does and a start that refuses
// doesn't.
func TestGuidedSetupAlwaysSaysHowToRecoverAnUnreadableChoice(t *testing.T) {
	for _, tc := range []struct {
		name  string
		args  []string
		runs  bool
		pings bool
	}{
		{name: "none running"},
		{name: "running and hearing Pings", runs: true, pings: true},
		{name: "running with --no-pings", args: []string{"--no-pings"}, runs: true},
		{name: "running with --project", args: []string{"--project", fmt.Sprint(setupProject)}, runs: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newOperatorFixture(t)
			r := startProfileConnector(t, f.s, "agent", tc.args...)
			r.awaitRunning(t)
			if !tc.runs {
				r.stop(t)
			}
			dir, err := connectStatePath("agent", f.file, false)
			require.NoError(t, err)
			path := filepath.Join(dir, connector.PingsFile)
			require.NoError(t, os.WriteFile(path, []byte("{hears"), 0o600))

			for _, serving := range []bool{true, false} {
				out := f.guidedSummary(t, serving)
				assert.Contains(t, out, "Remove "+path, "serving=%t:\n%s", serving, out)
				assert.Contains(t, out, "its next start will refuse", "serving=%t", serving)
				assert.NotContains(t, out, "leaves Pings to another connection", "serving=%t: nothing says who answers them", serving)
				ping := "or ping it"
				if !serving {
					ping = "only your pings reach it"
				}
				if tc.pings {
					assert.Contains(t, out, ping, "serving=%t: the connector running now hears them", serving)
				} else {
					assert.NotContains(t, out, ping, "serving=%t", serving)
				}
			}
		})
	}
}

// With this connector and two others on this computer hearing the agent's
// Pings, each is answered more than once, not twice: doctor says so, and so
// does the start that finds the other two, in one warning naming both, not
// one per connector. Beside a single other, it is twice.
func TestThreeConnectorsHearingPingsAnswerEachMoreThanOnce(t *testing.T) {
	f := newOperatorFixture(t)
	setUpSecondProfile(t, f.s, "laptop")
	setUpSecondProfile(t, f.s, "desk")
	laptop := startProfileConnector(t, f.s, "laptop")
	laptop.awaitRunning(t)
	desk := startProfileConnector(t, f.s, "desk")
	desk.awaitRunning(t)
	assert.Contains(t, lineContaining(desk.out.String(), pingsWarning), "answered twice", "beside one other")

	agent := startProfileConnector(t, f.s, "agent")
	agent.awaitRunning(t)
	out := agent.out.String()
	assert.Equal(t, 1, strings.Count(out, pingsWarning), "one warning, not one per connector:\n%s", out)
	warning := lineContaining(out, pingsWarning)
	assert.Contains(t, warning, "level=WARN")
	assert.Contains(t, warning, "more than once")
	assert.NotContains(t, warning, "twice")
	assert.Contains(t, warning, "desk")
	assert.Contains(t, warning, "laptop")

	doctor := f.doctorStyled(t)
	assert.Contains(t, doctor, "each Ping is answered more than once")
	assert.NotContains(t, doctor, "answered twice")
	assert.Contains(t, doctor, `profile "desk"`)
	assert.Contains(t, doctor, `profile "laptop"`)

	desk.stop(t)
	assert.Contains(t, f.doctorStyled(t), `and so does profile "laptop"`)
	assert.Contains(t, f.doctorStyled(t), "each Ping is answered twice", "this one and one other")
}

// Doctor on a profile that hears no Pings, beside two others here that do,
// points at those two: restarting this one with --no-pings changes nothing.
// An older one among them is upgraded first, since it can't be told.
func TestDoctorBesideTwoAnswerersPointsAtThem(t *testing.T) {
	for _, older := range []bool{false, true} {
		t.Run(fmt.Sprintf("older=%t", older), func(t *testing.T) {
			f := newOperatorFixture(t)
			setUpSecondProfile(t, f.s, "laptop")
			agent := startProfileConnector(t, f.s, "agent", "--no-pings")
			agent.awaitRunning(t)
			laptop := startProfileConnector(t, f.s, "laptop")
			laptop.awaitRunning(t)
			if older {
				stateHome, err := connectStateHome()
				require.NoError(t, err)
				legacy, err := ensurePrivateChain(stateHome, "basecamp", "connect", connector.LegacyStateDirName(f.file.AccountID, f.file.Agent.PersonID))
				require.NoError(t, err)
				lock, err := connector.AcquireInstanceLock(legacy, f.file.AccountID, f.file.Agent.PersonID, time.Now().Add(-time.Minute))
				require.NoError(t, err)
				t.Cleanup(func() { _ = lock.Release() })
				meta, err := json.Marshal(map[string]any{"pid": os.Getpid(), "started_at": time.Now().Add(time.Minute).UTC().Format(time.RFC3339),
					"account_id": f.file.AccountID, "agent_person_id": f.file.Agent.PersonID})
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(lock.Path()+".json", meta, 0o600), "as an older build writes it: nothing about Pings")
			} else {
				setUpSecondProfile(t, f.s, "desk")
				desk := startProfileConnector(t, f.s, "desk")
				desk.awaitRunning(t)
			}

			doctor := f.doctorStyled(t)
			require.Contains(t, doctor, "each Ping is answered more than once", doctor)
			hint := lineContaining(doctor, "Only one connector should answer Pings")
			assert.Contains(t, hint, "this one doesn't", doctor)
			assert.Contains(t, hint, "restart all but one of the others with --no-pings")
			assert.NotContains(t, hint, "basecamp connect -P agent", "this profile already leaves them to another")
			assert.NotContains(t, hint, "start this one with --no-pings")
			if older {
				assert.Contains(t, hint, "upgrade any older one")
			} else {
				assert.NotContains(t, hint, "older")
			}
		})
	}
}

// A lock file left behind by a connector that has stopped, whose pid now
// belongs to a process that started after it, describes nothing running.
func TestStaleLockMetadataIsNoRunningConnector(t *testing.T) {
	f := newOperatorFixture(t)
	r := startProfileConnector(t, f.s, "agent")
	r.awaitRunning(t)
	r.stop(t)
	dir, err := connectStatePath("agent", f.file, false)
	require.NoError(t, err)
	stale, err := json.Marshal(map[string]any{"pid": os.Getpid(), "started_at": "2020-01-01T00:00:00Z",
		"account_id": f.file.AccountID, "agent_person_id": f.file.Agent.PersonID, "pings": false, "pings_because": pingsBecauseProject})
	require.NoError(t, err)
	holder := filepath.Join(dir, fmt.Sprintf("instance-%s-%d.lock.json", f.file.AccountID, f.file.Agent.PersonID))
	require.NoError(t, os.WriteFile(holder, stale, 0o600))

	styled, err := f.run(t, output.FormatStyled, "status")
	require.NoError(t, err)
	assert.Contains(t, styled, "Pings          heard: the agent's first connection")
	assert.NotContains(t, styled, "the connector running now", "this pid's process started after the lock was taken")
}

// A running connector says what it does even when the profile has saved no
// choice and its credential can't be read, so nothing can say what the next
// start would do: doctor reports the run, and doesn't crash on the rest.
func TestDoctorReportsARunningConnectorWithoutACredential(t *testing.T) {
	f := newOperatorFixture(t)
	r := startProfileConnector(t, f.s, "agent", "--project", fmt.Sprint(setupProject))
	r.awaitRunning(t)
	_, saved := savedPingsChoice(t, f, "agent")
	require.False(t, saved, "a narrowed first run saves nothing")
	require.NoError(t, newConnectSetupApp(t, f.s, "agent").Auth.GetStore().Delete(auth.ProfileCredentialKey("agent")))

	doctor := f.doctorStyled(t)
	assert.Contains(t, doctor, "The connector running now doesn't hear the agent's Pings: --project narrows this run to projects")
}
