package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/output"
	"github.com/basecamp/basecamp-cli/internal/resilience"
)

func shortFilingWait(t *testing.T) {
	t.Helper()
	saved := filingWait
	filingWait.first = time.Millisecond
	filingWait.max = 2 * time.Millisecond
	filingWait.total = 50 * time.Millisecond
	t.Cleanup(func() { filingWait = saved })
}

func TestPollUntilStopsWhenDone(t *testing.T) {
	shortFilingWait(t)
	checks := 0
	done, err := pollUntil(context.Background(), func(context.Context) (bool, error) {
		checks++
		return checks == 3, nil
	})
	require.NoError(t, err)
	assert.True(t, done)
	assert.Equal(t, 3, checks)
}

func TestPollUntilGivesUpAfterTotal(t *testing.T) {
	shortFilingWait(t)
	start := time.Now()
	done, err := pollUntil(context.Background(), func(context.Context) (bool, error) { return false, nil })
	require.NoError(t, err)
	assert.False(t, done)
	assert.Less(t, time.Since(start), time.Second)
}

func TestPollUntilHonoursCancellation(t *testing.T) {
	shortFilingWait(t)
	filingWait.total = time.Minute
	ctx, cancel := context.WithCancel(context.Background())
	checks := 0
	done, err := pollUntil(ctx, func(context.Context) (bool, error) {
		checks++
		if checks == 2 {
			cancel()
		}
		return false, nil
	})
	assert.ErrorIs(t, err, context.Canceled)
	assert.False(t, done)
	assert.Equal(t, 2, checks)
}

// mockFilingTransport serves a card in one project and two other projects to
// move or copy it into, and plays back a filing's statuses.
//
//   - Card 456 is in project 123 ("Source"), column 777 on card table 555.
//   - Project 999 ("Dest") has card table 900 (Triage 901); with twoTables it
//     also has card table 950 ("Roadmap"), and always a disabled card table 960.
//   - A move to a column 404s, as for a column in another project.
//   - A move or copy POST answers filing 70, pending. Each GET of the filing
//     takes the next of statuses, holding the last; "completed" names card 4560.
type mockFilingTransport struct {
	twoTables      bool
	duplicateTitle bool
	postStatus     int
	postErr        error
	postBody       string
	statuses       []string

	mu        sync.Mutex
	requests  int
	posts     []string
	body      map[string]any
	filingGet int
}

