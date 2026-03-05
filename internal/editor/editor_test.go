package editor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenReturnsContent(t *testing.T) {
	// Create a script that acts as a no-op editor (leaves file unchanged)
	script := filepath.Join(t.TempDir(), "noop-editor")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n# no-op\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EDITOR", script)

	got, err := Open("hello world")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if got != "hello world" {
		t.Errorf("Open() = %q, want %q", got, "hello world")
	}
}

func TestOpenEmptyResultErrors(t *testing.T) {
	// Create a script that truncates the file
	script := filepath.Join(t.TempDir(), "empty-editor")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n: > \"$1\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EDITOR", script)

	_, err := Open("initial")
	if err == nil {
		t.Error("Open() should error on empty result")
	}
}

func TestOpenEditorNotFound(t *testing.T) {
	t.Setenv("EDITOR", "/nonexistent/editor")
	_, err := Open("")
	if err == nil {
		t.Error("Open() should error when editor not found")
	}
}

func TestOpenEditorWhitespaceFallsBack(t *testing.T) {
	// Whitespace-only EDITOR should be treated as unset (fall back to vi),
	// not panic or return a whitespace-specific error.
	t.Setenv("EDITOR", "   ")
	t.Setenv("PATH", "/nonexistent")
	_, err := Open("")
	if err == nil {
		t.Fatal("expected error when vi is not found")
	}
	if strings.Contains(err.Error(), "whitespace") {
		t.Error("whitespace-only EDITOR should fall back to vi, not error about whitespace")
	}
}

func TestOpenEditorWithArgs(t *testing.T) {
	// Create a script that appends " edited" to the file
	script := filepath.Join(t.TempDir(), "append-editor")
	// The script receives --flag as first arg (ignored) and file as second
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho \" edited\" >> \"$2\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EDITOR", script+" --flag")

	got, err := Open("hello")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	// Initial "hello" has no trailing newline, echo appends " edited\n"
	// TrimSpace produces "hello edited"
	if !strings.Contains(got, "edited") {
		t.Errorf("Open() = %q, want content containing 'edited'", got)
	}
}
