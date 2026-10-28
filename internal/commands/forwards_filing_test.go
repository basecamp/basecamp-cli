package commands

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/output"
)

// forwardsFilingTransport has forward 456 in Email Forwards 555 in Source
// (123), Dest (999) with Email Forwards 900, which the filed forward 4560 is
// in, and Bare (888) whose Email Forwards 880 is turned off.
func forwardsFilingTransport() *familyTransport {
	return &familyTransport{gets: map[string]string{
		"/projects":            filingProjects,
		"/projects/123":        `{"id":123,"name":"Source","dock":[{"name":"inbox","id":555,"title":"Forwards","enabled":true}]}`,
		"/projects/999":        `{"id":999,"name":"Dest","dock":[{"name":"inbox","id":900,"title":"Forwards","enabled":true,"app_url":"https://3.basecamp.com/99999/buckets/999/inboxes/900"}]}`,
		"/projects/888":        `{"id":888,"name":"Bare","dock":[{"name":"inbox","id":880,"title":"Forwards","enabled":false}]}`,
		"/inboxes/555":         `{"id":555,"title":"Forwards","bucket":{"id":123,"name":"Source"}}`,
		"/inbox_forwards/456":  `{"id":456,"type":"Inbox::Forward","subject":"Invoice","bucket":{"id":123,"name":"Source"},"parent":{"id":555,"title":"Forwards","type":"Inbox"}}`,
		"/inbox_forwards/4560": `{"id":4560,"type":"Inbox::Forward","subject":"Invoice","bucket":{"id":999,"name":"Dest"},"parent":{"id":900,"title":"Forwards","type":"Inbox"}}`,
	}}
}

func TestForwardsMoveToProject(t *testing.T) {
	transport := forwardsFilingTransport()
	run := runFamilyCommand(t, transport, NewForwardsCmd(), "move", "456", "--to-project", "Dest")
	require.NoError(t, run.err)

	assert.Equal(t, []string{"/99999/recordings/456/moves"}, transport.posts)
	assert.Equal(t, float64(900), transport.bodies[0]["filing_destination_id"])
	data := run.data(t)
	assert.Equal(t, float64(4560), data["id"])
	assert.Equal(t, float64(456), data["source_id"])
	assert.Equal(t, "Moved forward #456 to Dest › Forwards as forward #4560", run.envelope.Summary)
}

func TestForwardsMoveToTurnedOffEmailForwardsSaysHowToTurnThemOn(t *testing.T) {
	transport := forwardsFilingTransport()
	run := runFamilyCommand(t, transport, NewForwardsCmd(), "move", "456", "--to-project", "Bare")
	e := run.failure(t)
	assert.Equal(t, output.CodeNotFound, e.Code)
	assert.Equal(t, "Bare's Email Forwards (Forwards) is turned off, so nothing can be moved or copied into it", e.Message)
	assert.Equal(t, "Turn it on first with: basecamp tools enable 880 --in 888", e.Hint)
	assert.Empty(t, transport.posts)
}

func TestForwardsCopyWithoutDestinationGoesIntoItsOwnInbox(t *testing.T) {
	transport := forwardsFilingTransport()
	run := runFamilyCommand(t, transport, NewForwardsCmd(), "copy", "456", "--include-comments")
	require.NoError(t, run.err)
	assert.Equal(t, []string{"/99999/recordings/456/copies"}, transport.posts)
	assert.Equal(t, float64(555), transport.bodies[0]["filing_destination_id"])
	assert.Equal(t, true, transport.bodies[0]["include_comments"])
}