func (m *mockFilingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests++
	path := strings.TrimSuffix(req.URL.Path, ".json")
	respond := func(status int, body string) (*http.Response, error) {
		header := make(http.Header)
		header.Set("Content-Type", "application/json")
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: header}, nil
	}

	if req.Method == http.MethodPost {
		m.posts = append(m.posts, path)
		if req.Body != nil {
			raw, _ := io.ReadAll(req.Body)
			m.body = map[string]any{}
			_ = json.Unmarshal(raw, &m.body)
		}
		if m.postErr != nil {
			return nil, m.postErr
		}
		if m.postBody != "" {
			return respond(201, m.postBody)
		}
		if m.postStatus != 0 {
			return respond(m.postStatus, `{"error":"upstream"}`)
		}
		if strings.HasSuffix(path, "/card_tables/cards/456/moves") {
			// Basecamp finds column_id only in the card's own project.
			return respond(404, `{"error":"not found"}`)
		}
		kind := "Move"
		if strings.HasSuffix(path, "/copies") {
			kind = "Copy"
		}
		return respond(201, m.filingJSON("pending", kind))
	}

	switch {
	case strings.HasSuffix(path, "/projects"):
		return respond(200, `[{"id":123,"name":"Source"},{"id":999,"name":"Dest"},{"id":888,"name":"Bare"}]`)
	case strings.HasSuffix(path, "/projects/888"):
		return respond(200, `{"id":888,"dock":[{"name":"kanban_board","id":880,"title":"Off","enabled":false}]}`)
	case strings.HasSuffix(path, "/projects/123"):
		return respond(200, `{"id":123,"dock":[{"name":"kanban_board","id":555,"title":"Board","enabled":true}]}`)
	case strings.HasSuffix(path, "/projects/999"):
		dock := `{"name":"kanban_board","id":900,"title":"Ideas","enabled":true},{"name":"kanban_board","id":960,"title":"Old","enabled":false}`
		if m.twoTables {
			dock += `,{"name":"kanban_board","id":950,"title":"Roadmap","enabled":true}`
		}
		if m.duplicateTitle {
			dock += `,{"name":"kanban_board","id":970,"title":"roadmap","enabled":true}`
		}
		return respond(200, `{"id":999,"dock":[`+dock+`]}`)
	case strings.HasSuffix(path, "/card_tables/555"):
		return respond(200, `{"id":555,"title":"Board","bucket":{"id":123,"name":"Source"},"lists":[{"id":777,"title":"Developing"}]}`)
	case strings.HasSuffix(path, "/card_tables/900"):
		return respond(200, `{"id":900,"title":"Ideas","bucket":{"id":999,"name":"Dest"},"lists":[{"id":901,"title":"Triage","type":"Kanban::Triage"}]}`)
	case strings.HasSuffix(path, "/card_tables/950"):
		return respond(200, `{"id":950,"title":"Roadmap","bucket":{"id":999,"name":"Dest"},"lists":[{"id":951,"title":"Triage","type":"Kanban::Triage"}]}`)
	case strings.HasSuffix(path, "/card_tables/columns/777"):
		return respond(200, `{"id":777,"title":"Developing","parent":{"id":555,"title":"Board","type":"Kanban::Board"}}`)
	case strings.HasSuffix(path, "/card_tables/cards/458"):
		// A moved message's old id redirects through the card endpoint to the message.
		return respond(200, `{"id":459,"title":"Kickoff","type":"Message","bucket":{"id":123,"name":"Source","type":"Project"},"parent":{"id":555,"title":"Message Board","type":"Message::Board"}}`)
	case strings.HasSuffix(path, "/card_tables/cards/456"):
		return respond(200, `{"id":456,"title":"Test Card","bucket":{"id":123,"name":"Source","type":"Project"},"parent":{"id":777,"title":"Developing","type":"Kanban::Column"}}`)
	case strings.HasSuffix(path, "/card_tables/cards/4560"):
		return respond(200, `{"id":4560,"title":"Test Card","bucket":{"id":999,"name":"Dest","type":"Project"},"parent":{"id":901,"title":"Triage","type":"Kanban::Triage"}}`)
	case strings.Contains(path, "/filings/70"):
		status := "processing"
		if len(m.statuses) > 0 {
			status = m.statuses[min(m.filingGet, len(m.statuses)-1)]
		}
		m.filingGet++
		return respond(200, m.filingJSON(status, "Move"))
	}
	return respond(404, `{"error":"not found"}`)
}

func (m *mockFilingTransport) filingJSON(status, kind string) string {
	dest := ""
	if status == "completed" {
		dest = `,"destination_recording":{"id":4560,"type":"Kanban::Card","url":"https://3.basecampapi.com/99999/buckets/999/card_tables/cards/4560.json","app_url":"https://3.basecamp.com/99999/buckets/999/card_tables/cards/4560"}`
	}
	return fmt.Sprintf(`{"id":70,"status":%q,"type":%q,"source_recording_id":456,"url":"https://3.basecampapi.com/99999/filings/70.json"%s}`, status, kind, dest)
}

type filingRun struct {
	err      error
	envelope struct {
		Data    map[string]any `json:"data"`
		Summary string         `json:"summary"`
	}
	output string
}

