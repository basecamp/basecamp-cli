//go:build !unix

package commands

import (
	"context"

	"github.com/basecamp/basecamp-cli/internal/connector/setup"
)

func mcpHandshakeCheck(context.Context, string) setup.Check {
	return setup.Check{Name: "MCP handshake", Status: setup.StatusSkip, Message: "The connector runs on macOS and Linux only"}
}
