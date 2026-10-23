package commands

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/auth"
	"github.com/basecamp/basecamp-cli/internal/config"
	"github.com/basecamp/basecamp-cli/internal/output"
	"github.com/basecamp/basecamp-cli/internal/tui"
)

// connectAsServer is one Basecamp for both answers to "connect this
// computer as": the person's device flow and identity lookups (the login
// identity server, handing out dev-tok), and the agent-connection ceremony
// (intake, poll, and the client_credentials mint), at the paths a pinned
// issuer derives.
type connectAsServer struct {
	*loginIdentityServer

	mu          sync.Mutex
	intakes     int
	deviceAuths int
	mints       int

	// intakeStatus, when non-zero, refuses the intake with this status.
	intakeStatus int
	// pollStatus and pollBody, when set, answer the poll in place of an
	// approval of a full-scope agent in account 999.
	pollStatus int
	pollBody   string
}

func startConnectAsServer(t *testing.T) *connectAsServer {
	t.Helper()
	s := &connectAsServer{loginIdentityServer: startLoginIdentityServer(t, "dev-tok")}
	person := deviceGrantThen(t, s.srv.Config.Handler)
	s.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		s.mu.Lock()
		defer s.mu.Unlock()
		switch r.URL.Path {
		case "/oauth/agent_connections":
			s.intakes++
			if s.intakeStatus != 0 {
				w.WriteHeader(s.intakeStatus)
				return
			}
			fmt.Fprintf(w, `{"device_code":"dev-code-1","user_code":"WDJB-MJHT","verification_uri_complete":%q,"token_uri":%q,"expires_in":600,"interval":1}`,
				s.srv.URL+"/connect?user_code=WDJB-MJHT", s.srv.URL+"/oauth/agent_connection_tokens")
		case "/oauth/agent_connection_tokens":
			if s.pollStatus != 0 {
				w.WriteHeader(s.pollStatus)
				fmt.Fprint(w, s.pollBody)
				return
			}
			fmt.Fprintf(w, `{"client_id":"agent-client","client_secret":%q,"account_id":"999","scope":"full"}`, fakeConnectSecret)
		case "/oauth/tokens":
			require.NoError(t, r.ParseForm())
			if r.PostForm.Get("grant_type") == "client_credentials" {
				s.mints++
				fmt.Fprint(w, `{"access_token":"minted","token_type":"bearer","expires_in":3600,"resource":"urn:bc:agent:42","scope":"full"}`)
				return
			}
			person.ServeHTTP(w, r)
		case "/oauth/device_authorizations":
			s.deviceAuths++
			person.ServeHTTP(w, r)
		default:
			person.ServeHTTP(w, r)
		}
	})
	return s
}

func (s *connectAsServer) counts() (intakes, deviceAuths, mints int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.intakes, s.deviceAuths, s.mints
}

// connectAsApp is an App against the server, with the issuer pinned, no
// host heuristics deciding anything, and a person at the terminal unless
// the test says otherwise.
func connectAsApp(t *testing.T, s *connectAsServer, cfg *config.Config) *appctx.App {
	t.Helper()
	app, _ := loginTestApp(t, s.loginIdentityServer, cfg)
	t.Setenv("BASECAMP_OAUTH_ISSUER", s.srv.URL)
	t.Setenv("BASECAMP_NONINTERACTIVE", "")
	for _, name := range []string{"SSH_CONNECTION", "SSH_CLIENT", "SSH_TTY", "CI"} {
		t.Setenv(name, "")
	}
	t.Setenv("DISPLAY", ":0")
	app.Output = output.New(output.Options{Format: output.FormatStyled, Writer: &bytes.Buffer{}})
	atATerminal(t, true)
	return app
}

// realLoginAsks is the gate itself, kept before any test stands in for it.
var realLoginAsks = loginAsks

// atATerminal says whether a person is at the terminal to answer.
func atATerminal(t *testing.T, yes bool) {
	t.Helper()
	prev := loginAsks
	t.Cleanup(func() { loginAsks = prev })
	loginAsks = func(*appctx.App) bool { return yes }
}

