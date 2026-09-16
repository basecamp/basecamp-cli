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
		writeJSON(w, map[string]any{"client_id": "agent-client", "client_secret": fakeConnectSecret, "account_id": "999", "scope": "full"})
	})
	mux.HandleFunc("/oauth/tokens", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"access_token": setupAgentToken, "token_type": "bearer", "expires_in": 3600, "resource": "urn:bc:agent:42", "scope": "full"})
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
			writeJSON(w, map[string]any{"id": s.agentID, "name": "Marie Chef", "personable_type": "Agent"})
		case setupOperatorToken:
			writeJSON(w, map[string]any{"id": setupOperatorPerson, "name": "Operator", "personable_type": "User"})
		case setupBotToken:
			writeJSON(w, map[string]any{"id": setupBotPerson, "name": "Bot", "personable_type": "User"})
		default:
			w.WriteHeader(http.StatusUnauthorized)
		}
	})
	mux.HandleFunc(fmt.Sprintf("/999/people/%d", setupOperatorPerson), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"id": setupOperatorPerson, "name": "Operator", "personable_type": "User"})
	})
	mux.HandleFunc("/999/events/stream_ticket.json", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
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

// connectSetupApp is an App for connect setup: a file credential store and the
// global config under a temp XDG_CONFIG_HOME, and the issuer pinned to the
// mock server.
func connectSetupApp(t *testing.T, s *connectSetupServer, profile string) *appctx.App {
	t.Helper()
	t.Setenv("BASECAMP_NO_KEYRING", "1")
	t.Setenv("BASECAMP_TOKEN", "")
	t.Setenv("BASECAMP_NONINTERACTIVE", "")
	t.Setenv("BASECAMP_OAUTH_ISSUER", s.srv.URL)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
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
	cmd.SetArgs(append([]string{"setup", "--no-browser"}, args...))
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
	_, err := registerProfile(name, &config.ProfileConfig{BaseURL: s.srv.URL, AccountID: "999", Scope: "full"})
	require.NoError(t, err)
	cfg := config.Default()
	cfg.BaseURL = s.srv.URL
	cfg.ActiveProfile = name
	mgr := auth.NewManager(cfg, s.srv.Client())
	mgr.SetStore(auth.NewStore(config.GlobalConfigDir()))
	require.NoError(t, mgr.ImportToken(context.Background(), token, "full", "", "", time.Now().Add(24*time.Hour)))
}

func connectSetupPath(t *testing.T, profile string) string {
	t.Helper()
	path, err := setup.Path(config.GlobalConfigDir(), profile)
	require.NoError(t, err)
	return path
}

// The card's done-when on the ceremony path, on a clean machine: setup
// connects the agent, and leaves a profile that authenticates as it, a
// connect.json with a routed project that admission reads, and passing
// token, identity and mint checks.
func TestConnectSetupFromACleanMachine(t *testing.T) {
	s := startConnectSetupServer(t)
	app := connectSetupApp(t, s, "agent")
	repo := t.TempDir()

	out, err := runConnectSetupCmd(t, app,
		"--operator", fmt.Sprint(setupOperatorPerson),
		"--route", fmt.Sprintf("%d=%s", setupProject, repo),
		"--watch-completions", fmt.Sprint(setupProject),
		"--class", fmt.Sprintf("%d=internal", setupProject))
	require.NoError(t, err, out)

	assert.Contains(t, out, "WDJB-MJHT", "the ceremony ran: the operator compares the code")
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
	assert.Equal(t, 1, s.intakeCount(), "no second ceremony")
	again, err := setup.Load(connectSetupPath(t, "agent"))
	require.NoError(t, err)
	assert.Equal(t, 4, again.Concurrency)
	assert.Equal(t, f.Projects, again.Projects)
	assert.Equal(t, f.Trust, again.Trust)
}

// bc3 refuses admission's reads to an Agent identity today. Setup says so
// in words, and does not report the connector ready.
func TestConnectSetupNamesTheAgentReadRefusal(t *testing.T) {
	s := startConnectSetupServer(t)
	s.refuseAgentReads = true
	app := connectSetupApp(t, s, "agent")

	out, err := runConnectSetupCmd(t, app,
		"--operator", fmt.Sprint(setupOperatorPerson),
		"--route", fmt.Sprintf("%d=%s", setupProject, t.TempDir()))
	require.NoError(t, err, out)
	assert.Contains(t, out, "not ready")
	assert.Contains(t, out, "Agent identity")
	assert.Contains(t, out, "--expect-identity")
}

// Everything refusable without Basecamp is refused before an operator is
// sent to approve anything.
func TestConnectSetupRefusesBeforeTheCeremony(t *testing.T) {
	for name, args := range map[string][]string{
		"missing route directory": {"--route", fmt.Sprintf("%d=/does/not/exist", setupProject)},
		"class without a route":   {"--class", fmt.Sprintf("%d=internal", setupProject)},
		"bad trust mode":          {"--trust", "domain"},
		"allow outside allowlist": {"--trust", "project", "--allow", "7"},
		"bad driver":              {"--driver", "fork"},
		"bad concurrency":         {"--concurrency", "100"},
		"bad deadline":            {"--deadline", "5s"},
		"malformed route":         {"--route", "not-a-pair"},
	} {
		t.Run(name, func(t *testing.T) {
			s := startConnectSetupServer(t)
			app := connectSetupApp(t, s, "agent")
			out, err := runConnectSetupCmd(t, app, args...)
			require.Error(t, err, out)
			assert.Zero(t, s.intakeCount(), "no connection was requested")
			_, statErr := os.Stat(connectSetupPath(t, "agent"))
			assert.True(t, os.IsNotExist(statErr), "nothing was written")
		})
	}
}

func TestConnectSetupRefusesAConnectJSONOthersCanWrite(t *testing.T) {
	s := startConnectSetupServer(t)
	app := connectSetupApp(t, s, "agent")
	_, err := runConnectSetupCmd(t, app, "--operator", fmt.Sprint(setupOperatorPerson))
	require.NoError(t, err)

	path := connectSetupPath(t, "agent")
	require.NoError(t, os.Chmod(path, 0o666))
	before, err := os.ReadFile(path)
	require.NoError(t, err)

	app = newConnectSetupApp(t, s, "agent")
	out, err := runConnectSetupCmd(t, app, "--route", fmt.Sprintf("%d=%s", setupProject, t.TempDir()))
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
	app := connectSetupApp(t, s, "agent")
	_, err := runConnectSetupCmd(t, app, "--operator", fmt.Sprint(setupOperatorPerson))
	require.NoError(t, err)
	before, err := os.ReadFile(connectSetupPath(t, "agent"))
	require.NoError(t, err)

	s.agentID = 777
	app = newConnectSetupApp(t, s, "agent")
	out, err := runConnectSetupCmd(t, app)
	require.Error(t, err, out)
	assert.Contains(t, err.Error(), "person 777")
	after, err := os.ReadFile(connectSetupPath(t, "agent"))
	require.NoError(t, err)
	assert.Equal(t, before, after)
}

func TestConnectSetupRefusesTheAgentAsItsOwnOperator(t *testing.T) {
	s := startConnectSetupServer(t)
	app := connectSetupApp(t, s, "agent")
	out, err := runConnectSetupCmd(t, app, "--operator", fmt.Sprint(setupAgentPerson))
	require.Error(t, err, out)
	assert.Contains(t, err.Error(), "never authorizes", "refused by setup, before the file layer's own refusal")
	_, statErr := os.Stat(connectSetupPath(t, "agent"))
	assert.True(t, os.IsNotExist(statErr))
}

// The operator named by their own profile is who that credential proves
// they are, read in the agent's account.
func TestConnectSetupResolvesTheOperatorFromTheirProfile(t *testing.T) {
	s := startConnectSetupServer(t)
	connectSetupApp(t, s, "agent")
	storeConnectProfile(t, s, "me", setupOperatorToken)
	app := newConnectSetupApp(t, s, "agent")

	out, err := runConnectSetupCmd(t, app, "--operator-profile", "me")
	require.NoError(t, err, out)
	f, err := setup.Load(connectSetupPath(t, "agent"))
	require.NoError(t, err)
	assert.Equal(t, setupOperatorPerson, f.Trust.OperatorID)
	assert.Contains(t, out, `profile "me"`)
}

// With no operator named, the default profile's identity is the operator —
// the way the Ruby connector's --operator defaulted.
func TestConnectSetupDefaultsTheOperatorToTheDefaultProfile(t *testing.T) {
	s := startConnectSetupServer(t)
	connectSetupApp(t, s, "agent")
	storeConnectProfile(t, s, "me", setupOperatorToken) // the first profile is the default
	app := newConnectSetupApp(t, s, "agent")

	out, err := runConnectSetupCmd(t, app)
	require.NoError(t, err, out)
	f, err := setup.Load(connectSetupPath(t, "agent"))
	require.NoError(t, err)
	assert.Equal(t, setupOperatorPerson, f.Trust.OperatorID)
}

// The v1 path: a bot user's login, pinned by its identity.
func TestConnectSetupOnTheBotUserPath(t *testing.T) {
	s := startConnectSetupServer(t)
	connectSetupApp(t, s, "bot")
	storeConnectProfile(t, s, "bot", setupBotToken)
	app := newConnectSetupApp(t, s, "bot")

	_, err := runConnectSetupCmd(t, app, "--operator", fmt.Sprint(setupOperatorPerson))
	require.Error(t, err, "a person's login without --expect-identity could be the operator's own")
	assert.Contains(t, err.Error(), "a person's login, not an Agent's credential")
	_, statErr := os.Stat(connectSetupPath(t, "bot"))
	assert.True(t, os.IsNotExist(statErr))

	out, err := runConnectSetupCmd(t, newConnectSetupApp(t, s, "bot"), "--operator", fmt.Sprint(setupOperatorPerson), "--expect-identity", "1")
	require.Error(t, err, out)
	assert.Contains(t, err.Error(), "not the 1 --expect-identity names")
	_, statErr = os.Stat(connectSetupPath(t, "bot"))
	assert.True(t, os.IsNotExist(statErr), "a login that is not the pinned identity writes nothing")

	out, err = runConnectSetupCmd(t, newConnectSetupApp(t, s, "bot"),
		"--operator", fmt.Sprint(setupOperatorPerson),
		"--expect-identity", fmt.Sprint(setupBotIdentity),
		"--route", fmt.Sprintf("%d=%s", setupProject, t.TempDir()))
	require.NoError(t, err, out)
	assert.NotContains(t, out, "not ready")
	assert.NotContains(t, out, setupTicket)
	f, err := setup.Load(connectSetupPath(t, "bot"))
	require.NoError(t, err)
	assert.Equal(t, setup.Agent{PersonID: setupBotPerson, Kind: setup.KindBotUser, IdentityID: setupBotIdentity}, f.Agent)

	// The pinned identity is remembered: a rerun need not restate it.
	out, err = runConnectSetupCmd(t, newConnectSetupApp(t, s, "bot"), "--worktrees")
	require.NoError(t, err, out)
}

func TestConnectSetupRefusesExpectIdentityForAnAgent(t *testing.T) {
	s := startConnectSetupServer(t)
	app := connectSetupApp(t, s, "agent")
	_, err := runConnectSetupCmd(t, app, "--operator", fmt.Sprint(setupOperatorPerson))
	require.NoError(t, err)

	out, err := runConnectSetupCmd(t, newConnectSetupApp(t, s, "agent"), "--expect-identity", "4242")
	require.Error(t, err, out)
}
