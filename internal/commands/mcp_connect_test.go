package commands

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp/eventfeed"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/connector"
	"github.com/basecamp/basecamp-cli/internal/connector/admission"
)

const connectTestAgentID int64 = 52007412

// connectStateWithTask builds the connector's state directory for account 999
// and the agent under a private state home, with one admitted mention on a
// task, and returns the directory and the task's grant. XDG_STATE_HOME is set
// to that home, so run it after setupMCPTestApp, which sets its own.
func connectStateWithTask(t *testing.T) (string, connector.TaskGrant, *connector.Ledger) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("XDG_STATE_HOME", home)
	root, err := connector.StateRoot()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(root, 0o700))
	dir := filepath.Join(root, connector.StateDirName("999", connectTestAgentID))
	require.NoError(t, os.Mkdir(dir, 0o700))
	ledger, err := connector.OpenLedger(filepath.Join(dir, connector.LedgerFile))
	require.NoError(t, err)
	t.Cleanup(func() { _ = ledger.Close() })

	ctx := context.Background()
	_, err = ledger.RecordSeen(ctx, eventfeed.Event{
		ID: 1, EventType: "todo.created", Kind: "todo_created", Action: "created",
		BucketID: 48699913, CreatorID: 26909558, RecordingID: 501, CreatedAt: time.Now(),
	}, connector.LanePoll)
	require.NoError(t, err)
	_, err = ledger.Admission().Commit(ctx, admission.Verdict{
		EventID: 1, EventType: "todo.created", BucketID: 48699913, RecordingID: 501,
		RequesterID: 26909558, State: admission.StateAdmitted, Trigger: admission.TriggerMentioned,
		Acknowledge: true, ConversationKey: "recording:501",
		Reply:  &admission.ReplyDestination{Kind: admission.ReplyComment, RecordingID: 501},
		Routed: true, Route: "/work/secret-route", Class: "internal",
		Snapshot: &admission.Snapshot{Type: "Todo", Title: "A to-do", Content: "please do it"},
	})
	require.NoError(t, err)
	grant, err := ledger.CreateTask(ctx, []int64{1})
	require.NoError(t, err)
	return dir, grant, ledger
}

func unusedUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	t.Cleanup(upstream.Close)
	return upstream
}

func toolNames(t *testing.T, session *mcp.ClientSession) []string {
	t.Helper()
	var names []string
	for tool, err := range session.Tools(context.Background(), nil) {
		require.NoError(t, err)
		names = append(names, tool.Name)
	}
	return names
}

// connectMCPApp builds the app, then the connector state under the state home
// the command will read.
func connectMCPApp(t *testing.T, accountID, baseURL string) (*appctx.App, string, connector.TaskGrant, *connector.Ledger) {
	t.Helper()
	t.Setenv("BASECAMP_TOKEN", "test-token")
	app := setupMCPTestApp(t, accountID, baseURL)
	dir, grant, ledger := connectStateWithTask(t)
	return app, dir, grant, ledger
}

// Done when: the domain is served from the ledger with the task token, and a
// server started without the token does not expose it.
func TestMCPCommandServesTheConnectDomainFromTheLedger(t *testing.T) {
	app, dir, grant, ledger := connectMCPApp(t, "999", unusedUpstream(t).URL)
	t.Setenv(connectTaskTokenEnv, grant.Token)

	session := runMCPCommandWithApp(t, app, "--connect-state", dir)
	assert.Contains(t, toolNames(t, session), "basecamp_connect")
	assert.Empty(t, os.Getenv(connectTaskTokenEnv), "the token does not outlive startup in the environment")

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "basecamp_connect", Arguments: map[string]any{"action": "get_dispatch"},
	})
	require.NoError(t, err)
	require.False(t, res.IsError)
	text := res.Content[0].(*mcp.TextContent).Text
	var body struct {
		Instruction connector.Instruction `json:"instruction"`
	}
	require.NoError(t, json.Unmarshal([]byte(text), &body))
	assert.Equal(t, int64(1), body.Instruction.EventID)
	assert.Equal(t, "please do it", body.Instruction.Content)
	assert.NotContains(t, text, "secret-route")
	assert.NotContains(t, text, grant.Token)

	// Read back through the connector's own handle: a repeat writes nothing,
	// and reports the delivery the server wrote.
	d, err := ledger.Dispatch(context.Background(), grant.Token, connectTestAgentID)
	require.NoError(t, err)
	again, ok, err := d.Get(context.Background(), 1)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, connector.DeliveryExposed, again.Delivery, "exposure was written to the connector's ledger")
}