// fakeLoginPrompter answers the login's questions and records them.
type fakeLoginPrompter struct {
	t       *testing.T
	choice  string // what ConnectAs answers; "" fails the test if asked
	confirm bool

	asked     int
	confirmed []string
}

func (f *fakeLoginPrompter) ConnectAs() (string, error) {
	f.asked++
	if f.choice == "" {
		f.t.Error("the login asked who this computer connects as, and must not have")
		return connectAsMe, nil
	}
	return f.choice, nil
}

func (f *fakeLoginPrompter) Confirm(question string, _ bool) (bool, error) {
	f.confirmed = append(f.confirmed, question)
	return f.confirm, nil
}

func answerLogin(t *testing.T, f *fakeLoginPrompter) *fakeLoginPrompter {
	t.Helper()
	f.t = t
	prev := loginAsk
	t.Cleanup(func() { loginAsk = prev })
	loginAsk = f
	return f
}

func runConnectAsLogin(t *testing.T, app *appctx.App, args ...string) (string, error) {
	t.Helper()
	return runLogin(t, app, strings.NewReader(""), append([]string{"--no-browser"}, args...)...)
}

// The agent is asked about first, and a select opens on its first option
// (tui's TestSelectFieldAnsweredWithEnterIsItsFirstOption), so Enter alone
// connects the agent.
func TestAuthLoginOffersTheAgentFirst(t *testing.T) {
	require.Len(t, connectAsOptions, 2)
	assert.Equal(t, connectAsAgent, connectAsOptions[0].Value)
	assert.Contains(t, connectAsOptions[0].Label, "recommended")
	assert.Equal(t, connectAsMe, connectAsOptions[1].Value)
}

func TestParseConnectAs(t *testing.T) {
	for _, ok := range []string{"", "agent", "me"} {
		got, err := parseConnectAs(ok)
		require.NoError(t, err, ok)
		assert.Equal(t, ok, got)
	}
	for _, bad := range []string{"Agent", "ME", "myself", "self", "you", "bot", " agent"} {
		_, err := parseConnectAs(bad)
		var e *output.Error
		require.ErrorAs(t, err, &e, bad)
		assert.Equal(t, output.CodeUsage, e.Code, bad)
		assert.Contains(t, e.Message, "--as", bad)
	}
}

// An unknown --as is refused before anything is asked or requested.
func TestAuthLoginRefusesAnUnknownAs(t *testing.T) {
	s := startConnectAsServer(t)
	app := connectAsApp(t, s, &config.Config{})
	answerLogin(t, &fakeLoginPrompter{})

	_, err := runConnectAsLogin(t, app, "--as", "myself")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `Invalid --as "myself"`)
	assert.Empty(t, s.seenPaths())
	intakes, deviceAuths, _ := s.counts()
	assert.Zero(t, intakes+deviceAuths)
}

// The gate: a question is asked only of a person at a terminal, with no
// machine output mode reading the login and BASECAMP_NONINTERACTIVE unset.
// Off a terminal — go test's own stdin is a pipe here — it never asks.
func TestLoginAsksOnlyAPersonAtATerminal(t *testing.T) {
	t.Setenv("BASECAMP_NONINTERACTIVE", "")
	r, w, err := os.Pipe()
	require.NoError(t, err)
	t.Cleanup(func() { _ = r.Close(); _ = w.Close() })
	prev := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = prev })

	app := &appctx.App{Config: &config.Config{}}
	assert.False(t, realLoginAsks(app), "stdin is a pipe")

	t.Setenv("BASECAMP_NONINTERACTIVE", "1")
	assert.False(t, realLoginAsks(app))
	t.Setenv("BASECAMP_NONINTERACTIVE", "")

	app.Flags.JSON = true
	assert.False(t, realLoginAsks(app))
	app.Flags.JSON = false
	app.Config.Format = "json"
	assert.False(t, realLoginAsks(app), "a configured machine format reads the login too")
}

