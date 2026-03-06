//go:build darwin

package summarize

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// AppleProvider uses macOS Foundation Models (macOS 26+).
type AppleProvider struct{}

// NewAppleProvider creates an Apple Foundation Models provider.
func NewAppleProvider() *AppleProvider {
	return &AppleProvider{}
}

func (p *AppleProvider) Complete(ctx context.Context, prompt string, maxTokens int) (string, error) {
	// Read prompt from stdin to avoid string escaping issues in Swift source.
	script := `
import Foundation
import FoundationModels
let input = String(data: FileHandle.standardInput.readDataToEndOfFile(), encoding: .utf8) ?? ""
let session = LanguageModelSession()
let response = try await session.respond(to: input)
print(response.content)
`
	cmd := exec.CommandContext(ctx, "swift", "-e", script)
	cmd.Stdin = strings.NewReader(prompt)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("apple: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// IsAppleMLAvailable checks if macOS Foundation Models are available.
func IsAppleMLAvailable() bool {
	// Check for macOS 26+ by trying to compile a minimal Foundation Models import.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "swift", "-e", "import FoundationModels; print(\"ok\")")
	out, err := cmd.Output()
	return err == nil && strings.TrimSpace(string(out)) == "ok"
}
