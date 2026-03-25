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
		newAccountsUseCmd(),
		newAccountsShowCmd(),
		newAccountsUpdateCmd(),
		newAccountsLogoCmd(),
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

func newAccountsShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Show account details",
		Long:  "Show details for the current account including limits, settings, and subscription.",
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			account, err := app.Account().Account().GetAccount(cmd.Context())
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(account,
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "update",
						Cmd:         "basecamp accounts update --name '...'",
						Description: "Rename account",
					},
					output.Breadcrumb{
						Action:      "list",
						Cmd:         "basecamp accounts list",
						Description: "List accounts",
					},
				),
			)
		},
	}
}

func newAccountsUpdateCmd() *cobra.Command {
	var name string

	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update account settings",
		Long: `Update account settings.

  basecamp accounts update --name "New Company Name"`,
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			if !cmd.Flags().Changed("name") {
				return output.ErrUsage("No changes specified (use --name)")
			}

			account, err := app.Account().Account().UpdateName(cmd.Context(), name)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(account,
				output.WithSummary(fmt.Sprintf("Account renamed to %q", account.Name)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "show",
						Cmd:         "basecamp accounts show",
						Description: "View account",
					},
				),
			)
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "New account name")

	return cmd
}

func newAccountsLogoCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "logo",
		Short: "Manage account logo",
		Long:  "Upload or remove the account logo.",
	}

	cmd.AddCommand(
		newAccountsLogoUploadCmd(),
		newAccountsLogoRemoveCmd(),
	)

	return cmd
}

func newAccountsLogoUploadCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "upload <file>",
		Short: "Upload account logo",
		Long: `Upload or replace the account logo.

Accepts PNG, JPEG, GIF, WebP, AVIF, or HEIC files up to 5 MB.

  basecamp accounts logo upload logo.png`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			path := richtext.NormalizeDragPath(args[0])

			if err := richtext.ValidateFile(path); err != nil {
				return fmt.Errorf("%s: %w", args[0], err)
			}

			// Enforce logo-specific constraints (5 MB, image types only)
			info, err := os.Stat(path)
			if err != nil {
				return fmt.Errorf("%s: %w", args[0], err)
			}
			const maxLogoSize = 5 * 1024 * 1024
			if info.Size() > maxLogoSize {
				return output.ErrUsage(fmt.Sprintf("%s exceeds maximum logo size of 5 MB", filepath.Base(path)))
			}

			contentType := richtext.DetectMIME(path)
			switch contentType {
			case "image/png", "image/jpeg", "image/gif", "image/webp", "image/avif", "image/heic":
				// OK
			default:
				return output.ErrUsage(fmt.Sprintf("%s: unsupported image type %s (use PNG, JPEG, GIF, WebP, AVIF, or HEIC)", filepath.Base(path), contentType))
			}

			filename := filepath.Base(path)

			f, err := os.Open(path)
			if err != nil {
				return fmt.Errorf("%s: %w", args[0], err)
			}
			defer f.Close()

			err = app.Account().Account().UpdateLogo(cmd.Context(), f, filename, contentType)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(map[string]any{"uploaded": true, "filename": filename},
				output.WithSummary(fmt.Sprintf("Logo uploaded: %s", filename)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "show",
						Cmd:         "basecamp accounts show",
						Description: "View account",
					},
					output.Breadcrumb{
						Action:      "remove",
						Cmd:         "basecamp accounts logo remove",
						Description: "Remove logo",
					},
				),
			)
		},
	}
}

func newAccountsLogoRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove",
		Short: "Remove account logo",
		Long:  "Remove the account logo.",
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			err := app.Account().Account().RemoveLogo(cmd.Context())
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(map[string]any{"removed": true},
				output.WithSummary("Logo removed"),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "upload",
						Cmd:         "basecamp accounts logo upload <file>",
						Description: "Upload a new logo",
					},
				),
			)
		},
	}
}