// Compatibility: a login nobody can answer — a script, CI, no terminal —
// signs the person in exactly as before there was a question. The real gate
// runs here: stdin is a pipe. Nothing is asked, no agent connection is
// requested, and the credential stored is the person's.
func TestAuthLoginWithoutATerminalSignsYouInWithoutAsking(t *testing.T) {
	s := startConnectAsServer(t)
	app := connectAsApp(t, s, &config.Config{})
	loginAsks = func(app *appctx.App) bool {
		r, w, err := os.Pipe()
		require.NoError(t, err)
		defer func() { _ = r.Close(); _ = w.Close() }()
		prev := os.Stdin
		os.Stdin = r
		defer func() { os.Stdin = prev }()
		return realLoginAsks(app)
	}
	answerLogin(t, &fakeLoginPrompter{})

	out, err := runConnectAsLogin(t, app)
	require.NoError(t, err, out)

	assert.Contains(t, out, "Authentication successful")
	assert.NotContains(t, out, "Connect this computer to a Basecamp agent")
	assert.Equal(t, 1, strings.Count(out, "Authenticating via"), "discovery runs once, inside the login, as before")
	intakes, deviceAuths, mints := s.counts()
	assert.Zero(t, intakes, "no agent connection is requested")
	assert.Zero(t, mints)
	assert.Equal(t, 1, deviceAuths)

	creds, err := app.Auth.GetStore().Load(app.Auth.CredentialKey())
	require.NoError(t, err)
	assert.Equal(t, "bc5", creds.OAuthType)
	assert.Equal(t, "dev-tok", creds.AccessToken)
	assert.NotContains(t, readGlobalConfig(t), "profiles", "no agent profile appears")
}

// Enter at the question connects the agent: the agent-connection ceremony
// runs, under a new "agent" profile that, being the only one, becomes the
// default. Discovery runs once, before the question, and the person's
// device flow never starts.
func TestAuthLoginAskedConnectsTheAgent(t *testing.T) {
	s := startConnectAsServer(t)
	app := connectAsApp(t, s, &config.Config{})
	prompter := answerLogin(t, &fakeLoginPrompter{choice: connectAsAgent})

	out, err := runConnectAsLogin(t, app)
	require.NoError(t, err, out)

	assert.Equal(t, 1, prompter.asked)
	assert.Equal(t, 1, strings.Count(out, "Authenticating via"), "the ceremony uses the discovery the question was asked after")
	assert.Contains(t, out, "Connect this computer to a Basecamp agent")
	assert.Contains(t, out, `Connected profile "agent" to a Basecamp agent`)
	assert.Contains(t, out, `Created profile "agent" for account 999 (default)`)
	assert.NotContains(t, out, "It isn't the default profile")
	assert.NotContains(t, out, fakeConnectSecret)

	intakes, deviceAuths, mints := s.counts()
	assert.Equal(t, 1, intakes)
	assert.Equal(t, 1, mints)
	assert.Zero(t, deviceAuths, "the person's device flow never starts")

	creds, err := app.Auth.GetStore().Load("profile:agent")
	require.NoError(t, err)
	assert.Equal(t, "agent", creds.OAuthType)
	assert.Equal(t, fakeConnectSecret, creds.ClientSecret)
	assert.Equal(t, "agent", readGlobalConfig(t)["default_profile"])
}

// Where other profiles exist the agent's is not made the default behind
// the person's back; the login says how.
func TestAuthLoginConnectingTheAgentBesideOtherProfilesSaysHowToMakeItTheDefault(t *testing.T) {
	s := startConnectAsServer(t)
	app := connectAsApp(t, s, &config.Config{})
	require.NoError(t, os.MkdirAll(config.GlobalConfigDir(), 0o700))
	require.NoError(t, os.WriteFile(config.GlobalConfigDir()+"/config.json",
		[]byte(`{"profiles":{"work":{"base_url":"`+s.srv.URL+`","account_id":"999"}},"default_profile":"work"}`), 0o600))
	app.Config.Profiles = map[string]*config.ProfileConfig{"work": {BaseURL: s.srv.URL, AccountID: "999"}}
	app.Config.DefaultProfile = "work"
	app.Config.ActiveProfile = "work"
	answerLogin(t, &fakeLoginPrompter{choice: connectAsAgent})

	out, err := runConnectAsLogin(t, app)
	require.NoError(t, err, out)
	assert.Contains(t, out, `Connected profile "agent" to a Basecamp agent`)
	assert.Contains(t, out, "It isn't the default profile. To make it the default: basecamp profile set-default agent")
	assert.Equal(t, "work", readGlobalConfig(t)["default_profile"])
	_, err = app.Auth.GetStore().Load("profile:work")
	assert.Error(t, err, "the person's profile is left as it was")
}

