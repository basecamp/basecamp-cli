package commands

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/output"
)

// checkinsFilingTransport has question 456 in Automatic Check-ins 555 in
// Source (123), and Dest (999) with Automatic Check-ins 900, which the filed
// question 4560 is in.
func checkinsFilingTransport() *familyTransport {
	return &familyTransport{gets: map[string]string{
		"/projects":           filingProjects,
		"/projects/123":       `{"id":123,"name":"Source","dock":[{"name":"questionnaire","id":555,"title":"Automatic Check-ins","enabled":true}]}`,
		"/projects/999":       `{"id":999,"name":"Dest","dock":[{"name":"questionnaire","id":900,"title":"Automatic Check-ins","enabled":true}]}`,
		"/questionnaires/555": `{"id":555,"title":"Automatic Check-ins","bucket":{"id":123,"name":"Source"}}`,
		"/questionnaires/900": `{"id":900,"title":"Automatic Check-ins","bucket":{"id":999,"name":"Dest"}}`,
		"/questions/456":      `{"id":456,"type":"Question","title":"What did you work on?","bucket":{"id":123,"name":"Source"},"parent":{"id":555,"title":"Automatic Check-ins","type":"Questionnaire"}}`,
		"/questions/4560":     `{"id":4560,"type":"Question","title":"What did you work on?","bucket":{"id":999,"name":"Dest"},"parent":{"id":900,"title":"Automatic Check-ins","type":"Questionnaire"}}`,
	}}
}

func TestCheckinsQuestionMoveToProject(t *testing.T) {
	transport := checkinsFilingTransport()
	run := runFamilyCommand(t, transport, NewCheckinsCmd(), "question", "move", "456", "--to-project", "Dest")
	require.NoError(t, run.err)

	assert.Equal(t, []string{"/99999/recordings/456/moves"}, transport.posts)
	assert.Equal(t, float64(900), transport.bodies[0]["filing_destination_id"])
	data := run.data(t)
	assert.Equal(t, float64(4560), data["id"])
	assert.Equal(t, float64(456), data["source_id"])
	assert.Equal(t, "Moved question #456 to Dest › Automatic Check-ins as question #4560", run.envelope.Summary)
	assert.Equal(t, "basecamp checkins question show 4560 --in 999", run.envelope.Breadcrumbs[0].Cmd)
}

func TestCheckinsQuestionMoveRefusesTheOwnQuestionnaireFlag(t *testing.T) {
	transport := checkinsFilingTransport()
	run := runFamilyCommand(t, transport, NewCheckinsCmd(), "question", "move", "456", "--questionnaire", "555", "--to-project", "Dest")
	e := run.failure(t)
	assert.Contains(t, e.Hint, "--to-questionnaire")
	assert.Empty(t, transport.posts)
}

func TestCheckinsQuestionMoveOntoItsOwnQuestionnaireIsRefused(t *testing.T) {
	transport := checkinsFilingTransport()
	run := runFamilyCommand(t, transport, NewCheckinsCmd(), "question", "move", "456", "--to-questionnaire", "555")
	e := run.failure(t)
	assert.Equal(t, output.CodeUsage, e.Code)
	assert.Contains(t, e.Message, "is already in Automatic Check-ins 555")
	assert.Empty(t, transport.posts)
}

func TestCheckinsQuestionCopyWithoutDestinationGoesIntoItsOwnQuestionnaire(t *testing.T) {
	transport := checkinsFilingTransport()
	run := runFamilyCommand(t, transport, NewCheckinsCmd(), "question", "copy", "456")
	require.NoError(t, run.err)
	assert.Equal(t, []string{"/99999/recordings/456/copies"}, transport.posts)
	assert.Equal(t, float64(555), transport.bodies[0]["filing_destination_id"])
}
