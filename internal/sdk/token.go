// Package sdk provides core SDK interfaces for the Basecamp API client.
package sdk

import (
	"context"
	"os"
)

// TokenSource provides access tokens for API authentication.
// Implementations handle token acquisition, caching, and refresh.
type TokenSource interface {
	// Token returns a valid access token.
	// Implementations should handle token refresh automatically.
	Token(ctx context.Context) (string, error)

	// Refresh forces a token refresh, invalidating any cached token.
	Refresh(ctx context.Context) error
}

// EnvTokenSource returns a token from an environment variable.
// Useful for CI/CD or testing where OAuth flow isn't available.
type EnvTokenSource struct {
	EnvVar string // Environment variable name (default: BASECAMP_TOKEN)
}

// Token returns the token from the environment variable.
func (s *EnvTokenSource) Token(ctx context.Context) (string, error) {
	envVar := s.EnvVar
	if envVar == "" {
		envVar = "BASECAMP_TOKEN"
	}
	token := os.Getenv(envVar)
	if token == "" {
		return "", &TokenError{Message: "environment variable " + envVar + " not set"}
	}
	return token, nil
}

// Refresh is a no-op for environment tokens.
func (s *EnvTokenSource) Refresh(ctx context.Context) error {
	return nil
}

// StaticTokenSource provides a fixed token. Useful for testing.
type StaticTokenSource struct {
	AccessToken string
}

// Token returns the static token.
func (s *StaticTokenSource) Token(ctx context.Context) (string, error) {
	if s.AccessToken == "" {
		return "", &TokenError{Message: "no token configured"}
	}
	return s.AccessToken, nil
}

// Refresh is a no-op for static tokens.
func (s *StaticTokenSource) Refresh(ctx context.Context) error {
	return nil
}

// TokenError indicates a token sourcing error.
type TokenError struct {
	Message string
	Cause   error
}

func (e *TokenError) Error() string {
	if e.Cause != nil {
		return e.Message + ": " + e.Cause.Error()
	}
	return e.Message
}

func (e *TokenError) Unwrap() error {
	return e.Cause
}