// Answering "Yourself" is today's login, with the discovery the question
// was asked after.
func TestAuthLoginAskedYourselfSignsYouIn(t *testing.T) {
	s := startConnectAsServer(t)
	app := connectAsApp(t, s, &config.Config{})
	prompter := answerLogin(t, &fakeLoginPrompter{choice: connectAsMe})

	out, err := runConnectAsLogin(t, app)
	require.NoError(t, err, out)

	assert.Equal(t, 1, prompter.asked)
	assert.Equal(t, 1, strings.Count(out, "Authenticating via"))
	assert.Contains(t, out, "Authentication successful")
	intakes, deviceAuths, _ := s.counts()
	assert.Zero(t, intakes)
	assert.Equal(t, 1, deviceAuths)
	creds, err := app.Auth.GetStore().Load(app.Auth.CredentialKey())
	require.NoError(t, err)
	assert.Equal(t, "bc5", creds.OAuthType)
}

// --as answers ahead of time, at a terminal or not.
func TestAuthLoginAsAnswersAheadOfTime(t *testing.T) {
	for _, terminal := range []bool{true, false} {
		t.Run(fmt.Sprintf("agent, terminal %v", terminal), func(t *testing.T) {
			s := startConnectAsServer(t)
			app := connectAsApp(t, s, &config.Config{})
			atATerminal(t, terminal)
			answerLogin(t, &fakeLoginPrompter{})

			out, err := runConnectAsLogin(t, app, "--as", "agent")
			require.NoError(t, err, out)
			assert.Contains(t, out, `Connected profile "agent" to a Basecamp agent`)
			intakes, deviceAuths, _ := s.counts()
			assert.Equal(t, 1, intakes)
			assert.Zero(t, deviceAuths)
		})
		t.Run(fmt.Sprintf("me, terminal %v", terminal), func(t *testing.T) {
			s := startConnectAsServer(t)
			app := connectAsApp(t, s, &config.Config{})
			atATerminal(t, terminal)
			answerLogin(t, &fakeLoginPrompter{})

			out, err := runConnectAsLogin(t, app, "--as", "me")
			require.NoError(t, err, out)
			assert.Contains(t, out, "Authentication successful")
			intakes, deviceAuths, _ := s.counts()
			assert.Zero(t, intakes)
			assert.Equal(t, 1, deviceAuths)
		})
	}
}

// The flags that only mean something for a person: with --as agent they
// are refused before anything is requested; without --as they answer the
// question themselves, and nothing is asked.
func TestAuthLoginPersonOnlyFlags(t *testing.T) {
	for _, args := range [][]string{
		{"--login-hint", "jorge@example.com"},
		{"--expect-identity", "28142355"},
	} {
		t.Run("--as agent "+args[0], func(t *testing.T) {
			s := startConnectAsServer(t)
			app := connectAsApp(t, s, &config.Config{})
			answerLogin(t, &fakeLoginPrompter{})

			_, err := runConnectAsLogin(t, app, append([]string{"--as", "agent"}, args...)...)
			require.Error(t, err)
			assert.Contains(t, err.Error(), args[0])
			intakes, deviceAuths, _ := s.counts()
			assert.Zero(t, intakes+deviceAuths)
		})
		t.Run("no --as "+args[0], func(t *testing.T) {
			s := startConnectAsServer(t)
			app := connectAsApp(t, s, &config.Config{})
			answerLogin(t, &fakeLoginPrompter{})

			out, err := runConnectAsLogin(t, app, args...)
			require.NoError(t, err, out)
			assert.Contains(t, out, "Authentication successful")
			intakes, _, _ := s.counts()
			assert.Zero(t, intakes)
		})
	}

	for _, args := range [][]string{{"--with-token"}, {"--with-client-credentials", "--client-id", "x"}} {
		t.Run("--as with "+args[0], func(t *testing.T) {
			s := startConnectAsServer(t)
			app := connectAsApp(t, s, &config.Config{})
			_, err := runLogin(t, app, strings.NewReader(""), append([]string{"--as", "me"}, args...)...)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "none of the others can be")
		})
	}
}

