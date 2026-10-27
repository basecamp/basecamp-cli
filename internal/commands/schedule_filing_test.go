package commands

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/output"
)

// scheduleFilingTransport has event 456 on schedule 555 in Source (123), and
// Dest (999) with schedule 900, which the filed event 4560 is on.
func scheduleFilingTransport() *familyTransport {
	return &familyTransport{gets: map[string]string{
		"/projects":              filingProjects,
		"/projects/123":          `{"id":123,"name":"Source","dock":[{"name":"schedule","id":555,"title":"Schedule","enabled":true}]}`,
		"/projects/999":          `{"id":999,"name":"Dest","dock":[{"name":"schedule","id":900,"title":"Calendar","enabled":true}]}`,
		"/schedules/555":         `{"id":555,"title":"Schedule","bucket":{"id":123,"name":"Source"}}`,
		"/schedules/900":         `{"id":900,"title":"Calendar","bucket":{"id":999,"name":"Dest"}}`,
		"/schedule_entries/456":  `{"id":456,"type":"Schedule::Entry","summary":"Launch","bucket":{"id":123,"name":"Source"},"parent":{"id":555,"title":"Schedule","type":"Schedule"}}`,
		"/schedule_entries/4560": `{"id":4560,"type":"Schedule::Entry","summary":"Launch","bucket":{"id":999,"name":"Dest"},"parent":{"id":900,"title":"Calendar","type":"Schedule"}}`,
	}}
}

func TestScheduleMoveToProject(t *testing.T) {
	transport := scheduleFilingTransport()
	run := runFamilyCommand(t, transport, NewScheduleCmd(), "move", "456", "--to-project", "Dest")
	require.NoError(t, run.err)

	assert.Equal(t, []string{"/99999/recordings/456/moves"}, transport.posts)
	assert.Equal(t, float64(900), transport.bodies[0]["filing_destination_id"])
	data := run.data(t)
	assert.Equal(t, float64(4560), data["id"])
	assert.Equal(t, float64(456), data["source_id"])
	assert.Equal(t, "Moved schedule entry #456 to Dest › Calendar as schedule entry #4560", run.envelope.Summary)
	assert.Equal(t, "basecamp schedule show 4560 --in 999", run.envelope.Breadcrumbs[0].Cmd)
}

func TestScheduleMoveTakesAnOccurrenceURLAsItsSeries(t *testing.T) {
	transport := scheduleFilingTransport()
	run := runFamilyCommand(t, transport, NewScheduleCmd(), "move",
		"https://3.basecamp.com/99999/buckets/123/schedule_entries/456/occurrences/20261101", "--to-schedule", "900")
	require.NoError(t, run.err)
	assert.Equal(t, []string{"/99999/recordings/456/moves"}, transport.posts)
}

func TestScheduleMoveRefusesTheOwnScheduleFlag(t *testing.T) {
	transport := scheduleFilingTransport()
	run := runFamilyCommand(t, transport, NewScheduleCmd(), "move", "456", "-s", "555", "--to-project", "Dest")
	e := run.failure(t)
	assert.Equal(t, "--schedule names the schedule entry's own schedule, not where it goes", e.Message)
	assert.Empty(t, transport.posts)
}

func TestScheduleMoveOntoItsOwnScheduleIsRefused(t *testing.T) {
	transport := scheduleFilingTransport()
	run := runFamilyCommand(t, transport, NewScheduleCmd(), "move", "456", "--to-project", "Source")
	e := run.failure(t)
	assert.Equal(t, output.CodeUsage, e.Code)
	assert.Contains(t, e.Message, "is already in schedule 555")
	assert.Empty(t, transport.posts)
}

func TestScheduleCopyWithoutDestinationGoesOntoItsOwnSchedule(t *testing.T) {
	transport := scheduleFilingTransport()
	run := runFamilyCommand(t, transport, NewScheduleCmd(), "copy", "456")
	require.NoError(t, run.err)
	assert.Equal(t, []string{"/99999/recordings/456/copies"}, transport.posts)
	assert.Equal(t, float64(555), transport.bodies[0]["filing_destination_id"])
}

func TestScheduleMoveOfARecurringEventFilesTheSeries(t *testing.T) {
	transport := scheduleFilingTransport()
	// Basecamp answers a recurring event's read with an occurrence of it.
	transport.gets["/schedule_entries/457"] = `{"id":7001,"type":"Schedule::Entry::Occurrence","summary":"Standup","bucket":{"id":123,"name":"Source"},"parent":{"id":457,"title":"Standup","type":"Schedule::Entry"}}`
	transport.gets["/schedule_entries/4570"] = `{"id":7002,"type":"Schedule::Entry::Occurrence","summary":"Standup","bucket":{"id":999,"name":"Dest"},"parent":{"id":4570,"title":"Standup","type":"Schedule::Entry"}}`
	run := runFamilyCommand(t, transport, NewScheduleCmd(), "move", "457", "--to-project", "Dest")
	require.NoError(t, run.err)

	assert.Equal(t, []string{"/99999/recordings/457/moves"}, transport.posts)
	data := run.data(t)
	assert.Equal(t, float64(4570), data["id"], "the output names the moved event, not the occurrence read")
	assert.Equal(t, float64(7002), data["occurrence_id"])
	assert.Equal(t, "Schedule::Entry", data["type"])
	assert.Equal(t, float64(457), data["source_id"])
	assert.Equal(t, "Moved schedule entry #457 to Dest › Calendar as schedule entry #4570", run.envelope.Summary)
}

func TestScheduleMoveOfARecurringEventOntoItsOwnScheduleIsRefused(t *testing.T) {
	transport := scheduleFilingTransport()
	transport.gets["/schedule_entries/457"] = `{"id":7001,"type":"Schedule::Entry::Occurrence","summary":"Standup","bucket":{"id":123,"name":"Source"},"parent":{"id":457,"title":"Standup","type":"Schedule::Entry"}}`
	run := runFamilyCommand(t, transport, NewScheduleCmd(), "move", "457", "--to-project", "Source")
	e := run.failure(t)
	assert.Contains(t, e.Message, "is already in schedule 555")
	assert.Empty(t, transport.posts)
}
