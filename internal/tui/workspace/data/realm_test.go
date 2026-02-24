package data

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRealmNewAndContext(t *testing.T) {
	r := NewRealm("test", context.Background())
	assert.Equal(t, "test", r.Name())
	assert.NotNil(t, r.Context())
	assert.NoError(t, r.Context().Err())
}

func TestRealmRegisterAndLookup(t *testing.T) {
	r := NewRealm("test", context.Background())
	p := NewPool[int]("mypool", PoolConfig{}, nil)

	r.Register("mypool", p)
	assert.Same(t, p, r.Pool("mypool"))
	assert.Nil(t, r.Pool("missing"))
}

func TestRealmTeardownCancelsContext(t *testing.T) {
	r := NewRealm("test", context.Background())
	ctx := r.Context()
	assert.NoError(t, ctx.Err())

	r.Teardown()
	assert.Error(t, ctx.Err())
}

func TestRealmTeardownClearsPools(t *testing.T) {
	r := NewRealm("test", context.Background())
	p := NewPool("data", PoolConfig{}, func(ctx context.Context) (int, error) {
		return 42, nil
	})
	p.Fetch(context.Background())()
	assert.True(t, p.Get().HasData)

	r.Register("data", p)
	r.Teardown()

	assert.False(t, p.Get().HasData)
	assert.Nil(t, r.Pool("data"))
}

func TestRealmInvalidate(t *testing.T) {
	r := NewRealm("test", context.Background())
	p1 := NewPool("a", PoolConfig{}, func(ctx context.Context) (int, error) { return 1, nil })
	p2 := NewPool("b", PoolConfig{}, func(ctx context.Context) (int, error) { return 2, nil })

	p1.Fetch(context.Background())()
	p2.Fetch(context.Background())()
	r.Register("a", p1)
	r.Register("b", p2)

	r.Invalidate()
	assert.Equal(t, StateStale, p1.Get().State)
	assert.Equal(t, StateStale, p2.Get().State)
}

func TestRealmChildContextCancelledByParent(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	r := NewRealm("child", parent)
	childCtx := r.Context()
	assert.NoError(t, childCtx.Err())

	cancel()
	assert.Error(t, childCtx.Err())
}

func TestRealmPool(t *testing.T) {
	r := NewRealm("test", context.Background())
	created := 0

	// First call creates.
	p := RealmPool(r, "items", func() *Pool[[]int] {
		created++
		return NewPool[[]int]("items", PoolConfig{}, nil)
	})
	require.NotNil(t, p)
	assert.Equal(t, 1, created)

	// Second call returns existing.
	p2 := RealmPool(r, "items", func() *Pool[[]int] {
		created++
		return NewPool[[]int]("items", PoolConfig{}, nil)
	})
	assert.Same(t, p, p2)
	assert.Equal(t, 1, created)
}

func TestRealmPoolKeyedPool(t *testing.T) {
	r := NewRealm("test", context.Background())

	kp := RealmPool(r, "todos", func() *KeyedPool[int64, []string] {
		return NewKeyedPool(func(key int64) *Pool[[]string] {
			return NewPool[[]string]("todos", PoolConfig{}, nil)
		})
	})
	require.NotNil(t, kp)

	// KeyedPool implements Pooler.
	assert.NotNil(t, r.Pool("todos"))
}

func TestRealmPoolTypeMismatchPanics(t *testing.T) {
	r := NewRealm("test", context.Background())

	// Register a Pool[int] under key "items".
	RealmPool(r, "items", func() *Pool[int] {
		return NewPool[int]("items", PoolConfig{}, nil)
	})

	// Requesting a Pool[string] for the same key should panic.
	assert.Panics(t, func() {
		RealmPool(r, "items", func() *Pool[string] {
			return NewPool[string]("items", PoolConfig{}, nil)
		})
	})
}

func TestRealmPoolMutatingPool(t *testing.T) {
	r := NewRealm("test", context.Background())

	mp := RealmPool(r, "cards", func() *MutatingPool[[]int] {
		return NewMutatingPool[[]int]("cards", PoolConfig{}, nil)
	})
	require.NotNil(t, mp)
	assert.NotNil(t, r.Pool("cards"))
}
