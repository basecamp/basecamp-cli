package commands

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/output"
)

const clientsProjectPeoplePath = "/99999/projects/123/people.json"

func clientsProjectPeopleRoute() stubRoute {
	body := `[
		{"id":1001,"name":"Zed Team","email_address":"zed@example.com","employee":true},
		{"id":3002,"name":"beth client","email_address":"beth@springfield.example.com","client":true,"title":"Owner","company":{"id":9,"name":"Springfield Elementary"}},
		{"id":3001,"name":"Annie Bryan","email_address":"annie@springfield.example.com","client":true}
	]`
	return stubRoute{
		method: http.MethodGet,
		path:   clientsProjectPeoplePath,
		status: http.StatusOK,
		body:   body,
		pages:  []string{body},
	}
}

func TestPeopleClientsListKeepsOnlyClients(t *testing.T) {
	app, _, out := setupPersonalFeedApp(t, projectsRoute(), clientsProjectPeopleRoute())

	require.NoError(t, executeRecordingCommand(NewPeopleCmd(), app, "clients", "list", "--in", "123"))

	var envelope struct {
		Data []struct {
			ID           int64  `json:"id"`
			Name         string `json:"name"`
			EmailAddress string `json:"email_address"`
			Company      string `json:"company"`
		} `json:"data"`
		Summary string `json:"summary"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &envelope), "output: %s", out.String())
	require.Len(t, envelope.Data, 2)
	assert.Equal(t, int64(3001), envelope.Data[0].ID, "sorted by name, case-insensitively")
	assert.Equal(t, int64(3002), envelope.Data[1].ID)
	assert.Equal(t, "Springfield Elementary", envelope.Data[1].Company)
	assert.Equal(t, "2 client(s) on project #123", envelope.Summary)
}

func TestPeopleClientsListRequiresProject(t *testing.T) {
	app, transport, _ := setupPersonalFeedApp(t)

	err := executeRecordingCommand(NewPeopleCmd(), app, "clients", "list")
	requireBookmarksUsageError(t, err)
	assert.Empty(t, transport.recorded())
}

func TestParseClientInvitees(t *testing.T) {
	invitees, err := parseClientInvitees([]string{
		"annie@example.com",
		"Annie Bryan <annie@example.com>",
		`"Bryan, Annie" <annie@example.com>`,
		" <annie@example.com> ",
	})
	require.NoError(t, err)
	assert.Equal(t, []clientInvitee{
		{EmailAddress: "annie@example.com"},
		{Name: "Annie Bryan", EmailAddress: "annie@example.com"},
		{Name: "Bryan, Annie", EmailAddress: "annie@example.com"},
		{EmailAddress: "annie@example.com"},
	}, invitees)
}

func TestParseClientInviteesRejectsNonAddresses(t *testing.T) {
	for _, bad := range []string{"Annie Bryan", "annie@", "annie@example.com bob@example.com", ""} {
		_, err := parseClientInvitees([]string{bad})
		requireBookmarksUsageError(t, err)
	}
}

// Every malformed token is named at once, so a batch is fixed in one pass.
func TestParseClientInviteesNamesEveryMalformedToken(t *testing.T) {
	_, err := parseClientInvitees([]string{"ok@example.com", "Annie Bryan", "annie@"})

	outErr := requireBookmarksUsageError(t, err)
	assert.Contains(t, outErr.Message, `"Annie Bryan", "annie@"`)
	assert.NotContains(t, outErr.Message, "ok@example.com")
}

// people list carries each person's client flag, the only way a caller can
// tell which ids belong on people clients add rather than people add.
func TestPeopleListReportsTheClientFlag(t *testing.T) {
	app, _, out := setupPersonalFeedApp(t, accountPeopleRoute())

	require.NoError(t, executeRecordingCommand(NewPeopleCmd(), app, "list"))

	var envelope struct {
		Data []struct {
			ID     int64 `json:"id"`
			Client bool  `json:"client"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &envelope), "output: %s", out.String())
	clientByID := map[int64]bool{}
	for _, p := range envelope.Data {
		clientByID[p.ID] = p.Client
	}
	assert.True(t, clientByID[3001])
	assert.False(t, clientByID[1001])
}