// Signing a profile that holds the person's own login back in is not a new
// connection, and asks nothing: the remedies that name `auth login -P <p>`
// for an expired login still do what they say.
func TestAuthLoginSigningYourOwnProfileBackInAsksNothing(t *testing.T) {
	s := startConnectAsServer(t)
	app := connectAsApp(t, s, &config.Config{ActiveProfile: "bot", Profiles: map[string]*config.ProfileConfig{"bot": {AccountID: "999"}}})
	withAccount(app, "999", "profile")
	require.NoError(t, app.Auth.GetStore().Save("profile:bot", &auth.Credentials{AccessToken: "old", OAuthType: "bc5"}))
	answerLogin(t, &fakeLoginPrompter{})

	out, err := runConnectAsLogin(t, app)
	require.NoError(t, err, out)
	assert.Contains(t, out, "Authentication successful")
	creds, err := app.Auth.GetStore().Load("profile:bot")
	require.NoError(t, err)
	assert.Equal(t, "dev-tok", creds.AccessToken)
	intakes, _, _ := s.counts()
	assert.Zero(t, intakes)
}

// A profile holding a person's login is never turned into an agent's.
func TestAuthLoginAsAgentRefusesAProfileHoldingYourLogin(t *testing.T) {
	s := startConnectAsServer(t)
	app := connectAsApp(t, s, &config.Config{ActiveProfile: "bot", Profiles: map[string]*config.ProfileConfig{"bot": {AccountID: "999"}}})
	app.Flags.Profile = "bot"
	require.NoError(t, app.Auth.GetStore().Save("profile:bot", &auth.Credentials{AccessToken: "old", OAuthType: "bc5"}))

	_, err := runConnectAsLogin(t, app, "--as", "agent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `Profile "bot" holds a person's login, not an agent's`)
	intakes, _, _ := s.counts()
	assert.Zero(t, intakes, "refused before anyone is asked to approve")
	creds, err := app.Auth.GetStore().Load("profile:bot")
	require.NoError(t, err)
	assert.Equal(t, "old", creds.AccessToken)
}

// Reconnecting a profile that holds an agent already says that approving
// replaces it.
func TestAuthLoginAsAgentSaysWhenItReplacesAnAgentConnection(t *testing.T) {
	s := startConnectAsServer(t)
	app := connectAsApp(t, s, &config.Config{})
	require.NoError(t, os.MkdirAll(config.GlobalConfigDir(), 0o700))
	require.NoError(t, os.WriteFile(config.GlobalConfigDir()+"/config.json",
		[]byte(`{"profiles":{"agent":{"base_url":"`+s.srv.URL+`","account_id":"999"}},"default_profile":"agent"}`), 0o600))
	app.Config.Profiles = map[string]*config.ProfileConfig{"agent": {BaseURL: s.srv.URL, AccountID: "999"}}
	require.NoError(t, app.Auth.GetStore().Save("profile:agent", &auth.Credentials{OAuthType: "agent", ClientID: "old-client", ClientSecret: "old"}))

	out, err := runConnectAsLogin(t, app, "--as", "agent")
	require.NoError(t, err, out)
	assert.Contains(t, out, `Profile "agent" already holds an agent connection. Approving replaces the credential stored here.`)
	creds, err := app.Auth.GetStore().Load("profile:agent")
	require.NoError(t, err)
	assert.Equal(t, "agent-client", creds.ClientID)
}

// Launchpad has no agents: nothing is asked, and --as agent is refused
// before anything is requested.
func TestAuthLoginOnLaunchpad(t *testing.T) {
	s := startConnectAsServer(t)
	app := connectAsApp(t, s, &config.Config{})
	// No pinned issuer and no protected-resource metadata: Launchpad, at
	// this server.
	t.Setenv("BASECAMP_OAUTH_ISSUER", "")
	t.Setenv("BASECAMP_LAUNCHPAD_URL", s.srv.URL)
	devNull, err := os.Open(os.DevNull)
	require.NoError(t, err)
	t.Cleanup(func() { _ = devNull.Close() })
	prev := os.Stdin
	os.Stdin = devNull
	t.Cleanup(func() { os.Stdin = prev })
	prompter := answerLogin(t, &fakeLoginPrompter{})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := NewAuthCmd()
	cmd.SetArgs([]string{"login", "--device-code"})
	cmd.SetContext(appctx.WithApp(ctx, app))
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	err = cmd.Execute()
	require.Error(t, err, "the Launchpad paste prompt reads end of input")
	assert.Contains(t, out.String(), "Authenticating via launchpad")
	assert.Zero(t, prompter.asked)

	_, err = runConnectAsLogin(t, app, "--as", "agent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no agent grant")
	intakes, _, _ := s.counts()
	assert.Zero(t, intakes)
}

// When Basecamp connects no agent — the operator declined, or its page
// told them agents aren't available to them and they acknowledged it (the
// poll answers access_denied either way), or this Basecamp isn't issuing
// connections — a person at the terminal is offered to connect as
// themselves, and saying yes runs the person's login, under the profile
// they'd have had without the detour.
func TestAuthLoginWithNoAgentOffersToConnectYouInstead(t *testing.T) {
	for name, refuse := range map[string]func(*connectAsServer){
		"declined or unavailable": func(s *connectAsServer) { s.pollStatus, s.pollBody = 400, `{"error":"access_denied"}` },
		"not issuing":             func(s *connectAsServer) { s.intakeStatus = http.StatusServiceUnavailable },
	} {
		t.Run(name, func(t *testing.T) {
			s := startConnectAsServer(t)
			refuse(s)
			app := connectAsApp(t, s, &config.Config{})
			prompter := answerLogin(t, &fakeLoginPrompter{choice: connectAsAgent, confirm: true})

			out, err := runConnectAsLogin(t, app)
			require.NoError(t, err, out)

			assert.Contains(t, out, "No agent was connected: the connection was declined, or agents aren't available to you yet.")
			assert.Equal(t, []string{"Connect as yourself instead?"}, prompter.confirmed)
			assert.Contains(t, out, "Authentication successful")
			assert.Equal(t, "", app.Config.ActiveProfile, "the login lands where it would have without the agent")
			creds, err := app.Auth.GetStore().Load(app.Auth.CredentialKey())
			require.NoError(t, err)
			assert.Equal(t, "bc5", creds.OAuthType)
			_, err = app.Auth.GetStore().Load("profile:agent")
			assert.Error(t, err)
			assert.NotContains(t, readGlobalConfig(t), "profiles")
			_, deviceAuths, mints := s.counts()
			assert.Equal(t, 1, deviceAuths)
			assert.Zero(t, mints)
		})
	}
}

// Saying no to the offer stores nothing and says how to connect yourself.
func TestAuthLoginWithNoAgentDecliningTheOfferStoresNothing(t *testing.T) {
	s := startConnectAsServer(t)
	s.pollStatus, s.pollBody = 400, `{"error":"access_denied"}`
	app := connectAsApp(t, s, &config.Config{})
	answerLogin(t, &fakeLoginPrompter{choice: connectAsAgent, confirm: false})

	_, err := runConnectAsLogin(t, app)
	assertNoAgentRefusal(t, err)
	_, deviceAuths, _ := s.counts()
	assert.Zero(t, deviceAuths)
	_, err = app.Auth.GetStore().Load(app.Auth.CredentialKey())
	assert.Error(t, err)
}

// With nobody at the terminal, --as agent that connects no agent fails
// with the message and the remedy, and offers nothing.
func TestAuthLoginAsAgentWithNoAgentOffTheTerminalFails(t *testing.T) {
	s := startConnectAsServer(t)
	s.pollStatus, s.pollBody = 400, `{"error":"access_denied"}`
	app := connectAsApp(t, s, &config.Config{})
	atATerminal(t, false)
	prompter := answerLogin(t, &fakeLoginPrompter{})

	_, err := runConnectAsLogin(t, app, "--as", "agent")
	assertNoAgentRefusal(t, err)
	assert.Empty(t, prompter.confirmed)
	_, deviceAuths, _ := s.counts()
	assert.Zero(t, deviceAuths)
}

func assertNoAgentRefusal(t *testing.T, err error) {
	t.Helper()
	var e *output.Error
	require.ErrorAs(t, err, &e)
	assert.Equal(t, output.CodeAuth, e.Code)
	assert.Equal(t, "No agent was connected: the connection was declined, or agents aren't available to you yet", e.Message)
	assert.Equal(t, "Connect as yourself instead: basecamp auth login --as me", e.Hint)
	assert.ErrorIs(t, err, auth.ErrNoAgentConnection)
}

// Escape or Ctrl-C at the question is a canceled login.
func TestAuthLoginCanceledAtTheQuestionStoresNothing(t *testing.T) {
	s := startConnectAsServer(t)
	app := connectAsApp(t, s, &config.Config{})
	prev := loginAsk
	t.Cleanup(func() { loginAsk = prev })
	loginAsk = cancelingPrompter{}

	out, err := runConnectAsLogin(t, app)
	var e *output.Error
	require.ErrorAs(t, err, &e)
	assert.Equal(t, output.CodeInterrupted, e.Code)
	assert.Contains(t, out, "Login canceled. Nothing was stored.")
	intakes, deviceAuths, _ := s.counts()
	assert.Zero(t, intakes+deviceAuths)
}

type cancelingPrompter struct{}

func (cancelingPrompter) ConnectAs() (string, error)         { return "", tui.ErrCanceled }
func (cancelingPrompter) Confirm(string, bool) (bool, error) { return false, tui.ErrCanceled }

// --as agent under --json keeps the ceremony's contract: two JSON values on
// stdout, the verification and the result, and the person's words on
// stderr. Without --as, --json still refuses the login, as it always has.
func TestAuthLoginAsAgentJSONKeepsTheCeremonyContract(t *testing.T) {
	s := startConnectAsServer(t)
	app := connectAsApp(t, s, &config.Config{})
	app.Flags.JSON = true
	stdout := &lockedBuffer{}
	app.Output = output.New(output.Options{Format: output.FormatJSON, Writer: stdout})

	cmd := NewAuthCmd()
	cmd.SetArgs([]string{"login", "--no-browser", "--as", "agent"})
	cmd.SetContext(appctx.WithApp(context.Background(), app))
	var stderr bytes.Buffer
	cmd.SetOut(stdout)
	cmd.SetErr(&stderr)
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	require.NoError(t, cmd.Execute(), stderr.String())

	lines := jsonValues(t, stdout.String())
	require.Len(t, lines, 2)
	assert.Equal(t, "verification", lines[0]["type"])
	data, ok := lines[1]["data"].(map[string]any)
	require.True(t, ok, stdout.String())
	assert.Equal(t, "agent", data["profile"])
	assert.Equal(t, "agent_connection", data["source"])
	assert.Contains(t, stderr.String(), "WDJB-MJHT")
	assert.NotContains(t, stderr.String(), "It isn't the default profile")
	for _, stream := range []string{stdout.String(), stderr.String()} {
		assert.NotContains(t, stream, fakeConnectSecret)
	}

	_, err := runLogin(t, app, strings.NewReader(""), "--no-browser")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "machine output mode")
}

