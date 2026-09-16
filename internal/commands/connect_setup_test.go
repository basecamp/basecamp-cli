//go:build unix

package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/auth"
	"github.com/basecamp/basecamp-cli/internal/config"
	"github.com/basecamp/basecamp-cli/internal/connector/admission"
	"github.com/basecamp/basecamp-cli/internal/connector/setup"
	"github.com/basecamp/basecamp-cli/internal/output"
)

const (
	setupAgentPerson    int64 = 52007412
	setupOperatorPerson int64 = 26909558
	setupBotPerson      int64 = 51177542
	setupBotIdentity    int64 = 4242
	setupProject        int64 = 48699913
	setupClientPerson   int64 = 1003

	// Tokens the mock server tells apart. None is shaped like a real one.
	setupAgentToken    = "minted"
	setupOperatorToken = "operator-token"
	setupBotToken      = "bot-token"
	setupTicket        = "not-a-real-ticket"
)

// connectSetupServer is Basecamp for connect setup: the connection ceremony, and
// the account reads setup makes, answered per bearer.
type connectSetupServer struct {
	srv *httptest.Server

	mu      sync.Mutex
	intakes int
	paths   []string

	// agentID is who the agent's token reads back as.
	agentID int64
	// refuseAgentReads answers the project reads with 403 for an Agent token,
	// as bc3 does today.
	refuseAgentReads bool
	// refusePeople answers the people read with 403.
	refusePeople bool
	// agentAsUser answers the agent's identity read as a person, not an Agent.
	agentAsUser bool
	// grantScope is the scope the connection hands over; "" means full.
	grantScope string
	// duringMint runs when the stream ticket is minted, between setup's
	// identity read and its write, as another process's change would land.
	duringMint func()
	// mintFailure, when set, answers the stream ticket mint.
	mintFailure func(w http.ResponseWriter)
}

func startConnectSetupServer(t *testing.T) *connectSetupServer {
	t.Helper()
	s := &connectSetupServer{agentID: setupAgentPerson}
	mux := http.NewServeMux()
	bearer := func(r *http.Request) string { return strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ") }
	writeJSON := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(v))
	}

	mux.HandleFunc("/oauth/agent_connections", func(w http.ResponseWriter, _ *http.Request) {
		s.mu.Lock()
		s.intakes++
		s.mu.Unlock()
		writeJSON(w, map[string]any{
			"device_code": "dev-code-1", "user_code": "WDJB-MJHT",
			"verification_uri_complete": s.srv.URL + "/connect?user_code=WDJB-MJHT",
			"token_uri":                 s.srv.URL + "/oauth/agent_connection_tokens",
			"expires_in":                600, "interval": 1,
		})
	})
	mux.HandleFunc("/oauth/agent_connection_tokens", func(w http.ResponseWriter, _ *http.Request) {
		scope := s.grantScope
		if scope == "" {
			scope = "full"
		}
		writeJSON(w, map[string]any{"client_id": "agent-client", "client_secret": fakeConnectSecret, "account_id": "999", "scope": scope})
	})
	mux.HandleFunc("/oauth/tokens", func(w http.ResponseWriter, _ *http.Request) {
		scope := s.grantScope
		if scope == "" {
			scope = "full"
		}
		writeJSON(w, map[string]any{"access_token": setupAgentToken, "token_type": "bearer", "expires_in": 3600, "resource": "urn:bc:agent:42", "scope": scope})
	})
	mux.HandleFunc("/authorization.json", func(w http.ResponseWriter, r *http.Request) {
		if bearer(r) != setupBotToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		writeJSON(w, map[string]any{
			"identity": map[string]any{"id": setupBotIdentity, "first_name": "Bot", "last_name": "User"},
			"accounts": []map[string]any{{"id": 999, "name": "Acme", "href": s.srv.URL + "/999", "product": "bc3"}},
		})
	})
	mux.HandleFunc("/999/my/profile.json", func(w http.ResponseWriter, r *http.Request) {
		switch bearer(r) {
		case setupAgentToken:
			personable := "Agent"
			if s.agentAsUser {
				personable = "User"
			}
			writeJSON(w, map[string]any{"id": s.agentID, "name": "Marie Chef", "personable_type": personable})
		case setupOperatorToken:
			writeJSON(w, map[string]any{"id": setupOperatorPerson, "name": "Operator", "personable_type": "User"})
		case setupBotToken:
			writeJSON(w, map[string]any{"id": setupBotPerson, "name": "Bot", "personable_type": "User"})
		default:
			w.WriteHeader(http.StatusUnauthorized)
		}
	})
	mux.HandleFunc(fmt.Sprintf("/999/people/%d", setupOperatorPerson), func(w http.ResponseWriter, _ *http.Request) {
		if s.refusePeople {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		writeJSON(w, map[string]any{"id": setupOperatorPerson, "name": "Operator", "personable_type": "User"})
	})
	mux.HandleFunc(fmt.Sprintf("/999/people/%d", setupOperatorPerson+1), func(w http.ResponseWriter, r *http.Request) {
		if bearer(r) != setupOperatorToken {
			w.WriteHeader(http.StatusForbidden) // as bc3 refuses an Agent today
			return
		}
		writeJSON(w, map[string]any{"id": setupOperatorPerson + 1, "name": "Colleague", "personable_type": "User"})
	})
	mux.HandleFunc(fmt.Sprintf("/999/people/%d", setupAgentPerson), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"id": setupAgentPerson, "name": "Marie Chef", "personable_type": "Agent"})
	})
	mux.HandleFunc(fmt.Sprintf("/999/people/%d", setupClientPerson), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"id": setupClientPerson, "name": "Client", "personable_type": "User", "client": true})
	})
	mux.HandleFunc("/999/events/stream_ticket.json", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if s.duringMint != nil {
			s.duringMint()
		}
		if s.mintFailure != nil {
			s.mintFailure(w)
			return
		}
		writeJSON(w, map[string]any{"ticket": setupTicket, "expires_in": 120, "url": "wss://example.test/cable?ticket=" + setupTicket})
	})
	projectRead := func(w http.ResponseWriter, r *http.Request, body any) {
		if s.refuseAgentReads && bearer(r) == setupAgentToken {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		writeJSON(w, body)
	}
	mux.HandleFunc(fmt.Sprintf("/999/projects/%d", setupProject), func(w http.ResponseWriter, r *http.Request) {
		projectRead(w, r, map[string]any{"id": setupProject, "name": "Connector"})
	})
	mux.HandleFunc(fmt.Sprintf("/999/projects/%d/people.json", setupProject), func(w http.ResponseWriter, r *http.Request) {
		projectRead(w, r, []any{})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.paths = append(s.paths, r.Method+" "+r.URL.Path)
		s.mu.Unlock()
		http.NotFound(w, r)
	})

	s.srv = httptest.NewServer(mux)
	t.Cleanup(s.srv.Close)
	return s
}

