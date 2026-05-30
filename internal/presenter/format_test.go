package presenter

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestFormatDock(t *testing.T) {
	dock := []any{
		map[string]any{"name": "todoset", "title": "To-dos", "enabled": true, "id": float64(1)},
		map[string]any{"name": "message_board", "title": "Message Board", "enabled": true, "id": float64(2)},
	}

	got := formatDock(dock)
	want := "1  To-dos (todoset)\n2  Message Board (message_board)"
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
	want := "1  To-dos (todoset)\n3  Docs & Files (vault) [disabled]"
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
	want := "1  To-dos (todoset)\n2  Schedule (schedule)"
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
	want := "1  To-dos (todoset)\n2  Message Board (message_board)\n3  Docs & Files (vault)"
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
	want := "1  To-dos (todoset)\n3  Docs & Files (vault)"
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
	want := "1  To-dos (todoset)\n2  Message Board (message_board)\n3  Docs & Files (vault)"
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
	want := "2  Message Board (message_board)\n1  To-dos (todoset)\n3  Schedule (schedule) [disabled]"
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

func TestFormatDockRightAlignsIDs(t *testing.T) {
	dock := []any{
		map[string]any{"name": "todoset", "title": "To-dos", "enabled": true, "id": float64(1)},
		map[string]any{"name": "vault", "title": "Docs & Files", "enabled": true, "id": float64(100)},
	}

	got := formatDock(dock)
	want := "  1  To-dos (todoset)\n100  Docs & Files (vault)"
	if got != want {
		t.Errorf("formatDock(right-aligned IDs) = %q, want %q", got, want)
	}
}

func TestFormatDockTitleFallsBackToName(t *testing.T) {
	dock := []any{
		map[string]any{"name": "todoset", "enabled": true, "id": float64(1)},
	}

	got := formatDock(dock)
	want := "1  todoset"
	if got != want {
		t.Errorf("formatDock(title fallback) = %q, want %q", got, want)
	}
}

func TestFormatDockStripsANSIFromTitleAndName(t *testing.T) {
	dock := []any{
		map[string]any{
			"title":   "\x1b]8;;http://evil.example\x07To-dos\x1b]8;;\x07",
			"name":    "\x1b[31mtodoset\x1b[0m",
			"enabled": true,
			"id":      float64(1),
		},
	}

	got := formatDock(dock)
	want := "1  To-dos (todoset)"
	if got != want {
		t.Errorf("formatDock(ANSI in title/name) = %q, want %q", got, want)
	}
}

func TestFormatDockTitleFallbackStripsANSIFromName(t *testing.T) {
	dock := []any{
		map[string]any{
			"name":    "\x1b[31mtodoset\x1b[0m",
			"enabled": true,
			"id":      float64(1),
		},
	}

	got := formatDock(dock)
	want := "1  todoset"
	if got != want {
		t.Errorf("formatDock(ANSI in name fallback) = %q, want %q", got, want)
	}
}

func TestFormatPeopleStripsANSI(t *testing.T) {
	people := []any{
		map[string]any{"name": "\x1b[31mAlice\x1b[0m"},
		map[string]any{"name": "Bob"},
	}

	got := formatPeople(people)
	want := "Alice, Bob"
	if got != want {
		t.Errorf("formatPeople(ANSI names) = %q, want %q", got, want)
	}
}

func TestFormatDateStripsANSIFromUnparseableInput(t *testing.T) {
	locale := NewLocale("")
	for _, in := range []string{"not-a-date\x1b[31mX", "\x1b]0;pwn\x07bad"} {
		got := formatDate(in, locale)
		if strings.ContainsRune(got, 0x1b) {
			t.Errorf("formatDate(%q) = %q, want no escape byte", in, got)
		}
	}
}

func TestFormatRelativeTimeStripsANSIFromUnparseableInput(t *testing.T) {
	locale := NewLocale("")
	for _, in := range []string{"not-a-date\x1b[31mX", "\x1b]0;pwn\x07bad"} {
		got := formatRelativeTime(in, locale)
		if strings.ContainsRune(got, 0x1b) {
			t.Errorf("formatRelativeTime(%q) = %q, want no escape byte", in, got)
		}
	}
}

func TestFormatPeopleDropsAllEscapeName(t *testing.T) {
	people := []any{
		map[string]any{"name": "Alice"},
		map[string]any{"name": "\x1b[0m\x1b]8;;\x07"},
	}

	got := formatPeople(people)
	want := "Alice"
	if got != want {
		t.Errorf("formatPeople(all-escape name dropped) = %q, want %q", got, want)
	}
}
