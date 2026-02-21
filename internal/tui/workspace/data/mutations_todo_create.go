package data

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
)

// TodoCreateMutation optimistically prepends a new todo to a todolist.
// Implements Mutation[[]TodoInfo] for use with MutatingPool.
//
// The createdID field uses atomic.Int64 because ApplyRemotely runs in the
// mutation's tea.Cmd goroutine while a concurrent background fetch can
// trigger reconcile → IsReflectedIn under the pool lock in a different
// goroutine. Pointer receiver required so the atomic field is shared.
type TodoCreateMutation struct {
	Content    string
	TodolistID int64
	ProjectID  int64
	Client     *basecamp.AccountClient
	createdID  atomic.Int64 // set by ApplyRemotely, read by IsReflectedIn
	tempID     int64        // negative temp ID for optimistic entry
}

// ApplyLocally prepends a placeholder todo with a temporary negative ID.
func (m *TodoCreateMutation) ApplyLocally(todos []TodoInfo) []TodoInfo {
	m.tempID = -time.Now().UnixNano()
	result := make([]TodoInfo, 0, len(todos)+1)
	result = append(result, TodoInfo{
		ID:      m.tempID,
		Content: m.Content,
	})
	result = append(result, todos...)
	return result
}

// ApplyRemotely calls the SDK to create the todo.
func (m *TodoCreateMutation) ApplyRemotely(ctx context.Context) error {
	todo, err := m.Client.Todos().Create(ctx, m.ProjectID, m.TodolistID, &basecamp.CreateTodoRequest{
		Content: m.Content,
	})
	if err != nil {
		return err
	}
	m.createdID.Store(todo.ID)
	return nil
}

// IsReflectedIn returns true when the created todo appears in the remote data.
// Returns false if ApplyRemotely hasn't completed yet (createdID == 0).
func (m *TodoCreateMutation) IsReflectedIn(todos []TodoInfo) bool {
	id := m.createdID.Load()
	if id == 0 {
		return false // ApplyRemotely not yet complete
	}
	for _, t := range todos {
		if t.ID == id {
			return true
		}
	}
	return false
}