func (s *connectSetupServer) intakeCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.intakes
}

// bareSetupApp is an App for connect setup with nothing connected yet: a
// file credential store and the global config under a temp XDG_CONFIG_HOME,
// and the issuer pinned to the mock server.
func bareSetupApp(t *testing.T, s *connectSetupServer, profile string) *appctx.App {
	t.Helper()
	t.Setenv("BASECAMP_NO_KEYRING", "1")
	t.Setenv("BASECAMP_TOKEN", "")
	t.Setenv("BASECAMP_NONINTERACTIVE", "")
	t.Setenv("BASECAMP_OAUTH_ISSUER", s.srv.URL)
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	return newConnectSetupApp(t, s, profile)
}

// connectSetupApp is bareSetupApp with the profile already holding the
// agent's credential when it is "agent": connected the way an operator
// connects it, with `basecamp auth agent connect`.
func connectSetupApp(t *testing.T, s *connectSetupServer, profile string) *appctx.App {
	t.Helper()
	app := bareSetupApp(t, s, profile)
	if profile != "agent" {
		return app
	}
	out, err := runAgentConnect(t, app)
	require.NoError(t, err, out)
	return newConnectSetupApp(t, s, profile)
}

// newConnectSetupApp builds an App over the environment already set, as a second
// invocation of the CLI would: from the global config file on disk.
func newConnectSetupApp(t *testing.T, s *connectSetupServer, profile string) *appctx.App {
	t.Helper()
	cfg, err := config.Load(config.FlagOverrides{})
	require.NoError(t, err)
	cfg.BaseURL = s.srv.URL
	if _, ok := cfg.Profiles[profile]; ok {
		require.NoError(t, cfg.ApplyProfile(profile))
	} else {
		cfg.ActiveProfile = profile
	}
	authMgr := auth.NewManager(cfg, s.srv.Client())
	authMgr.SetStore(auth.NewStore(config.GlobalConfigDir()))
	return &appctx.App{
		Config: cfg,
		Auth:   authMgr,
		Output: output.New(output.Options{Format: output.FormatStyled, Writer: &bytes.Buffer{}}),
	}
}

func runConnectSetupCmd(t *testing.T, app *appctx.App, args ...string) (string, error) {
	t.Helper()
	cmd := NewConnectCmd()
	cmd.SetArgs(append([]string{"setup"}, args...))
	cmd.SetContext(appctx.WithApp(context.Background(), app))
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	err := cmd.Execute()
	return out.String(), err
}

// storeConnectProfile registers a profile in the global config and stores a
// person's token under it, as a completed login would have.
func storeConnectProfile(t *testing.T, s *connectSetupServer, name, token string) {
	t.Helper()
	storeConnectProfileScoped(t, s, name, token, "full")
}

func storeConnectProfileScoped(t *testing.T, s *connectSetupServer, name, token, scope string) {
	t.Helper()
	_, err := registerProfile(name, &config.ProfileConfig{BaseURL: s.srv.URL, AccountID: "999", Scope: scope})
	require.NoError(t, err)
	cfg := config.Default()
	cfg.BaseURL = s.srv.URL
	cfg.ActiveProfile = name
	mgr := auth.NewManager(cfg, s.srv.Client())
	mgr.SetStore(auth.NewStore(config.GlobalConfigDir()))
	require.NoError(t, mgr.ImportToken(context.Background(), token, scope, "", "", time.Now().Add(24*time.Hour)))
}

// routeArg routes the test project to a fresh directory.
func routeArg(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf("--route=%d=%s", setupProject, t.TempDir())
}

// firstSetup runs a successful first setup of the agent profile.
func firstSetup(t *testing.T, s *connectSetupServer) {
	t.Helper()
	out, err := runConnectSetupCmd(t, connectSetupApp(t, s, "agent"), "--operator", fmt.Sprint(setupOperatorPerson), routeArg(t))
	require.NoError(t, err, out)
}

func assertNotWritten(t *testing.T, profile string) {
	t.Helper()
	_, statErr := os.Stat(connectSetupPath(t, profile))
	assert.True(t, os.IsNotExist(statErr), "nothing was written")
}

