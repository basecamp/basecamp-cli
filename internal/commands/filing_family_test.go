package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// familyTransport serves JSON by path suffix and plays Basecamp's recording
// moves and copies: a POST to /recordings/<id>/moves or /copies answers a
// pending filing numbered <id>, whose GET completes with recording <id>0
// (or a still-processing filing for the ids in stuck). A source in refuse
// answers its POST with that status.
type familyTransport struct {
	gets   map[string]string
	refuse map[int64]int
	stuck  map[int64]bool

	mu     sync.Mutex
	posts  []string
	bodies []map[string]any
}

func (m *familyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	path := strings.TrimSuffix(req.URL.Path, ".json")
	respond := func(status int, body string) (*http.Response, error) {
		header := make(http.Header)
		header.Set("Content-Type", "application/json")
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: header}, nil
	}

	if req.Method == http.MethodPost {
		m.posts = append(m.posts, path)
		body := map[string]any{}
		if req.Body != nil {
			raw, _ := io.ReadAll(req.Body)
			_ = json.Unmarshal(raw, &body)
		}
		m.bodies = append(m.bodies, body)
		parts := strings.Split(path, "/")
		if len(parts) < 2 {
			return respond(404, `{"error":"not found"}`)
		}
		source, _ := strconv.ParseInt(parts[len(parts)-2], 10, 64)
		if status := m.refuse[source]; status != 0 {
			return respond(status, `{"error":"refused"}`)
		}
		kind := "Move"
		if strings.HasSuffix(path, "/copies") {
			kind = "Copy"
		}
		return respond(201, fmt.Sprintf(`{"id":%d,"status":"pending","type":%q,"source_recording_id":%d}`, source, kind, source))
	}

	if i := strings.Index(path, "/filings/"); i >= 0 {
		id, _ := strconv.ParseInt(path[i+len("/filings/"):], 10, 64)
		if m.stuck[id] {
			return respond(200, fmt.Sprintf(`{"id":%d,"status":"processing","type":"Move","source_recording_id":%d}`, id, id))
		}
		return respond(200, fmt.Sprintf(`{"id":%d,"status":"completed","type":"Move","source_recording_id":%d,"destination_recording":{"id":%d0}}`, id, id, id))
	}

	// Longest suffix wins, so /projects/999 is not answered as /projects.
	best := ""
	for suffix := range m.gets {
		if strings.HasSuffix(path, suffix) && len(suffix) > len(best) {
			best = suffix
		}
	}
	if best != "" {
		return respond(200, m.gets[best])
	}
	return respond(404, `{"error":"not found"}`)
}

// familyRun is one command run against a familyTransport.
type familyRun struct {
	err      error
	envelope struct {
		Data        json.RawMessage `json:"data"`
		Summary     string          `json:"summary"`
		Breadcrumbs []struct {
			Cmd string `json:"cmd"`
		} `json:"breadcrumbs"`
	}
}

func (r familyRun) data(t *testing.T) map[string]any {
	t.Helper()
	var data map[string]any
	require.NoError(t, json.Unmarshal(r.envelope.Data, &data))
	return data
}

func (r familyRun) failure(t *testing.T) *output.Error {
	t.Helper()
	require.Error(t, r.err)
	return output.AsError(r.err)
}

func runFamilyCommand(t *testing.T, transport *familyTransport, cmd *cobra.Command, args ...string) familyRun {
	t.Helper()
	shortFilingWait(t)
	app, buf := newTestAppWithTransport(t, transport)
	app.Flags.Hints = true
	var run familyRun
	run.err = executeCommand(cmd, app, args...)
	if run.err == nil {
		require.NoError(t, json.Unmarshal(buf.Bytes(), &run.envelope))
	}
	return run
}

// filingProjects are the projects every family test files between: 123
// "Source", 999 "Dest", and 888 "Bare", whose dock comes from each test.
const filingProjects = `[{"id":123,"name":"Source"},{"id":999,"name":"Dest"},{"id":888,"name":"Bare"}]`

