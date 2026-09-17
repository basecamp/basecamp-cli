package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/config"
)

func TestMCPCommandFlags(t *testing.T) {
	cmd := NewMCPCmd()
	assert.Equal(t, "mcp", cmd.Name())

	readOnly := cmd.Flags().Lookup("read-only")
	require.NotNil(t, readOnly)
	assert.Equal(t, "false", readOnly.DefValue, "full surface is the default, matching basecamp-mcp-server")
	require.NotNil(t, cmd.Flags().Lookup("domains"))

	// Stdout is the MCP JSON-RPC transport: the stdout_wire annotation makes
	// cli.Execute report this command's errors on stderr instead of writing
	// an error envelope into the protocol stream.
	assert.NotEmpty(t, cmd.Annotations["stdout_wire"], "basecamp mcp must keep errors off the MCP wire")
}

// setupMCPTestApp builds the app the way the root command would: real
// config, real auth manager (reading BASECAMP_TOKEN), real SDK client.
func setupMCPTestApp(t *testing.T, accountID, baseURL string) *appctx.App {
	t.Helper()
	t.Setenv("BASECAMP_NO_KEYRING", "1")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	cfg := config.Default()
	cfg.AccountID = accountID
	cfg.BaseURL = baseURL
	cfg.CacheDir = t.TempDir()
	return appctx.NewApp(cfg)
}

func executeMCPCommand(t *testing.T, app *appctx.App, args ...string) error {
	t.Helper()
	// As cli.Execute does, before the command tree runs at all.
	takenTaskToken.taken, takenTaskToken.token, takenTaskToken.err = false, "", nil
	TakeConnectTaskToken(testRootForMCP(t), append([]string{"mcp"}, args...))
	t.Cleanup(func() {
		takenTaskToken.taken, takenTaskToken.token, takenTaskToken.err = false, "", nil
	})
	cmd := NewMCPCmd()
	cmd.SetArgs(args)
	cmd.SetContext(appctx.WithApp(context.Background(), app))
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	return cmd.Execute()
}

func TestMCPCommandRequiresAuth(t *testing.T) {
	t.Setenv("BASECAMP_TOKEN", "")
	app := setupMCPTestApp(t, "999", "https://3.basecampapi.com")

	err := executeMCPCommand(t, app)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Not authenticated")
}

func TestMCPCommandRequiresAccount(t *testing.T) {
	t.Setenv("BASECAMP_TOKEN", "test-token")
	app := setupMCPTestApp(t, "", "https://3.basecampapi.com")

	err := executeMCPCommand(t, app)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Account ID required")
}