func TestMCPCommandMatchesTheAccountAsANumber(t *testing.T) {
	app, dir, grant, _ := connectMCPApp(t, "0999", unusedUpstream(t).URL)
	t.Setenv(connectTaskTokenEnv, grant.Token)

	session := runMCPCommandWithApp(t, app, "--connect-state", dir+"/")
	assert.Contains(t, toolNames(t, session), "basecamp_connect")
}

// Authentication can start helper processes, so the token is out of the
// environment before it runs — even when it then fails.
func TestMCPCommandTakesTheTokenBeforeAuthenticating(t *testing.T) {
	app, dir, grant, _ := connectMCPApp(t, "999", "https://3.basecampapi.com")
	t.Setenv("BASECAMP_TOKEN", "")
	t.Setenv(connectTaskTokenEnv, grant.Token)

	err := executeMCPCommand(t, app, "--connect-state", dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Not authenticated")
	assert.Empty(t, os.Getenv(connectTaskTokenEnv))
}

func TestMCPCommandRefusesReadOnlyBeforeTouchingTheToken(t *testing.T) {
	app, dir, grant, _ := connectMCPApp(t, "999", "https://3.basecampapi.com")
	t.Setenv(connectTaskTokenEnv, grant.Token)

	err := executeMCPCommand(t, app, "--connect-state", dir, "--read-only")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read-only")
	assert.Equal(t, grant.Token, os.Getenv(connectTaskTokenEnv))
}

func TestMCPCommandWithoutConnectStateHasNoConnectDomain(t *testing.T) {
	app, _, grant, _ := connectMCPApp(t, "999", unusedUpstream(t).URL)
	t.Setenv(connectTaskTokenEnv, grant.Token)

	session := runMCPCommandWithApp(t, app)
	assert.NotContains(t, toolNames(t, session), "basecamp_connect", "a token alone serves nothing")
}

func TestMCPCommandRefusesABadConnectState(t *testing.T) {
	app, dir, grant, _ := connectMCPApp(t, "999", "https://3.basecampapi.com")
	root, err := connector.StateRoot()
	require.NoError(t, err)
	mkdir := func(path string) string {
		require.NoError(t, os.MkdirAll(path, 0o700))
		return path
	}
	otherAccount := mkdir(filepath.Join(root, connector.StateDirName("1000", connectTestAgentID)))
	notAStateDir := mkdir(filepath.Join(root, "connect"))
	empty := mkdir(filepath.Join(root, connector.StateDirName("999", 1)))
	// The right name in the wrong place: a copy that renamed itself to match.
	elsewhere := mkdir(filepath.Join(t.TempDir(), connector.StateDirName("999", connectTestAgentID)))
	ledger, err := os.ReadFile(filepath.Join(dir, connector.LedgerFile))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(elsewhere, connector.LedgerFile), ledger, 0o600))

	for name, tc := range map[string]struct {
		dir, token, want string
	}{
		"no token":            {dir, "", connectTaskTokenEnv},
		"another account":     {otherAccount, grant.Token, "belongs to account 1000"},
		"not a state dir":     {notAStateDir, grant.Token, "not named <account>-<agent person id>"},
		"outside the root":    {elsewhere, grant.Token, "is not inside"},
		"no ledger":           {empty, grant.Token, "no connector ledger"},
		"a token for no task": {dir, "not-a-task-token", "names no current task"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(connectTaskTokenEnv, tc.token)
			err := executeMCPCommand(t, app, "--connect-state", tc.dir)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
	_, err = os.Stat(filepath.Join(empty, connector.LedgerFile))
	assert.True(t, os.IsNotExist(err), "a worker's server never creates the connector's ledger")
}
