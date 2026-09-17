package commands

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/connector"
	"github.com/basecamp/basecamp-cli/internal/mcpserver"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// mcpTransport is a seam so tests can drive the server over in-memory
// transports instead of the process's stdin/stdout.
var mcpTransport = func() mcp.Transport { return &mcp.StdioTransport{} }

// connectTaskTokenEnv carries a connector-started worker's task token. The
// token binds the basecamp_connect domain to one task, so it is taken from the
// environment the connector sets for the server and never from a flag, which
// any process on the machine can read from the command line.
const connectTaskTokenEnv = "BASECAMP_CONNECT_TASK_TOKEN"

// NewMCPCmd creates the mcp command serving Basecamp over MCP on stdio.
func NewMCPCmd() *cobra.Command {
	var readOnly bool
	var domains []string
	var connectState string

	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Serve Basecamp to MCP clients over stdio",
		Long: "Run an MCP (Model Context Protocol) server on stdin/stdout, serving Basecamp\n" +
			"projects, todos, cards, messages, and more as tools backed by your signed-in\n" +
			"account.\n\n" +
			"Register it with an MCP client as a stdio server, e.g.:\n\n" +
			"  claude mcp add basecamp -- basecamp mcp",
		Example: `  basecamp mcp
  basecamp mcp --read-only
  basecamp mcp --domains projects,todos,cards`,
		Args: cobra.NoArgs,
		Annotations: map[string]string{
			"agent_notes": "Long-running server; stdout speaks the MCP wire protocol. Not for interactive use.",
			// cli.Execute keeps errors off stdout for wire commands: an
			// error envelope there would be a malformed JSON-RPC message,
			// hiding the real failure behind the client's parse error.
			"stdout_wire": "mcp",
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			// CheckAuthenticated, not IsAuthenticated: this refuses to
			// start the server, and a store it merely could not read —
			// another process mid-write, a keyring that would not open —
			// must not be reported as "you are not logged in".
			authenticated, err := app.Auth.CheckAuthenticated(cmd.Context())
			if err != nil {
				return err
			}
			if !authenticated {
				return output.ErrAuth("Not authenticated. Run: basecamp auth login")
			}
			// Stdio belongs to the MCP wire, so the account cannot be
			// resolved interactively: require it configured up front, like
			// any other account-scoped command running non-interactively.
			if err := app.RequireAccount(); err != nil {
				return err
			}

			cfg := mcpserver.Config{ReadOnly: readOnly, Domains: domains}
			if connectState != "" {
				dispatch, closeLedger, err := openConnectDispatch(connectState, app.Config.AccountID)
				if err != nil {
					return err
				}
				defer closeLedger()
				cfg.Connect = dispatch
			}

			srv, err := mcpserver.New(app.Account(), cfg)
			if err != nil {
				return err
			}

			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			// Log to stderr: stdout belongs to the MCP wire.
			logger := slog.New(slog.NewTextHandler(cmd.ErrOrStderr(), nil))
			session, err := srv.BuildMCPServer(logger).Connect(ctx, mcpTransport(), nil)
			if err != nil {
				return err
			}
			logger.Info("MCP server running on stdio", "tools", len(srv.Domains()), "read_only", readOnly)

			return session.Wait()
		},
	}

	cmd.Flags().BoolVar(&readOnly, "read-only", false, "Serve only read-only actions")
	cmd.Flags().StringSliceVar(&domains, "domains", nil, "Narrow to specific domains (comma-separated; default all)")
	cmd.Flags().StringVar(&connectState, "connect-state", "", "Serve the basecamp_connect domain from this connector state directory, for the task named by $"+connectTaskTokenEnv)

	return cmd
}

// openConnectDispatch opens the connector's ledger in stateDir and binds it to
// the task token in the environment.
//
// The directory is the connector's own, named "<account>-<agent person id>",
// and it must belong to the account this server serves: that is where the
// agent's id comes from, and a ledger for another account is refused rather
// than served. The ledger must already exist — a worker's server reads the
// connector's ledger, it never starts one.
func openConnectDispatch(stateDir, accountID string) (*connector.TaskDispatch, func(), error) {
	token := os.Getenv(connectTaskTokenEnv)
	if strings.TrimSpace(token) == "" {
		return nil, nil, output.ErrUsage("--connect-state needs the task token in $" + connectTaskTokenEnv + "; the connector sets it when it starts a worker")
	}
	// Nothing this process starts needs it.
	_ = os.Unsetenv(connectTaskTokenEnv)

	name := filepath.Base(filepath.Clean(stateDir))
	account, agent, ok := strings.Cut(name, "-")
	agentID, err := strconv.ParseInt(agent, 10, 64)
	if !ok || err != nil || agentID <= 0 || account == "" {
		return nil, nil, output.ErrUsage(fmt.Sprintf("--connect-state %q is not a connector state directory (named <account>-<agent person id>)", stateDir))
	}
	if account != accountID {
		return nil, nil, output.ErrUsage(fmt.Sprintf("--connect-state %q belongs to account %s, not %s", stateDir, account, accountID))
	}

	path := filepath.Join(stateDir, connector.LedgerFile)
	if _, err := os.Lstat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil, output.ErrUsage(fmt.Sprintf("no connector ledger in %s", stateDir))
		}
		return nil, nil, err
	}
	ledger, err := connector.OpenLedger(path)
	if err != nil {
		return nil, nil, err
	}
	dispatch, err := ledger.Dispatch(token, agentID)
	if err != nil {
		_ = ledger.Close()
		return nil, nil, err
	}
	return dispatch, func() { _ = ledger.Close() }, nil
}
