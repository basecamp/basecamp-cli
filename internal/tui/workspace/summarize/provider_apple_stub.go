//go:build !darwin

package summarize

// AppleProvider is a stub for non-darwin platforms.
type AppleProvider struct{}

// NewAppleProvider returns nil on non-darwin platforms.
func NewAppleProvider() *AppleProvider { return nil }

// IsAppleMLAvailable returns false on non-darwin platforms.
func IsAppleMLAvailable() bool { return false }