func runFilingCommand(t *testing.T, transport *mockFilingTransport, args ...string) filingRun {
	t.Helper()
	shortFilingWait(t)
	app, buf := newTestAppWithTransport(t, transport)
	app.Flags.Hints = true
	var run filingRun
	run.err = executeCommand(NewCardsCmd(), app, args...)
	run.output = buf.String()
	if run.err == nil {
		require.NoError(t, json.Unmarshal(buf.Bytes(), &run.envelope))
	}
	return run
}

func requireFilingError(t *testing.T, run filingRun) *output.Error {
	t.Helper()
	var e *output.Error
	require.True(t, errors.As(run.err, &e), "expected *output.Error, got %T: %v", run.err, run.err)
	return e
}

func TestCardsMoveToProjectWithOneCardTable(t *testing.T) {
	transport := &mockFilingTransport{statuses: []string{"processing", "completed"}}
	run := runFilingCommand(t, transport, "move", "456", "--to-project", "Dest")
	require.NoError(t, run.err)

	assert.Equal(t, []string{"/99999/recordings/456/moves"}, transport.posts)
	assert.Equal(t, float64(900), transport.body["filing_destination_id"])
	assert.Equal(t, float64(4560), run.envelope.Data["id"])
	assert.Equal(t, float64(456), run.envelope.Data["source_id"])
	assert.Equal(t, "Moved card #456 to Dest › Triage as card #4560", run.envelope.Summary)
	assert.Contains(t, run.output, "basecamp cards show 4560 --in 999")
}

func TestCardsMoveToProjectWithSeveralCardTablesIsAmbiguous(t *testing.T) {
	transport := &mockFilingTransport{twoTables: true}
	run := runFilingCommand(t, transport, "move", "456", "--to-project", "999")
	e := requireFilingError(t, run)

	assert.Equal(t, output.CodeAmbiguous, e.Code)
	assert.Contains(t, e.Hint, "--to-card-table")
	assert.Contains(t, e.Hint, "950  Roadmap")
	assert.NotContains(t, e.Hint, "960", "a card table off the dock is not a destination")
	assert.Empty(t, transport.posts)
}

func TestCardsMoveToProjectWithoutCardTableNamesTheProject(t *testing.T) {
	transport := &mockFilingTransport{}
	run := runFilingCommand(t, transport, "move", "456", "--to-project", "Bare")
	e := requireFilingError(t, run)

	assert.Equal(t, output.CodeNotFound, e.Code)
	assert.Equal(t, "Project Bare has no card table", e.Message)
	assert.NotEmpty(t, e.Hint)
	assert.Empty(t, transport.posts)
}

func TestCardsMoveToCardTableNameSharedByTwoTablesIsAmbiguous(t *testing.T) {
	transport := &mockFilingTransport{twoTables: true, duplicateTitle: true}
	run := runFilingCommand(t, transport, "move", "456", "--to-project", "Dest", "--to-card-table", "Roadmap")
	e := requireFilingError(t, run)

	assert.Equal(t, output.CodeAmbiguous, e.Code)
	assert.Contains(t, e.Hint, "950  Roadmap")
	assert.Contains(t, e.Hint, "970  roadmap")
	assert.NotContains(t, e.Hint, "900", "only the tables with that name are candidates")
	assert.Empty(t, transport.posts)
}

func TestCardsCopyThatFailsUpstreamIsNotRetryable(t *testing.T) {
	transport := &mockFilingTransport{postStatus: 502}
	run := runFilingCommand(t, transport, "copy", "456", "--to-project", "Dest")
	e := requireFilingError(t, run)

	assert.False(t, e.Retryable, "a 502 can follow a copy Basecamp already started")
	assert.Contains(t, e.Hint, "may have started the copy")
	assert.Contains(t, e.Hint, "basecamp recordings list Kanban::Card --in 999 --status active --all")
	assert.Len(t, transport.posts, 1)
}

