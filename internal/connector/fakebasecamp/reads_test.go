package fakebasecamp_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/connector/fakebasecamp"
)

// The reads setup and admission make decode through the SDK the way the
// real ones do.

func TestAccountReads(t *testing.T) {
	s, bearer := start(t)
	s.Update(func(w *fakebasecamp.World) {
		w.People[fakebasecamp.AgentID].BossID = fakebasecamp.OperatorID
		w.People[1003] = &fakebasecamp.Person{ID: 1003, Name: "Client Person", Client: true}
		w.People[1004] = &fakebasecamp.Person{ID: 1004, Name: "Colleague", ReadableBy: []int64{fakebasecamp.OperatorID}}
	})
	agent := accountClient(s, bearer)
	operator := accountClient(s, fakebasecamp.OperatorToken)

	me, err := agent.People().Me(bounded(t))
	require.NoError(t, err)
	assert.Equal(t, fakebasecamp.AgentID, me.ID)
	assert.Equal(t, "Agent", me.PersonableType)
	id, ok := basecamp.PersonIDFromSGID(me.AttachableSGID)
	assert.True(t, ok)
	assert.Equal(t, fakebasecamp.AgentID, id, "the sgid names the person, as a mention's does")
	_, _, body := do(t, s, http.MethodGet, "/999/my/profile.json", bearer, "")
	assert.Contains(t, body, `"boss":{"id":26909558,"name":"Operator"}`)

	client, err := agent.People().Get(bounded(t), 1003)
	require.NoError(t, err)
	assert.True(t, client.Client)
	_, err = agent.People().Get(bounded(t), 1004)
	assertStatus(t, http.StatusForbidden, err)
	colleague, err := operator.People().Get(bounded(t), 1004)
	require.NoError(t, err)
	assert.Equal(t, "Colleague", colleague.Name)
	_, err = agent.People().Get(bounded(t), 424242)
	assertStatus(t, http.StatusNotFound, err)

	projects, err := agent.Projects().List(bounded(t), nil)
	require.NoError(t, err)
	require.Len(t, projects.Projects, 2)
	assert.Equal(t, fakebasecamp.ProjectID, projects.Projects[0].ID)
	project, err := agent.Projects().Get(bounded(t), fakebasecamp.ProjectID)
	require.NoError(t, err)
	assert.Equal(t, fakebasecamp.ProjectName, project.Name)
	people, err := agent.People().ListProjectPeople(bounded(t), fakebasecamp.ProjectID, nil)
	require.NoError(t, err)
	assert.Len(t, people.People, 2)

	// A project the caller is not on is not found.
	s.Update(func(w *fakebasecamp.World) {
		w.Projects[777] = &fakebasecamp.Project{ID: 777, Name: "Elsewhere", Members: []int64{fakebasecamp.OperatorID}}
	})
	_, err = agent.Projects().Get(bounded(t), 777)
	assertStatus(t, http.StatusNotFound, err)

	// authorization.json answers a person with a Launchpad identity, and
	// nobody else.
	status, _, _ := do(t, s, http.MethodGet, "/authorization.json", bearer, "")
	assert.Equal(t, http.StatusUnauthorized, status)
	s.Update(func(w *fakebasecamp.World) { w.People[fakebasecamp.OperatorID].IdentityID = 4242 })
	status, _, body = do(t, s, http.MethodGet, "/authorization.json", fakebasecamp.OperatorToken, "")
	assert.Equal(t, http.StatusOK, status)
	assert.Contains(t, body, `"id":4242`)

	// Nothing under the account answers without a token the fake issued.
	status, _, _ = do(t, s, http.MethodGet, "/999/projects.json", "forged", "")
	assert.Equal(t, http.StatusUnauthorized, status)
}

