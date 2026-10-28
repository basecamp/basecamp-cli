package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
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

	// Longest suffix wins, so /projects/999 is not answered as /projects. A
	// key with a query answers only a request with that query.
	best := ""
	for suffix := range m.gets {
		target := path
		if strings.Contains(suffix, "?") {
			target = path + "?" + req.URL.RawQuery
		}
		if strings.HasSuffix(target, suffix) && len(suffix) > len(best) {
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
		plural:   "message boards",
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

// Appending "s" to a container's noun once named a project's To-dos tools
// "To-doss", so every filingContainer literal spells its plural out. The test
// reads the package's source, so it covers containers no test resolves.
func TestEveryFilingContainerNamesItsPlural(t *testing.T) {
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	fset := token.NewFileSet()
	var containers int
	for _, name := range files {
		src, err := os.ReadFile(name)
		require.NoError(t, err)
		file, err := parser.ParseFile(fset, name, src, 0)
		require.NoError(t, err)
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}
			if typ, ok := lit.Type.(*ast.Ident); !ok || typ.Name != "filingContainer" {
				return true
			}
			containers++
			named := slices.ContainsFunc(lit.Elts, func(e ast.Expr) bool {
				kv, ok := e.(*ast.KeyValueExpr)
				if !ok {
					return false
				}
				key, ok := kv.Key.(*ast.Ident)
				value, isString := kv.Value.(*ast.BasicLit)
				return ok && key.Name == "plural" && isString && value.Value != `""`
			})
			assert.True(t, named, "%s: a filingContainer sets plural", fset.Position(lit.Pos()))
			return true
		})
	}
	assert.GreaterOrEqual(t, containers, 7, "the scan finds every family's container")
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
		lookFor:       copyCheck{listing: "basecamp files list --in 999", match: "look for the copy"},
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

