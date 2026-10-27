package commands

import (
	"strings"
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

// A recurring event in an employee calendar (bucket 777, a Calendar, not a
// project) is read as an occurrence; its schedule, 770, is the one the
// calendar links to.
func calendarTransport() *familyTransport {
	transport := scheduleFilingTransport()
	transport.gets["/calendars/777"] = `{"id":777,"type":"Calendar","name":"Jason's calendar","schedule_url":"https://3.basecampapi.com/99999/buckets/777/schedules/770.json"}`
	transport.gets["/schedules/770"] = `{"id":770,"title":"Jason's calendar","bucket":{"id":777,"name":"Jason's calendar","type":"Calendar"}}`
	transport.gets["/schedule_entries/458"] = `{"id":7003,"type":"Schedule::Entry::Occurrence","summary":"1:1","bucket":{"id":777,"name":"Jason's calendar","type":"Calendar"},"parent":{"id":458,"title":"1:1","type":"Schedule::Entry"}}`
	transport.gets["/schedule_entries/4580"] = `{"id":7004,"type":"Schedule::Entry::Occurrence","summary":"1:1","bucket":{"id":999,"name":"Dest","type":"Project"},"parent":{"id":4580,"title":"1:1","type":"Schedule::Entry"}}`
	transport.gets["/schedule_entries/4570"] = `{"id":7002,"type":"Schedule::Entry::Occurrence","summary":"Standup","bucket":{"id":777,"name":"Jason's calendar","type":"Calendar"},"parent":{"id":4570,"title":"Standup","type":"Schedule::Entry"}}`
	transport.gets["/schedule_entries/457"] = `{"id":7001,"type":"Schedule::Entry::Occurrence","summary":"Standup","bucket":{"id":123,"name":"Source","type":"Project"},"parent":{"id":457,"title":"Standup","type":"Schedule::Entry"}}`
	return transport
}

func TestScheduleFilesARecurringCalendarEventFromItsCalendarsSchedule(t *testing.T) {
	transport := calendarTransport()
	run := runFamilyCommand(t, transport, NewScheduleCmd(), "copy", "458")
	require.NoError(t, run.err)
	assert.Equal(t, float64(770), transport.bodies[0]["filing_destination_id"], "a copy with no destination goes onto the calendar it is on")

	transport = calendarTransport()
	run = runFamilyCommand(t, transport, NewScheduleCmd(), "move", "458", "--to-project", "Dest")
	require.NoError(t, run.err)
	assert.Equal(t, "Moved schedule entry #458 to Dest › Calendar as schedule entry #4580", run.envelope.Summary)

	transport = calendarTransport()
	e := runFamilyCommand(t, transport, NewScheduleCmd(), "move", "458", "--to-schedule", "770").failure(t)
	assert.Contains(t, e.Message, "is already in schedule 770")
	assert.Empty(t, transport.posts)
}

func TestScheduleFilesARecurringEventIntoACalendarAndReadsItBack(t *testing.T) {
	transport := calendarTransport()
	run := runFamilyCommand(t, transport, NewScheduleCmd(), "move", "457", "--to-schedule", "770")
	require.NoError(t, run.err, "the event lands in the calendar and is read back there")
	assert.Equal(t, float64(770), transport.bodies[0]["filing_destination_id"])
	assert.Equal(t, "Moved schedule entry #457 to Jason's calendar › Jason's calendar as schedule entry #4570", run.envelope.Summary)
}

func TestScheduleFindsARecurringEventsScheduleAmongSeveral(t *testing.T) {
	twoSchedules := func() *familyTransport {
		transport := calendarTransport()
		transport.gets["/projects/123"] = `{"id":123,"name":"Source","dock":[{"name":"schedule","id":555,"title":"Schedule","enabled":true},{"name":"schedule","id":556,"title":"Launches","enabled":true}]}`
		transport.gets["/schedules/555/entries"] = `[{"id":456,"type":"Schedule::Entry","summary":"Launch"}]`
		transport.gets["/schedules/556/entries"] = `[{"id":457,"type":"Schedule::Entry","summary":"Standup"}]`
		transport.gets["/schedules/556"] = `{"id":556,"title":"Launches","bucket":{"id":123,"name":"Source"}}`
		return transport
	}
	transport := twoSchedules()
	run := runFamilyCommand(t, transport, NewScheduleCmd(), "copy", "457")
	require.NoError(t, run.err)
	assert.Equal(t, float64(556), transport.bodies[0]["filing_destination_id"], "the copy goes onto the schedule listing the event")

	transport = twoSchedules()
	e := runFamilyCommand(t, transport, NewScheduleCmd(), "move", "457", "--to-schedule", "556").failure(t)
	assert.Contains(t, e.Message, "is already in schedule 556")
	assert.Empty(t, transport.posts)
}

// An entries listing shows active entries unless asked for another status,
// so an archived series is found among its project's schedules only in the
// archived listing.
func TestScheduleFindsAnArchivedRecurringEventsScheduleAmongSeveral(t *testing.T) {
	transport := scheduleFilingTransport()
	transport.gets["/projects/123"] = `{"id":123,"name":"Source","dock":[{"name":"schedule","id":555,"title":"Schedule","enabled":true},{"name":"schedule","id":556,"title":"Launches","enabled":true}]}`
	transport.gets["/schedules/556"] = `{"id":556,"title":"Launches","bucket":{"id":123,"name":"Source"}}`
	transport.gets["/schedule_entries/457"] = `{"id":7001,"status":"archived","type":"Schedule::Entry::Occurrence","summary":"Standup","bucket":{"id":123,"name":"Source","type":"Project"},"parent":{"id":457,"title":"Standup","type":"Schedule::Entry"}}`
	transport.gets["/schedules/555/entries"] = `[]`
	transport.gets["/schedules/556/entries"] = `[]`
	transport.gets["/schedules/555/entries?status=archived"] = `[]`
	transport.gets["/schedules/556/entries?status=archived"] = `[{"id":457,"status":"archived","type":"Schedule::Entry","summary":"Standup"}]`
	transport.gets["/schedule_entries/4570"] = `{"id":7002,"status":"archived","type":"Schedule::Entry::Occurrence","summary":"Standup","bucket":{"id":123,"name":"Source","type":"Project"},"parent":{"id":4570,"title":"Standup","type":"Schedule::Entry"}}`

	run := runFamilyCommand(t, transport, NewScheduleCmd(), "copy", "457")
	require.NoError(t, run.err)
	assert.Equal(t, []string{"/99999/recordings/457/copies"}, transport.posts)
	assert.Equal(t, float64(556), transport.bodies[0]["filing_destination_id"], "the copy goes onto the schedule whose archived entries list the event")
}

func TestScheduleCopyIntoACalendarIsLookedForInItsEntries(t *testing.T) {
	transport := calendarTransport()
	transport.stuck = map[int64]bool{456: true}
	e := runFamilyCommand(t, transport, NewScheduleCmd(), "copy", "456", "--to-schedule", "770").failure(t)
	assert.Equal(t, output.CodeInProgress, e.Code)
	assert.Contains(t, e.Hint, "basecamp schedule entries --in 777 --schedule 770 --status active --all",
		"the recordings listing does not serve a calendar")
}

// Basecamp lists drafted schedule entries nowhere in its API, neither among
// drafts nor in a recordings or entries listing.
func TestScheduleCopyOfADraftThatTimesOutNamesNoListing(t *testing.T) {
	for _, dest := range []string{"900", "770"} {
		transport := calendarTransport()
		transport.gets["/schedule_entries/456"] = strings.Replace(transport.gets["/schedule_entries/456"], `"type":"Schedule::Entry"`, `"type":"Schedule::Entry","status":"drafted"`, 1)
		transport.stuck = map[int64]bool{456: true}
		e := runFamilyCommand(t, transport, NewScheduleCmd(), "copy", "456", "--to-schedule", dest).failure(t)
		assert.Equal(t, output.CodeInProgress, e.Code)
		assert.Contains(t, e.Hint, "Basecamp's API lists no drafted Schedule::Entry", dest)
		assert.NotContains(t, e.Hint, "--status drafted", dest)
	}
}
