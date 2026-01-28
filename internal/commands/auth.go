// Package commands implements the CLI commands.
package commands

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/basecamp/bcq/internal/appctx"
	"github.com/basecamp/bcq/internal/auth"
	"github.com/basecamp/bcq/internal/config"
	"github.com/basecamp/bcq/internal/output"
)

// NewAuthCmd creates the auth command group.
func NewAuthCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Manage authentication",
		Long:  "Manage Basecamp authentication including login, logout, and status.",
	}

	cmd.AddCommand(
		newAuthLoginCmd(),
		newAuthLogoutCmd(),
		newAuthStatusCmd(),
		newAuthRefreshCmd(),
		newAuthTokenCmd(),
	)

	return cmd
}

func newAuthLoginCmd() *cobra.Command {
	var scope string
	var noBrowser bool

	cmd := &cobra.Command{
		Use:   "login",
		Short: "Authenticate with Basecamp",
		Long:  "Start the OAuth flow to authenticate with Basecamp.",
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if app == nil {
				return fmt.Errorf("app not initialized")
			}

			// Validate scope
			if scope != "" && scope != "read" && scope != "full" {
				return output.ErrUsage("Invalid scope. Use 'read' or 'full'")
			}

			if scope == "" {
				scope = "read"
			}

			fmt.Println("Starting Basecamp authentication...")
			if scope == "read" {
				fmt.Println("Scope: read-only (use --scope full for write access)")
			} else {
				fmt.Println("Scope: full (read and write access)")
			}

			if err := app.Auth.Login(cmd.Context(), auth.LoginOptions{
				Scope:     scope,
				NoBrowser: noBrowser,
				Logger:    func(msg string) { fmt.Println(msg) },
			}); err != nil {
				return err
			}

			fmt.Println("\nAuthentication successful!")

			// Try to fetch and store user profile
			resp, err := app.SDK.Get(cmd.Context(), "/my/profile.json")
			if err == nil {
				var profile struct {
					ID   int    `json:"id"`
					Name string `json:"name"`
				}
				if err := resp.UnmarshalData(&profile); err == nil {
					if err := app.Auth.SetUserID(fmt.Sprintf("%d", profile.ID)); err == nil {
						fmt.Printf("Logged in as: %s\n", profile.Name)
					}
				}
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&scope, "scope", "", "OAuth scope: 'read' (default) or 'full'")
	cmd.Flags().BoolVar(&noBrowser, "no-browser", false, "Don't open browser automatically")

	return cmd
}

func newAuthLogoutCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Remove stored credentials",
		Long:  "Remove stored authentication credentials for the current origin.",
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if app == nil {
				return fmt.Errorf("app not initialized")
			}

			if err := app.Auth.Logout(); err != nil {
				return err
			}

			return app.OK(map[string]string{
				"status": "logged_out",
			}, output.WithSummary("Successfully logged out"))
		},
	}
}

func newAuthStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show authentication status",
		Long:  "Display the current authentication status and token information.",
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if app == nil {
				return fmt.Errorf("app not initialized")
			}

			origin := config.NormalizeBaseURL(app.Config.BaseURL)

			// Check if using BASECAMP_TOKEN environment variable
			if envToken := os.Getenv("BASECAMP_TOKEN"); envToken != "" {
				return app.OK(map[string]any{
					"authenticated": true,
					"origin":        origin,
					"source":        "BASECAMP_TOKEN",
				}, output.WithSummary("Authenticated via BASECAMP_TOKEN env var"))
			}

			if !app.Auth.IsAuthenticated() {
				return app.OK(map[string]any{
					"authenticated": false,
					"origin":        origin,
				}, output.WithSummary("Not authenticated"))
			}

			// Get stored credentials info
			store := app.Auth.GetStore()
			creds, err := store.Load(origin)
			if err != nil {
				return err
			}

			status := map[string]any{
				"authenticated": true,
				"origin":        origin,
				"source":        "oauth",
				"oauth_type":    creds.OAuthType,
				"scope":         creds.Scope,
			}

			if creds.UserID != "" {
				status["user_id"] = creds.UserID
			}

			// Token expiration
			if creds.ExpiresAt > 0 {
				expiresIn := time.Until(time.Unix(creds.ExpiresAt, 0))
				status["expires_in"] = expiresIn.Round(time.Second).String()
				status["expired"] = expiresIn < 0
			}

			summary := "Authenticated"
			if creds.Scope != "" {
				summary += fmt.Sprintf(" (scope: %s)", creds.Scope)
			}

			return app.OK(status, output.WithSummary(summary))
		},
	}
}

func newAuthRefreshCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "refresh",
		Short: "Refresh the access token",
		Long:  "Force a refresh of the OAuth access token.",
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if app == nil {
				return fmt.Errorf("app not initialized")
			}

			if err := app.Auth.Refresh(cmd.Context()); err != nil {
				return err
			}

			return app.OK(map[string]string{
				"status": "refreshed",
			}, output.WithSummary("Token refreshed successfully"))
		},
	}
}

func newAuthTokenCmd() *cobra.Command {
	var stored bool

	cmd := &cobra.Command{
		Use:   "token",
		Short: "Print the auth token",
		Long: `Print the current access token to stdout for use with other tools.

If BASECAMP_TOKEN env is set, it is returned directly (no refresh).
Otherwise, stored OAuth credentials are used and auto-refreshed if near expiry.

Examples:
  export BASECAMP_TOKEN=$(bcq auth token)
  curl -H "Authorization: Bearer $(bcq auth token)" ...

Get tokens for different environments using the global --host flag:
  bcq --host localhost:3000 auth token
  bcq --host staging.example.com auth token

The --stored flag ignores BASECAMP_TOKEN and uses stored OAuth credentials:
  bcq auth token --stored

Output modes:
  bcq auth token           # Raw token (default, for shell substitution)
  bcq auth token --json    # JSON envelope with token in data field
  bcq auth token --stats   # Raw token + stats on stderr`,
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if app == nil {
				return fmt.Errorf("app not initialized")
			}

			var token string
			var err error

			if stored {
				// Use stored OAuth credentials (ignores BASECAMP_TOKEN env)
				// This also handles auto-refresh for near-expiry tokens
				token, err = app.Auth.StoredAccessToken(cmd.Context())
			} else {
				// Normal path: checks BASECAMP_TOKEN env first, then stored OAuth
				token, err = app.Auth.AccessToken(cmd.Context())
			}

			if err != nil {
				return err
			}

			// Output raw token by default for backwards compatibility with shell scripts.
			// Only use JSON envelope when --json is explicitly requested.
			if app.Flags.JSON || app.Flags.Agent {
				return app.OK(map[string]string{"token": token})
			}

			// Raw output: print token directly, with optional stats on stderr
			fmt.Println(token)
			return nil
		},
	}

	cmd.Flags().BoolVar(&stored, "stored", false, "Use stored OAuth token, ignoring BASECAMP_TOKEN env var")

	return cmd
}
