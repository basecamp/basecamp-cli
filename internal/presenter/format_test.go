package presenter

import (
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

func TestFormatDockSkipsDisabled(t *testing.T) {
	dock := []any{
		map[string]any{"name": "todoset", "title": "To-dos", "enabled": true, "id": float64(1)},
		map[string]any{"name": "vault", "title": "Docs & Files", "enabled": false, "id": float64(3)},
	}

	got := formatDock(dock)
	want := "To-dos (todoset, ID: 1)"
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

func TestFormatDockEmpty(t *testing.T) {
	if got := formatDock([]any{}); got != "" {
		t.Errorf("formatDock(empty) = %q, want empty", got)
	}
	if got := formatDock(nil); got != "" {
		t.Errorf("formatDock(nil) = %q, want empty", got)
	}
}
