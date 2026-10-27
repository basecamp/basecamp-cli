package commands

import (
	"encoding/json"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/output"
)

func TestTodolistsMoveToAProjectWithOneTodosetFilesIntoIt(t *testing.T) {
	transport := &mockTodoFilingTransport{}
	run := runTodoFilingCommand(t, transport, NewTodolistsCmd(), "move", "310", "--to-project", "Dest")
	require.NoError(t, run.err)

	assert.Equal(t, []string{"/recordings/310/moves"}, transport.posts)
	assert.Equal(t, float64(900), transport.bodies[0]["filing_destination_id"])
	var list map[string]any
	require.NoError(t, json.Unmarshal(run.data, &list))
	assert.Equal(t, float64(3101), list["id"])
	assert.Equal(t, float64(310), list["source_id"])
	assert.Equal(t, "Moved to-do list #310 to Dest › To-dos as to-do list #3101", run.summary)
	assert.Contains(t, run.output, "basecamp todolists show 3101 --in 999")
}

func TestTodolistsMoveToAProjectWithSeveralTodosetsIsAmbiguous(t *testing.T) {
	transport := &mockTodoFilingTransport{twoTodosets: true}
	run := runTodoFilingCommand(t, transport, NewTodolistsCmd(), "move", "310", "--to-project", "Dest")
	e := requireUsageRefusal(t, run, transport)

	assert.Equal(t, output.CodeAmbiguous, e.Code)
	assert.Contains(t, e.Hint, "--to-todoset")
	assert.Contains(t, e.Hint, "900  To-dos")
	assert.Contains(t, e.Hint, "950  To-dos")
	assert.NotContains(t, e.Hint, "960", "a To-dos turned off is not a destination")
}

func TestTodolistsMoveToAPickedTodoset(t *testing.T) {
	transport := &mockTodoFilingTransport{twoTodosets: true}
	run := runTodoFilingCommand(t, transport, NewTodolistsCmd(), "move", "310", "--to-project", "Dest", "--to-todoset", "950")
	require.NoError(t, run.err)

	assert.Equal(t, float64(950), transport.bodies[0]["filing_destination_id"])
}

func TestTodolistsMoveToAProjectWithoutTodosSaysHowToTurnItOn(t *testing.T) {
	transport := &mockTodoFilingTransport{}
	run := runTodoFilingCommand(t, transport, NewTodolistsCmd(), "move", "310", "--to-project", "Bare")
	e := requireUsageRefusal(t, run, transport)

	assert.Equal(t, "Project Bare has no To-dos", e.Message)
	assert.Contains(t, e.Hint, "basecamp tools enable 880 --in 888")
}

func TestTodolistsMoveAGroupFilesItIntoAList(t *testing.T) {
	transport := &mockTodoFilingTransport{}
	run := runTodoFilingCommand(t, transport, NewTodolistsCmd(), "move", "311", "--to-project", "Dest", "--to-list", "Launch")
	require.NoError(t, run.err)

	assert.Equal(t, []string{"/recordings/311/moves"}, transport.posts)
	assert.Equal(t, float64(901), transport.bodies[0]["filing_destination_id"])
	assert.Equal(t, "Moved to-do group #311 to Dest › Launch as to-do group #3111", run.summary)
}

func TestTodolistsMoveRefusesTheWrongKindOfDestination(t *testing.T) {
	for name, tc := range map[string]struct {
		args []string
		hint string
	}{
		"a list into a list":   {[]string{"move", "310", "--to-list", "901"}, "--to-project <project>"},
		"a group into To-dos":  {[]string{"move", "311", "--to-todoset", "900"}, "--to-list <list>"},
		"both kinds named":     {[]string{"move", "310", "--to-todoset", "900", "--to-list", "901"}, "a group goes into a to-do list"},
		"a reorder flag mixed": {[]string{"move", "310", "--to-project", "Dest", "--to", "1"}, ""},
	} {
		t.Run(name, func(t *testing.T) {
			transport := &mockTodoFilingTransport{}
			run := runTodoFilingCommand(t, transport, NewTodolistsCmd(), tc.args...)
			e := requireUsageRefusal(t, run, transport)

			assert.Equal(t, output.CodeUsage, e.Code)
			assert.Contains(t, e.Hint, tc.hint)
		})
	}
}

