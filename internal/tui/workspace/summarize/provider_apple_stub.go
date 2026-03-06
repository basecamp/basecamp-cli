//go:build !darwin

package summarize

import (
	"context"
	"fmt"
)

// AppleProvider is a stub for non-darwin platforms.
type AppleProvider struct{}

// NewAppleProvider returns nil on non-darwin platforms.
func NewAppleProvider() *AppleProvider { return nil }

// Complete is a stub that always returns an error on non-darwin platforms.
func (p *AppleProvider) Complete(_ context.Context, _ string, _ int) (string, error) {
	return "", fmt.Errorf("apple: not available on this platform")
}

// IsAppleMLAvailable returns false on non-darwin platforms.
func IsAppleMLAvailable() bool { return false }
