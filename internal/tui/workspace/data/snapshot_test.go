package data

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestSnapshotStateString(t *testing.T) {
	tests := []struct {
		state SnapshotState
		want  string
	}{
		{StateEmpty, "empty"},
		{StateFresh, "fresh"},
		{StateStale, "stale"},
		{StateLoading, "loading"},
		{StateError, "error"},
		{SnapshotState(99), "unknown"},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, tt.state.String())
	}
}

func TestSnapshotZeroValue(t *testing.T) {
	var snap Snapshot[[]int]
	assert.Equal(t, StateEmpty, snap.State)
	assert.False(t, snap.HasData)
	assert.False(t, snap.Fresh())
	assert.False(t, snap.Usable())
	assert.False(t, snap.Loading())
	assert.Nil(t, snap.Err)
}

func TestSnapshotFresh(t *testing.T) {
	snap := Snapshot[string]{
		Data:      "hello",
		State:     StateFresh,
		HasData:   true,
		FetchedAt: time.Now(),
	}
	assert.True(t, snap.Fresh())
	assert.True(t, snap.Usable())
	assert.False(t, snap.Loading())
}

func TestSnapshotStaleIsUsable(t *testing.T) {
	snap := Snapshot[int]{
		Data:    42,
		State:   StateStale,
		HasData: true,
	}
	assert.False(t, snap.Fresh())
	assert.True(t, snap.Usable())
}

func TestSnapshotLoadingWithData(t *testing.T) {
	snap := Snapshot[int]{
		Data:    42,
		State:   StateLoading,
		HasData: true,
	}
	assert.False(t, snap.Fresh())
	assert.True(t, snap.Usable())
	assert.True(t, snap.Loading())
}

func TestSnapshotErrorPreservesData(t *testing.T) {
	snap := Snapshot[string]{
		Data:    "old",
		State:   StateError,
		HasData: true,
		Err:     assert.AnError,
	}
	assert.False(t, snap.Fresh())
	assert.True(t, snap.Usable())
	assert.Equal(t, "old", snap.Data)
	assert.Error(t, snap.Err)
}