func connectSetupPath(t *testing.T, profile string) string {
	t.Helper()
	path, err := setup.Path(config.GlobalConfigDir(), profile)
	require.NoError(t, err)
	return path
}

// On a profile connected to the agent, setup leaves a connect.json with a
// routed project that admission reads, and passing token, identity and mint
// checks.
func TestConnectSetupOnAConnectedProfile(t *testing.T) {
	s := startConnectSetupServer(t)
	app := connectSetupApp(t, s, "agent")
	repo := t.TempDir()

	out, err := runConnectSetupCmd(t, app,
		"--operator", fmt.Sprint(setupOperatorPerson),
		"--route", fmt.Sprintf("%d=%s", setupProject, repo),
		"--watch-completions", fmt.Sprint(setupProject),
		"--class", fmt.Sprintf("%d=internal", setupProject))
	require.NoError(t, err, out)

	assert.NotContains(t, out, fakeConnectSecret, "the client secret is never echoed")
	assert.NotContains(t, out, setupTicket, "the stream ticket is never echoed")
	assert.NotContains(t, out, "not ready")
	for _, check := range []string{"Token", "Identity", "Operator", "Stream ticket", fmt.Sprintf("Project %d", setupProject)} {
		assert.Contains(t, out, check)
	}

	data, err := os.ReadFile(connectSetupPath(t, "agent"))
	require.NoError(t, err)
	p, err := admission.ParsePolicy(data)
	require.NoError(t, err)
	p.AgentID = setupAgentPerson
	require.NoError(t, p.Validate())
	assert.Equal(t, setupOperatorPerson, p.Trust.OperatorID)
	realRepo, err := filepath.EvalSymlinks(repo)
	require.NoError(t, err)
	assert.Equal(t, admission.Route{Path: realRepo, Class: "internal", WatchCompletions: true}, p.Projects[setupProject])

	f, err := setup.Load(connectSetupPath(t, "agent"))
	require.NoError(t, err)
	assert.Equal(t, setup.Agent{PersonID: setupAgentPerson, Kind: setup.KindAgent}, f.Agent)
	assert.Equal(t, "999", f.AccountID)
	assert.NotContains(t, string(data), fakeConnectSecret, "connect.json holds no credential")

	// A second run uses the connected profile as it is, and keeps what it
	// is not told to change.
	app = newConnectSetupApp(t, s, "agent")
	out, err = runConnectSetupCmd(t, app, "--concurrency", "4")
	require.NoError(t, err, out)
	again, err := setup.Load(connectSetupPath(t, "agent"))
	require.NoError(t, err)
	assert.Equal(t, 4, again.Concurrency)
	assert.Equal(t, f.Projects, again.Projects)
	assert.Equal(t, f.Trust, again.Trust)
}

// bc3 refuses admission's reads to an Agent identity today. Setup says so
// in words, and fails: a connector that would block every event is not
// ready, and the exit status says so to a script as the output does to a
// person.
func TestConnectSetupNamesTheAgentReadRefusal(t *testing.T) {
	s := startConnectSetupServer(t)
	s.refuseAgentReads = true
	app := connectSetupApp(t, s, "agent")

	out, err := runConnectSetupCmd(t, app, "--operator", fmt.Sprint(setupOperatorPerson), routeArg(t))
	require.Error(t, err, out)
	var apiErr *output.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, "not_ready", apiErr.Code)
	assert.Contains(t, apiErr.Message, "Agent identity")
	assert.Contains(t, apiErr.Hint, "--expect-identity")
	assert.Contains(t, out, "Agent identity", "the checks are still shown")
	assertNotWritten(t, "agent")
}

func TestConnectSetupWithNoRouteIsNotReady(t *testing.T) {
	s := startConnectSetupServer(t)
	out, err := runConnectSetupCmd(t, connectSetupApp(t, s, "agent"), "--operator", fmt.Sprint(setupOperatorPerson))
	require.Error(t, err, out)
	var apiErr *output.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, "not_ready", apiErr.Code)
}

// Bad input is refused before anything is read or written.
func TestConnectSetupRefusesBadInput(t *testing.T) {
	op := fmt.Sprint(setupOperatorPerson)
	wantMessage := map[string]string{
		"operator profile unlogged": "holds no credential",
		"missing operator profile":  "does not exist",
		"no operator":               "who the operator is",
		"expect-identity on agent":  "holds an Agent's credential",
	}
	for name, args := range map[string][]string{
		"missing route directory":   {"--operator", op, "--route", fmt.Sprintf("%d=/does/not/exist", setupProject)},
		"class without a route":     {"--operator", op, "--class", fmt.Sprintf("%d=internal", setupProject)},
		"bad trust mode":            {"--operator", op, "--trust", "domain"},
		"allow outside allowlist":   {"--operator", op, "--trust", "project", "--allow", "7"},
		"bad driver":                {"--operator", op, "--driver", "fork"},
		"bad concurrency":           {"--operator", op, "--concurrency", "100"},
		"zero concurrency":          {"--operator", op, "--concurrency", "0"},
		"bad deadline":              {"--operator", op, "--deadline", "5s"},
		"zero deadline":             {"--operator", op, "--deadline", "0"},
		"malformed route":           {"--operator", op, "--route", "not-a-pair"},
		"no operator":               {"--route", fmt.Sprintf("%d=%s", setupProject, os.TempDir())},
		"missing operator profile":  {"--operator-profile", "nobody"},
		"operator profile unlogged": {"--operator-profile", "unlogged"},
		"expect-identity on agent":  {"--operator", op, "--expect-identity", "4242"},
	} {
		t.Run(name, func(t *testing.T) {
			s := startConnectSetupServer(t)
			connectSetupApp(t, s, "agent")
			_, err := registerProfile("unlogged", &config.ProfileConfig{BaseURL: s.srv.URL, AccountID: "999"})
			require.NoError(t, err)
			out, err := runConnectSetupCmd(t, newConnectSetupApp(t, s, "agent"), args...)
			require.Error(t, err, out)
			assertNotWritten(t, "agent")
			if want, ok := wantMessage[name]; ok {
				assert.Contains(t, err.Error(), want)
			}
		})
	}
}

