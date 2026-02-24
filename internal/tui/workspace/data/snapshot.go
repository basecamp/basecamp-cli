package data

import "time"

// SnapshotState represents the freshness state of a data snapshot.
// Go equivalent of iOS DataUpdate<T> / Android Data<T>.
type SnapshotState int

const (
	StateEmpty   SnapshotState = iota // no data yet
	StateFresh                        // data within TTL
	StateStale                        // data past TTL, usable for SWR
	StateLoading                      // fetch in progress (may have stale data)
	StateError                        // fetch failed (may have stale data)
)

func (s SnapshotState) String() string {
	switch s {
	case StateEmpty:
		return "empty"
	case StateFresh:
		return "fresh"
	case StateStale:
		return "stale"
	case StateLoading:
		return "loading"
	case StateError:
		return "error"
	default:
		return "unknown"
	}
}

// Snapshot holds typed data along with its fetch state.
// Every pool exposes Snapshot[T] — consumers never see any.
type Snapshot[T any] struct {
	Data      T
	State     SnapshotState
	Err       error
	FetchedAt time.Time
	HasData   bool // distinguishes zero-value T from "never fetched"
}

// Fresh returns true if the snapshot has data in the Fresh state.
func (s Snapshot[T]) Fresh() bool {
	return s.HasData && s.State == StateFresh
}

// Usable returns true if the snapshot has data, regardless of freshness.
func (s Snapshot[T]) Usable() bool {
	return s.HasData
}

// Loading returns true if a fetch is in progress.
func (s Snapshot[T]) Loading() bool {
	return s.State == StateLoading
}
