package data

import (
	"context"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
)

// CardMoveMutation moves a card from one column to another.
// Implements Mutation[[]CardColumnInfo] for use with MutatingPool.
type CardMoveMutation struct {
	CardID         int64
	SourceColIdx   int   // index of the source column
	TargetColIdx   int   // index of the target column
	TargetColumnID int64 // API ID of target column
	Client         *basecamp.AccountClient
	ProjectID      int64
}

// ApplyLocally moves the card between columns in the local data.
func (m CardMoveMutation) ApplyLocally(columns []CardColumnInfo) []CardColumnInfo {
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

	if m.SourceColIdx < 0 || m.SourceColIdx >= len(result) ||
		m.TargetColIdx < 0 || m.TargetColIdx >= len(result) {
		return result
	}

	// Find and remove from source
	src := &result[m.SourceColIdx]
	var moved *CardInfo
	for i, c := range src.Cards {
		if c.ID == m.CardID {
			card := src.Cards[i]
			moved = &card
			src.Cards = append(src.Cards[:i], src.Cards[i+1:]...)
			break
		}
	}
	if moved == nil {
		return result
	}

	// Adjust counts
	result[m.SourceColIdx].CardsCount--
	result[m.TargetColIdx].CardsCount++

	// Append to target (even deferred columns get the card in local state)
	result[m.TargetColIdx].Cards = append(result[m.TargetColIdx].Cards, *moved)
	return result
}

// ApplyRemotely calls the SDK to move the card.
func (m CardMoveMutation) ApplyRemotely(ctx context.Context) error {
	return m.Client.Cards().Move(ctx, m.CardID, m.TargetColumnID)
}

// IsReflectedIn returns true when the card appears in the target column
// in the remote data. For deferred target columns (Done/NotNow) whose Cards
// slice is always empty, we check that the card is absent from the source
// column instead.
func (m CardMoveMutation) IsReflectedIn(columns []CardColumnInfo) bool {
	if m.TargetColIdx < 0 || m.TargetColIdx >= len(columns) {
		return false
	}
	target := columns[m.TargetColIdx]
	if target.Deferred {
		// Can't verify presence in deferred column; verify absence from source.
		if m.SourceColIdx < 0 || m.SourceColIdx >= len(columns) {
			return false
		}
		for _, c := range columns[m.SourceColIdx].Cards {
			if c.ID == m.CardID {
				return false // still in source — not yet reflected
			}
		}
		return true
	}
	for _, c := range target.Cards {
		if c.ID == m.CardID {
			return true
		}
	}
	return false
}
