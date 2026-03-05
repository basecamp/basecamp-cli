package commands

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// runCmdWithFlags creates a command, sets flags, and runs it with a background context.
func runCmdWithFlags(newCmd func() *cobra.Command, flags map[string]string) error {
	cmd := newCmd()
	cmd.SetContext(context.Background())
	for k, v := range flags {
		if err := cmd.Flags().Set(k, v); err != nil {
			return err
		}
	}
	return cmd.RunE(cmd, nil)
}

// TestEditContentMutualExclusion verifies --edit and --content cannot be combined.
func TestEditContentMutualExclusion(t *testing.T) {
	tests := []struct {
		name   string
		newCmd func() *cobra.Command
		flags  map[string]string
	}{
		{
			name:   "comment --edit --content",
			newCmd: NewCommentCmd,
			flags:  map[string]string{"content": "some text", "edit": "true", "on": "12345"},
		},
		{
			name:   "message --edit --content",
			newCmd: NewMessageCmd,
			flags:  map[string]string{"subject": "Test", "content": "some text", "edit": "true"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := runCmdWithFlags(tt.newCmd, tt.flags)
			if err == nil {
				t.Fatal("expected error for --edit + --content, got nil")
			}
			if !strings.Contains(err.Error(), "cannot combine --edit and --content") {
				t.Errorf("error = %q, want 'cannot combine' message", err)
			}
		})
	}
}

// TestEditRejectsPipedStdin verifies --edit fails when stdin is not a terminal.
func TestEditRejectsPipedStdin(t *testing.T) {
	// Swap os.Stdin to a pipe so the stdin-is-terminal check triggers.
	origStdin := os.Stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdin = r
	t.Cleanup(func() {
		os.Stdin = origStdin
		r.Close()
		w.Close()
	})

	tests := []struct {
		name   string
		newCmd func() *cobra.Command
		flags  map[string]string
	}{
		{
			name:   "comment --edit piped stdin",
			newCmd: NewCommentCmd,
			flags:  map[string]string{"edit": "true", "on": "12345"},
		},
		{
			name:   "message --edit piped stdin",
			newCmd: NewMessageCmd,
			flags:  map[string]string{"subject": "Test", "edit": "true"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := runCmdWithFlags(tt.newCmd, tt.flags)
			if err == nil {
				t.Fatal("expected error for --edit with piped stdin, got nil")
			}
			if !strings.Contains(err.Error(), "stdin is not a terminal") {
				t.Errorf("error = %q, want stdin terminal check message", err)
			}
		})
	}
}

// TestEditEmptyAborts verifies --edit aborts when editor produces empty content.
func TestEditEmptyAborts(t *testing.T) {
	// Create a script that truncates the file (produces empty content)
	script := filepath.Join(t.TempDir(), "empty-editor")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n: > \"$1\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EDITOR", script)

	// Swap os.Stdin to /dev/null — a char device that passes the
	// ModeCharDevice guard — so the command reaches editor.Open.
	devNull, err := os.Open("/dev/null")
	if err != nil {
		t.Skip("/dev/null not available")
	}
	origStdin := os.Stdin
	os.Stdin = devNull
	t.Cleanup(func() {
		os.Stdin = origStdin
		devNull.Close()
	})

	err = runCmdWithFlags(NewCommentCmd, map[string]string{"edit": "true", "on": "12345"})
	if err == nil {
		t.Fatal("expected error for empty editor content, got nil")
	}
	if !strings.Contains(err.Error(), "empty content") {
		t.Errorf("error = %q, want 'empty content' message", err)
	}
}

// TestEditWithoutContentAllowed verifies --edit alone does not trigger mutual exclusion.
func TestEditWithoutContentAllowed(t *testing.T) {
	// Use a no-op editor so we don't launch vi.
	script := filepath.Join(t.TempDir(), "noop-editor")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n# no-op\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EDITOR", script)

	cmd := NewCommentCmd()
	cmd.SetContext(context.Background())
	cmd.Flags().Set("edit", "true")
	cmd.Flags().Set("on", "12345")

	err := cmd.RunE(cmd, nil)
	if err == nil {
		t.Skip("RunE succeeded unexpectedly (needs app context)")
	}
	// The error should NOT be about --edit/--content conflict
	if strings.Contains(err.Error(), "cannot combine") {
		t.Error("--edit alone should not trigger mutual exclusion error")
	}
}
