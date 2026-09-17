package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/mcp/mcptest"

	"github.com/basecamp/basecamp-cli/internal/connector"
)

// fakeDispatch records what the domain asked of the ledger.
type fakeDispatch struct {
	getIDs    []int64
	acks      []*int64
	completes []connector.Completion
	err       error
	none      bool
}

func (f *fakeDispatch) Get(_ context.Context, eventID int64) (connector.Instruction, bool, error) {
	f.getIDs = append(f.getIDs, eventID)
	if f.err != nil || f.none {
		return connector.Instruction{}, false, f.err
	}
	return connector.Instruction{EventID: 7, EventType: "comment.created", Trigger: "mentioned", Delivery: connector.DeliveryExposed, Content: "do it"}, true, nil
}

func (f *fakeDispatch) Ack(_ context.Context, eventID int64, ackID *int64) (connector.Receipt, error) {
	f.acks = append(f.acks, ackID)
	if f.err != nil {
		return connector.Receipt{}, f.err
	}
	return connector.Receipt{EventID: eventID, Delivery: connector.DeliveryDelivered, AckID: ackID}, nil
}

func (f *fakeDispatch) Complete(_ context.Context, eventID int64, c connector.Completion) (connector.Receipt, error) {
	f.completes = append(f.completes, c)
	if f.err != nil {
		return connector.Receipt{}, f.err
	}
	return connector.Receipt{EventID: eventID, Delivery: connector.DeliveryCompleted, Outcome: c.Outcome, Links: c.Links, ReplyID: c.ReplyID}, nil
}

func noUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	upstream := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		t.Errorf("the connect domain must never reach Basecamp: %s %s", r.Method, r.URL.Path)
	}))
	t.Cleanup(upstream.Close)
	return upstream
}

func connectSession(t *testing.T, d Dispatch) *fakeDispatchSession {
	t.Helper()
	srv, err := New(newTestAPI(noUpstream(t)), Config{Connect: d})
	require.NoError(t, err)
	return &fakeDispatchSession{t: t, session: mcptest.Connect(t, srv.BuildMCPServer(slog.New(slog.DiscardHandler)))}
}

type fakeDispatchSession struct {
	t       *testing.T
	session *mcp.ClientSession
}

func (s *fakeDispatchSession) call(action string, params map[string]any) (string, bool) {
	s.t.Helper()
	args := map[string]any{"action": action}
	if params != nil {
		args["params"] = params
	}
	return mcptest.CallText(s.t, s.session, connectToolName, args)
}

// Done when: a server started without the token does not expose the domain.
func TestTheConnectDomainExistsOnlyWhenConfigured(t *testing.T) {
	srv, err := New(newTestAPI(noUpstream(t)), Config{})
	require.NoError(t, err)
	tools := mcptest.ListTools(t, mcptest.Connect(t, srv.BuildMCPServer(slog.New(slog.DiscardHandler))))
	assert.NotContains(t, tools, connectToolName)

	srv, err = New(newTestAPI(noUpstream(t)), Config{Connect: &fakeDispatch{}})
	require.NoError(t, err)
	tools = mcptest.ListTools(t, mcptest.Connect(t, srv.BuildMCPServer(slog.New(slog.DiscardHandler))))
	require.Contains(t, tools, connectToolName)
	var actions []string
	for _, line := range strings.Split(tools[connectToolName].Description, "\n") {
		if name, _, ok := strings.Cut(strings.TrimPrefix(line, "- "), ":"); ok && strings.HasPrefix(line, "- ") {
			actions = append(actions, name)
		}
	}
	assert.Equal(t, []string{ackDispatchAction, completeDispatch, getDispatchAction}, actions,
		"exactly these three: a worker never reads other tasks")
}

func TestTheConnectDomainIsNeverReadOnly(t *testing.T) {
	_, err := New(newTestAPI(noUpstream(t)), Config{Connect: &fakeDispatch{}, ReadOnly: true})
	require.Error(t, err)
}

func TestGetDispatchOverMCP(t *testing.T) {
	d := &fakeDispatch{}
	s := connectSession(t, d)

	text, isError := s.call(getDispatchAction, nil)
	require.False(t, isError, text)
	var body struct {
		Instruction connector.Instruction `json:"instruction"`
	}
	require.NoError(t, json.Unmarshal([]byte(text), &body))
	assert.Equal(t, int64(7), body.Instruction.EventID)

	_, isError = s.call(getDispatchAction, map[string]any{"event_id": "12"})
	require.False(t, isError)
	assert.Equal(t, []int64{0, 12}, d.getIDs, "no event_id asks for the earliest")

	text, isError = s.call(getDispatchAction, map[string]any{"task_id": 1})
	assert.True(t, isError)
	assert.Contains(t, text, "unknown parameter")

	d.none = true
	text, isError = s.call(getDispatchAction, nil)
	require.False(t, isError, text)
	assert.Contains(t, text, `"instruction": null`)
}

func TestAckAndCompleteOverMCP(t *testing.T) {
	d := &fakeDispatch{}
	s := connectSession(t, d)

	text, isError := s.call(ackDispatchAction, map[string]any{"event_id": 7, "ack_id": 99})
	require.False(t, isError, text)
	assert.Contains(t, text, `"delivery": "delivered"`)
	require.Len(t, d.acks, 1)
	require.NotNil(t, d.acks[0])
	assert.Equal(t, int64(99), *d.acks[0])

	_, isError = s.call(ackDispatchAction, map[string]any{})
	assert.True(t, isError, "event_id is required")

	text, isError = s.call(completeDispatch, map[string]any{
		"event_id": 7, "outcome": "succeeded", "links": []any{"https://example.com/pr"}, "reply_id": 100,
	})
	require.False(t, isError, text)
	require.Len(t, d.completes, 1)
	assert.Equal(t, connector.OutcomeSucceeded, d.completes[0].Outcome)
	assert.Equal(t, []string{"https://example.com/pr"}, d.completes[0].Links)
	require.NotNil(t, d.completes[0].ReplyID)

	_, isError = s.call(completeDispatch, map[string]any{"event_id": 7})
	assert.True(t, isError, "outcome is required")
	_, isError = s.call(completeDispatch, map[string]any{"event_id": 7, "outcome": "succeeded", "links": []any{1}})
	assert.True(t, isError, "links are strings")
}

func TestConnectRefusalsAreNamed(t *testing.T) {
	for kind, err := range map[string]error{
		"task_token_refused": connector.ErrTaskTokenRefused,
		"not_on_task":        connector.ErrNotOnTask,
		"not_exposed":        connector.ErrNotExposed,
		"report_conflict":    connector.ErrReportConflict,
		"not_dispatchable":   connector.ErrNotDispatchable,
	} {
		t.Run(kind, func(t *testing.T) {
			s := connectSession(t, &fakeDispatch{err: fmt.Errorf("connector: event 7: %w", err)})
			for _, call := range []struct {
				action string
				params map[string]any
			}{
				{getDispatchAction, nil},
				{ackDispatchAction, map[string]any{"event_id": 7}},
				{completeDispatch, map[string]any{"event_id": 7, "outcome": "failed"}},
			} {
				text, isError := s.call(call.action, call.params)
				assert.True(t, isError)
				assert.Contains(t, text, `"error": "`+kind+`"`)
			}
		})
	}

	s := connectSession(t, &fakeDispatch{err: errors.New("connector: disk I/O error")})
	text, isError := s.call(getDispatchAction, nil)
	assert.True(t, isError)
	assert.Contains(t, text, "disk I/O error")
}
