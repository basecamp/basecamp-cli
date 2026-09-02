// Package commands implements the CLI commands.
package commands

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/auth"
	"github.com/basecamp/basecamp-cli/internal/config"
	"github.com/basecamp/basecamp-cli/internal/harness"
	"github.com/basecamp/basecamp-cli/internal/output"
	"github.com/basecamp/basecamp-cli/internal/stdinarg"
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
			// Only use JSON envelope when --json/--agent/--jq is explicitly requested.
			if app.Flags.JSON || app.Flags.Agent || app.Flags.JQFilter != "" {
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

// AnnotationProfileMayCreate marks a command that registers the profile
// named by --profile / BASECAMP_PROFILE itself. The root pre-run lets an
// unknown name through for such a command instead of rejecting it, leaving
// the top-level configuration in place under that credential key.
const AnnotationProfileMayCreate = "profile_may_create"

// maxTokenBytes bounds what --with-token reads from stdin: an access token
// is a few hundred bytes, so anything larger is not one.
const maxTokenBytes = 4096

// buildLoginCmd constructs a login command with the given Use name.
// Shared by newAuthLoginCmd ("login" under auth) and NewLoginCmd (top-level).
func buildLoginCmd(use string) *cobra.Command {
	var scope string
	var noBrowser bool
	var remote bool
	var local bool
	var deviceCode bool
	var withToken bool
	var expectIdentity string
	var loginHint string

	cmd := &cobra.Command{
		Use:   use,
		Short: "Authenticate with Basecamp",
		Long: `Start the OAuth flow to authenticate with Basecamp, or import a personal access token.

Examples:
  basecamp auth login                              # Browser (or device) flow
  basecamp auth login --device-code                # Headless: approve the printed code elsewhere
  basecamp auth login --expect-identity 12345      # Refuse the login unless it is this identity

Import a personal access token from stdin (never pass it as an argument):
  op read "op://<vault>/<item>/credential" | basecamp auth login --with-token -P bot --account 999
  ... | basecamp auth login --with-token -P bot --account 999 --expect-identity 12345 --json

--with-token stores the token under the named profile, creating the profile
when --account is given, then verifies who it authenticates as.`,
		Annotations: map[string]string{AnnotationProfileMayCreate: "true"},
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if app == nil {
				return fmt.Errorf("app not initialized")
			}

			if withToken {
				return runLoginWithToken(cmd, app, scope, expectIdentity)
			}

			if app.Flags.JQFilter != "" {
				return output.ErrJQNotSupported("the login command")
			}
			if machineOutputFlagSet(app) {
				return output.ErrUsageHint("Interactive login cannot run under a machine output mode",
					"Browser and device logins print instructions and wait for approval, which no envelope can carry. "+
						"Check credentials with `basecamp auth status`, or import a token headlessly: "+
						"`... | basecamp auth login --with-token -P <profile> --account <id> --json`.")
			}
			if err := requireIdentityCheckable(expectIdentity); err != nil {
				return err
			}
			if name := app.Config.ActiveProfile; name != "" {
				if _, ok := app.Config.Profiles[name]; !ok {
					return output.ErrUsageHint(fmt.Sprintf("Profile %q does not exist", name),
						fmt.Sprintf("Create it with `basecamp profile create %s`, or import a token: `... | basecamp auth login --with-token -P %s --account <id>`.", name, name))
				}
			}

			if deviceCode {
				remote = true
			}

			w := cmd.OutOrStdout()
			r := output.NewRendererWithTheme(w, false, tui.ResolveTheme(tui.DetectDark()))

			if app.Config.ActiveProfile != "" {
				fmt.Fprintln(w, r.Summary.Render(fmt.Sprintf("Starting authentication for profile %q...", app.Config.ActiveProfile)))
			} else {
				fmt.Fprintln(w, r.Summary.Render("Starting Basecamp authentication..."))
			}

			restore := credentialRestorer(app)

			result, err := app.Auth.Login(cmd.Context(), auth.LoginOptions{
				Scope:     scope,
				NoBrowser: noBrowser,
				Remote:    remote,
				Local:     local,
				LoginHint: loginHint,
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

			// Without an expectation the identity line is informational, as
			// it always was; with one, a credential that cannot be verified
			// is not kept.
			who, err := verifyLoginIdentity(cmd.Context(), app, expectIdentity, expectIdentity != "")
			if err != nil {
				restore(cmd.ErrOrStderr())
				return err
			}
			if who != nil {
				fmt.Fprintln(w, r.Data.Render("Logged in as: "+who.label()))
			}

			printAgentNudge(w, r)

			return nil
		},
	}

	cmd.Flags().StringVar(&scope, "scope", "", "OAuth scope: 'read' or 'full' (default full; ignored by Launchpad)")
	cmd.Flags().BoolVar(&noBrowser, "no-browser", false, "Don't open browser automatically")
	cmd.Flags().BoolVar(&remote, "remote", false, "Force remote/headless mode (paste callback URL instead of local listener)")
	cmd.Flags().BoolVar(&local, "local", false, "Force local mode (override SSH auto-detection)")
	cmd.Flags().BoolVar(&deviceCode, "device-code", false, "Headless authentication with manual browser instructions")
	cmd.Flags().BoolVar(&withToken, "with-token", false, "Read a personal access token from stdin instead of running OAuth (requires --profile)")
	cmd.Flags().StringVar(&expectIdentity, "expect-identity", "", "Identity ID the login must authenticate as; otherwise discard the credentials")
	cmd.Flags().StringVar(&loginHint, "login-hint", "", "Email address to sign in as (device flow only; ignored by Launchpad)")
	cmd.MarkFlagsMutuallyExclusive("remote", "local")
	cmd.MarkFlagsMutuallyExclusive("device-code", "local")
	for _, flag := range []string{"device-code", "remote", "local", "no-browser", "login-hint"} {
		cmd.MarkFlagsMutuallyExclusive("with-token", flag)
	}

	return cmd
}

// runLoginWithToken imports a personal access token from stdin as the
// active profile's credential. The profile is registered only after the
// token has proven who it authenticates as, and a token that fails that
// check leaves whatever credential the profile had before.
func runLoginWithToken(cmd *cobra.Command, app *appctx.App, scope, expectIdentity string) error {
	if err := requireIdentityCheckable(expectIdentity); err != nil {
		return err
	}
	if os.Getenv("BASECAMP_TOKEN") != "" {
		return output.ErrUsageHint("BASECAMP_TOKEN is set",
			"Every request, including the identity check, would use it instead of the imported token. Unset it and retry.")
	}

	name := app.Config.ActiveProfile
	if name == "" {
		return output.ErrUsageHint("--with-token stores the token under a named profile",
			"Pass -P/--profile <name>; add --account <id> when the profile does not exist yet.")
	}
	if !isValidProfileName(name) {
		return output.ErrUsage(fmt.Sprintf("Invalid profile name %q: use only letters, numbers, hyphens, and underscores", name))
	}
	if scope == "" {
		scope = "full"
	}

	existing := app.Config.Profiles[name]
	var created *config.ProfileConfig
	switch {
	case existing == nil && app.Flags.Account == "":
		return output.ErrUsageHint(fmt.Sprintf("Profile %q does not exist", name),
			"Pass --account <id> to create it alongside the imported token.")
	case existing == nil:
		created = &config.ProfileConfig{BaseURL: app.Config.BaseURL, AccountID: app.Flags.Account}
	case app.Flags.Account != "" && existing.AccountID != "" && existing.AccountID != app.Flags.Account:
		return output.ErrUsageHint(fmt.Sprintf("Profile %q is bound to account %s, not %s", name, existing.AccountID, app.Flags.Account),
			"Import into a different profile, or pass the account the profile is bound to.")
	}

	token, err := readTokenFromStdin(cmd)
	if err != nil {
		return err
	}

	restore := credentialRestorer(app)
	if err := app.Auth.ImportToken(token, scope); err != nil {
		return err
	}

	who, err := verifyLoginIdentity(cmd.Context(), app, expectIdentity, true)
	if err != nil {
		restore(cmd.ErrOrStderr())
		return err
	}
	// The server's word on the token's scope beats the caller's declaration.
	if who.Scope != "" && who.Scope != scope {
		scope = who.Scope
		if err := setStoredScope(app, scope); err != nil {
			restore(cmd.ErrOrStderr())
			return err
		}
	}

	isDefault := false
	if created != nil {
		created.Scope = scope
		if isDefault, err = registerProfile(name, created); err != nil {
			restore(cmd.ErrOrStderr())
			return err
		}
		if app.Config.Profiles == nil {
			app.Config.Profiles = make(map[string]*config.ProfileConfig)
		}
		app.Config.Profiles[name] = created
	}

	accountID := app.Flags.Account
	if existing != nil && existing.AccountID != "" {
		accountID = existing.AccountID
	}

	data := map[string]any{
		"profile":         name,
		"base_url":        app.Config.BaseURL,
		"source":          "token",
		"oauth_type":      "bc5",
		"scope":           scope,
		"expires_at":      nil,
		"person":          map[string]any{"id": who.PersonID, "name": who.Name, "email": who.Email},
		"profile_created": created != nil,
	}
	if accountID != "" {
		data["account_id"] = accountID
	}
	if who.IdentityID != 0 {
		data["identity"] = map[string]any{"id": who.IdentityID, "email": who.Email}
	}
	if isDefault {
		data["default"] = true
	}

	if app.IsMachineOutput() {
		return app.OK(data, output.WithSummary("Logged in as "+who.label()))
	}

	w := cmd.OutOrStdout()
	r := output.NewRendererWithTheme(w, false, tui.ResolveTheme(tui.DetectDark()))
	fmt.Fprintln(w, r.Success.Render("Logged in as "+who.label()))
	fmt.Fprintln(w, r.Muted.Render(fmt.Sprintf("Profile: %s · Access: %s · Token: personal access token (does not expire)", name, scope)))
	if created != nil {
		line := fmt.Sprintf("Created profile %q for account %s", name, created.AccountID)
		if isDefault {
			line += " (default)"
		}
		fmt.Fprintln(w, r.Muted.Render(line))
	}
	return nil
}

// readTokenFromStdin reads one access token from the command's stdin. A
// terminal is refused outright — a secret typed at a prompt lands in shell
// and terminal history, and the command exists to be piped from a secret
// store. The token is never echoed, logged, or included in an error.
func readTokenFromStdin(cmd *cobra.Command) (string, error) {
	in := cmd.InOrStdin()
	if stdinarg.IsTerminal(in) {
		return "", output.ErrUsageHint("--with-token reads the token from stdin, and stdin is a terminal",
			"Pipe it in from a secret store: `op read \"op://<vault>/<item>/credential\" | basecamp auth login --with-token -P <profile> --account <id>`.")
	}

	data, err := io.ReadAll(io.LimitReader(in, maxTokenBytes+1))
	if err != nil {
		return "", fmt.Errorf("reading token from stdin: %w", err)
	}
	if len(data) > maxTokenBytes {
		return "", output.ErrUsage(fmt.Sprintf("Token on stdin is longer than %d bytes; expected a single access token", maxTokenBytes))
	}

	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", output.ErrUsageHint("No token on stdin",
			"Pipe it in from a secret store: `op read \"op://<vault>/<item>/credential\" | basecamp auth login --with-token -P <profile> --account <id>`.")
	}
	if strings.IndexFunc(token, func(c rune) bool { return unicode.IsSpace(c) || unicode.IsControl(c) }) >= 0 {
		return "", output.ErrUsage("Token on stdin must be a single line with no whitespace or control characters")
	}
	return token, nil
}