func TestCardsCopyWithNoReadableAnswerMayHaveStarted(t *testing.T) {
	for name, transport := range map[string]*mockFilingTransport{
		"dropped connection": {postErr: errors.New("connection reset by peer")},
		"unreadable 201":     {postBody: `{"id":70,"status":`},
	} {
		t.Run(name, func(t *testing.T) {
			run := runFilingCommand(t, transport, "copy", "456", "--to-project", "Dest")
			e := requireFilingError(t, run)

			assert.False(t, e.Retryable, "Basecamp may have started the copy")
			assert.Contains(t, e.Hint, "may have started the copy")
			assert.Contains(t, e.Hint, "basecamp recordings list Kanban::Card --in 999 --status active --all")
			assert.Len(t, transport.posts, 1)
		})
	}
}

func TestCardsCopyThatBasecampRefusesKeepsItsError(t *testing.T) {
	transport := &mockFilingTransport{postStatus: 403}
	run := runFilingCommand(t, transport, "copy", "456", "--to-project", "Dest")
	e := requireFilingError(t, run)

	assert.Equal(t, output.CodeForbidden, e.Code)
	assert.NotContains(t, e.Hint, "may have started the copy")
	assert.Len(t, transport.posts, 1)
}

func TestCardsMoveWithNoReadableAnswerKeepsItsError(t *testing.T) {
	transport := &mockFilingTransport{postErr: errors.New("connection reset by peer")}
	run := runFilingCommand(t, transport, "move", "456", "--to-project", "Dest")
	require.Error(t, run.err)

	assert.ErrorContains(t, run.err, "connection reset by peer")
	assert.NotContains(t, output.AsError(run.err).Hint, "may have started the copy")
	assert.Len(t, transport.posts, 1)
}

func TestCardsCopyThatIsRateLimitedStaysRetryable(t *testing.T) {
	transport := &mockFilingTransport{postStatus: 429}
	run := runFilingCommand(t, transport, "copy", "456", "--to-project", "Dest")
	e := requireFilingError(t, run)

	assert.True(t, e.Retryable, "Basecamp refuses a rate-limited request before copying")
	assert.NotContains(t, e.Hint, "may have started the copy")
}

func TestAcceptedCopyStaysNotRetryableWhenTheWaitHitsTheRateLimitGate(t *testing.T) {
	f := cardFiling(nil, basecamp.FilingCopy,
		&basecamp.Card{ID: 456},
		&basecamp.CardTable{ID: 900, Title: "Ideas", Bucket: &basecamp.Bucket{ID: 999}},
		false)
	gate := &resilience.GateError{Message: "rate limited by a recent 429", Hint: "Re-run in 30s"}

	e := output.AsError(stillFilingError(f, 70, gate))

	assert.Equal(t, output.CodeInProgress, e.Code)
	assert.False(t, e.Retryable, "the copy was accepted, so re-running makes a second one")
	assert.Contains(t, e.Hint, "Do not copy it again")
}

func TestCardsFilingRefusesARecordThatIsNotACard(t *testing.T) {
	for name, args := range map[string][]string{
		"move": {"move", "458", "--to-card-table", "900"},
		"copy": {"copy", "458", "--to-card-table", "900"},
	} {
		t.Run(name, func(t *testing.T) {
			transport := &mockFilingTransport{}
			run := runFilingCommand(t, transport, args...)
			e := requireFilingError(t, run)

			assert.Equal(t, output.CodeUsage, e.Code)
			assert.Equal(t, "458 is a Message, not a card", e.Message)
			assert.Empty(t, transport.posts)
		})
	}
}

func TestCardsMoveToExplicitCardTable(t *testing.T) {
	for name, args := range map[string][]string{
		"id":              {"--to-card-table", "950"},
		"url":             {"--to-card-table", "https://3.basecamp.com/99999/buckets/999/card_tables/950"},
		"name in project": {"--to-project", "Dest", "--to-card-table", "roadmap"},
	} {
		t.Run(name, func(t *testing.T) {
			transport := &mockFilingTransport{twoTables: true, statuses: []string{"completed"}}
			run := runFilingCommand(t, transport, append([]string{"move", "456"}, args...)...)
			require.NoError(t, run.err)
			assert.Equal(t, float64(950), transport.body["filing_destination_id"])
			assert.Equal(t, float64(4560), run.envelope.Data["id"])
		})
	}
}