func boardsContainer() filingContainer {
	return filingContainer{
		dockName: "message_board",
		noun:     "message board",
		flag:     "--to-board",
		urlKind:  "message_boards",
		urlShape: "…/message_boards/<id>",
		get: func(ctx context.Context, app *appctx.App, id int64) (filingTarget, error) {
			return filingTarget{id: id, title: "Board", projectID: 999, projectName: "Dest"}, nil
		},
	}
}

func resolveTestTarget(t *testing.T, transport *familyTransport, toProject, toContainer string) (filingTarget, error) {
	t.Helper()
	var target filingTarget
	cmd := &cobra.Command{RunE: func(cmd *cobra.Command, _ []string) error {
		var err error
		target, err = resolveFilingTarget(cmd, appctx.FromContext(cmd.Context()), boardsContainer(), toProject, toContainer)
		return err
	}}
	app, _ := newTestAppWithTransport(t, transport)
	err := executeCommand(cmd, app)
	return target, err
}

func dockTransport(dock string) *familyTransport {
	return &familyTransport{gets: map[string]string{
		"/projects":     filingProjects,
		"/projects/999": `{"id":999,"name":"Dest","dock":[` + dock + `]}`,
	}}
}

func TestFilingTargetIsTheProjectsOnlyEnabledContainer(t *testing.T) {
	transport := dockTransport(`{"name":"message_board","id":900,"title":"Board","enabled":true},{"name":"message_board","id":901,"title":"Old","enabled":false}`)
	target, err := resolveTestTarget(t, transport, "Dest", "")
	require.NoError(t, err)
	assert.Equal(t, filingTarget{id: 900, title: "Board", projectID: 999, projectName: "Dest"}, target)
}

func TestFilingTargetAmongSeveralIsAmbiguousAndListsThem(t *testing.T) {
	transport := dockTransport(`{"name":"message_board","id":900,"title":"Board","enabled":true},{"name":"message_board","id":950,"title":"News","enabled":true}`)
	_, err := resolveTestTarget(t, transport, "Dest", "")
	e := output.AsError(err)
	assert.Equal(t, output.CodeAmbiguous, e.Code)
	assert.Contains(t, e.Hint, "--to-board <id>")
	assert.Contains(t, e.Hint, "900  Board")
	assert.Contains(t, e.Hint, "950  News")
}

func TestFilingTargetNamedByATitleTwoShareIsAmbiguous(t *testing.T) {
	transport := dockTransport(`{"name":"message_board","id":900,"title":"Board","enabled":true},{"name":"message_board","id":950,"title":"board","enabled":true},{"name":"message_board","id":960,"title":"News","enabled":true}`)
	_, err := resolveTestTarget(t, transport, "Dest", "Board")
	e := output.AsError(err)
	assert.Equal(t, output.CodeAmbiguous, e.Code)
	assert.Contains(t, e.Hint, "950  board")
	assert.NotContains(t, e.Hint, "960", "only the containers with that title are candidates")
}

func TestFilingTargetByTitleOrID(t *testing.T) {
	transport := dockTransport(`{"name":"message_board","id":900,"title":"Board","enabled":true},{"name":"message_board","id":950,"title":"News","enabled":true}`)
	byTitle, err := resolveTestTarget(t, transport, "Dest", "news")
	require.NoError(t, err)
	assert.Equal(t, int64(950), byTitle.id)
	byID, err := resolveTestTarget(t, transport, "Dest", "900")
	require.NoError(t, err)
	assert.Equal(t, int64(900), byID.id)
}

func TestFilingTargetNotInTheProjectListsWhatIs(t *testing.T) {
	transport := dockTransport(`{"name":"message_board","id":900,"title":"Board","enabled":true}`)
	_, err := resolveTestTarget(t, transport, "Dest", "Announcements")
	e := output.AsError(err)
	assert.Equal(t, `--to-board "Announcements" is not a message board in Dest`, e.Message)
	assert.Contains(t, e.Hint, "900 (Board)")
}

