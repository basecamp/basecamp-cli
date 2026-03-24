package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/output"
	"github.com/basecamp/basecamp-cli/internal/richtext"
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
		newAccountsShowCmd(),
		newAccountsUseCmd(),
		newAccountsRenameCmd(),
		newAccountsLogoCmd(),
	)

	return cmd
}

func newAccountsShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Show current account details",
		Long:  "Show details for the currently selected Basecamp account.",
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if app == nil {
				return fmt.Errorf("app not initialized")
			}
			if err := app.RequireAccount(); err != nil {
				return err
			}

			account, err := app.Account().Account().GetAccount(cmd.Context())
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(account,
				output.WithSummary(fmt.Sprintf("Account: %s", account.Name)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "rename",
						Cmd:         "basecamp accounts rename <name>",
						Description: "Rename this account",
					},
					output.Breadcrumb{
						Action:      "logo",
						Cmd:         "basecamp accounts logo set <path>",
						Description: "Update the account logo",
					},
				),
			)
		},
	}
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

			count := len(rows)
			label := "accounts"
			if count == 1 {
				label = "account"
			}

			return app.OK(rows,
				output.WithSummary(fmt.Sprintf("%d %s", count, label)),
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

			// Persist the canonical account ID (e.g. "007" → "7")
			canonicalID := strconv.FormatInt(accountID, 10)
			if err := resolve.PersistValue("account_id", canonicalID, scope); err != nil {
				return fmt.Errorf("failed to save account: %w", err)
			}

			summary := fmt.Sprintf("Default account set to %s (#%s, %s)", accountName, canonicalID, scope)

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

func newAccountsRenameCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rename <name>",
		Short: "Rename the current account",
		Long:  "Rename the currently selected Basecamp account.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if app == nil {
				return fmt.Errorf("app not initialized")
			}
			if err := app.RequireAccount(); err != nil {
				return err
			}

			account, err := app.Account().Account().UpdateName(cmd.Context(), args[0])
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(account,
				output.WithSummary(fmt.Sprintf("Renamed account to %s", account.Name)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "show",
						Cmd:         "basecamp accounts show",
						Description: "Show account details",
					},
				),
			)
		},
	}
}

func newAccountsLogoCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "logo",
		Short: "Manage the current account logo",
		Long:  "Upload, replace, or remove the logo for the currently selected Basecamp account.",
	}

	cmd.AddCommand(
		newAccountsLogoSetCmd(),
		newAccountsLogoRemoveCmd(),
	)

	return cmd
}

func newAccountsLogoSetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "set <path>",
		Short: "Upload or replace the account logo",
		Long:  "Upload or replace the logo for the currently selected Basecamp account.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if app == nil {
				return fmt.Errorf("app not initialized")
			}
			if err := app.RequireAccount(); err != nil {
				return err
			}

			path := filepath.Clean(args[0])
			if err := validateAccountLogoFile(path); err != nil {
				return output.ErrUsage(err.Error())
			}

			f, err := os.Open(path)
			if err != nil {
				return fmt.Errorf("open logo: %w", err)
			}
			defer f.Close()

			filename := filepath.Base(path)
			contentType := richtext.DetectMIME(path)
			if err := app.Account().Account().UpdateLogo(cmd.Context(), f, filename, contentType); err != nil {
				return convertSDKError(err)
			}

			return app.OK(map[string]any{
				"logo":         filename,
				"content_type": contentType,
				"updated":      true,
			},
				output.WithSummary(fmt.Sprintf("Updated account logo from %s", filename)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "show",
						Cmd:         "basecamp accounts show",
						Description: "Show account details",
					},
					output.Breadcrumb{
						Action:      "remove",
						Cmd:         "basecamp accounts logo remove",
						Description: "Remove the account logo",
					},
				),
			)
		},
	}
}

func newAccountsLogoRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove",
		Short: "Remove the account logo",
		Long:  "Remove the logo from the currently selected Basecamp account.",
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if app == nil {
				return fmt.Errorf("app not initialized")
			}
			if err := app.RequireAccount(); err != nil {
				return err
			}

			if err := app.Account().Account().RemoveLogo(cmd.Context()); err != nil {
				return convertSDKError(err)
			}

			return app.OK(map[string]any{"removed": true},
				output.WithSummary("Removed account logo"),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "show",
						Cmd:         "basecamp accounts show",
						Description: "Show account details",
					},
					output.Breadcrumb{
						Action:      "set",
						Cmd:         "basecamp accounts logo set <path>",
						Description: "Upload a new account logo",
					},
				),
			)
		},
	}
}

func validateAccountLogoFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("cannot access %s: %w", filepath.Base(path), err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", filepath.Base(path))
	}
	if info.Size() > 5*1024*1024 {
		return fmt.Errorf("%s exceeds maximum size of 5MB", filepath.Base(path))
	}
	contentType := richtext.DetectMIME(path)
	switch contentType {
	case "image/png", "image/jpeg", "image/gif", "image/webp", "image/avif", "image/heic":
		return nil
	default:
		return fmt.Errorf("%s must be PNG, JPEG, GIF, WebP, AVIF, or HEIC", filepath.Base(path))
	}
}