func TestCardsMoveRefusesItsOwnCardTable(t *testing.T) {
	transport := &mockFilingTransport{}
	run := runFilingCommand(t, transport, "move", "456", "--to-card-table", "555")
	e := requireFilingError(t, run)

	assert.Equal(t, output.CodeUsage, e.Code)
	assert.Contains(t, e.Message, "already on card table 555")
	assert.Contains(t, e.Hint, "basecamp cards move 456 --to <column>")
	assert.Empty(t, transport.posts)
}

func TestCardsMoveToCardTableRejectsBadFlags(t *testing.T) {
	for name, args := range map[string][]string{
		"with --to":            {"--to-project", "Dest", "--to", "Done"},
		"with --to-wormhole":   {"--to-project", "Dest", "--to-wormhole", "111"},
		"with --card-table":    {"--to-project", "Dest", "--card-table", "555"},
		"name without project": {"--to-card-table", "Roadmap"},
	} {
		t.Run(name, func(t *testing.T) {
			transport := &mockFilingTransport{}
			run := runFilingCommand(t, transport, append([]string{"move", "456"}, args...)...)
			assert.Equal(t, output.CodeUsage, requireFilingError(t, run).Code)
			assert.Empty(t, transport.posts)
		})
	}
}

func TestCardsMoveReportsAFailedFiling(t *testing.T) {
	transport := &mockFilingTransport{statuses: []string{"failed"}}
	run := runFilingCommand(t, transport, "move", "456", "--to-project", "Dest")
	e := requireFilingError(t, run)

	assert.Equal(t, output.CodeAPI, e.Code)
	assert.Contains(t, e.Message, "could not move card #456")
	assert.Len(t, transport.posts, 1)
}

func TestCardsMoveStillRunningWhenTheWaitRunsOut(t *testing.T) {
	transport := &mockFilingTransport{statuses: []string{"processing"}}
	run := runFilingCommand(t, transport, "move", "456", "--to-project", "Dest")
	e := requireFilingError(t, run)

	assert.Equal(t, output.CodeInProgress, e.Code)
	assert.Contains(t, e.Message, "still moving card #456")
	assert.Contains(t, e.Hint, "basecamp filings show 70")
	assert.Len(t, transport.posts, 1, "the move is never sent twice")
	assert.Greater(t, transport.filingGet, 1)
}

func TestCardsCopyDefaultsToItsOwnCardTable(t *testing.T) {
	transport := &mockFilingTransport{statuses: []string{"completed"}}
	run := runFilingCommand(t, transport, "copy", "456")
	require.NoError(t, run.err)

	assert.Equal(t, []string{"/99999/recordings/456/copies"}, transport.posts)
	assert.Equal(t, float64(555), transport.body["filing_destination_id"])
	assert.NotContains(t, transport.body, "include_comments")
	assert.Equal(t, float64(4560), run.envelope.Data["id"])
	assert.Equal(t, float64(456), run.envelope.Data["source_id"])
	assert.True(t, strings.HasPrefix(run.envelope.Summary, "Copied card #456 to "), run.envelope.Summary)
}

func TestCardsCopyToProjectIncludingComments(t *testing.T) {
	transport := &mockFilingTransport{statuses: []string{"completed"}}
	run := runFilingCommand(t, transport, "copy", "456", "--to-project", "Dest", "--include-comments")
	require.NoError(t, run.err)

	assert.Equal(t, float64(900), transport.body["filing_destination_id"])
	assert.Equal(t, true, transport.body["include_comments"])
	assert.Equal(t, float64(4560), run.envelope.Data["id"])
}

