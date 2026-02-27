package data

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
)

// CardCreateMutation optimistically prepends a new card to a kanban column.
// Implements Mutation[[]CardColumnInfo] for use with MutatingPool.
//
// The createdID field uses atomic.Int64 because ApplyRemotely runs in the
// mutation's tea.Cmd goroutine while a concurrent background fetch can
// trigger reconcile → IsReflectedIn under the pool lock in a different
// goroutine. Pointer receiver required so the atomic field is shared.
type CardCreateMutation struct {
	Title     string
	ColumnID  int64 // identity-based column target (not index)
	ProjectID int64
	Client    *basecamp.AccountClient
	createdID atomic.Int64 // set by ApplyRemotely, read by IsReflectedIn
}

// ApplyLocally prepends a placeholder card to the target column (by ID).
// If the column is not found (e.g. reorder during flight), no-op.
func (m *CardCreateMutation) ApplyLocally(columns []CardColumnInfo) []CardColumnInfo {
	result := make([]CardColumnInfo, len(columns))
	for i, col := range columns {
		result[i] = CardColumnInfo{
			ID:         col.ID,
			Title:      col.Title,
			Color:      col.Color,
			Type:       col.Type,
			CardsCount: col.CardsCount,
			Deferred:   col.Deferred,
			Cards:      make([]CardInfo, len(col.Cards)),
		}
		copy(result[i].Cards, col.Cards)
	}

	for i := range result {
		if result[i].ID == m.ColumnID {
			tempCard := CardInfo{
				ID:    -time.Now().UnixNano(),
				Title: m.Title,
			}
			result[i].Cards = append([]CardInfo{tempCard}, result[i].Cards...)
			result[i].CardsCount++
			break
		}
	}

	return result
}

// ApplyRemotely calls the SDK to create the card.
func (m *CardCreateMutation) ApplyRemotely(ctx context.Context) error {
	card, err := m.Client.Cards().Create(ctx, m.ColumnID, &basecamp.CreateCardRequest{
		Title: m.Title,
	})
	if err != nil {
		return err
	}
	m.createdID.Store(card.ID)
	return nil
}

// IsReflectedIn returns true when the created card appears in the target column.
// Returns false if ApplyRemotely hasn't completed yet (createdID == 0).
func (m *CardCreateMutation) IsReflectedIn(columns []CardColumnInfo) bool {
	id := m.createdID.Load()
	if id == 0 {
		return false // ApplyRemotely not yet complete
	}
	for _, col := range columns {
		if col.ID == m.ColumnID {
			for _, c := range col.Cards {
				if c.ID == id {
					return true
				}
			}
			return false
		}
	}
	return false
}
