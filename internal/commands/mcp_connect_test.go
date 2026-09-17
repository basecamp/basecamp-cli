package commands

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp/eventfeed"

	"github.com/basecamp/basecamp-cli/internal/connector"
	"github.com/basecamp/basecamp-cli/internal/connector/admission"
)

const connectTestAgentID int64 = 52007412

// connectStateWithTask builds the connector's state directory for account 999
// and the agent, with one admitted mention on a task, and returns the
// directory and the task's grant.
func connectStateWithTask(t *testing.T) (string, connector.TaskGrant, *connector.Ledger) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), connector.StateDirName("999", connectTestAgentID))
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

// Done when: the domain is served from the ledger with the task token, and a
// server started without the token does not expose it.
func TestMCPCommandServesTheConnectDomainFromTheLedger(t *testing.T) {
	dir, grant, ledger := connectStateWithTask(t)
	t.Setenv(connectTaskTokenEnv, grant.Token)

	session := runMCPCommand(t, unusedUpstream(t), "--connect-state", dir)
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

	record, ok, err := ledger.Get(context.Background(), 1)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, connector.StateDispatched, record.State, "exposure was written to the connector's ledger")
}

func TestMCPCommandWithoutConnectStateHasNoConnectDomain(t *testing.T) {
	_, grant, _ := connectStateWithTask(t)
	t.Setenv(connectTaskTokenEnv, grant.Token)

	session := runMCPCommand(t, unusedUpstream(t))
	assert.NotContains(t, toolNames(t, session), "basecamp_connect", "a token alone serves nothing")
}

func TestMCPCommandRefusesABadConnectState(t *testing.T) {
	dir, grant, _ := connectStateWithTask(t)
	otherAccount := filepath.Join(t.TempDir(), connector.StateDirName("1000", connectTestAgentID))
	require.NoError(t, os.Mkdir(otherAccount, 0o700))
	notAStateDir := filepath.Join(t.TempDir(), "connect")
	require.NoError(t, os.Mkdir(notAStateDir, 0o700))
	empty := filepath.Join(t.TempDir(), connector.StateDirName("999", connectTestAgentID))
	require.NoError(t, os.Mkdir(empty, 0o700))

	for name, tc := range map[string]struct {
		dir, token, want string
	}{
		"no token":          {dir, "", connectTaskTokenEnv},
		"another account":   {otherAccount, grant.Token, "belongs to account 1000"},
		"not a state dir":   {notAStateDir, grant.Token, "not a connector state directory"},
		"no ledger":         {empty, grant.Token, "no connector ledger"},
		"read-only refused": {dir, grant.Token, "read-only"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("BASECAMP_TOKEN", "test-token")
			t.Setenv(connectTaskTokenEnv, tc.token)
			app := setupMCPTestApp(t, "999", "https://3.basecampapi.com")
			args := []string{"--connect-state", tc.dir}
			if strings.HasPrefix(name, "read-only") {
				args = append(args, "--read-only")
			}
			err := executeMCPCommand(t, app, args...)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
	_, err := os.Stat(filepath.Join(empty, connector.LedgerFile))
	assert.True(t, os.IsNotExist(err), "a worker's server never creates the connector's ledger")
}