// Setup does not obtain a credential: a profile without one is refused
// with the command that connects it, and nothing is requested or written.
func TestConnectSetupRefusesAProfileWithNoCredential(t *testing.T) {
	s := startConnectSetupServer(t)
	bareSetupApp(t, s, "agent")
	_, err := registerProfile("agent", &config.ProfileConfig{BaseURL: s.srv.URL, AccountID: "999"})
	require.NoError(t, err)

	out, err := runConnectSetupCmd(t, newConnectSetupApp(t, s, "agent"), "--operator", fmt.Sprint(setupOperatorPerson), routeArg(t))
	require.Error(t, err, out)
	var apiErr *output.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, output.CodeAuth, apiErr.Code)
	assert.Contains(t, apiErr.Hint, "basecamp auth agent connect -P agent")
	assert.Zero(t, s.intakeCount(), "setup never starts a connection")
	assertNotWritten(t, "agent")

	out, err = runConnectSetupCmd(t, newConnectSetupApp(t, s, "agent"), "--operator", fmt.Sprint(setupOperatorPerson), "--expect-identity", "4242", routeArg(t))
	require.Error(t, err, out)
	require.ErrorAs(t, err, &apiErr)
	assert.Contains(t, apiErr.Hint, "basecamp auth login -P agent --expect-identity 4242")
}

// Setup works in the account the agent's profile is bound to: never a
// config-wide default, and an explicit --account naming another account is
// refused rather than preferred.
func TestConnectSetupWorksInTheProfilesOwnAccount(t *testing.T) {
	s := startConnectSetupServer(t)
	connectSetupApp(t, s, "agent")

	app := newConnectSetupApp(t, s, "agent")
	app.Config.AccountID = "1000" // a config-wide default, not the profile's
	app.Config.Sources["account_id"] = string(config.SourceGlobal)
	out, err := runConnectSetupCmd(t, app, "--operator", fmt.Sprint(setupOperatorPerson), routeArg(t))
	require.NoError(t, err, out)
	f, err := setup.Load(connectSetupPath(t, "agent"))
	require.NoError(t, err)
	assert.Equal(t, "999", f.AccountID)

	app = newConnectSetupApp(t, s, "agent")
	app.Config.AccountID = "1000"
	app.Config.Sources["account_id"] = string(config.SourceFlag)
	out, err = runConnectSetupCmd(t, app, "--concurrency", "3")
	require.Error(t, err, out)
	assert.Contains(t, err.Error(), "bound to account 999")
}

// A credential that reads back no person id is not the agent.
func TestConnectSetupRefusesAZeroPersonID(t *testing.T) {
	s := startConnectSetupServer(t)
	connectSetupApp(t, s, "agent")
	s.agentID = 0
	out, err := runConnectSetupCmd(t, newConnectSetupApp(t, s, "agent"), "--operator", fmt.Sprint(setupOperatorPerson), routeArg(t))
	require.Error(t, err, out)
	var apiErr *output.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, output.CodeAuth, apiErr.Code)
	assert.Contains(t, apiErr.Message, "no person id")
	assertNotWritten(t, "agent")
}

// Every setting setup writes can be reversed by setup: a class is cleared
// with <project-id>=.
func TestConnectSetupClearsAClass(t *testing.T) {
	s := startConnectSetupServer(t)
	out, err := runConnectSetupCmd(t, connectSetupApp(t, s, "agent"), "--operator", fmt.Sprint(setupOperatorPerson), routeArg(t),
		"--class", fmt.Sprintf("%d=internal", setupProject))
	require.NoError(t, err, out)

	out, err = runConnectSetupCmd(t, newConnectSetupApp(t, s, "agent"), "--class", fmt.Sprintf("%d=", setupProject))
	require.NoError(t, err, out)
	f, err := setup.Load(connectSetupPath(t, "agent"))
	require.NoError(t, err)
	assert.Empty(t, f.Projects[setupProject].Class)
}

// Setup never changes the credential: a readiness failure, a wrong agent
// or a scope that is too narrow leaves it as it was, and each reports its
// own code.
func TestConnectSetupNeverChangesTheCredential(t *testing.T) {
	credential := func(t *testing.T) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(config.GlobalConfigDir(), "credentials.json"))
		require.NoError(t, err)
		return string(data)
	}
	for name, tc := range map[string]struct {
		prepare func(s *connectSetupServer)
		code    string
	}{
		"a readiness check fails": {prepare: func(s *connectSetupServer) { s.refuseAgentReads = true }, code: codeNotReady},
		"not an Agent":            {prepare: func(s *connectSetupServer) { s.agentAsUser = true }, code: output.CodeAuth},
		"granted read only":       {prepare: func(s *connectSetupServer) { s.grantScope = "read" }, code: codeNotReady},
	} {
		t.Run(name, func(t *testing.T) {
			s := startConnectSetupServer(t)
			tc.prepare(s)
			connectSetupApp(t, s, "agent")
			before := credential(t)

			out, err := runConnectSetupCmd(t, newConnectSetupApp(t, s, "agent"), "--operator", fmt.Sprint(setupOperatorPerson), routeArg(t))
			require.Error(t, err, out)
			var apiErr *output.Error
			require.ErrorAs(t, err, &apiErr)
			assert.Equal(t, tc.code, apiErr.Code)
			assert.Equal(t, before, credential(t))
			assertNotWritten(t, "agent")
		})
	}
}

