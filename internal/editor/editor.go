// Package editor provides $EDITOR integration for composing content.
package editor

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Open launches $EDITOR with initialContent and returns the edited text.
// Falls back to vi if $EDITOR is not set. Supports editors with arguments
// (e.g. EDITOR="code --wait").
// Returns an error if the editor exits non-zero or the result is empty.
func Open(initialContent string) (string, error) {
	editorCmd := strings.TrimSpace(os.Getenv("EDITOR"))
	if editorCmd == "" {
		editorCmd = "vi"
	}

	tmp, err := os.CreateTemp("", "basecamp-*.md")
	if err != nil {
		return "", fmt.Errorf("creating temp file: %w", err)
	}
	defer os.Remove(tmp.Name())

	if initialContent != "" {
		if _, err := tmp.WriteString(initialContent); err != nil {
			tmp.Close()
			return "", fmt.Errorf("writing initial content: %w", err)
		}
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("closing temp file: %w", err)
	}

	// Split editor command to support arguments (e.g. "code --wait")
	parts := strings.Fields(editorCmd)
	args := append(parts[1:], tmp.Name())
	cmd := exec.Command(parts[0], args...) //nolint:gosec,noctx // G204: EDITOR is user-configured
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("editor exited with error: %w", err)
	}

	data, err := os.ReadFile(tmp.Name())
	if err != nil {
		return "", fmt.Errorf("reading edited file: %w", err)
	}

	result := strings.TrimSpace(string(data))
	if result == "" {
		return "", fmt.Errorf("empty content — aborting")
	}

	return result, nil
}
