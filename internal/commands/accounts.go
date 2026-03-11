package commands

import (
	"fmt"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/output"
	"github.com/basecamp/basecamp-cli/internal/tui/resolve"
)

// NewAccountsCmd creates the accounts command group.
func NewAccountsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "accounts",
		Aliases: []string{"account"},
		Short:   "Manage accounts",
		Long:    "List authorized Basecamp accounts and set the default.",
	}

	cmd.AddCommand(
		newAccountsListCmd(),
		newAccountsUseCmd(),
	)

	return cmd
}

func newAccountsListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List authorized accounts",
		Long:  "List all Basecamp accounts you have access to.",
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if app == nil {
				return fmt.Errorf("app not initialized")
			}

			accounts, err := app.Resolve().ListAccounts(cmd.Context())
			if err != nil {
				return err
			}

			if len(accounts) == 0 {
				return output.ErrNotFound("account", "any")
			}

			// Convert to a serializable format
			type accountRow struct {
				ID   int64  `json:"id"`
				Name string `json:"name"`
				Href string `json:"href"`
			}
			rows := make([]accountRow, len(accounts))
			for i, acct := range accounts {
				rows[i] = accountRow{
					ID:   acct.ID,
					Name: acct.Name,
					Href: acct.HREF,
				}
			}

			return app.OK(rows,
				output.WithSummary(fmt.Sprintf("%d account(s)", len(rows))),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "use",
						Cmd:         "basecamp accounts use <id>",
						Description: "Set default account",
					},
				),
			)
		},
	}

	return cmd
}

func newAccountsUseCmd() *cobra.Command {
	var scope string

	cmd := &cobra.Command{
		Use:   "use <id>",
		Short: "Set default account",
		Long:  "Set the default Basecamp account for CLI commands.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if app == nil {
				return fmt.Errorf("app not initialized")
			}

			// Validate scope
			if scope != "global" && scope != "local" {
				return output.ErrUsage("--scope must be \"global\" or \"local\"")
			}

			accountIDStr := args[0]

			// Validate it's a number
			accountID, err := strconv.ParseInt(accountIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid account ID")
			}

			// Validate account exists
			accounts, err := app.Resolve().ListAccounts(cmd.Context())
			if err != nil {
				return err
			}

			var found bool
			var accountName string
			for _, acct := range accounts {
				if acct.ID == accountID {
					found = true
					accountName = acct.Name
					break
				}
			}
			if !found {
				return output.ErrNotFound("account", accountIDStr)
			}

			// Persist the account ID
			if err := resolve.PersistValue("account_id", accountIDStr, scope); err != nil {
				return fmt.Errorf("failed to save account: %w", err)
			}

			summary := fmt.Sprintf("Default account set to %s (#%s, %s)", accountName, accountIDStr, scope)

			return app.OK(map[string]any{
				"id":    accountID,
				"name":  accountName,
				"scope": scope,
			},
				output.WithSummary(summary),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "list",
						Cmd:         "basecamp accounts list",
						Description: "List accounts",
					},
					output.Breadcrumb{
						Action:      "projects",
						Cmd:         "basecamp projects list",
						Description: "List projects",
					},
				),
			)
		},
	}

	cmd.Flags().StringVar(&scope, "scope", "global", "Config scope (global or local)")

	return cmd
}