func TestFilingTargetThatIsTurnedOffSaysHowToTurnItOn(t *testing.T) {
	transport := dockTransport(`{"name":"message_board","id":901,"title":"Old","enabled":false}`)
	_, err := resolveTestTarget(t, transport, "Dest", "")
	e := output.AsError(err)
	assert.Equal(t, output.CodeNotFound, e.Code)
	assert.Equal(t, "Dest's message board (Old) is turned off, so nothing can be moved or copied into it", e.Message)
	assert.Equal(t, "Turn it on first with: basecamp tools enable 901 --in 999", e.Hint)
}

func TestFilingTargetInAProjectWithoutOneNamesTheProject(t *testing.T) {
	transport := dockTransport(`{"name":"chat","id":700,"title":"Chat","enabled":true}`)
	_, err := resolveTestTarget(t, transport, "Dest", "")
	e := output.AsError(err)
	assert.Equal(t, output.CodeNotFound, e.Code)
	assert.Equal(t, "Project Dest has no message board", e.Message)
}

func TestFilingTargetURLMustAgreeWithToProject(t *testing.T) {
	transport := dockTransport(`{"name":"message_board","id":900,"title":"Board","enabled":true}`)
	_, err := resolveTestTarget(t, transport, "Dest", "https://3.basecamp.com/99999/buckets/123/message_boards/900")
	e := output.AsError(err)
	assert.Equal(t, output.CodeUsage, e.Code)
	assert.Contains(t, e.Message, "--to-project points at project 999")
}

func TestFilingTargetURLFromAnotherAccountIsRefused(t *testing.T) {
	transport := dockTransport(``)
	_, err := resolveTestTarget(t, transport, "", "https://3.basecamp.com/11111/buckets/999/message_boards/900")
	e := output.AsError(err)
	assert.Contains(t, e.Message, "URL is for account 11111")
}

func TestFilingTargetByIDAloneReadsTheContainer(t *testing.T) {
	transport := dockTransport(``)
	target, err := resolveTestTarget(t, transport, "", "https://3.basecamp.com/99999/buckets/999/message_boards/900")
	require.NoError(t, err)
	assert.Equal(t, int64(900), target.id)
	assert.Empty(t, transport.posts)
}

func testFiling(app *appctx.App, kind basecamp.FilingType, sourceID int64) recordingFiling {
	return recordingFiling{
		kind:          kind,
		sourceID:      sourceID,
		noun:          "file",
		destinationID: 900,
		parentID:      950,
		destination:   "Docs & Files 900 in Dest",
		showCmd:       "basecamp files show",
		lookFor:       "basecamp files list --in 999",
		landing:       "It lands in the folder",
		read: func(_ context.Context, id int64) (filedRecording, error) {
			return filedRecording{
				result: map[string]any{"id": id, "source_id": sourceID},
				id:     id,
				bucket: &basecamp.Bucket{ID: 999, Name: "Dest"},
				parent: &basecamp.Parent{ID: 950, Title: "Folder"},
			}, nil
		},
	}
}

func runTestFilings(t *testing.T, transport *familyTransport, kind basecamp.FilingType, sources ...int64) familyRun {
	t.Helper()
	cmd := &cobra.Command{RunE: func(cmd *cobra.Command, _ []string) error {
		app := appctx.FromContext(cmd.Context())
		filings := make([]recordingFiling, len(sources))
		for i, source := range sources {
			filings[i] = testFiling(app, kind, source)
		}
		return runFilings(cmd, app, followUpScope{}, filings)
	}}
	return runFamilyCommand(t, transport, cmd)
}

func TestBulkFilingFilesEachAndListsTheNewRecordings(t *testing.T) {
	transport := &familyTransport{}
	run := runTestFilings(t, transport, basecamp.FilingCopy, 11, 22)
	require.NoError(t, run.err)

	assert.Equal(t, []string{"/99999/recordings/11/copies", "/99999/recordings/22/copies"}, transport.posts)
	assert.Equal(t, float64(950), transport.bodies[0]["parent_id"], "a folder inside the destination goes as parent_id")
	var data []map[string]any
	require.NoError(t, json.Unmarshal(run.envelope.Data, &data))
	require.Len(t, data, 2)
	assert.Equal(t, float64(110), data[0]["id"])
	assert.Equal(t, float64(22), data[1]["source_id"])
	assert.Equal(t, "Copied 2 files: #11 as #110 in Dest › Folder, #22 as #220 in Dest › Folder", run.envelope.Summary)
}