func TestCardsCopyStillRunningSaysNotToCopyAgain(t *testing.T) {
	transport := &mockFilingTransport{statuses: []string{"pending"}}
	run := runFilingCommand(t, transport, "copy", "456", "--to-project", "Dest")
	e := requireFilingError(t, run)

	assert.Equal(t, output.CodeInProgress, e.Code)
	assert.Contains(t, e.Hint, "Do not copy it again")
	assert.Contains(t, e.Hint, "basecamp filings show 70")
	assert.Len(t, transport.posts, 1)
}

func TestCardsMoveToAForeignColumnPointsAtCrossProjectMoves(t *testing.T) {
	transport := &mockFilingTransport{}
	run := runFilingCommand(t, transport, "move", "456", "--to", "8888", "--in", "123")
	e := requireFilingError(t, run)

	assert.Equal(t, output.CodeNotFound, e.Code)
	assert.Contains(t, e.Message, "column 8888")
	assert.Contains(t, e.Hint, "basecamp cards move 456 --to-project <project>")
	assert.Contains(t, e.Hint, "--to-wormhole")
}

func TestCardsFilingRefusesAProjectTheCardIsNotIn(t *testing.T) {
	foreignURL := "https://3.basecamp.com/99999/buckets/888/card_tables/cards/456"
	for name, args := range map[string][]string{
		"move with --in":          {"move", "456", "--in", "888", "--to-project", "Dest"},
		"move by a card URL":      {"move", foreignURL, "--to-project", "Dest"},
		"copy with --in":          {"copy", "456", "--in", "888", "--to-project", "Dest"},
		"copy onto its own table": {"copy", "456", "--in", "Bare"},
		"copy by a card URL":      {"copy", foreignURL},
	} {
		t.Run(name, func(t *testing.T) {
			transport := &mockFilingTransport{statuses: []string{"completed"}}
			run := runFilingCommand(t, transport, args...)
			e := requireFilingError(t, run)

			assert.Equal(t, output.CodeUsage, e.Code)
			assert.Contains(t, e.Message, "The card is in project 123")
			assert.Contains(t, e.Message, "project 888")
			assert.Empty(t, transport.posts)
		})
	}
}

func TestCardsFilingAcceptsTheCardsOwnProject(t *testing.T) {
	ownURL := "https://3.basecamp.com/99999/buckets/123/card_tables/cards/456"
	for name, args := range map[string][]string{
		"move with --in":     {"move", "456", "--in", "Source", "--to-project", "Dest"},
		"move by a card URL": {"move", ownURL, "--to-project", "Dest"},
		"copy with --in":     {"copy", "456", "--in", "123"},
		"copy by a card URL": {"copy", ownURL, "--to-project", "Dest"},
	} {
		t.Run(name, func(t *testing.T) {
			transport := &mockFilingTransport{statuses: []string{"completed"}}
			run := runFilingCommand(t, transport, args...)
			require.NoError(t, run.err)
			assert.Len(t, transport.posts, 1)
			assert.Equal(t, float64(4560), run.envelope.Data["id"])
		})
	}
}

func TestCardsFilingRefusesASourceURLThatIsNotThisAccountsCard(t *testing.T) {
	for name, tc := range map[string]struct{ url, message string }{
		"untrusted host":  {"https://3.basecamp.com.evil.example/99999/buckets/123/card_tables/cards/456", "not a Basecamp URL"},
		"another account": {"https://3.basecamp.com/11111/buckets/123/card_tables/cards/456", "URL is for account 11111"},
		"a to-do":         {"https://3.basecamp.com/99999/buckets/123/todos/456", "the URL names something else"},
		"a cards listing": {"https://3.basecamp.com/99999/buckets/123/card_tables/lists/456/cards", "the URL names something else"},
		"a card table":    {"https://3.basecamp.com/99999/buckets/123/card_tables/456", "the URL names something else"},
	} {
		for _, verb := range []string{"move", "copy"} {
			t.Run(verb+" "+name, func(t *testing.T) {
				transport := &mockFilingTransport{statuses: []string{"completed"}}
				run := runFilingCommand(t, transport, verb, tc.url, "--to-project", "Dest")
				e := requireFilingError(t, run)

				assert.Equal(t, output.CodeUsage, e.Code)
				assert.Contains(t, e.Message, tc.message)
				assert.Zero(t, transport.requests, "refused before any request")
			})
		}
	}
}