func TestResolveClientInviteeTokensReadsStdinLines(t *testing.T) {
	cmd := &cobra.Command{Use: "invite"}
	cmd.SetIn(strings.NewReader("annie@example.com\r\n\n  Annie Bryan <annie@example.com>  \n"))

	tokens, err := resolveClientInviteeTokens(cmd, []string{"-"})
	require.NoError(t, err)
	assert.Equal(t, []string{"annie@example.com", "Annie Bryan <annie@example.com>"}, tokens)
}

func TestResolveClientInviteeTokensPassesArgsThrough(t *testing.T) {
	cmd := &cobra.Command{Use: "invite"}
	cmd.SetIn(strings.NewReader("ignored@example.com\n"))

	tokens, err := resolveClientInviteeTokens(cmd, []string{"a@example.com", "b@example.com"})
	require.NoError(t, err)
	assert.Equal(t, []string{"a@example.com", "b@example.com"}, tokens)
}

func TestResolveClientInviteeTokensRejectsMixedDash(t *testing.T) {
	cmd := &cobra.Command{Use: "invite"}
	cmd.SetIn(strings.NewReader("annie@example.com\n"))

	_, err := resolveClientInviteeTokens(cmd, []string{"-", "b@example.com"})
	requireBookmarksUsageError(t, err)
}

func clientsProjectRoute(clientsEnabled bool) stubRoute {
	body := `{"id":123,"name":"Test Project","clients_enabled":false}`
	if clientsEnabled {
		body = `{"id":123,"name":"Test Project","clients_enabled":true}`
	}
	return stubRoute{method: http.MethodGet, path: "/99999/projects/123", status: http.StatusOK, body: body}
}

func TestClientsForbiddenErrorNamesDisabledClients(t *testing.T) {
	app, _ := setupRecordingTestApp(t, clientsProjectRoute(false))

	err := clientsForbiddenError(t.Context(), app, 123, "123", basecamp.ErrForbidden("access denied"))

	var outErr *output.Error
	require.True(t, errors.As(err, &outErr))
	assert.Equal(t, output.CodeForbidden, outErr.Code)
	assert.Equal(t, "Clients are not enabled on project #123", outErr.Message)
	assert.Contains(t, outErr.Hint, "basecamp people clients enable --in 123")
}

func TestClientsForbiddenErrorFallsBackToPermission(t *testing.T) {
	app, _ := setupRecordingTestApp(t, clientsProjectRoute(true))

	err := clientsForbiddenError(t.Context(), app, 123, "123", basecamp.ErrForbidden("access denied"))

	var outErr *output.Error
	require.True(t, errors.As(err, &outErr))
	assert.Equal(t, output.CodeForbidden, outErr.Code)
	assert.Contains(t, outErr.Message, "permission to manage its people")
}

func TestClientsForbiddenErrorPassesOtherErrorsThrough(t *testing.T) {
	app, transport := setupRecordingTestApp(t)

	err := clientsForbiddenError(t.Context(), app, 123, "123", basecamp.ErrNotFound("project", "123"))

	var outErr *output.Error
	require.True(t, errors.As(err, &outErr))
	assert.Equal(t, output.CodeNotFound, outErr.Code)
	assert.Empty(t, transport.recorded(), "no project read-back for a non-403")
}

func TestClientSeatLimitErrorRemapsBare429(t *testing.T) {
	err := clientSeatLimitError(basecamp.ErrRateLimit(0))

	var outErr *output.Error
	require.True(t, errors.As(err, &outErr))
	assert.Equal(t, output.CodeLimitExceeded, outErr.Code)
	assert.False(t, outErr.Retryable)
	assert.Equal(t, output.ExitLimit, output.ExitCodeFor(outErr.Code))
	assert.Contains(t, outErr.Message, "Not enough seats")
}

func TestClientSeatLimitErrorKeepsRealThrottling(t *testing.T) {
	err := clientSeatLimitError(basecamp.ErrRateLimit(30))

	var outErr *output.Error
	require.True(t, errors.As(err, &outErr))
	assert.Equal(t, output.CodeRateLimit, outErr.Code)
	assert.True(t, outErr.Retryable)
}

const (
	clientUsersPath      = "/99999/projects/123/people/client_users.json"
	clientEnablementPath = "/99999/projects/123/client_enablement.json"
)

func clientUsersRoute(status int, body string) stubRoute {
	return stubRoute{method: http.MethodPut, path: clientUsersPath, status: status, body: body}
}

