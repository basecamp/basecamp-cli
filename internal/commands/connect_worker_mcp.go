package commands

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/connector"
	"github.com/basecamp/basecamp-cli/internal/connector/driver"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// connectWorkerMCPDial bounds the bridge's wait for the connector's socket.
const connectWorkerMCPDial = 30 * time.Second

// newConnectWorkerMCPCmd is the MCP server command the connector hands an
// agent for a worker: the bridge that takes the task token from the
// connector's one-use socket (see connector's "The task token's carriage")
// and becomes `basecamp mcp` with the token on a pipe.
//
// Hidden: nobody runs it by hand. It exists because an agent starts its MCP
// servers itself and can hand them only standard I/O.
func newConnectWorkerMCPCmd() *cobra.Command {
	var socket, state string
	cmd := &cobra.Command{
		Use:    "worker-mcp",
		Short:  "The MCP server a connector-started worker runs (internal)",
		Hidden: true,
		Args:   cobra.NoArgs,
		Annotations: map[string]string{
			"stdout_wire": "mcp",
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			app := appctx.FromContext(cmd.Context())
			if socket == "" || state == "" {
				return output.ErrUsage("worker-mcp needs --socket and --connect-state; the connector passes both")
			}
			profile := app.Config.ActiveProfile
			if profile == "" {
				return output.ErrUsage("worker-mcp needs the agent's profile (-P)")
			}
			token, err := receiveTaskToken(socket, connectWorkerMCPDial)
			if err != nil {
				return err
			}
			exe, err := os.Executable()
			if err != nil {
				return err
			}
			return execWorkerMCP(exe, profile, state, token)
		},
	}
	cmd.Flags().StringVar(&socket, "socket", "", "The connector's one-use token socket for this attempt")
	cmd.Flags().StringVar(&state, "connect-state", "", "The connector's state directory")
	return cmd
}

// receiveTaskToken takes the token from the connector's socket. A socket that
// hands over nothing — this process is not the worker's, or the socket was
// already used — is a refusal, not an empty token.
func receiveTaskToken(path string, timeout time.Duration) (string, error) {
	dialer := net.Dialer{Timeout: timeout}
	conn, err := dialer.DialContext(context.Background(), "unix", path)
	if err != nil {
		return "", fmt.Errorf("worker-mcp: the connector's token socket: %w", err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	line, err := bufio.NewReaderSize(conn, 256).ReadString('\n')
	token := strings.TrimSpace(line)
	if token == "" {
		if err == nil {
			err = errors.New("empty")
		}
		return "", fmt.Errorf("worker-mcp: the connector handed over no token: %w", err)
	}
	return token, nil
}

// workerMCPArgs is what the bridge becomes. The token is on descriptor fd,
// never in argv.
func workerMCPArgs(exe, profile, state string, fd int) []string {
	return []string{exe, "mcp", "--profile", profile, "--connect-state", state, "--connect-token-fd", strconv.Itoa(fd)}
}

// workerMCPEnv is the environment the bridge hands `basecamp mcp`: what the
// connector declared for its server, and nothing an agent added to it.
func workerMCPEnv() []string {
	return driver.BuildEnv(append(append([]string{}, driver.BaseEnv...), connector.MCPServerEnv...), os.LookupEnv, nil)
}