func TestConnectSetupRefusesAConnectJSONOthersCanWrite(t *testing.T) {
	s := startConnectSetupServer(t)
	firstSetup(t, s)

	path := connectSetupPath(t, "agent")
	require.NoError(t, os.Chmod(path, 0o666))
	before, err := os.ReadFile(path)
	require.NoError(t, err)

	out, err := runConnectSetupCmd(t, newConnectSetupApp(t, s, "agent"), "--concurrency", "3")
	require.Error(t, err, out)
	assert.Contains(t, err.Error(), "not private")
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, before, after)
}

// A profile re-pointed at another agent does not inherit the first agent's
// trust and routes.
func TestConnectSetupRefusesADifferentAgentUnderTheSameProfile(t *testing.T) {
	s := startConnectSetupServer(t)
	firstSetup(t, s)
	before, err := os.ReadFile(connectSetupPath(t, "agent"))
	require.NoError(t, err)

	s.agentID = 777
	out, err := runConnectSetupCmd(t, newConnectSetupApp(t, s, "agent"))
	require.Error(t, err, out)
	assert.Contains(t, err.Error(), "person 777")
	var apiErr *output.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, output.CodeAuth, apiErr.Code, "a different agent is a wrong credential")
	after, err := os.ReadFile(connectSetupPath(t, "agent"))
	require.NoError(t, err)
	assert.Equal(t, before, after)
}

func TestConnectSetupRefusesAnotherAccountUnderTheSameProfile(t *testing.T) {
	s := startConnectSetupServer(t)
	firstSetup(t, s)
	before, err := os.ReadFile(connectSetupPath(t, "agent"))
	require.NoError(t, err)

	configData, configPath, err := loadGlobalConfigFile()
	require.NoError(t, err)
	globalProfileEntry(configData, "agent")["account_id"] = "1000" // the profile rebound elsewhere
	require.NoError(t, atomicWriteJSON(configPath, configData))

	out, err := runConnectSetupCmd(t, newConnectSetupApp(t, s, "agent"))
	require.Error(t, err, out)
	assert.Contains(t, err.Error(), "set up in account 999")
	after, err := os.ReadFile(connectSetupPath(t, "agent"))
	require.NoError(t, err)
	assert.Equal(t, before, after)
}

func TestConnectSetupRefusesTheAgentAsItsOwnOperator(t *testing.T) {
	s := startConnectSetupServer(t)
	out, err := runConnectSetupCmd(t, connectSetupApp(t, s, "agent"), "--operator", fmt.Sprint(setupAgentPerson), routeArg(t))
	require.Error(t, err, out)
	assert.Contains(t, err.Error(), "never authorizes", "refused by setup, before the file layer's own refusal")
	assertNotWritten(t, "agent")
}

// The trust anchor is verified before it is recorded: an operator id that
// is a client, or that the agent cannot read, is refused and nothing is
// written.
func TestConnectSetupVerifiesTheOperatorBeforeWriting(t *testing.T) {
	s := startConnectSetupServer(t)
	out, err := runConnectSetupCmd(t, connectSetupApp(t, s, "agent"), "--operator", fmt.Sprint(setupClientPerson), routeArg(t))
	require.Error(t, err, out)
	assert.Contains(t, err.Error(), "client")
	assertNotWritten(t, "agent")

	s.refusePeople = true
	out, err = runConnectSetupCmd(t, newConnectSetupApp(t, s, "agent"), "--operator", fmt.Sprint(setupOperatorPerson), routeArg(t))
	require.Error(t, err, out)
	assert.Contains(t, err.Error(), "cannot be verified")
	assertNotWritten(t, "agent")
}

// The operator named by their own profile is who that credential proves
// they are, read in the agent's account.
func TestConnectSetupResolvesTheOperatorFromTheirProfile(t *testing.T) {
	s := startConnectSetupServer(t)
	s.refusePeople = true // the agent need not read the operator: their own credential did
	connectSetupApp(t, s, "agent")
	storeConnectProfile(t, s, "me", setupOperatorToken)
	app := newConnectSetupApp(t, s, "agent")

	out, err := runConnectSetupCmd(t, app, "--operator-profile", "me", routeArg(t))
	require.NoError(t, err, out)
	f, err := setup.Load(connectSetupPath(t, "agent"))
	require.NoError(t, err)
	assert.Equal(t, setupOperatorPerson, f.Trust.OperatorID)
	assert.Contains(t, out, `profile "me"`)
}

