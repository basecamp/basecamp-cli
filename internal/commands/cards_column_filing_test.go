package commands

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/output"
)

// columnsTransport has column 456 ("Doing") and Triage 457 on card table 555
// in Source (123), and Dest (999) with card table 900, which the filed
// column 4560 is on.
func columnsTransport() *familyTransport {
	return &familyTransport{gets: map[string]string{
		"/projects":                 filingProjects,
		"/projects/123":             `{"id":123,"name":"Source","dock":[{"name":"kanban_board","id":555,"title":"Board","enabled":true}]}`,
		"/projects/999":             `{"id":999,"name":"Dest","dock":[{"name":"kanban_board","id":900,"title":"Roadmap","enabled":true,"app_url":"https://3.basecamp.com/99999/buckets/999/card_tables/900"}]}`,
		"/card_tables/555":          `{"id":555,"title":"Board","bucket":{"id":123,"name":"Source"}}`,
		"/card_tables/900":          `{"id":900,"title":"Roadmap","bucket":{"id":999,"name":"Dest"}}`,
		"/card_tables/columns/456":  `{"id":456,"type":"Kanban::Column","title":"Doing","bucket":{"id":123,"name":"Source"},"parent":{"id":555,"title":"Board","type":"Kanban::Board"}}`,
		"/card_tables/columns/457":  `{"id":457,"type":"Kanban::Triage","title":"Triage","bucket":{"id":123,"name":"Source"},"parent":{"id":555,"title":"Board","type":"Kanban::Board"}}`,
		"/card_tables/columns/4560": `{"id":4560,"type":"Kanban::Column","title":"Doing","bucket":{"id":999,"name":"Dest"},"parent":{"id":900,"title":"Roadmap","type":"Kanban::Board"}}`,
	}}
}

func TestCardsColumnMoveToProject(t *testing.T) {
	transport := columnsTransport()
	run := runFamilyCommand(t, transport, NewCardsCmd(), "column", "move", "456", "--to-project", "Dest")
	require.NoError(t, run.err)

	assert.Equal(t, []string{"/99999/recordings/456/moves"}, transport.posts)
	assert.Equal(t, float64(900), transport.bodies[0]["filing_destination_id"])
	data := run.data(t)
	assert.Equal(t, float64(4560), data["id"])
	assert.Equal(t, float64(456), data["source_id"])
	assert.Equal(t, "Moved column #456 to Dest › Roadmap as column #4560", run.envelope.Summary)
}

func TestCardsColumnMoveRefusesTriageAndTheLike(t *testing.T) {
	transport := columnsTransport()
	run := runFamilyCommand(t, transport, NewCardsCmd(), "column", "move", "457", "--to-project", "Dest")
	e := run.failure(t)
	assert.Equal(t, output.CodeUsage, e.Code)
	assert.Equal(t, "Column #457 (Triage) is one every card table has, which Basecamp does not move or copy", e.Message)
	assert.Empty(t, transport.posts)
}

func TestCardsColumnMoveOntoItsOwnTablePointsAtPosition(t *testing.T) {
	transport := columnsTransport()
	run := runFamilyCommand(t, transport, NewCardsCmd(), "column", "move", "456", "--to-card-table", "555")
	e := run.failure(t)
	assert.Equal(t, "Reposition it within that card table with: basecamp cards column move 456 --position <n> --card-table 555 --in 123", e.Hint)
	assert.Empty(t, transport.posts)
}

func TestCardsColumnMoveRefusesPositionBesideToProject(t *testing.T) {
	transport := columnsTransport()
	run := runFamilyCommand(t, transport, NewCardsCmd(), "column", "move", "456", "--position", "2", "--to-project", "Dest")
	e := run.failure(t)
	assert.Contains(t, e.Message, "cannot be combined with --to-project")
	assert.Empty(t, transport.posts)
}

func TestCardsColumnMoveRefusesTheOwnCardTableFlag(t *testing.T) {
	transport := columnsTransport()
	run := runFamilyCommand(t, transport, NewCardsCmd(), "column", "move", "456", "--card-table", "900", "--to-project", "Dest")
	e := run.failure(t)
	assert.Contains(t, e.Hint, "--to-card-table")
	assert.Empty(t, transport.posts)
}

func TestCardsColumnCopyWithoutDestinationGoesOntoItsOwnTable(t *testing.T) {
	transport := columnsTransport()
	transport.gets["/card_tables/columns/4560"] = `{"id":4560,"type":"Kanban::Column","title":"Doing","bucket":{"id":123,"name":"Source"},"parent":{"id":555,"title":"Board","type":"Kanban::Board"}}`
	run := runFamilyCommand(t, transport, NewCardsCmd(), "column", "copy", "456", "--include-comments")
	require.NoError(t, run.err)
	assert.Equal(t, []string{"/99999/recordings/456/copies"}, transport.posts)
	assert.Equal(t, float64(555), transport.bodies[0]["filing_destination_id"])
	assert.Equal(t, true, transport.bodies[0]["include_comments"])
}

func TestCardsColumnCopyRefusesDone(t *testing.T) {
	transport := columnsTransport()
	transport.gets["/card_tables/columns/457"] = `{"id":457,"type":"Kanban::DoneColumn","title":"Done","bucket":{"id":123,"name":"Source"},"parent":{"id":555,"title":"Board","type":"Kanban::Board"}}`
	run := runFamilyCommand(t, transport, NewCardsCmd(), "column", "copy", "457", "--to-project", "Dest")
	e := run.failure(t)
	assert.Contains(t, e.Message, "one every card table has")
	assert.Empty(t, transport.posts)
}

func TestCardsColumnRepositionThatBasecampCannotFindPointsAtToCardTable(t *testing.T) {
	transport := columnsTransport()
	// Basecamp finds the reposition's card table only in the column's project.
	transport.refuse = map[int64]int{900: 404}
	run := runFamilyCommand(t, transport, NewCardsCmd(), "column", "move", "456", "--position", "1", "--card-table", "900", "--in", "Dest")
	e := run.failure(t)
	assert.Equal(t, output.CodeNotFound, e.Code)
	assert.Contains(t, e.Hint, "basecamp cards column move 456 --to-card-table 900")
}