// At a terminal, `auth login -P <new>` asks before refusing the profile
// that doesn't exist yet: the agent creates it, and only yourself needs it
// to exist already.
func TestAuthLoginAsksBeforeRefusingANewProfile(t *testing.T) {
	t.Run("agent creates it", func(t *testing.T) {
		s := startConnectAsServer(t)
		app := connectAsApp(t, s, &config.Config{ActiveProfile: "new-agent"})
		app.Flags.Profile = "new-agent"
		prompter := answerLogin(t, &fakeLoginPrompter{choice: connectAsAgent})

		out, err := runConnectAsLogin(t, app)
		require.NoError(t, err, out)
		assert.Equal(t, 1, prompter.asked)
		assert.Contains(t, out, `Connected profile "new-agent" to a Basecamp agent`)
		creds, err := app.Auth.GetStore().Load("profile:new-agent")
		require.NoError(t, err)
		assert.Equal(t, "agent", creds.OAuthType)
	})
	t.Run("yourself still needs it", func(t *testing.T) {
		s := startConnectAsServer(t)
		app := connectAsApp(t, s, &config.Config{ActiveProfile: "new-agent"})
		app.Flags.Profile = "new-agent"
		prompter := answerLogin(t, &fakeLoginPrompter{choice: connectAsMe})

		_, err := runConnectAsLogin(t, app)
		require.Error(t, err)
		assert.Contains(t, err.Error(), `Profile "new-agent" does not exist`)
		assert.Equal(t, 1, prompter.asked)
		intakes, deviceAuths, _ := s.counts()
		assert.Zero(t, intakes+deviceAuths)
	})
	t.Run("nobody to ask", func(t *testing.T) {
		s := startConnectAsServer(t)
		app := connectAsApp(t, s, &config.Config{ActiveProfile: "new-agent"})
		atATerminal(t, false)
		answerLogin(t, &fakeLoginPrompter{})

		_, err := runConnectAsLogin(t, app)
		require.Error(t, err)
		assert.Contains(t, err.Error(), `Profile "new-agent" does not exist`)
		assert.Empty(t, s.seenPaths(), "refused before any request, as before")
	})
}