// A person's login under the agent profile could be the operator's own, so
// the bot-user path is pinned by identity.
func TestConnectSetupOnTheBotUserPath(t *testing.T) {
	s := startConnectSetupServer(t)
	connectSetupApp(t, s, "bot")
	storeConnectProfile(t, s, "bot", setupBotToken)

	_, err := runConnectSetupCmd(t, newConnectSetupApp(t, s, "bot"), "--operator", fmt.Sprint(setupOperatorPerson), routeArg(t))
	require.Error(t, err, "a person's login without --expect-identity could be the operator's own")
	assert.Contains(t, err.Error(), "a person's login, not an Agent's credential")
	assertNotWritten(t, "bot")

	out, err := runConnectSetupCmd(t, newConnectSetupApp(t, s, "bot"), "--operator", fmt.Sprint(setupOperatorPerson), "--expect-identity", "1", routeArg(t))
	require.Error(t, err, out)
	assert.Contains(t, err.Error(), "not the 1 --expect-identity names")
	assertNotWritten(t, "bot")

	out, err = runConnectSetupCmd(t, newConnectSetupApp(t, s, "bot"),
		"--operator", fmt.Sprint(setupOperatorPerson),
		"--expect-identity", fmt.Sprint(setupBotIdentity),
		routeArg(t))
	require.NoError(t, err, out)
	assert.NotContains(t, out, setupTicket)
	f, err := setup.Load(connectSetupPath(t, "bot"))
	require.NoError(t, err)
	assert.Equal(t, setup.Agent{PersonID: setupBotPerson, Kind: setup.KindBotUser, IdentityID: setupBotIdentity}, f.Agent)

	// The pinned identity is remembered: a rerun need not restate it.
	out, err = runConnectSetupCmd(t, newConnectSetupApp(t, s, "bot"), "--worktrees")
	require.NoError(t, err, out)
}

// A read-only credential cannot reply or acknowledge, so it is not ready.
func TestConnectSetupReadOnlyCredentialIsNotReady(t *testing.T) {
	s := startConnectSetupServer(t)
	connectSetupApp(t, s, "bot")
	storeConnectProfileScoped(t, s, "bot", setupBotToken, "read")

	out, err := runConnectSetupCmd(t, newConnectSetupApp(t, s, "bot"),
		"--operator", fmt.Sprint(setupOperatorPerson), "--expect-identity", fmt.Sprint(setupBotIdentity), routeArg(t))
	require.Error(t, err, out)
	var apiErr *output.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, "not_ready", apiErr.Code)
	assert.Contains(t, apiErr.Message, `granted "read"`)
}

func TestConnectSetupRefusesExpectIdentityForAnAgent(t *testing.T) {
	s := startConnectSetupServer(t)
	firstSetup(t, s)
	before, err := os.ReadFile(connectSetupPath(t, "agent"))
	require.NoError(t, err)

	out, err := runConnectSetupCmd(t, newConnectSetupApp(t, s, "agent"), "--expect-identity", "4242")
	require.Error(t, err, out)
	assert.Contains(t, err.Error(), "holds an Agent's credential")
	after, err := os.ReadFile(connectSetupPath(t, "agent"))
	require.NoError(t, err)
	assert.Equal(t, before, after)
}

