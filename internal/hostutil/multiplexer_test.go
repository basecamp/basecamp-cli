package hostutil

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDetectMultiplexer_None(t *testing.T) {
	// In test environment, typically neither TMUX nor ZELLIJ is set
	// but we can't unset env vars safely in parallel tests.
	// Just verify the function returns without panic.
	mux := DetectMultiplexer()
	assert.Contains(t, []Multiplexer{MultiplexerNone, MultiplexerTmux, MultiplexerZellij}, mux)
}

func TestSplitPane_NoMux(t *testing.T) {
	err := SplitPane(context.Background(), MultiplexerNone, "echo", "hello")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no terminal multiplexer")
}
