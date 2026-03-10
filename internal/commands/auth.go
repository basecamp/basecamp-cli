// Package commands implements the CLI commands.
package commands

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/auth"
	"github.com/basecamp/basecamp-cli/internal/harness"
	"github.com/basecamp/basecamp-cli/internal/output"
	"github.com/basecamp/basecamp-cli/internal/tui"
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
	return buildLoginCmd("login")
}

func newAuthLogoutCmd() *cobra.Command {
	return buildLogoutCmd("logout")
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

			credKey := app.Auth.CredentialKey()

			// Check if using BASECAMP_TOKEN environment variable
			if envToken := os.Getenv("BASECAMP_TOKEN"); envToken != "" {
				result := map[string]any{
					"authenticated": true,
					"source":        "BASECAMP_TOKEN",
				}
				if app.Config.ActiveProfile != "" {
					result["profile"] = app.Config.ActiveProfile
				}
				return app.OK(result, output.WithSummary("Authenticated via BASECAMP_TOKEN env var"))
			}

			if !app.Auth.IsAuthenticated() {
				result := map[string]any{
					"authenticated": false,
				}
				if app.Config.ActiveProfile != "" {
					result["profile"] = app.Config.ActiveProfile
				}
				return app.OK(result, output.WithSummary("Not authenticated"))
			}

			// Get stored credentials info
			store := app.Auth.GetStore()
			creds, err := store.Load(credKey)
			if err != nil {
				return err
			}

			// Suppress scope for Launchpad (scopes are not supported)
			effectiveScope := creds.Scope
			if creds.OAuthType == "launchpad" {
				effectiveScope = ""
			}

			status := map[string]any{
				"authenticated": true,
				"source":        "oauth",
				"oauth_type":    creds.OAuthType,
			}
			if effectiveScope != "" {
				status["scope"] = effectiveScope
			}
			if app.Config.ActiveProfile != "" {
				status["profile"] = app.Config.ActiveProfile
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
			if effectiveScope != "" {
				summary += fmt.Sprintf(" (scope: %s)", effectiveScope)
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
  export BASECAMP_TOKEN=$(basecamp auth token)
  curl -H "Authorization: Bearer $(basecamp auth token)" ...

Get tokens for different profiles:
  basecamp --profile personal auth token
  basecamp --profile staging auth token

The --stored flag ignores BASECAMP_TOKEN and uses stored OAuth credentials:
  basecamp auth token --stored

Output modes:
  basecamp auth token           # Raw token (default, for shell substitution)
  basecamp auth token --json    # JSON envelope with token in data field
  basecamp auth token --stats   # Raw token + stats on stderr`,
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
			fmt.Println(strings.ReplaceAll(strings.ReplaceAll(token, "\n", ""), "\r", ""))
			return nil
		},
	}

	cmd.Flags().BoolVar(&stored, "stored", false, "Use stored OAuth token, ignoring BASECAMP_TOKEN env var")

	return cmd
}

// NewLoginCmd creates the top-level login shortcut.
func NewLoginCmd() *cobra.Command {
	return buildLoginCmd("login")
}

// NewLogoutCmd creates the top-level logout shortcut.
func NewLogoutCmd() *cobra.Command {
	return buildLogoutCmd("logout")
}

// buildLoginCmd constructs a login command with the given Use name.
// Shared by newAuthLoginCmd ("login" under auth) and NewLoginCmd (top-level).
func buildLoginCmd(use string) *cobra.Command {
	var scope string
	var noBrowser bool
	var remote bool
	var local bool

	cmd := &cobra.Command{
		Use:   use,
		Short: "Authenticate with Basecamp",
		Long:  "Start the OAuth flow to authenticate with Basecamp.",
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if app == nil {
				return fmt.Errorf("app not initialized")
			}

			w := cmd.OutOrStdout()
			r := output.NewRendererWithTheme(w, false, tui.ResolveTheme(tui.DetectDark()))

			if app.Config.ActiveProfile != "" {
				fmt.Fprintln(w, r.Summary.Render(fmt.Sprintf("Starting authentication for profile %q...", app.Config.ActiveProfile)))
			} else {
				fmt.Fprintln(w, r.Summary.Render("Starting Basecamp authentication..."))
			}

			result, err := app.Auth.Login(cmd.Context(), auth.LoginOptions{
				Scope:     scope,
				NoBrowser: noBrowser,
				Remote:    remote,
				Local:     local,
				Logger:    func(msg string) { fmt.Fprintln(w, msg) },
			})
			if err != nil {
				return err
			}

			fmt.Fprintln(w)
			fmt.Fprintln(w, r.Success.Render("Authentication successful!"))

			if result.Scope != "" {
				fmt.Fprintln(w, r.Muted.Render(fmt.Sprintf("Access: %s", result.Scope)))
			}

			resp, profileErr := app.SDK.Get(cmd.Context(), "/my/profile.json")
			if profileErr == nil {
				var profile struct {
					ID    int    `json:"id"`
					Name  string `json:"name"`
					Email string `json:"email_address"`
				}
				if err := resp.UnmarshalData(&profile); err == nil {
					if err := app.Auth.SetUserIdentity(fmt.Sprintf("%d", profile.ID), profile.Email); err == nil {
						fmt.Fprintln(w, r.Data.Render(fmt.Sprintf("Logged in as: %s", profile.Name)))
					}
				}
			}

			printAgentNudge(w, r)

			return nil
		},
	}

	cmd.Flags().StringVar(&scope, "scope", "", "OAuth scope: 'read' or 'full' (BC3 only)")
	cmd.Flags().BoolVar(&noBrowser, "no-browser", false, "Don't open browser automatically")
	cmd.Flags().BoolVar(&remote, "remote", false, "Force remote/headless mode (paste callback URL instead of local listener)")
	cmd.Flags().BoolVar(&local, "local", false, "Force local mode (override SSH auto-detection)")
	cmd.MarkFlagsMutuallyExclusive("remote", "local")

	return cmd
}

// buildLogoutCmd constructs a logout command with the given Use name.
// Shared by newAuthLogoutCmd ("logout" under auth) and NewLogoutCmd (top-level).
func buildLogoutCmd(use string) *cobra.Command {
	return &cobra.Command{
		Use:   use,
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

// printAgentNudge prints a hint about coding agent setup after login.
func printAgentNudge(w io.Writer, r *output.Renderer) {
	for _, agent := range harness.DetectedAgents() {
		if agent.Checks == nil {
			continue
		}
		for _, c := range agent.Checks() {
			if c.Status != "pass" {
				fmt.Fprintln(w)
				fmt.Fprintln(w, r.Muted.Render(fmt.Sprintf("  %s detected. Connect it to Basecamp:", agent.Name)))
				fmt.Fprintln(w, r.Data.Render(fmt.Sprintf("  basecamp setup %s", agent.ID)))
				return // one nudge is enough
			}
		}
	}
}