// accountPeopleRoute serves the account roster the person resolver reads.
func accountPeopleRoute() stubRoute {
	return stubRoute{
		method: http.MethodGet,
		path:   "/99999/people.json",
		status: http.StatusOK,
		body:   `[{"id":3001,"name":"Annie Bryan","email_address":"annie@springfield.example.com","client":true},{"id":1001,"name":"Zed Team","email_address":"zed@example.com"}]`,
		pages:  []string{`[{"id":3001,"name":"Annie Bryan","email_address":"annie@springfield.example.com","client":true},{"id":1001,"name":"Zed Team","email_address":"zed@example.com"}]`},
	}
}

func clientUsersBody(t *testing.T, transport *recordingTransport) map[string]any {
	t.Helper()
	for _, call := range transport.recorded() {
		if call.Method == http.MethodPut && call.Path == clientUsersPath {
			var body map[string]any
			require.NoError(t, json.Unmarshal([]byte(call.Body), &body))
			return body
		}
	}
	t.Fatalf("no PUT %s recorded: %+v", clientUsersPath, transport.recorded())
	return nil
}

func TestPeopleClientsAddGrantsResolvedIDs(t *testing.T) {
	app, transport, out := setupPersonalFeedApp(t, projectsRoute(), accountPeopleRoute(),
		clientUsersRoute(http.StatusOK, `{"granted":[{"id":3001,"name":"Annie Bryan","client":true}],"revoked":[]}`))

	require.NoError(t, executeRecordingCommand(NewPeopleCmd(), app, "clients", "add", "annie@springfield.example.com", "--in", "123"))

	body := clientUsersBody(t, transport)
	assert.Equal(t, []any{float64(3001)}, body["grant"])
	assert.NotContains(t, body, "revoke")
	assert.NotContains(t, body, "create")

	env := bubbleUpData(t, out)
	assert.Equal(t, "Added 1 client(s) to project #123", env["summary"])
}