func TestMCPCommandFailsClosedOnUnknownDomain(t *testing.T) {
	t.Setenv("BASECAMP_TOKEN", "test-token")
	app := setupMCPTestApp(t, "999", "https://3.basecampapi.com")

	err := executeMCPCommand(t, app, "--domains", "projects,bogus")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unknown domain "bogus"`)
}

// stubMCPTransport swaps the stdio transport for the server side of an
// in-memory pipe and returns the client side.
func stubMCPTransport(t *testing.T) mcp.Transport {
	t.Helper()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	orig := mcpTransport
	mcpTransport = func() mcp.Transport { return serverTransport }
	t.Cleanup(func() { mcpTransport = orig })
	return clientTransport
}

// runMCPCommand runs `basecamp mcp` against a stub Basecamp server and
// connects a real MCP client to it over the transport seam. The command
// exits when the client session closes.
func runMCPCommand(t *testing.T, upstream *httptest.Server, args ...string) *mcp.ClientSession {
	t.Helper()
	t.Setenv("BASECAMP_TOKEN", "test-token")
	return runMCPCommandWithApp(t, setupMCPTestApp(t, "999", upstream.URL), args...)
}

// runMCPCommandWithApp is runMCPCommand for an app the test has already
// built, so it can adjust the environment the command will read.
func runMCPCommandWithApp(t *testing.T, app *appctx.App, args ...string) *mcp.ClientSession {
	t.Helper()
	clientTransport := stubMCPTransport(t)

	done := make(chan error, 1)
	go func() { done <- executeMCPCommand(t, app, args...) }()

	// Raced against the command: one that refuses to start exits without
	// serving, and a client connect waiting on it would never return.
	type connected struct {
		session *mcp.ClientSession
		err     error
	}
	connecting := make(chan connected, 1)
	go func() {
		client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.0"}, nil)
		session, err := client.Connect(context.Background(), clientTransport, nil)
		connecting <- connected{session, err}
	}()
	select {
	case err := <-done:
		require.NoError(t, err, "basecamp mcp refused to start")
		t.Fatal("basecamp mcp exited before serving")
		return nil
	case c := <-connecting:
		require.NoError(t, c.err, "MCP initialize failed")
		t.Cleanup(func() {
			require.NoError(t, <-done, "basecamp mcp exited with error")
		})
		t.Cleanup(func() { _ = c.session.Close() })
		return c.session
	}
}

func TestMCPCommandServesMCP(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/999/projects.json" {
			t.Errorf("unexpected HTTP request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		// Tool calls must ride on the CLI's own credentials.
		assert.Equal(t, "Bearer test-token", r.Header.Get("Authorization"), "want the CLI's token")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":1,"name":"Making a Podcast"}]`))
	}))
	t.Cleanup(upstream.Close)

	session := runMCPCommand(t, upstream)

	assert.Equal(t, "basecamp-cli", session.InitializeResult().ServerInfo.Name)

	var names []string
	for tool, err := range session.Tools(context.Background(), nil) {
		require.NoError(t, err)
		names = append(names, tool.Name)
	}
	assert.Len(t, names, 17, "tools = %v", names)

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "basecamp_projects",
		Arguments: map[string]any{"action": "list_projects", "params": map[string]any{}},
	})
	require.NoError(t, err)
	require.False(t, result.IsError, "list_projects failed: %v", result.Content)
	text, ok := result.Content[0].(*mcp.TextContent)
	require.True(t, ok, "content = %T", result.Content[0])
	var projects []struct {
		Name string `json:"name"`
	}
	require.NoError(t, json.Unmarshal([]byte(text.Text), &projects))
	require.NotEmpty(t, projects)
	assert.Equal(t, "Making a Podcast", projects[0].Name)
}

func TestMCPCommandFlagPassthrough(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected HTTP request: %s %s", r.Method, r.URL.Path)
		http.NotFound(w, r)
	}))
	t.Cleanup(upstream.Close)

	session := runMCPCommand(t, upstream, "--read-only", "--domains", "projects")

	var tools []*mcp.Tool
	for tool, err := range session.Tools(context.Background(), nil) {
		require.NoError(t, err)
		tools = append(tools, tool)
	}
	require.Len(t, tools, 1)
	assert.Equal(t, "basecamp_projects", tools[0].Name)
	assert.True(t, strings.Contains(tools[0].Description, "list_projects"),
		"read-only basecamp_projects lost its read actions")
	assert.False(t, strings.Contains(tools[0].Description, "create_project"),
		"read-only basecamp_projects still lists a write action")
}

// testRootForMCP is the command tree TakeConnectTaskToken resolves against:
// a root carrying this command, as cli.Execute builds it.
func testRootForMCP(t *testing.T) *cobra.Command {
	t.Helper()
	root := &cobra.Command{Use: "basecamp"}
	root.PersistentFlags().Bool("json", false, "")
	root.PersistentFlags().CountP("verbose", "v", "")
	root.PersistentFlags().String("project", "", "")
	root.AddCommand(NewMCPCmd())
	root.AddCommand(&cobra.Command{Use: "search", RunE: func(*cobra.Command, []string) error { return nil }})
	return root
}

// The startup read must not touch the flags of the command that then runs:
// pflag's slice and count values append and increment once a value has been
// set, so sharing them would double what the command was given.
func TestTakeConnectTaskTokenLeavesTheCommandsOwnFlagsAlone(t *testing.T) {
	t.Cleanup(func() { takenTaskToken.taken, takenTaskToken.token, takenTaskToken.err = false, "", nil })
	root := testRootForMCP(t)
	argv := []string{"-v", "mcp", "--domains", "todos,cards", "--connect-state", "/x", "--connect-token-fd", "3"}

	TakeConnectTaskToken(root, argv)

	target, rest, err := root.Find(argv)
	require.NoError(t, err)
	require.NoError(t, target.ParseFlags(rest))
	domains, err := target.Flags().GetStringSlice("domains")
	require.NoError(t, err)
	assert.Equal(t, []string{"todos", "cards"}, domains, "the command sees what it was given, once")
	verbose, err := root.PersistentFlags().GetCount("verbose")
	require.NoError(t, err)
	assert.Equal(t, 1, verbose)
}