// setStoredScope rewrites the scope on the active credential, keeping
// everything else the login recorded on it.
func setStoredScope(app *appctx.App, scope string) error {
	store := app.Auth.GetStore()
	key := app.Auth.CredentialKey()
	creds, err := store.Load(key)
	if err != nil {
		return err
	}
	creds.Scope = scope
	return store.Save(key, creds)
}

// machineOutputFlagSet reports whether an explicit output flag asked for a
// machine format. The config-driven formats are deliberately excluded: a
// configured format=json must not lock a person out of an interactive login.
func machineOutputFlagSet(app *appctx.App) bool {
	return app.Flags.Agent || app.Flags.JSON || app.Flags.Quiet || app.Flags.IDsOnly || app.Flags.Count
}

// requireIdentityCheckable rejects an --expect-identity that could not be
// honored: a malformed ID, or a BASECAMP_TOKEN that every request — the
// identity check included — would use instead of the credential being
// verified.
func requireIdentityCheckable(expectIdentity string) error {
	if expectIdentity == "" {
		return nil
	}
	if _, err := strconv.ParseInt(expectIdentity, 10, 64); err != nil {
		return output.ErrUsage(fmt.Sprintf("Invalid --expect-identity %q: expected a numeric identity ID", expectIdentity))
	}
	if os.Getenv("BASECAMP_TOKEN") != "" {
		return output.ErrUsageHint("--expect-identity cannot be checked while BASECAMP_TOKEN is set",
			"The identity check would run as the environment token, not the new credential. Unset it and retry.")
	}
	return nil
}

