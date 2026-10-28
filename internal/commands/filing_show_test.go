package commands

import (
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Reading a moved record by its old id answers with the record at its new id,
// and the old id no longer takes writes, so every follow-up a show command
// advertises names the id it read back.
func TestShowingAMovedRecordByItsOldIDFollowsUpOnTheNewID(t *testing.T) {
	for _, tc := range []struct {
		name string
		root func() *cobra.Command
		args []string
		path string
		body string
		want string
	}{
		{"cards show", NewCardsCmd, []string{"show"}, "/card_tables/cards/456",
			`{"id":4560,"type":"Kanban::Card","title":"Card","bucket":{"id":999,"name":"Dest"}}`, "basecamp cards done 4560"},
		{"cards column show", NewCardsCmd, []string{"column", "show"}, "/card_tables/columns/456",
			`{"id":4560,"type":"Kanban::Column","title":"Doing","bucket":{"id":999,"name":"Dest"}}`, "basecamp cards column update 4560 --in 999"},
		{"todos show", NewTodosCmd, []string{"show"}, "/todos/456",
			`{"id":4560,"type":"Todo","content":"Ship","bucket":{"id":999,"name":"Dest"}}`, "basecamp todos complete 4560"},
		{"todolists show", NewTodolistsCmd, []string{"show"}, "/todolists/456",
			`{"id":4560,"type":"Todolist","name":"Launch","bucket":{"id":999,"name":"Dest"}}`, "basecamp todos --list 4560"},
		{"messages show", NewMessagesCmd, []string{"show"}, "/messages/456",
			`{"id":4560,"type":"Message","subject":"Kickoff","bucket":{"id":999,"name":"Dest"}}`, "basecamp comments create 4560"},
		{"files show", NewFilesCmd, []string{"show"}, "/documents/456",
			`{"id":4560,"type":"Document","title":"Plan","bucket":{"id":999,"name":"Dest"}}`, "basecamp files update 4560 --in 999"},
		{"files show a folder", NewFilesCmd, []string{"show"}, "/vaults/456",
			`{"id":4560,"type":"Vault","title":"Specs","bucket":{"id":999,"name":"Dest"}}`, "basecamp files --vault 4560 --in 999"},
		{"schedule show", NewScheduleCmd, []string{"show"}, "/schedule_entries/456",
			`{"id":4560,"type":"Schedule::Entry","summary":"Launch","bucket":{"id":999,"name":"Dest"}}`, "basecamp schedule update 4560"},
		{"schedule show a recurring event", NewScheduleCmd, []string{"show"}, "/schedule_entries/456",
			`{"id":7000,"type":"Schedule::Entry::Occurrence","summary":"Standup","bucket":{"id":999,"name":"Dest"},"parent":{"id":4560,"type":"Schedule::Entry"}}`, "basecamp schedule update 4560"},
		{"checkins question show", NewCheckinsCmd, []string{"question", "show"}, "/questions/456",
			`{"id":4560,"type":"Question","title":"What did you work on?","bucket":{"id":999,"name":"Dest"}}`, "basecamp checkins answers 4560 --in 999"},
		{"forwards show", NewForwardsCmd, []string{"show"}, "/inbox_forwards/456",
			`{"id":4560,"type":"Inbox::Forward","subject":"Invoice","bucket":{"id":999,"name":"Dest"}}`, "basecamp forwards replies 4560 --in 999"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			transport := &familyTransport{gets: map[string]string{tc.path: tc.body}}
			run := runFamilyCommand(t, transport, tc.root(), append(tc.args, "456")...)
			require.NoError(t, run.err)

			var crumbs []string
			for _, crumb := range run.envelope.Breadcrumbs {
				crumbs = append(crumbs, crumb.Cmd)
				assert.NotRegexp(t, regexp.MustCompile(`\b456\b`), crumb.Cmd, "the old id takes no writes")
			}
			assert.True(t, slices.ContainsFunc(crumbs, func(cmd string) bool { return strings.HasPrefix(cmd, tc.want) }),
				"want %q among %q", tc.want, crumbs)
		})
	}
}
