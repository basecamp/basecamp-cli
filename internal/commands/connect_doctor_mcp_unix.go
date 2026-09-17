//go:build unix

package commands

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/basecamp/basecamp-cli/internal/connector"
	"github.com/basecamp/basecamp-cli/internal/connector/driver"
	"github.com/basecamp/basecamp-cli/internal/connector/setup"
	"github.com/basecamp/basecamp-cli/internal/version"
)

// mcpServerCommand is the agent's MCP server as a worker's is started: this
// binary's mcp command on the profile. A test seam.
var mcpServerCommand = func(profile string) (string, []string, error) {
	exe, err := os.Executable()
	return exe, []string{"mcp", "--profile", profile}, err
}

// mcpHandshakeCheck starts the agent's MCP server the way the dispatcher
// starts a worker's — this binary's mcp command, the profile, an allowlisted
// environment, its own process group — completes the MCP handshake and lists
// its tools, then ends the group it started.
func mcpHandshakeCheck(ctx context.Context, profile string) setup.Check {
	c := setup.Check{Name: "MCP handshake"}
	exe, args, err := mcpServerCommand(profile)
	if err != nil {
		c.Status, c.Message = setup.StatusFail, "Cannot locate this binary: "+err.Error()
		return c
	}
	ctx, cancel := context.WithTimeout(ctx, mcpHandshakeTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, exe, args...) //nolint:gosec // this binary, with a validated profile name
	cmd.Env = driver.BuildEnv(append(append([]string{}, driver.BaseEnv...), connector.MCPServerEnv...), os.LookupEnv, nil)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	defer func() {
		// The group this check started, and nothing else.
		if cmd.Process != nil && cmd.Process.Pid > 1 {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			_ = cmd.Wait()
		}
	}()

	client := mcp.NewClient(&mcp.Implementation{Name: "basecamp-connect-doctor", Version: version.Version}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		c.Status, c.Message = setup.StatusFail, "The agent's MCP server did not complete the handshake: "+setup.ErrorText(err)
		c.Hint = "Run basecamp mcp -P " + shellQuote(profile) + " and read its stderr."
		return c
	}
	defer func() { _ = session.Close() }()
	tools := 0
	for _, err := range session.Tools(ctx, nil) {
		if err != nil {
			c.Status, c.Message = setup.StatusFail, "The agent's MCP server did not list its tools: "+setup.ErrorText(err)
			return c
		}
		tools++
	}
	if tools == 0 {
		c.Status, c.Message = setup.StatusFail, "The agent's MCP server lists no tools"
		return c
	}
	c.Status, c.Message = setup.StatusPass, fmt.Sprintf("The agent's MCP server (basecamp mcp -P %s) answered with %d tools", profile, tools)
	return c
}
