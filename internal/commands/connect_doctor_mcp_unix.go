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

// mcpHandshakeCheck starts the agent's Basecamp MCP server with what the
// dispatcher gives a worker's — this binary's mcp command on the profile, the
// same allowlisted environment, its own process group — completes the MCP
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
	// A timeout ends the whole group, not only the leader: exec calls Cancel
	// before it waits, while the group id is still reserved.
	cmd.Cancel = func() error { return killUnreapedGroup(cmd) }

	client := mcp.NewClient(&mcp.Implementation{Name: "basecamp-connect-doctor", Version: version.Version}, nil)
	session, err := client.Connect(ctx, &groupTransport{cmd: cmd}, nil)
	if err != nil {
		// The client closed the connection, and groupTransport ended the group
		// before the leader was reaped.
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
	c.Status, c.Message = setup.StatusPass, fmt.Sprintf("The agent's MCP server (basecamp mcp -P %s) answered with %d tools; the basecamp_connect domain is served only to a dispatched worker", profile, tools)
	return c
}

// groupTransport is mcp.CommandTransport whose connection ends the command's
// whole process group when it closes, before the SDK waits on (and so reaps)
// the leader: a descendant the server started goes with it, and the group id
// is still this command's when it is signaled.
type groupTransport struct {
	cmd *exec.Cmd
}

func (t *groupTransport) Connect(ctx context.Context) (mcp.Connection, error) {
	conn, err := (&mcp.CommandTransport{Command: t.cmd}).Connect(ctx)
	if err != nil {
		_ = killUnreapedGroup(t.cmd)
		return nil, err
	}
	return &groupConn{Connection: conn, cmd: t.cmd}, nil
}

type groupConn struct {
	mcp.Connection
	cmd *exec.Cmd
}

func (c *groupConn) Close() error {
	_ = killUnreapedGroup(c.cmd)
	return c.Connection.Close()
}

// killUnreapedGroup signals the command's process group, and only while the
// leader has not been reaped: after that its id could name another group.
func killUnreapedGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil || cmd.Process.Pid <= 1 || cmd.ProcessState != nil {
		return nil
	}
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