// Every recording the connector admits summarizes through the SDK's own
// router, with the agent's mention found where that type keeps its rich
// text; and the subscription and history reads answer what admission asks.
func TestRecordingReads(t *testing.T) {
	s, bearer := start(t)
	mention := "<p>Over to you " + fakebasecamp.Mention(fakebasecamp.AgentID) + "</p>"
	project := fakebasecamp.ProjectID
	s.Update(func(w *fakebasecamp.World) {
		for _, r := range []*fakebasecamp.Recording{
			{ID: 101, Type: "Message", Title: "Kickoff", Content: mention},
			{ID: 102, Type: "Comment", ParentID: 101, Content: mention, SubscriberIDs: []int64{fakebasecamp.AgentID}},
			{ID: 103, Type: "Todo", Title: "Ship it", Content: mention, AssigneeIDs: []int64{fakebasecamp.AgentID}},
			{ID: 104, Type: "Kanban::Card", Title: "Card", Content: mention},
			{ID: 105, Type: "Chat::Transcript", Title: "Campfire"},
			{ID: 106, Type: "Chat::Lines::RichText", ParentID: 105, Content: mention},
		} {
			r.BucketID, r.CreatorID = project, fakebasecamp.OperatorID
			w.Recordings[r.ID] = r
		}
	})
	agent := accountClient(s, bearer)

	for eventType, id := range map[string]int64{
		"message.created": 101, "comment.created": 102, "todo.created": 103, "card.created": 104, "chat.line.created": 106,
	} {
		summary, err := agent.Recordings().Summarize(bounded(t), basecamp.RecordingRef{BucketID: project, RecordingID: id, EventType: eventType})
		require.NoError(t, err, eventType)
		assert.Equal(t, id, summary.ID, eventType)
		assert.Equal(t, []int64{fakebasecamp.AgentID}, summary.MentionedPersonIDs, eventType)
		assert.Equal(t, fakebasecamp.OperatorID, summary.Creator.ID, eventType)
	}
	todo, err := agent.Todos().Get(bounded(t), 103)
	require.NoError(t, err)
	require.Len(t, todo.Assignees, 1)
	assert.Equal(t, fakebasecamp.AgentID, todo.Assignees[0].ID)

	// A typed read of another type's recording is not found.
	_, err = agent.Comments().Get(bounded(t), 101)
	assertStatus(t, http.StatusNotFound, err)

	sub, err := agent.Subscriptions().Get(bounded(t), 102)
	require.NoError(t, err)
	assert.True(t, sub.Subscribed)
	sub, err = agent.Subscriptions().Get(bounded(t), 101)
	require.NoError(t, err)
	assert.False(t, sub.Subscribed)

	// An emitted event about a recording lands in its history, under the
	// feed's id, with the delta admission reads.
	ev := s.Emit(fakebasecamp.Event{
		EventType: "todo.assignment_changed", BucketID: project, RecordingID: 103,
		CreatorID: fakebasecamp.OperatorID, AddedPersonIDs: []int64{fakebasecamp.AgentID},
	}, fakebasecamp.Both)
	history, err := agent.Events().List(bounded(t), 103, &basecamp.EventListOptions{Page: 1})
	require.NoError(t, err)
	require.Len(t, history.Events, 1)
	assert.Equal(t, ev.ID, history.Events[0].ID)
	assert.Equal(t, "assignment_changed", history.Events[0].Action)
	require.NotNil(t, history.Events[0].Details)
	assert.Equal(t, []int64{fakebasecamp.AgentID}, history.Events[0].Details.AddedPersonIDs)
	empty, err := agent.Events().List(bounded(t), 103, &basecamp.EventListOptions{Page: 2})
	require.NoError(t, err)
	assert.Empty(t, empty.Events)

	// Nothing in a project the caller is not on can be read.
	s.Update(func(w *fakebasecamp.World) {
		w.Projects[project].Members = []int64{fakebasecamp.OperatorID}
	})
	_, err = agent.Recordings().Summarize(bounded(t), basecamp.RecordingRef{BucketID: project, RecordingID: 102, EventType: "comment.created"})
	assertStatus(t, http.StatusNotFound, err)
	_, err = agent.Subscriptions().Get(bounded(t), 102)
	assertStatus(t, http.StatusNotFound, err)
}

func assertStatus(t *testing.T, status int, err error) {
	t.Helper()
	var apiErr *basecamp.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, status, apiErr.HTTPStatus)
}
