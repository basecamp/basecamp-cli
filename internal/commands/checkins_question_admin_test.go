package commands

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
)

const checkinsRemindersPath = "/99999/my/question_reminders.json"

func checkinsQuestionSubPath(id int64, suffix string) string {
	return fmt.Sprintf("/99999/questions/%d/%s", id, suffix)
}

func TestCheckinsQuestionPauseAndResume(t *testing.T) {
	for _, tc := range []struct {
		verb   string
		suffix string
		method string
	}{
		// resume is a DELETE against the same pause resource, not its own path.
		{"pause", "pause.json", http.MethodPost},
		{"resume", "pause.json", http.MethodDelete},
	} {
		t.Run(tc.verb, func(t *testing.T) {
			path := checkinsQuestionSubPath(789, tc.suffix)
			app, transport, _ := setupPersonalFeedApp(t, noContentRoute(tc.method, path))

			require.NoError(t, executeRecordingCommand(NewCheckinsCmd(), app, "question", tc.verb, "789"))

			call := transport.last(t)
			assert.Equal(t, tc.method, call.Method)
			assert.Equal(t, path, call.Path)
		})
	}
}

// The notification settings are tri-state: an unpassed flag must stay out of
// the request entirely so the server leaves that setting alone, while an
// explicit --no-... must send false. A single bool could not tell those apart
// and would silently overwrite a setting nobody asked about.
func TestCheckinsQuestionNotifyIsTriState(t *testing.T) {
	settingsRoute := stubRoute{
		method: http.MethodPut,
		path:   checkinsQuestionSubPath(789, "notification_settings.json"),
		status: http.StatusOK,
		body:   `{"responding": true, "subscribed": true}`,
	}

	for _, tc := range []struct {
		name string
		args []string
		want map[string]any
	}{
		{
			name: "only --on-answer is sent",
			args: []string{"question", "notify", "789", "--on-answer"},
			want: map[string]any{"notify_on_answer": true},
		},
		{
			name: "--no-on-answer sends an explicit false",
			args: []string{"question", "notify", "789", "--no-on-answer"},
			want: map[string]any{"notify_on_answer": false},
		},
		{
			name: "the untouched setting is omitted",
			args: []string{"question", "notify", "789", "--digest-include-unanswered"},
			want: map[string]any{"digest_include_unanswered": true},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app, transport, _ := setupPersonalFeedApp(t, settingsRoute)

			require.NoError(t, executeRecordingCommand(NewCheckinsCmd(), app, tc.args...))

			var body map[string]any
			require.NoError(t, json.Unmarshal([]byte(transport.last(t).Body), &body))
			assert.Equal(t, tc.want, body, "only the named settings may reach the wire")
		})
	}
}

func TestCheckinsQuestionNotifyRejectsContradictoryFlags(t *testing.T) {
	app, transport, _ := setupPersonalFeedApp(t)

	err := executeRecordingCommand(NewCheckinsCmd(), app,
		"question", "notify", "789", "--on-answer", "--no-on-answer")

	outErr := requireBookmarksUsageError(t, err)
	assert.Contains(t, outErr.Message, "mutually exclusive")
	assert.Empty(t, transport.recorded())
}

func TestCheckinsQuestionNotifyRequiresASetting(t *testing.T) {
	app, transport, _ := setupPersonalFeedApp(t)

	err := executeRecordingCommand(NewCheckinsCmd(), app, "question", "notify", "789")

	requireBookmarksUsageError(t, err)
	assert.Empty(t, transport.recorded(), "a no-op update must not reach the server")
}

func TestCheckinsQuestionAnswerersLists(t *testing.T) {
	app, transport, _ := setupPersonalFeedApp(t, stubRoute{
		method: http.MethodGet,
		path:   checkinsQuestionSubPath(789, "answers/by.json"),
		status: http.StatusOK,
		body:   `[{"id": 1, "name": "Ann"}, {"id": 2, "name": "Bob"}]`,
	})

	require.NoError(t, executeRecordingCommand(NewCheckinsCmd(), app, "question", "answerers", "789"))

	assert.Equal(t, checkinsQuestionSubPath(789, "answers/by.json"), transport.last(t).Path)
}

// PeopleListOptions and QuestionReminderListOptions both document that the page
// number is not honored, so neither command registers --page. A flag that
// cannot do what it says is the defect the pagination contract exists to stop.
func TestCheckinsPageIsNotRegisteredWhereItCannotWork(t *testing.T) {
	root := NewCheckinsCmd()

	for _, path := range [][]string{
		{"question", "answerers"},
		{"reminders"},
	} {
		t.Run(fmt.Sprint(path), func(t *testing.T) {
			cmd, _, err := root.Find(path)
			require.NoError(t, err)
			assert.Nil(t, cmd.Flags().Lookup("page"),
				"the SDK does not honor a page number here, so no --page may be offered")
			assert.NotNil(t, cmd.Flags().Lookup("limit"), "--limit is a real bound and stays")
		})
	}
}

func TestCheckinsRemindersLists(t *testing.T) {
	app, transport, out := setupPersonalFeedApp(t, stubRoute{
		method: http.MethodGet,
		path:   checkinsRemindersPath,
		status: http.StatusOK,
		body: `[{
			"group_on": "2026-08-01",
			"remind_at": "2026-08-01T09:00:00.000Z",
			"reminder_id": 5,
			"question": {
				"id": 789, "title": "What did you work on?", "status": "active",
				"created_at": "2026-06-01T10:00:00.000Z",
				"updated_at": "2026-06-01T10:00:00.000Z",
				"bucket": {"id": 977190, "name": "JD test proj", "type": "Project"}
			}
		}]`,
	})

	require.NoError(t, executeRecordingCommand(NewCheckinsCmd(), app, "reminders"))

	assert.Equal(t, checkinsRemindersPath, transport.last(t).Path)

	var envelope struct {
		Summary string `json:"summary"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &envelope))
	assert.Equal(t, "1 pending check-in reminders", envelope.Summary)
}

// The reminder feed nests the question it is about, and the renderer skips
// nested objects — so a generic render would say when something is due without
// saying what, or where.
func TestFlattenQuestionRemindersCarriesTheQuestion(t *testing.T) {
	rows := flattenQuestionReminders([]basecamp.QuestionReminder{{
		RemindAt: time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC),
		Question: basecamp.Question{
			ID:     789,
			Title:  "What did you work on?",
			Bucket: &basecamp.Bucket{ID: 977190, Name: "JD test proj"},
		},
	}})

	require.Len(t, rows, 1)
	assert.Equal(t, int64(789), rows[0]["question_id"])
	assert.Equal(t, "What did you work on?", rows[0]["question"])
	assert.Equal(t, "JD test proj", rows[0]["project"])
	assert.Contains(t, rows[0], "remind_at")
}
