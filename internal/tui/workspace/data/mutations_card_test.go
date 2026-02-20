package data

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func sampleCardColumns() []CardColumnInfo {
	return []CardColumnInfo{
		{
			ID: 1, Title: "Triage", Color: "blue", Type: "Kanban::Triage",
			CardsCount: 2,
			Cards: []CardInfo{
				{ID: 10, Title: "Card A", Position: 1, CommentsCount: 3},
				{ID: 11, Title: "Card B", Position: 2, Completed: true},
			},
		},
		{
			ID: 2, Title: "In Progress", Color: "green", Type: "Kanban::Column",
			CardsCount: 1,
			Cards: []CardInfo{
				{ID: 20, Title: "Card C", Position: 1, StepsTotal: 5, StepsDone: 2},
			},
		},
		{
			ID: 3, Title: "Done", Type: "Kanban::DoneColumn",
			CardsCount: 47, Deferred: true,
		},
	}
}

func TestCardMoveMutation_ApplyLocally(t *testing.T) {
	m := CardMoveMutation{
		CardID:       10,
		SourceColIdx: 0,
		TargetColIdx: 1,
	}

	result := m.ApplyLocally(sampleCardColumns())

	// Card removed from source
	assert.Len(t, result[0].Cards, 1)
	assert.Equal(t, int64(11), result[0].Cards[0].ID)
	assert.Equal(t, 1, result[0].CardsCount)

	// Card added to target
	assert.Len(t, result[1].Cards, 2)
	assert.Equal(t, int64(20), result[1].Cards[0].ID)
	assert.Equal(t, int64(10), result[1].Cards[1].ID)
	assert.Equal(t, 2, result[1].CardsCount)
}

func TestCardMoveMutation_ApplyLocally_PreservesNewFields(t *testing.T) {
	m := CardMoveMutation{
		CardID:       10,
		SourceColIdx: 0,
		TargetColIdx: 1,
	}

	result := m.ApplyLocally(sampleCardColumns())

	// New fields preserved on columns
	assert.Equal(t, "Kanban::Triage", result[0].Type)
	assert.Equal(t, "Kanban::Column", result[1].Type)
	assert.Equal(t, "Kanban::DoneColumn", result[2].Type)
	assert.True(t, result[2].Deferred)
	assert.Equal(t, 47, result[2].CardsCount)

	// Card enriched fields preserved
	movedCard := result[1].Cards[1]
	assert.Equal(t, 3, movedCard.CommentsCount)
}

func TestCardMoveMutation_ApplyLocally_ToDeferred(t *testing.T) {
	m := CardMoveMutation{
		CardID:       10,
		SourceColIdx: 0,
		TargetColIdx: 2, // Done (deferred)
	}

	result := m.ApplyLocally(sampleCardColumns())

	// Card removed from source
	assert.Len(t, result[0].Cards, 1)
	assert.Equal(t, 1, result[0].CardsCount)

	// Deferred target count incremented
	assert.Equal(t, 48, result[2].CardsCount)
	// Card appended to deferred (for optimistic local state)
	assert.Len(t, result[2].Cards, 1)
	assert.Equal(t, int64(10), result[2].Cards[0].ID)
}

func TestCardMoveMutation_ApplyLocally_DeepCopy(t *testing.T) {
	original := sampleCardColumns()
	m := CardMoveMutation{
		CardID:       10,
		SourceColIdx: 0,
		TargetColIdx: 1,
	}

	result := m.ApplyLocally(original)

	// Original should be unchanged
	assert.Len(t, original[0].Cards, 2)
	assert.Len(t, original[1].Cards, 1)
	// Result is different
	require.Len(t, result[0].Cards, 1)
	require.Len(t, result[1].Cards, 2)
}

func TestCardMoveMutation_IsReflectedIn_Normal(t *testing.T) {
	m := CardMoveMutation{
		CardID:       10,
		SourceColIdx: 0,
		TargetColIdx: 1,
	}

	// Before move: card not in target
	assert.False(t, m.IsReflectedIn(sampleCardColumns()))

	// After move: card in target
	moved := m.ApplyLocally(sampleCardColumns())
	assert.True(t, m.IsReflectedIn(moved))
}

func TestCardMoveMutation_IsReflectedIn_DeferredTarget(t *testing.T) {
	m := CardMoveMutation{
		CardID:       10,
		SourceColIdx: 0,
		TargetColIdx: 2, // Done (deferred)
	}

	// Before: card still in source
	assert.False(t, m.IsReflectedIn(sampleCardColumns()))

	// Simulate server re-fetch where card is gone from source
	// but deferred target has no cards (server doesn't return them)
	serverState := []CardColumnInfo{
		{
			ID: 1, Title: "Triage", Type: "Kanban::Triage",
			Cards: []CardInfo{
				{ID: 11, Title: "Card B"}, // card 10 is gone
			},
		},
		{
			ID: 2, Title: "In Progress", Type: "Kanban::Column",
			Cards: []CardInfo{{ID: 20, Title: "Card C"}},
		},
		{
			ID: 3, Title: "Done", Type: "Kanban::DoneColumn",
			Deferred: true, CardsCount: 48,
			// No Cards slice — server doesn't fetch deferred cards
		},
	}
	assert.True(t, m.IsReflectedIn(serverState))
}

func TestCardMoveMutation_OutOfBounds(t *testing.T) {
	cols := sampleCardColumns()

	// Out-of-bounds source
	m := CardMoveMutation{CardID: 10, SourceColIdx: -1, TargetColIdx: 1}
	result := m.ApplyLocally(cols)
	assert.Len(t, result[1].Cards, 1) // unchanged

	// Out-of-bounds target
	m = CardMoveMutation{CardID: 10, SourceColIdx: 0, TargetColIdx: 10}
	result = m.ApplyLocally(cols)
	assert.Len(t, result[0].Cards, 2) // unchanged

	// IsReflectedIn with out-of-bounds
	assert.False(t, m.IsReflectedIn(cols))
}

func TestCardMoveMutation_CardNotFound(t *testing.T) {
	m := CardMoveMutation{
		CardID:       999, // doesn't exist
		SourceColIdx: 0,
		TargetColIdx: 1,
	}

	result := m.ApplyLocally(sampleCardColumns())
	// Nothing should change
	assert.Len(t, result[0].Cards, 2)
	assert.Len(t, result[1].Cards, 1)
}