// Asking means discovering for the base URL the login started on, which
// also fixes this process's OAuth client to it. When choosing the agent
// would move the login onto a profile bound to another Basecamp, it is
// refused before anything is requested, naming the command that starts
// there. --as agent asks nothing and discovers nothing first, so it
// connects that profile on its own Basecamp.
func TestAuthLoginRefusesAnAgentProfileOnAnotherBasecampAfterAsking(t *testing.T) {
	setup := func(t *testing.T) (*connectAsServer, *appctx.App, string) {
		s := startConnectAsServer(t)
		app := connectAsApp(t, s, &config.Config{})
		other := strings.Replace(s.srv.URL, "127.0.0.1", "localhost", 1)
		require.NotEqual(t, s.srv.URL, other)
		require.NoError(t, os.MkdirAll(config.GlobalConfigDir(), 0o700))
		require.NoError(t, os.WriteFile(config.GlobalConfigDir()+"/config.json",
			[]byte(`{"profiles":{"agent":{"base_url":"`+other+`","account_id":"999"}}}`), 0o600))
		app.Config.Profiles = map[string]*config.ProfileConfig{"agent": {BaseURL: other, AccountID: "999"}}
		return s, app, other
	}

	t.Run("asked", func(t *testing.T) {
		s, app, other := setup(t)
		answerLogin(t, &fakeLoginPrompter{choice: connectAsAgent})

		_, err := runConnectAsLogin(t, app)
		var e *output.Error
		require.ErrorAs(t, err, &e)
		assert.Equal(t, fmt.Sprintf("Profile %q is on %s, not %s where this login started", "agent", other, s.srv.URL), e.Message)
		assert.Equal(t, "Connect your agent there: basecamp auth login --as agent -P agent", e.Hint)
		intakes, deviceAuths, _ := s.counts()
		assert.Zero(t, intakes+deviceAuths)
	})
	t.Run("--as agent", func(t *testing.T) {
		_, app, _ := setup(t)
		answerLogin(t, &fakeLoginPrompter{})

		out, err := runConnectAsLogin(t, app, "--as", "agent")
		require.NoError(t, err, out)
		assert.Contains(t, out, `Connected profile "agent" to a Basecamp agent`)
	})
	// Having talked to the agent profile's Basecamp, this process can't
	// sign the person in on the first one: no offer, just the refusal and
	// its remedy, which runs in a process of its own.
	t.Run("--as agent, no agent", func(t *testing.T) {
		s, app, _ := setup(t)
		s.pollStatus, s.pollBody = 400, `{"error":"access_denied"}`
		prompter := answerLogin(t, &fakeLoginPrompter{confirm: true})

		_, err := runConnectAsLogin(t, app, "--as", "agent")
		assertNoAgentRefusal(t, err)
		assert.Empty(t, prompter.confirmed)
		_, deviceAuths, _ := s.counts()
		assert.Zero(t, deviceAuths)
	})
}