// Machine output carries the result, and neither the client secret nor the
// stream ticket.
func TestConnectSetupJSONOutput(t *testing.T) {
	s := startConnectSetupServer(t)
	firstSetup(t, s)

	app := newConnectSetupApp(t, s, "agent")
	var buf bytes.Buffer
	app.Output = output.New(output.Options{Format: output.FormatJSON, Writer: &buf})
	out, err := runConnectSetupCmd(t, app, "--concurrency", "3")
	require.NoError(t, err, out)

	var envelope struct {
		OK   bool `json:"ok"`
		Data struct {
			Ready         bool  `json:"ready"`
			Written       bool  `json:"written"`
			AgentPersonID int64 `json:"agent_person_id"`
			OperatorID    int64 `json:"operator_id"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &envelope), buf.String())
	assert.True(t, envelope.OK)
	assert.True(t, envelope.Data.Ready)
	assert.True(t, envelope.Data.Written)
	assert.Equal(t, setupAgentPerson, envelope.Data.AgentPersonID)
	assert.Equal(t, setupOperatorPerson, envelope.Data.OperatorID)
	assert.NotContains(t, buf.String(), setupTicket)
	assert.NotContains(t, buf.String(), fakeConnectSecret)
}

// A credential store that cannot be read is not "no credential": setup must
// not run a new connection over a credential it only failed to load.
func TestConnectSetupRefusesAnUnreadableCredentialStore(t *testing.T) {
	s := startConnectSetupServer(t)
	app := connectSetupApp(t, s, "agent")
	require.NoError(t, os.MkdirAll(config.GlobalConfigDir(), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(config.GlobalConfigDir(), "credentials.json"), []byte("{not json"), 0o600))

	out, err := runConnectSetupCmd(t, app, "--operator", fmt.Sprint(setupOperatorPerson), routeArg(t))
	require.Error(t, err, out)
	assertNotWritten(t, "agent")
}

// An operator proven by their own profile stays proven: a later run that
// changes only a route keeps it, even though the agent cannot read people.
func TestConnectSetupKeepsARecordedOperatorTheAgentCannotRead(t *testing.T) {
	s := startConnectSetupServer(t)
	s.refusePeople = true
	connectSetupApp(t, s, "agent")
	storeConnectProfile(t, s, "me", setupOperatorToken)

	out, err := runConnectSetupCmd(t, newConnectSetupApp(t, s, "agent"), "--operator-profile", "me", routeArg(t))
	require.NoError(t, err, out)

	out, err = runConnectSetupCmd(t, newConnectSetupApp(t, s, "agent"), routeArg(t))
	require.NoError(t, err, out)
	assert.Contains(t, out, "could not be re-read")
	f, err := setup.Load(connectSetupPath(t, "agent"))
	require.NoError(t, err)
	assert.Equal(t, setupOperatorPerson, f.Trust.OperatorID)

	// A different id is a new trust anchor, verified as one: here it reads
	// back as a client and is refused.
	s.refusePeople = false
	out, err = runConnectSetupCmd(t, newConnectSetupApp(t, s, "agent"), "--operator", fmt.Sprint(setupClientPerson))
	require.Error(t, err, out)
	assert.Contains(t, err.Error(), "client")
}

// Everyone connect.json trusts is verified before it is written, the
// allowlist included: an id nobody can read, or one that is a client, is
// refused and nothing is written.
func TestConnectSetupVerifiesTheAllowlistBeforeWriting(t *testing.T) {
	for name, id := range map[string]int64{"unknown person": 424242, "client": setupClientPerson, "the agent": setupAgentPerson} {
		t.Run(name, func(t *testing.T) {
			s := startConnectSetupServer(t)
			connectSetupApp(t, s, "agent")
			storeConnectProfile(t, s, "me", setupOperatorToken)

			out, err := runConnectSetupCmd(t, newConnectSetupApp(t, s, "agent"), "--operator-profile", "me", "--allow", fmt.Sprint(id), routeArg(t))
			require.Error(t, err, out)
			assert.Contains(t, err.Error(), fmt.Sprintf("Allowlist %d", id))
			assertNotWritten(t, "agent")
		})
	}

	s := startConnectSetupServer(t)
	connectSetupApp(t, s, "agent")
	storeConnectProfile(t, s, "me", setupOperatorToken)
	out, err := runConnectSetupCmd(t, newConnectSetupApp(t, s, "agent"), "--operator-profile", "me", "--allow", fmt.Sprint(setupOperatorPerson+1), routeArg(t))
	require.NoError(t, err, out)
	f, err := setup.Load(connectSetupPath(t, "agent"))
	require.NoError(t, err)
	assert.Equal(t, []int64{setupOperatorPerson + 1}, f.Trust.AllowlistIDs)
}

// The scope that decides readiness is the one the credential was granted,
// not the one the profile's configuration names.
func TestConnectSetupReadsTheGrantedScopeNotTheProfiles(t *testing.T) {
	s := startConnectSetupServer(t)
	connectSetupApp(t, s, "bot")
	storeConnectProfile(t, s, "bot", setupBotToken) // configured full
	cfg := config.Default()
	cfg.BaseURL = s.srv.URL
	cfg.ActiveProfile = "bot"
	mgr := auth.NewManager(cfg, s.srv.Client())
	mgr.SetStore(auth.NewStore(config.GlobalConfigDir()))
	require.NoError(t, mgr.ImportToken(context.Background(), setupBotToken, "read", "", "", time.Now().Add(24*time.Hour))) // granted read

	out, err := runConnectSetupCmd(t, newConnectSetupApp(t, s, "bot"),
		"--operator", fmt.Sprint(setupOperatorPerson), "--expect-identity", fmt.Sprint(setupBotIdentity), routeArg(t))
	require.Error(t, err, out)
	var apiErr *output.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, "not_ready", apiErr.Code)
	assert.Contains(t, apiErr.Message, `"read"`)
}

// The stream ticket is a bearer credential. When the mint fails with a body
// that carries one, as a malformed error response can, that body reaches no
// output: not the checks, not the error, not connect.json.
func TestConnectSetupNeverPrintsATicketFromAFailedMint(t *testing.T) {
	const canary = "CANARY-bearer-ticket-7f3a"
	s := startConnectSetupServer(t)
	s.mintFailure = func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		fmt.Fprintf(w, `{"error":%q,"message":%q,"ticket":%q,"url":"wss://example.test/cable?ticket=%s"}`, canary, "ticket "+canary, canary, canary)
	}
	connectSetupApp(t, s, "agent")
	storeConnectProfile(t, s, "me", setupOperatorToken)

	app := newConnectSetupApp(t, s, "agent")
	var envelope bytes.Buffer
	app.Output = output.New(output.Options{Format: output.FormatStyled, Writer: &envelope})
	out, err := runConnectSetupCmd(t, app, "--operator-profile", "me", routeArg(t))
	require.Error(t, err, out)

	assert.NotContains(t, out, canary, "command output")
	assert.NotContains(t, err.Error(), canary, "the error")
	assert.NotContains(t, envelope.String(), canary, "the app's output")
	assertNotWritten(t, "agent")
	assert.Contains(t, err.Error(), "HTTP 422", "the failure is still reported, by status")
}

// A command setup suggests is meant to be pasted, so a config-derived
// profile name in it is shell-quoted.
func TestConnectSetupQuotesProfileNamesInSuggestedCommands(t *testing.T) {
	s := startConnectSetupServer(t)
	connectSetupApp(t, s, "agent")
	_, err := registerProfile("my profile;rm", &config.ProfileConfig{BaseURL: s.srv.URL, AccountID: "999"})
	require.NoError(t, err)

	out, err := runConnectSetupCmd(t, newConnectSetupApp(t, s, "agent"), "--operator-profile", "my profile;rm")
	require.Error(t, err, out)
	assert.Contains(t, err.Error(), `basecamp auth login -P 'my profile;rm'`)
}

// connect.json's path comes from the environment, and reaches one-line
// terminal sinks: control characters in it must not break those lines.
func TestConnectSetupSanitizesThePathItPrints(t *testing.T) {
	s := startConnectSetupServer(t)
	bareSetupApp(t, s, "bot")
	home := filepath.Join(t.TempDir(), "evil\nFAKE ✓ line")
	require.NoError(t, os.Mkdir(home, 0o700))
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("USERPROFILE", home)
	storeConnectProfile(t, s, "bot", setupBotToken)

	args := []string{"--operator", fmt.Sprint(setupOperatorPerson), "--expect-identity", fmt.Sprint(setupBotIdentity)}
	out, err := runConnectSetupCmd(t, newConnectSetupApp(t, s, "bot"), args...) // no route: not ready, and the error names the path
	require.Error(t, err, out)
	assert.NotContains(t, err.Error(), "evil\nFAKE")

	out, err = runConnectSetupCmd(t, newConnectSetupApp(t, s, "bot"), append(args, routeArg(t))...)
	require.NoError(t, err, out)
	assert.Contains(t, out, "connect.json written")
	assert.NotContains(t, out, "evil\nFAKE")
}

// A profile whose credential changed kind since connect.json was written is
// a wrong credential, refused as auth with connect.json untouched.
func TestConnectSetupRefusesACredentialOfAnotherKind(t *testing.T) {
	s := startConnectSetupServer(t)
	bareSetupApp(t, s, "agent")
	storeConnectProfile(t, s, "agent", setupBotToken)
	out, err := runConnectSetupCmd(t, newConnectSetupApp(t, s, "agent"),
		"--operator", fmt.Sprint(setupOperatorPerson), "--expect-identity", fmt.Sprint(setupBotIdentity), routeArg(t))
	require.NoError(t, err, out)
	before, err := os.ReadFile(connectSetupPath(t, "agent"))
	require.NoError(t, err)

	// The profile is now connected to an Agent instead.
	app := newConnectSetupApp(t, s, "agent")
	out, err = runAgentConnect(t, app)
	require.NoError(t, err, out)

	out, err = runConnectSetupCmd(t, newConnectSetupApp(t, s, "agent"))
	require.Error(t, err, out)
	var apiErr *output.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, output.CodeAuth, apiErr.Code)
	after, err := os.ReadFile(connectSetupPath(t, "agent"))
	require.NoError(t, err)
	assert.Equal(t, before, after)
}

// A route kept from connect.json whose directory has gone makes a rerun not
// ready, and connect.json is left as it was.
func TestConnectSetupRechecksRetainedRoutes(t *testing.T) {
	s := startConnectSetupServer(t)
	repo := filepath.Join(t.TempDir(), "repo")
	require.NoError(t, os.Mkdir(repo, 0o700))
	out, err := runConnectSetupCmd(t, connectSetupApp(t, s, "agent"), "--operator", fmt.Sprint(setupOperatorPerson), fmt.Sprintf("--route=%d=%s", setupProject, repo))
	require.NoError(t, err, out)
	before, err := os.ReadFile(connectSetupPath(t, "agent"))
	require.NoError(t, err)

	require.NoError(t, os.Remove(repo))
	out, err = runConnectSetupCmd(t, newConnectSetupApp(t, s, "agent"), "--concurrency", "3")
	require.Error(t, err, out)
	var apiErr *output.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, codeNotReady, apiErr.Code)
	assert.Contains(t, apiErr.Message, "no longer usable")
	after, err := os.ReadFile(connectSetupPath(t, "agent"))
	require.NoError(t, err)
	assert.Equal(t, before, after)
}

// Setup checks one credential: when another process stores a different one
// under the profile while the checks run, nothing is written for either.
func TestConnectSetupRefusesACredentialReplacedDuringTheChecks(t *testing.T) {
	s := startConnectSetupServer(t)
	bareSetupApp(t, s, "bot")
	storeConnectProfile(t, s, "bot", setupBotToken)
	s.duringMint = func() {
		cfg := config.Default()
		cfg.BaseURL = s.srv.URL
		cfg.ActiveProfile = "bot"
		mgr := auth.NewManager(cfg, s.srv.Client())
		mgr.SetStore(auth.NewStore(config.GlobalConfigDir()))
		require.NoError(t, mgr.ImportToken(context.Background(), setupOperatorToken, "full", "", "", time.Now().Add(24*time.Hour)))
	}

	out, err := runConnectSetupCmd(t, newConnectSetupApp(t, s, "bot"),
		"--operator", fmt.Sprint(setupOperatorPerson), "--expect-identity", fmt.Sprint(setupBotIdentity), routeArg(t))
	require.Error(t, err, out)
	var apiErr *output.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, output.CodeAuth, apiErr.Code)
	assert.Contains(t, apiErr.Message, "changed while setup was checking it")
	assertNotWritten(t, "bot")
}

// What setup writes names the identity the profile's credential
// authenticates as, and connect.json's own check refuses any other. That is
// what makes a credential replaced after setup harmless: the connector
// stops instead of acting as the wrong agent.
func TestConnectSetupWritesAPolicyBoundToTheCredential(t *testing.T) {
	s := startConnectSetupServer(t)
	out, err := runConnectSetupCmd(t, connectSetupApp(t, s, "agent"), "--operator", fmt.Sprint(setupOperatorPerson), routeArg(t))
	require.NoError(t, err, out)

	f, err := setup.Load(connectSetupPath(t, "agent"))
	require.NoError(t, err)
	require.NoError(t, f.VerifyAgent(setup.KindAgent, setupAgentPerson, 0))

	// The profile is pointed at someone else afterwards.
	assert.Error(t, f.VerifyAgent(setup.KindBotUser, setupBotPerson, setupBotIdentity))
	assert.Error(t, f.VerifyAgent(setup.KindAgent, 777, 0))

	// And setup itself refuses the next run for the same reason.
	storeConnectProfile(t, s, "agent", setupBotToken)
	out, err = runConnectSetupCmd(t, newConnectSetupApp(t, s, "agent"), "--expect-identity", fmt.Sprint(setupBotIdentity))
	require.Error(t, err, out)
	var apiErr *output.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, output.CodeAuth, apiErr.Code)
}