func TestCardsFilingRefusesADestinationURLThatIsNotThisAccountsCardTable(t *testing.T) {
	for name, tc := range map[string]struct{ url, message string }{
		"untrusted host":  {"https://3.basecamp.com.evil.example/99999/buckets/999/card_tables/900", "not a Basecamp URL"},
		"another account": {"https://3.basecamp.com/11111/buckets/999/card_tables/900", "URL is for account 11111"},
		"a column":        {"https://3.basecamp.com/99999/buckets/999/card_tables/columns/901", "the URL names something else"},
	} {
		for _, verb := range []string{"move", "copy"} {
			t.Run(verb+" "+name, func(t *testing.T) {
				transport := &mockFilingTransport{statuses: []string{"completed"}}
				run := runFilingCommand(t, transport, verb, "456", "--to-card-table", tc.url)
				e := requireFilingError(t, run)

				assert.Equal(t, output.CodeUsage, e.Code)
				assert.Contains(t, e.Message, tc.message)
				assert.Zero(t, transport.requests, "refused before any request")
			})
		}
	}
}

func TestCardsFilingRefusesACardTableURLInAnotherProjectThanToProject(t *testing.T) {
	for _, verb := range []string{"move", "copy"} {
		t.Run(verb, func(t *testing.T) {
			transport := &mockFilingTransport{statuses: []string{"completed"}}
			run := runFilingCommand(t, transport, verb, "456",
				"--to-project", "Dest", "--to-card-table", "https://3.basecamp.com/99999/buckets/123/card_tables/900")
			e := requireFilingError(t, run)

			assert.Equal(t, output.CodeUsage, e.Code)
			assert.Contains(t, e.Message, "URL is in project 123, but --to-project points at project 999")
			assert.Empty(t, transport.posts)
		})
	}
}

func TestCardsFilingRefusesACardTableURLWhoseProjectIsNotTheTables(t *testing.T) {
	for _, verb := range []string{"move", "copy"} {
		t.Run(verb, func(t *testing.T) {
			transport := &mockFilingTransport{statuses: []string{"completed"}}
			run := runFilingCommand(t, transport, verb, "456",
				"--to-card-table", "https://3.basecamp.com/99999/buckets/123/card_tables/900")
			e := requireFilingError(t, run)

			assert.Equal(t, output.CodeUsage, e.Code)
			assert.Equal(t, "The --to-card-table URL names project 123, but card table 900 is in project 999", e.Message)
			assert.Empty(t, transport.posts)
		})
	}
}

func TestCardsFilingAcceptsACardTableURLOnItsOwn(t *testing.T) {
	transport := &mockFilingTransport{statuses: []string{"completed"}}
	run := runFilingCommand(t, transport, "copy", "456",
		"--to-card-table", "https://3.basecamp.com/99999/buckets/999/card_tables/900")
	require.NoError(t, run.err)
	assert.Equal(t, float64(900), transport.body["filing_destination_id"])
}

func TestCardsFilingAcceptsACardTableURLInToProject(t *testing.T) {
	transport := &mockFilingTransport{twoTables: true, statuses: []string{"completed"}}
	run := runFilingCommand(t, transport, "copy", "456",
		"--to-project", "Dest", "--to-card-table", "https://3.basecamp.com/99999/buckets/999/card_tables/950")
	require.NoError(t, run.err)
	assert.Equal(t, float64(950), transport.body["filing_destination_id"])
}
