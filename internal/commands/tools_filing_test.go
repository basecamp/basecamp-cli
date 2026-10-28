package commands

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/output"
)

// toolsFilingTransport has message board 456 and door 457 on Source's (123)
// dock; the board moved to Dest (999) reads back as tool 4560.
func toolsFilingTransport() *familyTransport {
	return &familyTransport{gets: map[string]string{
		"/projects":        filingProjects,
		"/dock/tools/456":  `{"id":456,"type":"Message::Board","title":"Message Board","bucket":{"id":123,"name":"Source"}}`,
		"/dock/tools/457":  `{"id":457,"type":"Door","title":"Figma","bucket":{"id":123,"name":"Source"}}`,
		"/dock/tools/458":  `{"id":458,"type":"Vault","title":"Specs","bucket":{"id":123,"name":"Source"},"parent":{"id":500,"title":"Docs & Files","type":"Vault"}}`,
		"/dock/tools/4560": `{"id":4560,"type":"Message::Board","title":"Message Board","bucket":{"id":999,"name":"Dest"}}`,
	}}
}

func TestToolsMoveToProjectSendsTheProject(t *testing.T) {
	transport := toolsFilingTransport()
	run := runFamilyCommand(t, transport, NewToolsCmd(), "move", "456", "--to-project", "Dest")
	require.NoError(t, run.err)

	assert.Equal(t, []string{"/99999/recordings/456/moves"}, transport.posts)
	assert.Equal(t, float64(999), transport.bodies[0]["destination_project_id"])
	assert.NotContains(t, transport.bodies[0], "filing_destination_id")
	data := run.data(t)
	assert.Equal(t, float64(4560), data["id"])
	assert.Equal(t, float64(456), data["source_id"])
	assert.Equal(t, "Moved tool #456 to Dest as tool #4560", run.envelope.Summary)
	assert.Equal(t, "basecamp tools show 4560 --in 999", run.envelope.Breadcrumbs[0].Cmd)
}

func TestToolsMoveByToolURL(t *testing.T) {
	transport := toolsFilingTransport()
	run := runFamilyCommand(t, transport, NewToolsCmd(), "move", "https://3.basecamp.com/99999/buckets/123/message_boards/456", "--to-project", "999")
	require.NoError(t, run.err)
	assert.Equal(t, []string{"/99999/recordings/456/moves"}, transport.posts)
}

func TestToolsMoveRefusesADoor(t *testing.T) {
	transport := toolsFilingTransport()
	run := runFamilyCommand(t, transport, NewToolsCmd(), "move", "457", "--to-project", "Dest")
	e := run.failure(t)
	assert.Equal(t, "Tool 457 (Figma) is a door, an external link, which Basecamp does not move to another project", e.Message)
	assert.Empty(t, transport.posts)
}

func TestToolsMoveRefusesAFolderThatIsNotOnTheDock(t *testing.T) {
	transport := toolsFilingTransport()
	run := runFamilyCommand(t, transport, NewToolsCmd(), "move", "458", "--to-project", "Dest")
	e := run.failure(t)
	assert.Equal(t, output.CodeUsage, e.Code)
	assert.Contains(t, e.Message, "not a tool on a project's dock")
	assert.Empty(t, transport.posts)
}

func TestToolsMoveToItsOwnProjectPointsAtPosition(t *testing.T) {
	transport := toolsFilingTransport()
	run := runFamilyCommand(t, transport, NewToolsCmd(), "move", "456", "--to-project", "Source")
	e := run.failure(t)
	assert.Equal(t, "Reorder it in that dock with: basecamp tools move 456 --position <n> --in 123", e.Hint)
	assert.Empty(t, transport.posts)
}

func TestToolsMoveRefusesPositionBesideToProject(t *testing.T) {
	transport := toolsFilingTransport()
	run := runFamilyCommand(t, transport, NewToolsCmd(), "move", "456", "--position", "2", "--to-project", "Dest")
	e := run.failure(t)
	assert.Contains(t, e.Message, "cannot be combined with --to-project")
	assert.Empty(t, transport.posts)
}

func TestToolsMoveWithPositionStillRepositionsInTheDock(t *testing.T) {
	transport := toolsFilingTransport()
	transport.gets["/recordings/456/position"] = `{}`
	run := runFamilyCommand(t, transport, NewToolsCmd(), "move", "456", "--position", "2")
	require.NoError(t, run.err)
	assert.Empty(t, transport.posts, "a reposition is not a filing")
	assert.Equal(t, "Tool 456 moved to position 2", run.envelope.Summary)
}

// A moved tool's old id reads as deleted rather than leading to the new one,
// so a move still running is checked by its filing.
func TestToolsMoveStillRunningPointsAtItsFiling(t *testing.T) {
	transport := toolsFilingTransport()
	transport.stuck = map[int64]bool{456: true}
	run := runFamilyCommand(t, transport, NewToolsCmd(), "move", "456", "--to-project", "Dest")
	e := run.failure(t)
	assert.Equal(t, output.CodeInProgress, e.Code)
	assert.Equal(t, "Do not move it again. Check it with: basecamp filings show 456, which names the tool's new id once it lands", e.Hint)
}

func TestToolsMoveRefusesADeletedOrMovedTool(t *testing.T) {
	transport := toolsFilingTransport()
	transport.gets["/dock/tools/456"] = `{"id":456,"status":"deleted","type":"Message::Board","title":"Message Board","bucket":{"id":123,"name":"Source"}}`
	run := runFamilyCommand(t, transport, NewToolsCmd(), "move", "456", "--to-project", "Dest")
	e := run.failure(t)
	assert.Contains(t, e.Message, "was moved to another project under a new ID")
	assert.Empty(t, transport.posts)
}
