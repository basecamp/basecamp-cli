package data

import (
	"context"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
)

// TodoCompleteMutation toggles a todo's completion state.
// Implements Mutation[[]TodoInfo] for use with MutatingPool.
type TodoCompleteMutation struct {
	TodoID    int64
	Completed bool // target state (true = complete, false = uncomplete)
	Client    *basecamp.AccountClient
	ProjectID int64
}

// ApplyLocally toggles the todo's Completed field in the local data.
func (m TodoCompleteMutation) ApplyLocally(todos []TodoInfo) []TodoInfo {
	result := make([]TodoInfo, len(todos))
	copy(result, todos)
	for i := range result {
		if result[i].ID == m.TodoID {
			result[i].Completed = m.Completed
			break
		}
	}
	return result
}

// ApplyRemotely calls the SDK to complete or uncomplete the todo.
func (m TodoCompleteMutation) ApplyRemotely(ctx context.Context) error {
	if m.Completed {
		return m.Client.Todos().Complete(ctx, m.TodoID)
	}
	return m.Client.Todos().Uncomplete(ctx, m.TodoID)
}

// IsReflectedIn returns true when the remote data shows the todo in the
// target completion state.
func (m TodoCompleteMutation) IsReflectedIn(todos []TodoInfo) bool {
	for _, t := range todos {
		if t.ID == m.TodoID {
			return t.Completed == m.Completed
		}
	}
	return false
}
