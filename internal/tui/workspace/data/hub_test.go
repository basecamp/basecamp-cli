package data

import (
	"testing"

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
