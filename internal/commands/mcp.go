package commands

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
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

			// The task token is taken out of the environment before anything
			// else runs: authentication can start helper processes, and a
			// child started then would inherit it.
			var taskToken string
			if connectState != "" {
				if readOnly {
					// Every connect action records something; refused before
					// the token or the ledger is touched.
					return output.ErrUsage("--connect-state cannot be combined with --read-only: every basecamp_connect action records what the worker did")
				}
				taskToken = takeConnectTaskToken()
			}

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
				dispatch, closeLedger, err := openConnectDispatch(cmd.Context(), connectState, app.Config.AccountID, taskToken)
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

// stateDirHint says what the refused directory should have been.
func stateDirHint(refusal *connector.StateDirError) string {
	switch refusal.Why {
	case connector.StateDirOtherAccount:
		return fmt.Sprintf("It belongs to account %s; this server serves account %s.", refusal.Account, refusal.Want)
	default:
		return fmt.Sprintf("The connector's state directories live in %s, named <account>-<agent person id>.", refusal.Root)
	}
}

// takeConnectTaskToken reads the task token and removes it from the
// environment, so nothing this process starts inherits it. That clears it from
// what the process hands on, not from its own /proc environ, which only this
// user can read.
func takeConnectTaskToken() string {
	token := os.Getenv(connectTaskTokenEnv)
	_ = os.Unsetenv(connectTaskTokenEnv)
	return token
}

// openConnectDispatch opens the connector's ledger in stateDir and binds it to
// the task token in the environment.
//
// The directory is the connector's own, named "<account>-<agent person id>",
// and it must belong to the account this server serves: that is where the
// agent's id comes from, and a ledger for another account is refused rather
// than served. The ledger must already exist — a worker's server reads the
// connector's ledger, it never starts one.
func openConnectDispatch(ctx context.Context, stateDir, accountID, token string) (*connector.TaskDispatch, func(), error) {
	if strings.TrimSpace(token) == "" {
		return nil, nil, output.ErrUsage("--connect-state needs the task token in $" + connectTaskTokenEnv + "; the connector sets it when it starts a worker")
	}

	agentID, err := connector.ResolveStateDir(stateDir, accountID)
	if err != nil {
		var refusal *connector.StateDirError
		if errors.As(err, &refusal) {
			return nil, nil, output.ErrUsageHint(
				fmt.Sprintf("--connect-state %s is %s", refusal.Dir, refusal.Why),
				stateDirHint(refusal))
		}
		return nil, nil, err
	}

	// The connector owns the ledger: a worker's server opens it as it is, and
	// never creates or migrates it.
	ledger, err := connector.OpenExistingLedger(ctx, filepath.Join(stateDir, connector.LedgerFile))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil, output.ErrUsage(fmt.Sprintf("no connector ledger in %s", stateDir))
		}
		return nil, nil, err
	}
	dispatch, err := ledger.Dispatch(ctx, token, agentID)
	if err != nil {
		_ = ledger.Close()
		if errors.Is(err, connector.ErrTaskTokenRefused) {
			return nil, nil, output.ErrUsage("$" + connectTaskTokenEnv + " names no current task in " + stateDir)
		}
		return nil, nil, err
	}
	return dispatch, func() { _ = ledger.Close() }, nil
}