func TestTodolistsMoveRefusesWhereItAlreadyIs(t *testing.T) {
	for name, tc := range map[string]struct {
		group *cobra.Command
		args  []string
		hint  string
	}{
		"a list in its To-dos": {NewTodolistsCmd(), []string{"move", "310", "--to-todoset", "300"}, "basecamp todolists move 310 --to <position>"},
		"a group in its list":  {NewTodolistgroupsCmd(), []string{"move", "311", "--to-list", "310"}, "basecamp todolistgroups move 311 --position <position>"},
	} {
		t.Run(name, func(t *testing.T) {
			transport := &mockTodoFilingTransport{}
			run := runTodoFilingCommand(t, transport, tc.group, tc.args...)
			e := requireUsageRefusal(t, run, transport)

			assert.Contains(t, e.Message, "is already in")
			assert.Equal(t, "Reorder it there with: "+tc.hint, e.Hint)
		})
	}
}

func TestTodolistsRepositionStillReordersWithoutFilingFlags(t *testing.T) {
	transport := &mockTodoFilingTransport{}
	run := runTodoFilingCommand(t, transport, NewTodolistsCmd(), "move", "310", "--to", "2")
	require.NoError(t, run.err)

	assert.Empty(t, transport.posts)
	assert.Contains(t, transport.requests, "PUT /todosets/todolists/310/position")
	assert.Equal(t, "Moved todolist #310 to position 2", run.summary)
}

func TestEveryTodolistGroupAliasMovesAndCopiesTheSameWay(t *testing.T) {
	for _, group := range []string{"todolists", "todolist", "todolistgroups", "todolistgroup", "tlgroups", "tlgroup"} {
		for verb, path := range map[string]string{"move": "/recordings/311/moves", "copy": "/recordings/311/copies"} {
			t.Run(group+" "+verb, func(t *testing.T) {
				root := &cobra.Command{Use: "basecamp"}
				root.AddCommand(NewTodolistsCmd(), NewTodolistgroupsCmd())
				transport := &mockTodoFilingTransport{}
				run := runTodoFilingCommand(t, transport, root, group, verb, "311", "--to-list", "901")
				require.NoError(t, run.err)

				assert.Equal(t, []string{path}, transport.posts)
				assert.Equal(t, float64(901), transport.bodies[0]["filing_destination_id"])
			})
		}
	}
}

func TestTodolistgroupsMoveRefusesItsOwnListFlagAsTheDestination(t *testing.T) {
	transport := &mockTodoFilingTransport{}
	run := runTodoFilingCommand(t, transport, NewTodolistgroupsCmd(), "move", "311", "--list", "901", "--to-project", "Dest")
	e := requireUsageRefusal(t, run, transport)

	assert.Equal(t, "Use --to-list for the destination list", e.Hint)
}

func TestTodolistsCopyWithoutDestinationGoesNextToTheOriginal(t *testing.T) {
	for name, tc := range map[string]struct {
		id   string
		into float64
	}{
		"a list":  {"310", 300},
		"a group": {"311", 310},
	} {
		t.Run(name, func(t *testing.T) {
			transport := &mockTodoFilingTransport{}
			run := runTodoFilingCommand(t, transport, NewTodolistsCmd(), "copy", tc.id, "--include-comments")
			require.NoError(t, run.err)

			assert.Equal(t, []string{"/recordings/" + tc.id + "/copies"}, transport.posts)
			assert.Equal(t, tc.into, transport.bodies[0]["filing_destination_id"])
			assert.Equal(t, true, transport.bodies[0]["include_comments"])
		})
	}
}

func TestTodolistsCopyToAnotherProjectIsNotRetried(t *testing.T) {
	transport := &mockTodoFilingTransport{postStatus: 502}
	run := runTodoFilingCommand(t, transport, NewTodolistsCmd(), "copy", "310", "--to-project", "Dest")
	e := output.AsError(run.err)
	require.NotNil(t, run.err)

	assert.Len(t, transport.posts, 1)
	assert.False(t, e.Retryable)
	assert.Contains(t, e.Hint, "basecamp recordings list Todolist --in 999 --status active --sort created_at --direction desc --all")
}

// A filing's recovery points at `todolists show <id>`, which reads the list by
// its account-wide ID, so it must not need a project to be configured.
func TestTodolistsShowNeedsNoProject(t *testing.T) {
	transport := &mockTodoFilingTransport{}
	app, buf := newTestAppWithTransport(t, transport)
	app.Config.ProjectID = ""
	app.Flags.Project = ""

	require.NoError(t, executeCommand(NewTodolistsCmd(), app, "show", "901"))
	assert.Contains(t, buf.String(), `901`)
}