// credentialRestorer snapshots the active credential and returns a function
// that puts it back — or removes what a login stored when there was none —
// so a login that fails verification leaves the profile as it found it.
func credentialRestorer(app *appctx.App) func(warn io.Writer) {
	store := app.Auth.GetStore()
	key := app.Auth.CredentialKey()
	prev, err := store.Load(key)
	if err != nil {
		prev = nil
	}
	return func(warn io.Writer) {
		var restoreErr error
		if prev != nil {
			restoreErr = store.Save(key, prev)
		} else {
			restoreErr = store.Delete(key)
		}
		if restoreErr != nil {
			fmt.Fprintf(warn, "Warning: could not restore credentials for %s: %v\n", key, restoreErr)
		}
	}
}

// loginIdentity is who a freshly stored credential authenticates as: the
// account-scoped person (from /my/profile.json) and the account-independent
// identity (from the authorization endpoint), which is what --expect-identity
// compares against.
type loginIdentity struct {
	PersonID   int64
	Name       string
	Email      string
	IdentityID int64
	Scope      string
}

func (l *loginIdentity) label() string {
	label := l.Name
	if l.Email != "" {
		label += " <" + l.Email + ">"
	}
	parts := []string{}
	if l.IdentityID != 0 {
		parts = append(parts, fmt.Sprintf("identity %d", l.IdentityID))
	}
	if l.PersonID != 0 {
		parts = append(parts, fmt.Sprintf("person %d", l.PersonID))
	}
	if len(parts) > 0 {
		label += " (" + strings.Join(parts, ", ") + ")"
	}
	return label
}

