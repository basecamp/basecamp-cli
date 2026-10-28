package commands

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/output"
)

// messagesTransport has message 456 on board 555 in Source (123), and
// Dest (999) with board 900, which the moved or copied message 4560 is on.
func messagesTransport() *familyTransport {
	return &familyTransport{gets: map[string]string{
		"/projects":           filingProjects,
		"/projects/123":       `{"id":123,"name":"Source","dock":[{"name":"message_board","id":555,"title":"Message Board","enabled":true}]}`,
		"/projects/999":       `{"id":999,"name":"Dest","dock":[{"name":"message_board","id":900,"title":"Announcements","enabled":true}]}`,
		"/projects/888":       `{"id":888,"name":"Bare","dock":[{"name":"chat","id":880,"title":"Chat","enabled":true}]}`,
		"/message_boards/555": `{"id":555,"title":"Message Board","bucket":{"id":123,"name":"Source"}}`,
		"/message_boards/900": `{"id":900,"title":"Announcements","bucket":{"id":999,"name":"Dest"}}`,
		"/messages/456":       `{"id":456,"type":"Message","subject":"Kickoff","bucket":{"id":123,"name":"Source"},"parent":{"id":555,"title":"Message Board","type":"Message::Board"}}`,
		"/messages/4560":      `{"id":4560,"type":"Message","subject":"Kickoff","bucket":{"id":999,"name":"Dest"},"parent":{"id":900,"title":"Announcements","type":"Message::Board"}}`,
		"/messages/457":       `{"id":457,"type":"Message","subject":"Draft","bucket":{"id":999,"name":"Dest"},"parent":{"id":900,"title":"Announcements","type":"Message::Board"}}`,
		"/messages/458":       `{"id":4580,"type":"Todo","bucket":{"id":123,"name":"Source"}}`,
		"/messages/5550":      `{"id":5550,"type":"Message","subject":"Kickoff","bucket":{"id":123,"name":"Source"},"parent":{"id":555,"title":"Message Board","type":"Message::Board"}}`,
	}}
}

func TestMessagesMoveToProjectByName(t *testing.T) {
	transport := messagesTransport()
	run := runFamilyCommand(t, transport, NewMessagesCmd(), "move", "456", "--to-project", "Dest")
	require.NoError(t, run.err)

	assert.Equal(t, []string{"/99999/recordings/456/moves"}, transport.posts)
	assert.Equal(t, float64(900), transport.bodies[0]["filing_destination_id"])
	data := run.data(t)
	assert.Equal(t, float64(4560), data["id"])
	assert.Equal(t, float64(456), data["source_id"])
	assert.Equal(t, "Moved message #456 to Dest › Announcements as message #4560", run.envelope.Summary)
	require.NotEmpty(t, run.envelope.Breadcrumbs)
	assert.Equal(t, "basecamp messages show 4560 --in 999", run.envelope.Breadcrumbs[0].Cmd)
}

func TestMessagesMoveToBoardURL(t *testing.T) {
	transport := messagesTransport()
	run := runFamilyCommand(t, transport, NewMessagesCmd(), "move",
		"https://3.basecamp.com/99999/buckets/123/messages/456",
		"--to-board", "https://3.basecamp.com/99999/buckets/999/message_boards/900")
	require.NoError(t, run.err)
	assert.Equal(t, float64(900), transport.bodies[0]["filing_destination_id"])
}

func TestMessagesMoveNeedsADestination(t *testing.T) {
	t.Setenv("BASECAMP_NONINTERACTIVE", "1")
	transport := messagesTransport()
	run := runFamilyCommand(t, transport, NewMessagesCmd(), "move", "456")
	e := run.failure(t)
	assert.Equal(t, output.CodeUsage, e.Code)
	assert.Contains(t, e.Message, "--to-project")
	assert.Empty(t, transport.posts)
}

func TestMessagesMoveRefusesTheOwnBoardFlag(t *testing.T) {
	transport := messagesTransport()
	run := runFamilyCommand(t, transport, NewMessagesCmd(), "move", "456", "--message-board", "900", "--to-project", "Dest")
	e := run.failure(t)
	assert.Equal(t, "--message-board names the message's own message board, not where it goes", e.Message)
	assert.Contains(t, e.Hint, "--to-board")
	assert.Empty(t, transport.posts)
}

func TestMessagesMoveBoardNameNeedsAProject(t *testing.T) {
	transport := messagesTransport()
	run := runFamilyCommand(t, transport, NewMessagesCmd(), "move", "456", "--to-board", "Announcements")
	e := run.failure(t)
	assert.Contains(t, e.Message, "is a name, which needs a project")
	assert.Empty(t, transport.posts)
}

func TestMessagesMoveOntoItsOwnBoardIsRefused(t *testing.T) {
	transport := messagesTransport()
	run := runFamilyCommand(t, transport, NewMessagesCmd(), "move", "456", "--to-project", "Source")
	e := run.failure(t)
	assert.Equal(t, output.CodeUsage, e.Code)
	assert.Equal(t, "Message #456 is already in message board 555 (Message Board) in Source", e.Message)
	assert.Empty(t, transport.posts)
}

