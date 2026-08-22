package presenter

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
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

// TestFormatPeopleStripsC1Controls covers UTF-8-encoded Unicode C1 controls
// (U+0080-U+009F) and DEL, which survive ansi.Strip but are executed by
// C1-honoring terminals.
func TestFormatPeopleStripsC1Controls(t *testing.T) {
	people := []any{
		map[string]any{"name": "\u009b31mAlice\u009b0m"}, // U+009B CSI
		map[string]any{"name": "\u009d0;evil\aBob"},      // U+009D OSC, BEL-terminated
		map[string]any{"name": "Ca\u007frol"},            // DEL
	}

	got := formatPeople(people)
	want := "31mAlice0m, 0;evilBob, Carol"
	if got != want {
		t.Errorf("formatPeople(C1 names) = %q, want %q", got, want)
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

// TestFormatDateStripsC1ControlsFromUnparseableInput covers UTF-8-encoded C1
// controls and DEL, which survive ansi.Strip but execute on C1-honoring
// terminals.
func TestFormatDateStripsC1ControlsFromUnparseableInput(t *testing.T) {
	locale := NewLocale("")
	for _, in := range []string{"not-a-date\u009b31mX", "\u009d0;pwn\abad", "not-a-date\u007fX"} {
		got := formatDate(in, locale)
		for _, r := range got {
			if r < 0x20 && r != '\n' && r != '\t' || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
				t.Errorf("formatDate(%q) = %q, want no control rune U+%04X", in, got, r)
			}
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

func TestFormatDateParsesTimestampWithEmbeddedANSI(t *testing.T) {
	locale := NewLocale("")
	want := formatDate("2026-07-01T12:00:00Z", locale)
	for _, in := range []string{
		"\x1b[31m2026-07-01T12:00:00Z\x1b[0m",
		"2026-07-01T\x1b[31m12:00:00Z",
	} {
		got := formatDate(in, locale)
		if got != want {
			t.Errorf("formatDate(%q) = %q, want formatted date %q", in, got, want)
		}
	}

	wantDateOnly := formatDate("2026-07-01", locale)
	in := "\x1b[31m2026-07-01\x1b[0m"
	if got := formatDate(in, locale); got != wantDateOnly {
		t.Errorf("formatDate(%q) = %q, want formatted date %q", in, got, wantDateOnly)
	}
}

func TestFormatRelativeTimeParsesTimestampWithEmbeddedANSI(t *testing.T) {
	locale := NewLocale("")
	clean := time.Now().Add(-2 * time.Hour).Format(time.RFC3339)
	want := formatRelativeTime(clean, locale)
	for _, in := range []string{
		"\x1b[31m" + clean + "\x1b[0m",
		clean[:10] + "\x1b[31m" + clean[10:],
	} {
		got := formatRelativeTime(in, locale)
		if got != want {
			t.Errorf("formatRelativeTime(%q) = %q, want relative time %q", in, got, want)
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

// assertSingleLine fails if s contains a newline, carriage return, or tab —
// i.e. if a single-line sink leaked whitespace that could break its row.
func assertSingleLine(t *testing.T, label, s string) {
	t.Helper()
	if strings.ContainsAny(s, "\n\r\t") {
		t.Errorf("%s = %q, want single line (no \\n/\\r/\\t)", label, s)
	}
}

// formatPeople feeds a single-line detail row (e.g. todo.assignees), so a name
// with a bare CR plus embedded newline/tab must collapse to one line with the
// CR acting as a word separator rather than gluing words together.
func TestFormatPeopleCollapsesWhitespace(t *testing.T) {
	people := []any{
		map[string]any{"name": "Ann\rBob"},
		map[string]any{"name": "Carol\nDave\tErin"},
	}

	got := formatPeople(people)
	want := "Ann Bob, Carol Dave Erin"
	if got != want {
		t.Errorf("formatPeople(embedded whitespace) = %q, want %q", got, want)
	}
	assertSingleLine(t, "formatPeople", got)
}

// formatPerson is a single key/value detail row value, so embedded
// CR/newlines/tabs collapse to spaces.
func TestFormatPersonCollapsesWhitespace(t *testing.T) {
	got := formatPerson(map[string]any{"name": "Ann\rBob\nCarol\tDave"})
	want := "Ann Bob Carol Dave"
	if got != want {
		t.Errorf("formatPerson(embedded whitespace) = %q, want %q", got, want)
	}
	assertSingleLine(t, "formatPerson", got)
}

// A dock title/name occupies one line of the dock listing, so embedded
// CR/newlines/tabs must collapse rather than break the row layout.
func TestFormatDockCollapsesWhitespaceInTitleAndName(t *testing.T) {
	dock := []any{
		map[string]any{
			"title":   "To\rdos\nList",
			"name":    "todo\tset",
			"enabled": true,
			"id":      float64(1),
		},
	}

	got := formatDock(dock)
	want := "1  To dos List (todo set)"
	if got != want {
		t.Errorf("formatDock(embedded whitespace) = %q, want %q", got, want)
	}
	assertSingleLine(t, "formatDock", got)
}

// A date value renders in a single-line detail field; an unparseable value with
// embedded CR/newlines/tabs falls back to the sanitized string, which must stay
// on one line.
func TestFormatDateCollapsesWhitespaceInUnparseableInput(t *testing.T) {
	locale := NewLocale("")
	got := formatDate("not-a-date\rmore\nstuff\there", locale)
	want := "not-a-date more stuff here"
	if got != want {
		t.Errorf("formatDate(embedded whitespace) = %q, want %q", got, want)
	}
	assertSingleLine(t, "formatDate", got)
}

// A relative-time value renders in a single-line detail field; an unparseable
// value with embedded CR/newlines/tabs falls back to the sanitized string,
// which must stay on one line.
func TestFormatRelativeTimeCollapsesWhitespaceInUnparseableInput(t *testing.T) {
	locale := NewLocale("")
	got := formatRelativeTime("not-a-date\rmore\nstuff\there", locale)
	want := "not-a-date more stuff here"
	if got != want {
		t.Errorf("formatRelativeTime(embedded whitespace) = %q, want %q", got, want)
	}
	assertSingleLine(t, "formatRelativeTime", got)
}