func TestPeopleClientsAddReportsIDsTheServerDropped(t *testing.T) {
	app, _, out := setupPersonalFeedApp(t, projectsRoute(), accountPeopleRoute(),
		clientUsersRoute(http.StatusOK, `{"granted":[{"id":3001,"name":"Annie Bryan","client":true}],"revoked":[]}`))

	require.NoError(t, executeRecordingCommand(NewPeopleCmd(), app, "clients", "add", "3001", "1001", "1001", "--in", "123"))

	var envelope struct {
		Summary string `json:"summary"`
		Notice  string `json:"notice"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &envelope))
	assert.Equal(t, "Added 1 client(s) to project #123", envelope.Summary)
	assert.Equal(t, "Not added (already on the project, or not a client user): 1001", envelope.Notice)
}

func TestPeopleClientsRemoveRevokes(t *testing.T) {
	app, transport, out := setupPersonalFeedApp(t, projectsRoute(), accountPeopleRoute(),
		clientUsersRoute(http.StatusOK, `{"granted":[],"revoked":[{"id":3001,"name":"Annie Bryan","client":true}]}`))

	require.NoError(t, executeRecordingCommand(NewPeopleCmd(), app, "clients", "remove", "3001", "--in", "123"))

	body := clientUsersBody(t, transport)
	assert.Equal(t, []any{float64(3001)}, body["revoke"])
	assert.NotContains(t, body, "grant")

	env := bubbleUpData(t, out)
	assert.Equal(t, "Removed 1 client(s) from project #123", env["summary"])
}

func TestPeopleClientsInviteSendsCreateRows(t *testing.T) {
	app, transport, out := setupPersonalFeedApp(t, projectsRoute(),
		clientUsersRoute(http.StatusOK, `{"granted":[{"id":4001,"name":"Annie Bryan","client":true},{"id":4002,"name":"bob@example.com","client":true}],"revoked":[]}`))

	require.NoError(t, executeRecordingCommand(NewPeopleCmd(), app, "clients", "invite",
		"Annie Bryan <annie@example.com>", "bob@example.com", "--company", "Springfield Elementary", "--in", "123"))

	body := clientUsersBody(t, transport)
	assert.NotContains(t, body, "grant")
	assert.NotContains(t, body, "revoke")
	assert.Equal(t, []any{
		map[string]any{"name": "Annie Bryan", "email_address": "annie@example.com", "company_name": "Springfield Elementary"},
		map[string]any{"email_address": "bob@example.com", "company_name": "Springfield Elementary"},
	}, body["create"])

	env := bubbleUpData(t, out)
	assert.Equal(t, "Invited 2 client(s) to project #123", env["summary"])
}

func TestPeopleClientsInviteReadsStdin(t *testing.T) {
	app, transport, _ := setupPersonalFeedApp(t, projectsRoute(),
		clientUsersRoute(http.StatusOK, `{"granted":[],"revoked":[]}`))

	cmd := NewPeopleCmd()
	cmd.SetIn(strings.NewReader("annie@example.com\nBob Client <bob@example.com>\n"))
	require.NoError(t, executeRecordingCommand(cmd, app, "clients", "invite", "-", "--in", "123"))

	body := clientUsersBody(t, transport)
	assert.Equal(t, []any{
		map[string]any{"email_address": "annie@example.com"},
		map[string]any{"name": "Bob Client", "email_address": "bob@example.com"},
	}, body["create"])
}

func TestPeopleClientsInviteTitleTakesOneInvitee(t *testing.T) {
	app, transport, _ := setupPersonalFeedApp(t, projectsRoute())

	err := executeRecordingCommand(NewPeopleCmd(), app, "clients", "invite", "a@example.com", "b@example.com", "--title", "Owner", "--in", "123")
	requireBookmarksUsageError(t, err)
	assert.Empty(t, transport.recorded())

	app, transport, _ = setupPersonalFeedApp(t, projectsRoute(), clientUsersRoute(http.StatusOK, `{"granted":[],"revoked":[]}`))
	require.NoError(t, executeRecordingCommand(NewPeopleCmd(), app, "clients", "invite", "a@example.com", "--title", "Owner", "--in", "123"))
	body := clientUsersBody(t, transport)
	assert.Equal(t, []any{map[string]any{"email_address": "a@example.com", "title": "Owner"}}, body["create"])
}

// The wire 422 is row-keyed; the SDK folds it into FieldErrors by address, and
// the hint names each rejected row.
func TestPeopleClientsInviteNamesRejectedRows(t *testing.T) {
	app, _, _ := setupPersonalFeedApp(t, projectsRoute(),
		clientUsersRoute(http.StatusUnprocessableEntity, `{"errors":[{"email_address":"nope@example.com","messages":["Email address is invalid"]}]}`))

	err := executeRecordingCommand(NewPeopleCmd(), app, "clients", "invite", "ok@example.com", "nope@example.com", "--in", "123")

	var outErr *output.Error
	require.True(t, errors.As(err, &outErr), "got %T: %v", err, err)
	assert.Equal(t, output.CodeValidation, outErr.Code)
	assert.Equal(t, output.ExitValidation, output.ExitCodeFor(outErr.Code))
	assert.Contains(t, outErr.Message, "Invitation rejected")
	assert.Contains(t, outErr.Hint, "Nobody was invited")
	assert.Contains(t, outErr.Hint, "nope@example.com: Email address is invalid")
	assert.NotContains(t, outErr.Hint, "ok@example.com")
}

func TestClientInviteValidationErrorNamesEachRejectedRow(t *testing.T) {
	sdkErr := &basecamp.Error{
		Code:       basecamp.CodeValidation,
		HTTPStatus: 422,
		FieldErrors: map[string][]string{
			"nope@example.com": {"Email address is invalid"},
			"":                 {"Email address can't be blank"},
		},
	}

	err := clientInviteValidationError(sdkErr, []clientInvitee{{EmailAddress: "nope@example.com"}})

	var outErr *output.Error
	require.True(t, errors.As(err, &outErr))
	assert.Equal(t, output.CodeValidation, outErr.Code)
	assert.Contains(t, outErr.Hint, "nope@example.com: Email address is invalid")
	assert.Contains(t, outErr.Hint, ": Email address can't be blank")
}

func TestPeopleClientsInviteSeatLimit(t *testing.T) {
	app, _, _ := setupPersonalFeedApp(t, projectsRoute(), clientUsersRoute(http.StatusTooManyRequests, ``))

	err := executeRecordingCommand(NewPeopleCmd(), app, "clients", "invite", "a@example.com", "--in", "123")

	var outErr *output.Error
	require.True(t, errors.As(err, &outErr))
	assert.Equal(t, output.CodeLimitExceeded, outErr.Code)
	assert.False(t, outErr.Retryable)
}

func TestPeopleClientsAddWhenClientsAreOff(t *testing.T) {
	app, _, _ := setupPersonalFeedApp(t, projectsRoute(), accountPeopleRoute(), clientsProjectRoute(false),
		clientUsersRoute(http.StatusForbidden, ``))

	err := executeRecordingCommand(NewPeopleCmd(), app, "clients", "add", "3001", "--in", "123")

	var outErr *output.Error
	require.True(t, errors.As(err, &outErr))
	assert.Equal(t, output.CodeForbidden, outErr.Code)
	assert.Equal(t, "Clients are not enabled on project #123", outErr.Message)
	assert.Contains(t, outErr.Hint, "basecamp people clients enable --in 123")
}

func TestPeopleClientsEnable(t *testing.T) {
	app, transport, out := setupPersonalFeedApp(t, projectsRoute(),
		stubRoute{method: http.MethodPost, path: clientEnablementPath, status: http.StatusOK, body: `{"clients_enabled":true}`})

	require.NoError(t, executeRecordingCommand(NewPeopleCmd(), app, "clients", "enable", "--in", "123"))

	assert.Equal(t, http.MethodPost, transport.last(t).Method)
	assert.Equal(t, clientEnablementPath, transport.last(t).Path)
	env := bubbleUpData(t, out)
	assert.Equal(t, true, env["data"].(map[string]any)["clients_enabled"])
	assert.Equal(t, "Enabled clients on project #123", env["summary"])
}

func TestPeopleClientsDisable(t *testing.T) {
	app, transport, out := setupPersonalFeedApp(t, projectsRoute(),
		stubRoute{method: http.MethodDelete, path: clientEnablementPath, status: http.StatusOK, body: `{"clients_enabled":false}`})

	require.NoError(t, executeRecordingCommand(NewPeopleCmd(), app, "clients", "disable", "--in", "123"))

	assert.Equal(t, http.MethodDelete, transport.last(t).Method)
	env := bubbleUpData(t, out)
	assert.Equal(t, false, env["data"].(map[string]any)["clients_enabled"])
}

func TestPeopleClientsDisableNamesRemainingClients(t *testing.T) {
	app, _, _ := setupPersonalFeedApp(t, projectsRoute(), clientsProjectPeopleRoute(),
		stubRoute{method: http.MethodDelete, path: clientEnablementPath, status: http.StatusForbidden, body: ``})

	err := executeRecordingCommand(NewPeopleCmd(), app, "clients", "disable", "--in", "123")

	var outErr *output.Error
	require.True(t, errors.As(err, &outErr))
	assert.Equal(t, output.CodeForbidden, outErr.Code)
	assert.Contains(t, outErr.Message, "2 client(s) still have access")
	assert.Contains(t, outErr.Hint, "basecamp people clients remove 3002 3001 --in 123")
}

// With no client on the roster the 403 is not evidence that clients remain,
// so the message must not claim it.
func TestPeopleClientsDisableWithoutClientsIsAPlainDenial(t *testing.T) {
	roster := stubRoute{method: http.MethodGet, path: clientsProjectPeoplePath, status: http.StatusOK,
		body: `[{"id":1001,"name":"Zed Team","employee":true}]`, pages: []string{`[{"id":1001,"name":"Zed Team","employee":true}]`}}
	app, _, _ := setupPersonalFeedApp(t, projectsRoute(), roster,
		stubRoute{method: http.MethodDelete, path: clientEnablementPath, status: http.StatusForbidden, body: ``})

	err := executeRecordingCommand(NewPeopleCmd(), app, "clients", "disable", "--in", "123")

	var outErr *output.Error
	require.True(t, errors.As(err, &outErr))
	assert.Equal(t, output.CodeForbidden, outErr.Code)
	assert.NotContains(t, outErr.Message, "still have access")
	assert.Contains(t, outErr.Hint, "permission")
}

// A stray positional must not be discarded: with a default project configured,
// "disable 123" would otherwise act on the configured project.
func TestPeopleClientsProjectOnlyVerbsRejectPositionals(t *testing.T) {
	for _, verb := range []string{"list", "enable", "disable"} {
		app, transport, _ := setupPersonalFeedApp(t, projectsRoute())
		app.Config.ProjectID = "123"

		err := executeRecordingCommand(NewPeopleCmd(), app, "clients", verb, "456")
		require.Error(t, err, verb)
		assert.Empty(t, transport.recorded(), "%s must not reach the API", verb)
	}
}
