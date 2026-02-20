package data

import (
	"testing"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHubNewHasGlobalRealm(t *testing.T) {
	h := NewHub(nil, nil)
	require.NotNil(t, h.Global())
	assert.Nil(t, h.Account())
	assert.Nil(t, h.Project())
}

func TestHubEnsureAccount(t *testing.T) {
	h := NewHub(nil, nil)

	r := h.EnsureAccount("123")
	require.NotNil(t, r)
	assert.Equal(t, "account:123", r.Name())
	assert.Same(t, r, h.Account())

	// Calling again returns the same realm.
	r2 := h.EnsureAccount("123")
	assert.Same(t, r, r2)
}

func TestHubSwitchAccount(t *testing.T) {
	h := NewHub(nil, nil)
	r1 := h.EnsureAccount("aaa")
	r1Ctx := r1.Context()

	h.SwitchAccount("bbb")

	// Old realm is torn down.
	assert.Error(t, r1Ctx.Err())

	// New realm created.
	r2 := h.Account()
	require.NotNil(t, r2)
	assert.Equal(t, "account:bbb", r2.Name())
	assert.NotSame(t, r1, r2)
}

func TestHubSwitchAccountTearsDownProject(t *testing.T) {
	h := NewHub(nil, nil)
	h.EnsureAccount("aaa")
	pr := h.EnsureProject(42)
	prCtx := pr.Context()

	h.SwitchAccount("bbb")

	assert.Error(t, prCtx.Err())
	assert.Nil(t, h.Project())
}

func TestHubEnsureProject(t *testing.T) {
	h := NewHub(nil, nil)
	h.EnsureAccount("aaa")

	r := h.EnsureProject(42)
	require.NotNil(t, r)
	assert.Equal(t, "project:42", r.Name())
	assert.Same(t, r, h.Project())

	// Context is child of account realm.
	h.Account().Teardown()
	assert.Error(t, r.Context().Err())
}

func TestHubLeaveProject(t *testing.T) {
	h := NewHub(nil, nil)
	h.EnsureAccount("aaa")
	pr := h.EnsureProject(42)
	prCtx := pr.Context()

	h.LeaveProject()
	assert.Error(t, prCtx.Err())
	assert.Nil(t, h.Project())
}

func TestHubShutdown(t *testing.T) {
	h := NewHub(nil, nil)
	h.EnsureAccount("aaa")
	h.EnsureProject(42)

	gCtx := h.Global().Context()
	aCtx := h.Account().Context()
	pCtx := h.Project().Context()

	h.Shutdown()

	assert.Error(t, gCtx.Err())
	assert.Error(t, aCtx.Err())
	assert.Error(t, pCtx.Err())
	assert.Nil(t, h.Account())
	assert.Nil(t, h.Project())
}

func TestHubProjectWithoutAccount(t *testing.T) {
	h := NewHub(nil, nil)

	// Project realm without account uses global as parent.
	pr := h.EnsureProject(42)
	require.NotNil(t, pr)
	assert.NoError(t, pr.Context().Err())
}

func TestHubEnsureAccountSameIDReuses(t *testing.T) {
	h := NewHub(nil, nil)
	r1 := h.EnsureAccount("aaa")
	r2 := h.EnsureAccount("aaa")
	assert.Same(t, r1, r2, "same ID should return same realm")
}

func TestHubEnsureAccountDifferentIDTearsDown(t *testing.T) {
	h := NewHub(nil, nil)
	r1 := h.EnsureAccount("aaa")
	r1Ctx := r1.Context()

	r2 := h.EnsureAccount("bbb")

	// Old realm should be torn down.
	assert.Error(t, r1Ctx.Err())
	assert.NotSame(t, r1, r2)
	assert.Equal(t, "account:bbb", r2.Name())
}

func TestHubEnsureAccountDifferentIDTearsDownProject(t *testing.T) {
	h := NewHub(nil, nil)
	h.EnsureAccount("aaa")
	pr := h.EnsureProject(42)
	prCtx := pr.Context()

	h.EnsureAccount("bbb")

	// Project realm should also be torn down.
	assert.Error(t, prCtx.Err())
	assert.Nil(t, h.Project())
}

func TestHubEnsureProjectSameIDReuses(t *testing.T) {
	h := NewHub(nil, nil)
	h.EnsureAccount("aaa")
	r1 := h.EnsureProject(42)
	r2 := h.EnsureProject(42)
	assert.Same(t, r1, r2, "same ID should return same realm")
}

func TestHubEnsureProjectDifferentIDTearsDown(t *testing.T) {
	h := NewHub(nil, nil)
	h.EnsureAccount("aaa")
	r1 := h.EnsureProject(42)
	r1Ctx := r1.Context()

	r2 := h.EnsureProject(99)

	// Old project realm should be torn down.
	assert.Error(t, r1Ctx.Err())
	assert.NotSame(t, r1, r2)
	assert.Equal(t, "project:99", r2.Name())
}

func TestHubDependencies(t *testing.T) {
	ms := NewMultiStore(nil)
	poller := NewPoller()
	h := NewHub(ms, poller)

	assert.Same(t, ms, h.MultiStore())
	assert.Same(t, poller, h.Poller())
}

// -- Context helper tests

func TestHubProjectContext(t *testing.T) {
	h := NewHub(NewMultiStore(nil), nil)

	// No account, no project — falls back to global.
	ctx := h.ProjectContext()
	assert.NoError(t, ctx.Err())

	// With account — falls back to account.
	h.EnsureAccount("aaa")
	ctx = h.ProjectContext()
	assert.NoError(t, ctx.Err())

	// With project — returns project context.
	pr := h.EnsureProject(42)
	ctx = h.ProjectContext()
	assert.Equal(t, pr.Context(), ctx)

	// LeaveProject — falls back to account.
	h.LeaveProject()
	ctx = h.ProjectContext()
	assert.NoError(t, ctx.Err())
}

func TestHubProjectContextCanceledOnLeave(t *testing.T) {
	h := NewHub(NewMultiStore(nil), nil)
	h.EnsureAccount("aaa")
	h.EnsureProject(42)
	ctx := h.ProjectContext()
	assert.NoError(t, ctx.Err())

	h.LeaveProject()
	assert.Error(t, ctx.Err(), "project context should be canceled on leave")
}

func TestHubAccountContext(t *testing.T) {
	h := NewHub(NewMultiStore(nil), nil)

	// No account — falls back to global.
	ctx := h.AccountContext()
	assert.NoError(t, ctx.Err())

	// With account.
	acct := h.EnsureAccount("aaa")
	ctx = h.AccountContext()
	assert.Equal(t, acct.Context(), ctx)
}

func TestHubAccountContextCanceledOnSwitch(t *testing.T) {
	h := NewHub(NewMultiStore(nil), nil)
	h.EnsureAccount("aaa")
	ctx := h.AccountContext()
	assert.NoError(t, ctx.Err())

	h.SwitchAccount("bbb")
	assert.Error(t, ctx.Err(), "account context should be canceled on switch")
}

// -- Typed pool accessor tests

func TestHubScheduleEntries(t *testing.T) {
	h := NewHub(NewMultiStore(nil), nil)
	h.EnsureAccount("aaa")

	pool := h.ScheduleEntries(42, 99)
	require.NotNil(t, pool)
	assert.Equal(t, "schedule-entries:42:99", pool.Key())

	// Same call returns same pool (memoized via RealmPool).
	pool2 := h.ScheduleEntries(42, 99)
	assert.Same(t, pool, pool2)

	// Different IDs return different pool.
	pool3 := h.ScheduleEntries(42, 100)
	assert.NotSame(t, pool, pool3)
	assert.Equal(t, "schedule-entries:42:100", pool3.Key())
}

func TestHubScheduleEntriesScopedToProject(t *testing.T) {
	h := NewHub(NewMultiStore(nil), nil)
	h.EnsureAccount("aaa")

	pool := h.ScheduleEntries(42, 99)
	require.NotNil(t, pool)

	// Switching to a different project tears down the pool.
	h.EnsureProject(99) // different project ID
	pool2 := h.ScheduleEntries(99, 99)
	assert.NotSame(t, pool, pool2)
}

func TestHubCheckins(t *testing.T) {
	h := NewHub(NewMultiStore(nil), nil)
	h.EnsureAccount("aaa")

	pool := h.Checkins(42, 88)
	require.NotNil(t, pool)
	assert.Equal(t, "checkins:42:88", pool.Key())

	pool2 := h.Checkins(42, 88)
	assert.Same(t, pool, pool2)
}

func TestHubCampfireLines(t *testing.T) {
	h := NewHub(NewMultiStore(nil), nil)
	h.EnsureAccount("aaa")

	pool := h.CampfireLines(42, 55)
	require.NotNil(t, pool)
	assert.Equal(t, "campfire-lines:42:55", pool.Key())

	pool2 := h.CampfireLines(42, 55)
	assert.Same(t, pool, pool2)

	// Verify polling config was set.
	assert.NotZero(t, pool.PollInterval(), "campfire pool should have non-zero poll interval")
}

func TestHubMessages(t *testing.T) {
	h := NewHub(NewMultiStore(nil), nil)
	h.EnsureAccount("aaa")

	pool := h.Messages(42, 33)
	require.NotNil(t, pool)
	assert.Equal(t, "messages:42:33", pool.Key())

	pool2 := h.Messages(42, 33)
	assert.Same(t, pool, pool2)
}

func TestHubDocsFiles(t *testing.T) {
	h := NewHub(NewMultiStore(nil), nil)
	h.EnsureAccount("aaa")

	pool := h.DocsFiles(42, 77)
	require.NotNil(t, pool)
	assert.Equal(t, "docsfiles:42:77", pool.Key())

	pool2 := h.DocsFiles(42, 77)
	assert.Same(t, pool, pool2)
}

func TestHubPeople(t *testing.T) {
	h := NewHub(NewMultiStore(nil), nil)
	h.EnsureAccount("aaa")

	pool := h.People()
	require.NotNil(t, pool)
	assert.Equal(t, "people", pool.Key())

	pool2 := h.People()
	assert.Same(t, pool, pool2)
}

func TestHubPeoplePanicsWithoutAccount(t *testing.T) {
	h := NewHub(NewMultiStore(nil), nil)
	assert.Panics(t, func() { h.People() }, "People() without EnsureAccount should panic")
}

func TestHubPeopleScopedToAccount(t *testing.T) {
	h := NewHub(NewMultiStore(nil), nil)
	h.EnsureAccount("aaa")

	pool := h.People()
	require.NotNil(t, pool)

	// Switching accounts tears down the account realm and its pools.
	h.SwitchAccount("bbb")
	pool2 := h.People()
	assert.NotSame(t, pool, pool2, "account switch should produce fresh pool")
}

func TestHubForwards(t *testing.T) {
	h := NewHub(NewMultiStore(nil), nil)
	h.EnsureAccount("aaa")

	pool := h.Forwards(42, 66)
	require.NotNil(t, pool)
	assert.Equal(t, "forwards:42:66", pool.Key())

	pool2 := h.Forwards(42, 66)
	assert.Same(t, pool, pool2)
}

// -- Card column/card mapping tests (pure functions extracted from Hub.Cards)

func TestIsColumnDeferred(t *testing.T) {
	assert.True(t, isColumnDeferred("Kanban::DoneColumn"))
	assert.True(t, isColumnDeferred("Kanban::NotNowColumn"))
	assert.False(t, isColumnDeferred("Kanban::Triage"))
	assert.False(t, isColumnDeferred("Kanban::Column"))
	assert.False(t, isColumnDeferred(""))
}

func TestBuildCardColumns_DeferredSkipping(t *testing.T) {
	lists := []basecamp.CardColumn{
		{ID: 1, Title: "Triage", Color: "blue", Type: "Kanban::Triage", CardsCount: 5},
		{ID: 2, Title: "Active", Color: "green", Type: "Kanban::Column", CardsCount: 3},
		{ID: 3, Title: "Done", Type: "Kanban::DoneColumn", CardsCount: 150},
		{ID: 4, Title: "Not Now", Type: "Kanban::NotNowColumn", CardsCount: 42},
	}

	columns, jobs := buildCardColumns(lists)

	// All 4 columns are present in output, preserving order
	require.Len(t, columns, 4)
	assert.Equal(t, "Triage", columns[0].Title)
	assert.Equal(t, "Active", columns[1].Title)
	assert.Equal(t, "Done", columns[2].Title)
	assert.Equal(t, "Not Now", columns[3].Title)

	// Only non-deferred columns produce fetch jobs
	require.Len(t, jobs, 2)
	assert.Equal(t, int64(1), jobs[0].colID)
	assert.Equal(t, 0, jobs[0].idx)
	assert.Equal(t, int64(2), jobs[1].colID)
	assert.Equal(t, 1, jobs[1].idx)

	// Deferred columns have metadata but Deferred=true
	assert.False(t, columns[0].Deferred)
	assert.False(t, columns[1].Deferred)
	assert.True(t, columns[2].Deferred)
	assert.True(t, columns[3].Deferred)
	assert.Equal(t, 150, columns[2].CardsCount)
	assert.Equal(t, 42, columns[3].CardsCount)

	// Color and Type preserved
	assert.Equal(t, "blue", columns[0].Color)
	assert.Equal(t, "Kanban::Triage", columns[0].Type)
}

func TestBuildCardColumns_AllDeferred(t *testing.T) {
	lists := []basecamp.CardColumn{
		{ID: 1, Title: "Done", Type: "Kanban::DoneColumn", CardsCount: 100},
	}

	columns, jobs := buildCardColumns(lists)

	assert.Len(t, columns, 1)
	assert.Empty(t, jobs)
	assert.True(t, columns[0].Deferred)
}

func TestBuildCardColumns_Empty(t *testing.T) {
	columns, jobs := buildCardColumns(nil)

	assert.Empty(t, columns)
	assert.Empty(t, jobs)
}

func TestBuildCardColumns_PreservesOrder(t *testing.T) {
	// Interleaved deferred and active columns
	lists := []basecamp.CardColumn{
		{ID: 1, Title: "A", Type: "Kanban::Column"},
		{ID: 2, Title: "B", Type: "Kanban::DoneColumn"},
		{ID: 3, Title: "C", Type: "Kanban::Column"},
		{ID: 4, Title: "D", Type: "Kanban::NotNowColumn"},
		{ID: 5, Title: "E", Type: "Kanban::Triage"},
	}

	columns, jobs := buildCardColumns(lists)

	// Column order matches input
	require.Len(t, columns, 5)
	for i, title := range []string{"A", "B", "C", "D", "E"} {
		assert.Equal(t, title, columns[i].Title)
	}

	// Jobs reference correct indices (0, 2, 4 — the non-deferred ones)
	require.Len(t, jobs, 3)
	assert.Equal(t, 0, jobs[0].idx)
	assert.Equal(t, 2, jobs[1].idx)
	assert.Equal(t, 4, jobs[2].idx)
}

func TestMapCardInfo_BasicFields(t *testing.T) {
	card := basecamp.Card{
		ID:       42,
		Title:    "Fix the bug",
		DueOn:    "2024-03-15",
		Position: 3,
	}

	info := mapCardInfo(card)

	assert.Equal(t, int64(42), info.ID)
	assert.Equal(t, "Fix the bug", info.Title)
	assert.Equal(t, "2024-03-15", info.DueOn)
	assert.Equal(t, 3, info.Position)
	assert.False(t, info.Completed)
	assert.Equal(t, 0, info.StepsTotal)
	assert.Equal(t, 0, info.StepsDone)
	assert.Equal(t, 0, info.CommentsCount)
	assert.Empty(t, info.Assignees)
}

func TestMapCardInfo_Enrichment(t *testing.T) {
	card := basecamp.Card{
		ID:            42,
		Title:         "Enriched card",
		Completed:     true,
		CommentsCount: 7,
		Assignees: []basecamp.Person{
			{Name: "Alice"},
			{Name: "Bob"},
		},
		Steps: []basecamp.CardStep{
			{Completed: true},
			{Completed: false},
			{Completed: true},
		},
	}

	info := mapCardInfo(card)

	assert.True(t, info.Completed)
	assert.Equal(t, 7, info.CommentsCount)
	assert.Equal(t, []string{"Alice", "Bob"}, info.Assignees)
	assert.Equal(t, 3, info.StepsTotal)
	assert.Equal(t, 2, info.StepsDone)
}

func TestMapCardInfo_NoSteps(t *testing.T) {
	card := basecamp.Card{ID: 1, Title: "Simple"}

	info := mapCardInfo(card)

	assert.Equal(t, 0, info.StepsTotal)
	assert.Equal(t, 0, info.StepsDone)
}

func TestMapCardInfo_AllStepsComplete(t *testing.T) {
	card := basecamp.Card{
		ID:    1,
		Title: "All done",
		Steps: []basecamp.CardStep{
			{Completed: true},
			{Completed: true},
		},
	}

	info := mapCardInfo(card)

	assert.Equal(t, 2, info.StepsTotal)
	assert.Equal(t, 2, info.StepsDone)
}
