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

// mcpHandshakeCheck starts the agent's Basecamp MCP server with the
// environment and process group the dispatcher gives a worker's, through this
// binary's ordinary mcp command rather than the worker subcommand, completes the MCP
// handshake and lists its tools, then ends the group it started. It does not
// serve the basecamp_connect domain: that needs a live task's token, which
// only a dispatch mints, and doctor starts no task. The connector's ledger,
// which that domain reads, is checked on its own.
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
	// The context bounds the MCP calls, not the process: this function alone
	// ends the process and alone waits on it, so its group is always signaled
	// while the leader is unreaped and the group id is still this command's.
	cmd.Cancel = func() error { return nil }
	stdin, err := cmd.StdinPipe()
	if err != nil {
		c.Status, c.Message = setup.StatusFail, "Cannot start the agent's MCP server: "+setup.ErrorText(err)
		return c
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		c.Status, c.Message = setup.StatusFail, "Cannot start the agent's MCP server: "+setup.ErrorText(err)
		return c
	}
	if err := cmd.Start(); err != nil {
		c.Status, c.Message = setup.StatusFail, "Cannot start the agent's MCP server: "+setup.ErrorText(err)
		return c
	}
	leader := cmd.Process.Pid
	var session *mcp.ClientSession
	defer func() {
		if session != nil {
			_ = session.Close()
		}
		// The group, then the leader: nothing else waits on it, so it is not
		// reaped before this signal, and a descendant goes with it.
		if leader > 1 {
			_ = syscall.Kill(-leader, syscall.SIGKILL)
		}
		_ = stdin.Close()
		_ = stdout.Close()
		_ = cmd.Wait()
	}()

	client := mcp.NewClient(&mcp.Implementation{Name: "basecamp-connect-doctor", Version: version.Version}, nil)
	session, err = client.Connect(ctx, &mcp.IOTransport{Reader: stdout, Writer: stdin}, nil)
	if err != nil {
		session = nil
		c.Status, c.Message = setup.StatusFail, "The agent's MCP server did not complete the handshake: "+setup.ErrorText(err)
		c.Hint = "Run basecamp mcp -P " + shellQuote(profile) + " and read its stderr."
		return c
	}
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
	c.Status, c.Message = setup.StatusPass, fmt.Sprintf("The agent's MCP server (basecamp mcp -P %s) answered with %d tools; the basecamp_connect domain is served only to a dispatched worker", shellQuote(profile), tools)
	return c
}