func TestSingleFilingOutputsTheRecordingItself(t *testing.T) {
	transport := &familyTransport{}
	run := runTestFilings(t, transport, basecamp.FilingMove, 11)
	require.NoError(t, run.err)
	assert.Equal(t, float64(110), run.data(t)["id"])
	assert.Equal(t, float64(950), transport.bodies[0]["parent_id"])
}

func TestWithSourceIDKeepsLargeIDsExact(t *testing.T) {
	raw, err := json.Marshal(withSourceID(map[string]any{"id": int64(9007199254740993)}, 7))
	require.NoError(t, err)
	assert.JSONEq(t, `{"id":9007199254740993,"source_id":7}`, string(raw))
}

// A copy keeps its source's status, so a copy that may have landed is looked
// for among recordings in that status, every page; a type the recordings
// listing does not serve is looked for in its own listing, every page, or in
// the timeline when that listing leaves its status out.
func TestCopyRecoveryListsWhereTheCopyShows(t *testing.T) {
	for name, tc := range map[string]struct {
		transport func() *familyTransport
		cmd       func() *cobra.Command
		source    string
		archived  bool
		want      string
	}{
		"a message":                     {messagesTransport, NewMessagesCmd, "/messages/456", false, "basecamp recordings list Message --in 999 --status active --sort created_at --direction desc --all"},
		"an archived message":           {messagesTransport, NewMessagesCmd, "/messages/456", true, "basecamp recordings list Message --in 999 --status archived --sort created_at --direction desc --all"},
		"an event":                      {scheduleFilingTransport, NewScheduleCmd, "/schedule_entries/456", false, "basecamp recordings list Schedule::Entry --in 999 --status active --sort created_at --direction desc --all"},
		"an archived event":             {scheduleFilingTransport, NewScheduleCmd, "/schedule_entries/456", true, "basecamp recordings list Schedule::Entry --in 999 --status archived --sort created_at --direction desc --all"},
		"a check-in question":           {checkinsFilingTransport, NewCheckinsCmd, "/questions/456", false, "basecamp checkins questions --in 999 --questionnaire 900 --all"},
		"an archived check-in question": {checkinsFilingTransport, NewCheckinsCmd, "/questions/456", true, "basecamp timeline --in 999 --all"},
		"an email forward":              {forwardsFilingTransport, NewForwardsCmd, "/inbox_forwards/456", false, "basecamp forwards list --in 999 --inbox 900 --all"},
		"an archived email forward":     {forwardsFilingTransport, NewForwardsCmd, "/inbox_forwards/456", true, "basecamp timeline --in 999 --all"},
		"a card table column":           {columnsTransport, NewCardsCmd, "/card_tables/columns/456", false, "basecamp cards columns --in 999 --card-table 900"},
	} {
		t.Run(name, func(t *testing.T) {
			transport := tc.transport()
			if tc.archived {
				transport.gets[tc.source] = strings.Replace(transport.gets[tc.source], `"id":456,`, `"id":456,"status":"archived",`, 1)
			}
			transport.stuck = map[int64]bool{456: true}
			args := []string{"copy", "456", "--to-project", "Dest"}
			switch {
			case strings.Contains(name, "check-in"):
				args = append([]string{"question"}, args...)
			case strings.Contains(name, "column"):
				args = append([]string{"column"}, args...)
			}
			e := runFamilyCommand(t, transport, tc.cmd(), args...).failure(t)
			assert.Equal(t, output.CodeInProgress, e.Code)
			assert.Contains(t, e.Hint, tc.want)
		})
	}
}