func TestMessagesMoveToProjectWithoutBoardNamesIt(t *testing.T) {
	transport := messagesTransport()
	run := runFamilyCommand(t, transport, NewMessagesCmd(), "move", "456", "--to-project", "Bare")
	e := run.failure(t)
	assert.Equal(t, "Project Bare has no message board", e.Message)
	assert.Empty(t, transport.posts)
}

func TestMessagesMoveRefusesAnIDThatIsNotAMessage(t *testing.T) {
	transport := messagesTransport()
	run := runFamilyCommand(t, transport, NewMessagesCmd(), "move", "458", "--to-project", "Dest")
	e := run.failure(t)
	assert.Equal(t, "458 is a Todo, not a message", e.Message)
	assert.Empty(t, transport.posts)
}

func TestMessagesMoveRefusesAConflictingProject(t *testing.T) {
	transport := messagesTransport()
	run := runFamilyCommand(t, transport, NewMessagesCmd(), "move", "456", "--in", "Dest", "--to-project", "Dest")
	e := run.failure(t)
	assert.Contains(t, e.Message, "The message is in project 123, but --in/--project points at project 999")
	assert.Empty(t, transport.posts)
}

func TestMessagesCopyWithoutDestinationGoesOntoItsOwnBoard(t *testing.T) {
	transport := messagesTransport()
	transport.gets["/messages/4560"] = transport.gets["/messages/5550"]
	run := runFamilyCommand(t, transport, NewMessagesCmd(), "copy", "456", "--include-comments")
	require.NoError(t, run.err)

	assert.Equal(t, []string{"/99999/recordings/456/copies"}, transport.posts)
	assert.Equal(t, float64(555), transport.bodies[0]["filing_destination_id"])
	assert.Equal(t, true, transport.bodies[0]["include_comments"])
	assert.Equal(t, float64(456), run.data(t)["source_id"])
}

func TestMessagesCopyToAnotherProject(t *testing.T) {
	transport := messagesTransport()
	run := runFamilyCommand(t, transport, NewMessagesCmd(), "copy", "456", "--to-project", "999", "--to-board", "Announcements")
	require.NoError(t, run.err)
	assert.Equal(t, float64(900), transport.bodies[0]["filing_destination_id"])
	assert.NotContains(t, transport.bodies[0], "include_comments")
	assert.Equal(t, "Copied message #456 to Dest › Announcements as message #4560", run.envelope.Summary)
}

func TestMessagesCopyThatMayHaveStartedSaysWhereToLook(t *testing.T) {
	transport := messagesTransport()
	transport.refuse = map[int64]int{456: 502}
	run := runFamilyCommand(t, transport, NewMessagesCmd(), "copy", "456", "--to-project", "Dest")
	e := run.failure(t)
	assert.False(t, e.Retryable)
	assert.Contains(t, e.Hint, "basecamp recordings list Message --in 999 --status active --sort created_at --direction desc --all")
	assert.Len(t, transport.posts, 1)
}

// A copy keeps its source's status, and the recordings listing serves no
// drafted status (Basecamp answers it with 400), so a drafted message's copy
// is looked for among the person's drafts.
func TestMessagesCopyOfADraftWithNoFilingIsLookedForAmongDrafts(t *testing.T) {
	transport := messagesTransport()
	transport.gets["/messages/456"] = strings.Replace(transport.gets["/messages/456"], `"type":"Message"`, `"type":"Message","status":"drafted"`, 1)
	transport.refuse = map[int64]int{456: 502}
	e := runFamilyCommand(t, transport, NewMessagesCmd(), "copy", "456", "--to-project", "Dest").failure(t)
	assert.Contains(t, e.Hint, "may have started the copy")
	assert.Contains(t, e.Hint, "basecamp drafts list --all")
	assert.NotContains(t, e.Hint, "--status drafted")
}

// A flat API URL names no project, and its id can be a board's title too. A
// URL that names no board of the project is refused, never read as that title.
func TestMessagesBoardURLThatNamesNoBoardIsNeverATitle(t *testing.T) {
	transport := messagesTransport()
	transport.gets["/projects/999"] = `{"id":999,"name":"Dest","dock":[{"name":"message_board","id":900,"title":"Announcements","enabled":true},{"name":"message_board","id":901,"title":"2026","enabled":true}]}`
	url := "https://3.basecampapi.com/99999/message_boards/2026.json"
	e := runFamilyCommand(t, transport, NewMessagesCmd(), "copy", "456", "--to-project", "Dest", "--to-board", url).failure(t)
	assert.Equal(t, output.CodeNotFound, e.Code)
	assert.Contains(t, e.Message, url)
	assert.Contains(t, e.Hint, "901 (2026)")
	assert.Empty(t, transport.posts)
}