// verifyLoginIdentity resolves who the active credential authenticates as
// and records it on the credential. With strict set, a lookup failure is the
// caller's error to act on; otherwise it is reported as no identity (nil,
// nil), as the post-login line has always been best-effort. A non-empty
// expectIdentity must match the identity ID, and is checked strictly.
func verifyLoginIdentity(ctx context.Context, app *appctx.App, expectIdentity string, strict bool) (*loginIdentity, error) {
	resp, err := app.SDK.Get(ctx, "/my/profile.json")
	if err != nil {
		if !strict {
			return nil, nil
		}
		return nil, convertSDKError(err)
	}
	var person struct {
		ID    int64  `json:"id"`
		Name  string `json:"name"`
		Email string `json:"email_address"`
	}
	if err := resp.UnmarshalData(&person); err != nil {
		if !strict {
			return nil, nil
		}
		return nil, output.ErrAPI(0, fmt.Sprintf("unexpected /my/profile.json response: %v", err))
	}
	who := &loginIdentity{PersonID: person.ID, Name: person.Name, Email: person.Email}

	endpoint, err := app.Auth.AuthorizationEndpoint(ctx)
	if err == nil {
		var info *basecamp.AuthorizationInfo
		info, err = app.SDK.Authorization().GetInfo(ctx, &basecamp.GetInfoOptions{Endpoint: endpoint, FilterProduct: "bc3"})
		if err == nil {
			who.IdentityID = info.Identity.ID
			who.Scope = info.Scope
			if who.Email == "" {
				who.Email = info.Identity.EmailAddress
			}
		}
	}
	if err != nil && expectIdentity != "" {
		return nil, output.ErrAuth(fmt.Sprintf("Could not verify the identity of the new credential: %v", err))
	}

	if expectIdentity != "" && strconv.FormatInt(who.IdentityID, 10) != expectIdentity {
		return nil, output.ErrAuth(fmt.Sprintf("Authenticated as %s, not identity %s; the credential was not kept", who.label(), expectIdentity))
	}

	_ = app.Auth.SetUserIdentity(strconv.FormatInt(person.ID, 10), who.Email)
	return who, nil
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
//
// Detection proves presence, not intent: with a single detected-unhealthy agent
// it points at that agent; with several, it never guesses — it prints every
// `basecamp setup <id>` choice so the user picks.
func printAgentNudge(w io.Writer, r *output.Renderer) {
	type nudgeAgent struct{ id, name string }
	var unhealthy []nudgeAgent
	for _, agent := range harness.DetectedAgents() {
		if agent.Checks == nil {
			continue
		}
		for _, c := range agent.Checks() {
			if c.Status != "pass" {
				unhealthy = append(unhealthy, nudgeAgent{id: agent.ID, name: agent.Name})
				break
			}
		}
	}
	sort.Slice(unhealthy, func(i, j int) bool { return unhealthy[i].id < unhealthy[j].id })

	switch len(unhealthy) {
	case 0:
		return
	case 1:
		fmt.Fprintln(w)
		fmt.Fprintln(w, r.Muted.Render(fmt.Sprintf("  %s detected. Connect it to Basecamp:", unhealthy[0].name)))
		fmt.Fprintln(w, r.Data.Render(fmt.Sprintf("  basecamp setup %s", unhealthy[0].id)))
	default:
		fmt.Fprintln(w)
		fmt.Fprintln(w, r.Muted.Render("  Multiple coding agents detected. Choose one:"))
		for _, a := range unhealthy {
			fmt.Fprintln(w, r.Data.Render(fmt.Sprintf("  basecamp setup %s", a.id)))
		}
	}
}
