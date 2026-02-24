package data

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestKeyedPoolGetCreatesPool(t *testing.T) {
	kp := NewKeyedPool(func(key int64) *Pool[[]string] {
		return NewPool(fmt.Sprintf("items:%d", key), PoolConfig{}, func(ctx context.Context) ([]string, error) {
			return []string{fmt.Sprintf("item-%d", key)}, nil
		})
	})

	p := kp.Get(42)
	assert.NotNil(t, p)
	assert.Equal(t, "items:42", p.Key())
	assert.True(t, kp.Has(42))
	assert.False(t, kp.Has(99))
}

func TestKeyedPoolGetReturnsSamePool(t *testing.T) {
	created := 0
	kp := NewKeyedPool(func(key int) *Pool[int] {
		created++
		return NewPool(fmt.Sprintf("p:%d", key), PoolConfig{}, func(ctx context.Context) (int, error) {
			return key, nil
		})
	})

	p1 := kp.Get(1)
	p2 := kp.Get(1)
	assert.Same(t, p1, p2)
	assert.Equal(t, 1, created)
}

func TestKeyedPoolInvalidate(t *testing.T) {
	kp := NewKeyedPool(func(key int) *Pool[int] {
		return NewPool(fmt.Sprintf("p:%d", key), PoolConfig{}, func(ctx context.Context) (int, error) {
			return key, nil
		})
	})

	p1 := kp.Get(1)
	p2 := kp.Get(2)
	p1.Fetch(context.Background())()
	p2.Fetch(context.Background())()

	kp.Invalidate()
	assert.Equal(t, StateStale, p1.Get().State)
	assert.Equal(t, StateStale, p2.Get().State)
}

func TestKeyedPoolClear(t *testing.T) {
	kp := NewKeyedPool(func(key int) *Pool[int] {
		return NewPool(fmt.Sprintf("p:%d", key), PoolConfig{}, func(ctx context.Context) (int, error) {
			return key, nil
		})
	})

	p1 := kp.Get(1)
	p1.Fetch(context.Background())()
	assert.True(t, p1.Get().HasData)

	kp.Clear()
	assert.False(t, kp.Has(1))

	// After clear, Get creates a new pool.
	p1New := kp.Get(1)
	assert.NotSame(t, p1, p1New)
	assert.False(t, p1New.Get().HasData)
}
