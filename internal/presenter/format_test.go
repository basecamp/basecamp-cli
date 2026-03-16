package presenter

import (
	"encoding/json"
	"testing"
)

func TestFormatDock(t *testing.T) {
	dock := []any{
		map[string]any{"name": "todoset", "title": "To-dos", "enabled": true, "id": float64(1)},
		map[string]any{"name": "message_board", "title": "Message Board", "enabled": true, "id": float64(2)},
	}

	got := formatDock(dock)
	want := "To-dos (todoset, ID: 1)\nMessage Board (message_board, ID: 2)"
	if got != want {
		t.Errorf("formatDock(enabled items) = %q, want %q", got, want)
	}
}

func TestFormatDockAnnotatesDisabled(t *testing.T) {
	dock := []any{
		map[string]any{"name": "todoset", "title": "To-dos", "enabled": true, "id": float64(1), "position": float64(1)},
		map[string]any{"name": "vault", "title": "Docs & Files", "enabled": false, "id": float64(3)},
	}

	got := formatDock(dock)
	want := "To-dos (todoset, ID: 1)\nDocs & Files (vault, ID: 3) [disabled]"
	if got != want {
		t.Errorf("formatDock(with disabled) = %q, want %q", got, want)
	}
}

func TestFormatDockDefaultsEnabledWhenAbsent(t *testing.T) {
	dock := []any{
		map[string]any{"name": "todoset", "title": "To-dos", "id": float64(1)},
		map[string]any{"name": "schedule", "title": "Schedule", "id": float64(2)},
	}

	got := formatDock(dock)
	want := "To-dos (todoset, ID: 1)\nSchedule (schedule, ID: 2)"
	if got != want {
		t.Errorf("formatDock(no enabled field) = %q, want %q", got, want)
	}
}

func TestFormatDockSortsByPosition(t *testing.T) {
	dock := []any{
		map[string]any{"name": "vault", "title": "Docs & Files", "enabled": true, "id": float64(3), "position": float64(3)},
		map[string]any{"name": "todoset", "title": "To-dos", "enabled": true, "id": float64(1), "position": float64(1)},
		map[string]any{"name": "message_board", "title": "Message Board", "enabled": true, "id": float64(2), "position": float64(2)},
	}

	got := formatDock(dock)
	want := "To-dos (todoset, ID: 1)\nMessage Board (message_board, ID: 2)\nDocs & Files (vault, ID: 3)"
	if got != want {
		t.Errorf("formatDock(position sort) = %q, want %q", got, want)
	}
}

func TestFormatDockItemsWithoutPositionSortLast(t *testing.T) {
	dock := []any{
		map[string]any{"name": "vault", "title": "Docs & Files", "enabled": true, "id": float64(3)},
		map[string]any{"name": "todoset", "title": "To-dos", "enabled": true, "id": float64(1), "position": float64(1)},
	}

	got := formatDock(dock)
	want := "To-dos (todoset, ID: 1)\nDocs & Files (vault, ID: 3)"
	if got != want {
		t.Errorf("formatDock(missing position) = %q, want %q", got, want)
	}
}

func TestFormatDockAcceptsMapSlice(t *testing.T) {
	// NormalizeData produces []map[string]any with json.Number positions.
	dock := []map[string]any{
		{"name": "vault", "title": "Docs & Files", "enabled": true, "id": json.Number("3"), "position": json.Number("3")},
		{"name": "todoset", "title": "To-dos", "enabled": true, "id": json.Number("1"), "position": json.Number("1")},
		{"name": "message_board", "title": "Message Board", "enabled": true, "id": json.Number("2"), "position": json.Number("2")},
	}

	got := formatDock(dock)
	want := "To-dos (todoset, ID: 1)\nMessage Board (message_board, ID: 2)\nDocs & Files (vault, ID: 3)"
	if got != want {
		t.Errorf("formatDock([]map with json.Number) = %q, want %q", got, want)
	}
}

func TestFormatDockDisabledSortAfterEnabled(t *testing.T) {
	dock := []map[string]any{
		{"name": "schedule", "title": "Schedule", "enabled": false, "id": json.Number("3")},
		{"name": "todoset", "title": "To-dos", "enabled": true, "id": json.Number("1"), "position": json.Number("2")},
		{"name": "message_board", "title": "Message Board", "enabled": true, "id": json.Number("2"), "position": json.Number("1")},
	}

	got := formatDock(dock)
	want := "Message Board (message_board, ID: 2)\nTo-dos (todoset, ID: 1)\nSchedule (schedule, ID: 3) [disabled]"
	if got != want {
		t.Errorf("formatDock(disabled sort last) = %q, want %q", got, want)
	}
}

func TestFormatDockEmpty(t *testing.T) {
	if got := formatDock([]any{}); got != "" {
		t.Errorf("formatDock(empty) = %q, want empty", got)
	}
	if got := formatDock(nil); got != "" {
		t.Errorf("formatDock(nil) = %q, want empty", got)
	}
}