// A still-running item's line says once not to file it again, and names the
// filing that shows where it got to.
func TestBulkFilingStillRunningSaysNotToFileAgainOnce(t *testing.T) {
	for _, kind := range []basecamp.FilingType{basecamp.FilingMove, basecamp.FilingCopy} {
		t.Run(string(kind), func(t *testing.T) {
			shortFilingWait(t)
			transport := &familyTransport{stuck: map[int64]bool{11: true, 22: true}}
			e := runTestFilings(t, transport, kind, 11, 22).failure(t)
			lines := strings.Split(e.Hint, "\n")
			require.Len(t, lines, 2, e.Hint)
			for i, id := range []int{11, 22} {
				assert.True(t, strings.HasPrefix(lines[i], fmt.Sprintf("#%d: ", id)), lines[i])
				assert.Equal(t, 1, strings.Count(lines[i], "Do not"), lines[i])
				assert.Contains(t, lines[i], fmt.Sprintf("basecamp filings show %d", id))
			}
		})
	}
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

// A copy whose start went unanswered has no filing to read, and keeps its
// source's status, so it is looked for among recordings in that status, every
// page; a type the recordings listing does not serve is looked for in its own
// listing, every page, or in the timeline when that listing leaves its status
// out, and where no listing shows that status, in the destination in Basecamp.
func TestCopyRecoveryListsWhereTheCopyShows(t *testing.T) {
	for name, tc := range map[string]struct {
		transport func() *familyTransport
		cmd       func() *cobra.Command
		source    string
		status    string
		want      string
	}{
		"a message":                     {messagesTransport, NewMessagesCmd, "/messages/456", "", "basecamp recordings list Message --in 999 --status active --all"},
		"an archived message":           {messagesTransport, NewMessagesCmd, "/messages/456", "archived", "basecamp recordings list Message --in 999 --status archived --all"},
		"an event":                      {scheduleFilingTransport, NewScheduleCmd, "/schedule_entries/456", "", "basecamp recordings list Schedule::Entry --in 999 --status active --all"},
		"an archived event":             {scheduleFilingTransport, NewScheduleCmd, "/schedule_entries/456", "archived", "basecamp recordings list Schedule::Entry --in 999 --status archived --all"},
		"a check-in question":           {checkinsFilingTransport, NewCheckinsCmd, "/questions/456", "", "basecamp checkins questions --in 999 --questionnaire 900 --all"},
		"an archived check-in question": {checkinsFilingTransport, NewCheckinsCmd, "/questions/456", "archived", "basecamp timeline --in 999 --all"},
		"a trashed check-in question":   {checkinsFilingTransport, NewCheckinsCmd, "/questions/456", "trashed", "open the destination in Basecamp: https://3.basecamp.com/99999/buckets/999/questionnaires/900"},
		"an email forward":              {forwardsFilingTransport, NewForwardsCmd, "/inbox_forwards/456", "", "basecamp forwards list --in 999 --inbox 900 --all"},
		"an archived email forward":     {forwardsFilingTransport, NewForwardsCmd, "/inbox_forwards/456", "archived", "open the destination in Basecamp: https://3.basecamp.com/99999/buckets/999/inboxes/900"},
		"a trashed email forward":       {forwardsFilingTransport, NewForwardsCmd, "/inbox_forwards/456", "trashed", "open the destination in Basecamp: https://3.basecamp.com/99999/buckets/999/inboxes/900"},
		"a card table column":           {columnsTransport, NewCardsCmd, "/card_tables/columns/456", "", "basecamp cards columns --in 999 --card-table 900"},
		"an archived card table column": {columnsTransport, NewCardsCmd, "/card_tables/columns/456", "archived", "open the destination in Basecamp: https://3.basecamp.com/99999/buckets/999/card_tables/900"},
	} {
		t.Run(name, func(t *testing.T) {
			transport := tc.transport()
			if tc.status != "" {
				transport.gets[tc.source] = strings.Replace(transport.gets[tc.source], `"id":456,`, `"id":456,"status":"`+tc.status+`",`, 1)
			}
			transport.refuse = map[int64]int{456: 502}
			args := []string{"copy", "456", "--to-project", "Dest"}
			switch {
			case strings.Contains(name, "check-in"):
				args = append([]string{"question"}, args...)
			case strings.Contains(name, "column"):
				args = append([]string{"column"}, args...)
			}
			e := runFamilyCommand(t, transport, tc.cmd(), args...).failure(t)
			assert.Contains(t, e.Hint, "may have started the copy")
			assert.Contains(t, e.Hint, tc.want)
		})
	}
}

// Every recording a move or copy files, in every status a copy can keep, is
// looked for in a listing known to show it there, or at the destination's page
// in Basecamp. Each listing below carries the places it is known to show; a
// recovery naming anything else, or a listing for a place it hides, fails.
// Each listing carries the run's --profile and --account, so it searches the
// account the copy went to; the destination's page needs neither.
func TestEveryCopyRecoveryCanShowTheCopy(t *testing.T) {
	const scope = " --profile work --account 99999"
	dest := filingTarget{id: 7, title: "Dest tool", appURL: "https://3.basecamp.com/99999/buckets/42/vaults/7", projectID: 42}
	listedStatuses := []string{"active", "archived", "trashed"}
	// bc3 doc/api/sections/recordings.md: the types GET /projects/recordings.json takes.
	recordingsTypes := []string{"Comment", "Document", "Door", "Kanban::Card", "Kanban::Step", "Message", "Question::Answer", "Schedule::Entry", "Todo", "Todolist", "Upload", "Vault"}
	type knownListing struct {
		listing *regexp.Regexp
		shows   func(place copyPlace, match []string) bool
	}
	knownGood := []knownListing{
		{regexp.MustCompile(`^basecamp recordings list (\S+) --in 42 --status (\S+) --all$`),
			func(p copyPlace, m []string) bool {
				return !p.calendar && m[1] == p.typ && m[2] == p.status && slices.Contains(recordingsTypes, p.typ) && slices.Contains(listedStatuses, p.status)
			}},
		// drafts.md: messages, documents and uploads, and no other drafts.
		{regexp.MustCompile(`^basecamp drafts list --all$`),
			func(p copyPlace, _ []string) bool {
				return p.status == "drafted" && slices.Contains([]string{"Message", "Document", "Upload"}, p.typ)
			}},
		// schedule_entries.md: a schedule's entries by status, active, archived or trashed.
		{regexp.MustCompile(`^basecamp schedule entries --in 42 --schedule 7 --status (\S+) --all$`),
			func(p copyPlace, m []string) bool {
				return p.typ == "Schedule::Entry" && m[1] == p.status && slices.Contains(listedStatuses, p.status)
			}},
		// Seen against bc3: a copied cloud file (active or archived) and an
		// archived question show as reposted; nothing trashed or drafted does.
		{regexp.MustCompile(`^basecamp timeline --in 42 --all$`),
			func(p copyPlace, _ []string) bool {
				return !p.calendar && (p.typ == "CloudFile" && (p.status == "active" || p.status == "archived") ||
					p.typ == "Question" && p.status == "archived")
			}},
		// These list only recordings that inherit their status: active ones.
		{regexp.MustCompile(`^basecamp checkins questions --in 42 --questionnaire 7 --all$`),
			func(p copyPlace, _ []string) bool { return !p.calendar && p.typ == "Question" && p.status == "active" }},
		{regexp.MustCompile(`^basecamp forwards list --in 42 --inbox 7 --all$`),
			func(p copyPlace, _ []string) bool {
				return !p.calendar && p.typ == "Inbox::Forward" && p.status == "active"
			}},
		{regexp.MustCompile(`^basecamp cards columns --in 42 --card-table 7$`),
			func(p copyPlace, _ []string) bool {
				return !p.calendar && p.typ == "Kanban::Column" && p.status == "active"
			}},
	}

	types := []string{"Todo", "Todolist", "Kanban::Card"}
	for _, fam := range []filingFamily{messageFamily(), filesFamily(), columnFamily(), scheduleEntryFamily(), questionFamily(), forwardFamily()} {
		types = append(types, fam.types...)
	}
	statuses := make([]string, 0, 4+len(copyListings))
	statuses = append(statuses, "active", "archived", "trashed", "drafted")
	for place := range copyListings {
		types, statuses = append(types, place.typ), append(statuses, place.status)
	}
	listed := map[copyPlace]bool{}
	for _, typ := range types {
		for _, status := range statuses {
			for _, calendar := range []bool{false, true} {
				place := copyPlace{typ: typ, status: status, calendar: calendar}
				target := dest
				target.calendar = calendar
				hint, capped := strings.CutSuffix(copyLookFor(filingSource{id: 456, typ: typ, status: status, title: "Plan"}, target).text(scope),
					". That listing stops at the most recently updated, so if the copy is not there, open the destination in Basecamp before copying again: "+dest.appURL)
				hint, matched := strings.CutSuffix(hint, `, then look for "Plan" in Dest tool (#7); a copy has its original's title and dates`)
				require.True(t, matched, "%+v: %q names the copy by its title in the destination", place, hint)
				if hint == "open the destination in Basecamp: "+dest.appURL {
					continue
				}
				hint, matched = strings.CutSuffix(hint, scope)
				require.True(t, matched, "%+v: %q carries the profile and account to reach this account", place, hint)
				listed[place] = true
				// The drafts listing holds only the 250 most recently updated,
				// and a copy keeps its original's updated_at.
				assert.Equal(t, strings.HasPrefix(hint, "basecamp drafts list"), capped,
					"%+v: %q sends a capped listing's miss to the destination", place, hint)
				shown := slices.ContainsFunc(knownGood, func(good knownListing) bool {
					match := good.listing.FindStringSubmatch(hint)
					return match != nil && good.shows(place, match)
				})
				assert.True(t, shown, "%+v: %q is not known to show this copy", place, hint)
			}
		}
	}
	assert.Len(t, listed, len(copyListings), "every listing in the table was checked")
}

// A filing's breadcrumbs name a show command for the record, which reads it by
// its account-wide ID, so each runs with no project configured and names the
// record's own project in its breadcrumbs.
func TestFilingAdvertisedShowCommandsNeedNoProject(t *testing.T) {
	families := []struct {
		group     string
		transport func() *familyTransport
		root      func() *cobra.Command
		move      []string
	}{
		{"messages", messagesTransport, NewMessagesCmd, []string{"move"}},
		{"files", filesFilingTransport, NewFilesCmd, []string{"move"}},
		{"schedule", scheduleFilingTransport, NewScheduleCmd, []string{"move"}},
		{"cards column", columnsTransport, NewCardsCmd, []string{"column", "move"}},
		{"checkins question", checkinsFilingTransport, NewCheckinsCmd, []string{"question", "move"}},
		{"forwards", forwardsFilingTransport, NewForwardsCmd, []string{"move"}},
	}
	showCmd := regexp.MustCompile(`basecamp [a-z ]+? show \d+( --in \d+)?`)
	for _, f := range families {
		t.Run(f.group, func(t *testing.T) {
			args := append(slices.Clone(f.move), "456", "--to-project", "Dest")
			transport := f.transport()
			run := runFamilyCommand(t, transport, f.root(), args...)
			require.NoError(t, run.err)

			line := run.envelope.Breadcrumbs[0].Cmd
			require.Regexp(t, showCmd, line)
			app, buf := newTestAppWithTransport(t, transport)
			app.Config.ProjectID = ""
			require.NoError(t, executeCommand(f.root(), app, strings.Fields(line)[2:]...), line)

			var shown familyRun
			require.NoError(t, json.Unmarshal(buf.Bytes(), &shown.envelope), line)
			for _, crumb := range shown.envelope.Breadcrumbs {
				assert.NotContains(t, crumb.Cmd, "<project>", line)
			}
		})
	}
}

// A move or copy still running when the wait ends is checked by its filing,
// which says where it got to whatever the recording's type and status, rather
// than by a listing that may not show it.
func TestStillFilingNamesTheFilingForEveryFamily(t *testing.T) {
	type family struct {
		run    func(t *testing.T, verb string) error
		filing string
	}
	familyRun := func(transport func() *familyTransport, root func() *cobra.Command, prefix ...string) func(*testing.T, string) error {
		return func(t *testing.T, verb string) error {
			tr := transport()
			tr.stuck = map[int64]bool{456: true}
			args := append(slices.Clone(prefix), verb, "456", "--to-project", "Dest")
			return runFamilyCommand(t, tr, root(), args...).err
		}
	}
	families := map[string]family{
		"cards": {func(t *testing.T, verb string) error {
			return runFilingCommand(t, &mockFilingTransport{statuses: []string{"processing"}}, verb, "456", "--to-project", "Dest").err
		}, "70"},
		"to-dos": {func(t *testing.T, verb string) error {
			transport := &mockTodoFilingTransport{stuck: map[string]bool{"456": true}}
			return runTodoFilingCommand(t, transport, NewTodosCmd(), verb, "456", "--to-list", "901").err
		}, "456"},
		"messages":           {familyRun(messagesTransport, NewMessagesCmd), "456"},
		"files":              {familyRun(filesFilingTransport, NewFilesCmd), "456"},
		"columns":            {familyRun(columnsTransport, NewCardsCmd, "column"), "456"},
		"schedule entries":   {familyRun(scheduleFilingTransport, NewScheduleCmd), "456"},
		"check-in questions": {familyRun(checkinsFilingTransport, NewCheckinsCmd, "question"), "456"},
		"email forwards":     {familyRun(forwardsFilingTransport, NewForwardsCmd), "456"},
	}
	for name, f := range families {
		for _, verb := range []string{"move", "copy"} {
			t.Run(name+" "+verb, func(t *testing.T) {
				err := f.run(t, verb)
				require.Error(t, err)
				e := output.AsError(err)
				assert.Equal(t, output.CodeInProgress, e.Code)
				assert.Contains(t, e.Hint, "Do not "+verb+" it again")
				assert.Contains(t, e.Hint, "Check it with: basecamp filings show "+f.filing+",")
				assert.NotContains(t, e.Hint, " list")
				assert.NotContains(t, e.Hint, "timeline")
			})
		}
	}
}
