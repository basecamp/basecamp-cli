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
	"github.com/basecamp/basecamp-cli/internal/richtext"
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

--with-token verifies the token against the server — who it authenticates as,
and that it can reach the profile's account — before storing it under the
named profile, creating the profile when --account is given.

--login-hint names the account to sign in as on the device-flow approval page.
This build tells you the hint instead of sending it to the server.`,
		Annotations: map[string]string{AnnotationProfileMayCreate: "true"},
		// No positional arguments, so a token pasted after --with-token is
		// refused rather than silently ignored while it sits in shell history.
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if app == nil {
				return fmt.Errorf("app not initialized")
			}

			expect, err := parseExpectIdentity(expectIdentity)
			if err != nil {
				return err
			}

			if withToken {
				return runLoginWithToken(cmd, app, scope, expect)
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
			if expect != 0 && os.Getenv("BASECAMP_TOKEN") != "" {
				return errEnvTokenShadows("--expect-identity cannot be checked while BASECAMP_TOKEN is set")
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

			// With an expectation the login is assertive: the token is
			// checked before it is stored, and a mismatch stores nothing.
			// Without one the identity line stays informational.
			verifier := &loginVerifier{app: app, expectIdentity: expect, account: app.Config.AccountID, strict: expect != 0}
			result, err := app.Auth.Login(cmd.Context(), auth.LoginOptions{
				Scope:     scope,
				NoBrowser: noBrowser,
				Remote:    remote,
				Local:     local,
				LoginHint: loginHint,
				Logger:    func(msg string) { fmt.Fprintln(w, msg) },
				Verify:    verifier.verify,
			})
			if err != nil {
				return err
			}

			fmt.Fprintln(w)
			fmt.Fprintln(w, r.Success.Render("Authentication successful!"))

			if result.Scope != "" {
				fmt.Fprintln(w, r.Muted.Render(fmt.Sprintf("Access: %s", result.Scope)))
			}

			if who := verifier.who; who != nil {
				if who.PersonID != 0 {
					_ = app.Auth.SetUserIdentity(strconv.FormatInt(who.PersonID, 10), who.Email)
				}
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
	cmd.Flags().StringVar(&expectIdentity, "expect-identity", "", "Identity ID the login must authenticate as; otherwise store nothing")
	cmd.Flags().StringVar(&loginHint, "login-hint", "", "Email address to sign in as (device flow only; announced, not yet sent; ignored by Launchpad)")
	cmd.MarkFlagsMutuallyExclusive("remote", "local")
	cmd.MarkFlagsMutuallyExclusive("device-code", "local")
	for _, flag := range []string{"device-code", "remote", "local", "no-browser", "login-hint"} {
		cmd.MarkFlagsMutuallyExclusive("with-token", flag)
	}

	return cmd
}

// runLoginWithToken imports a personal access token from stdin as the
// active profile's credential. Everything that can be checked without the
// token is checked before stdin is read; the token is then verified through
// a client of its own — identity, and access to the profile's account —
// and only a token that passes is stored and its profile registered.
func runLoginWithToken(cmd *cobra.Command, app *appctx.App, scope string, expect int64) error {
	if scope == "" {
		scope = "full"
	}
	if scope != "read" && scope != "full" {
		return output.ErrUsage("Invalid scope. Use 'read' or 'full'")
	}
	if os.Getenv("BASECAMP_TOKEN") != "" {
		return errEnvTokenShadows("BASECAMP_TOKEN is set")
	}

	name := app.Config.ActiveProfile
	if name == "" {
		return output.ErrUsageHint("--with-token stores the token under a named profile",
			"Pass -P/--profile <name>; add --account <id> when the profile does not exist yet.")
	}
	if !isValidProfileName(name) {
		return output.ErrUsage(fmt.Sprintf("Invalid profile name %q: use only letters, numbers, hyphens, and underscores", name))
	}

	// The effective account and base URL are what every later command will
	// address under this profile, so they are what the token must be
	// verified for — and they must be the profile's own. A --account /
	// BASECAMP_ACCOUNT_ID / BASECAMP_BASE_URL override that disagrees with
	// the binding would verify the token for one place and store it for
	// another.
	account := app.Config.AccountID
	existing := app.Config.Profiles[name]
	var created *config.ProfileConfig
	bindAccount := false
	switch {
	case existing == nil && !accountGivenExplicitly(app):
		return output.ErrUsageHint(fmt.Sprintf("Profile %q does not exist", name),
			"Pass --account <id> to create it alongside the imported token.")
	case existing == nil:
		created = &config.ProfileConfig{BaseURL: app.Config.BaseURL, AccountID: account}
	case existing.BaseURL != "" && config.NormalizeBaseURL(existing.BaseURL) != config.NormalizeBaseURL(app.Config.BaseURL):
		return output.ErrUsageHint(fmt.Sprintf("Profile %q is bound to %s, not %s", name, existing.BaseURL, app.Config.BaseURL),
			"Import into a different profile, or drop the BASECAMP_BASE_URL override.")
	case existing.AccountID == "" && !accountGivenExplicitly(app):
		return output.ErrUsageHint(fmt.Sprintf("Profile %q has no account", name),
			"Pass --account <id> to bind it alongside the imported token.")
	case existing.AccountID == "" && !globalProfileIsUnbound(name):
		// Binding rewrites the global config file. The effective profile
		// is accountless, so if the global entry is missing or already
		// carries an account, the accountless one came from a system, repo
		// or local config and would keep shadowing whatever is written.
		return output.ErrUsageHint(fmt.Sprintf("Profile %q has no account and is not the global config's entry", name),
			"Add account_id to the config file that defines it, then rerun the import.")
	case existing.AccountID == "":
		bindAccount = true
	case !accountIDsEqual(account, existing.AccountID):
		return output.ErrUsageHint(fmt.Sprintf("Profile %q is bound to account %s, not %s", name, existing.AccountID, account),
			"Import into a different profile, or drop the --account / BASECAMP_ACCOUNT_ID override.")
	}
	if err := requireNumericAccount(account); err != nil {
		return err
	}

	token, err := readTokenFromStdin(cmd)
	if err != nil {
		return err
	}

	verifier := &loginVerifier{app: app, expectIdentity: expect, account: account, strict: true}
	if err := verifier.verify(cmd.Context(), token, "bc5"); err != nil {
		return err
	}
	who := verifier.who
	// The server's word on the token's scope beats the caller's declaration.
	if who.Scope != "" {
		scope = who.Scope
	}

	// The profile entry goes first: an entry without a credential is a
	// visible, harmless state (profile list shows it unauthenticated), where
	// a stored secret without an entry would be an orphan.
	isDefault := app.Config.DefaultProfile == name
	switch {
	case created != nil:
		created.Scope = scope
		if isDefault, err = registerProfile(name, created); err != nil {
			return err
		}
		if app.Config.Profiles == nil {
			app.Config.Profiles = make(map[string]*config.ProfileConfig)
		}
		app.Config.Profiles[name] = created
	case bindAccount:
		if err := bindProfileAccount(name, account); err != nil {
			return err
		}
		existing.AccountID = account
	}

	if err := app.Auth.ImportToken(token, scope, strconv.FormatInt(who.PersonID, 10), who.Email, who.ExpiresAt); err != nil {
		return fmt.Errorf("profile %q is registered but the token could not be stored (rerun the import): %w", name, err)
	}

	var expiresAt any
	tokenLine := "personal access token (does not expire)"
	if !who.ExpiresAt.IsZero() {
		expiresAt = who.ExpiresAt.UTC().Format(time.RFC3339)
		tokenLine = "expires " + who.ExpiresAt.Local().Format("2006-01-02")
	}

	data := map[string]any{
		"profile":         name,
		"account_id":      account,
		"base_url":        app.Config.BaseURL,
		"source":          "token",
		"oauth_type":      "bc5",
		"scope":           scope,
		"expires_at":      expiresAt,
		"identity":        map[string]any{"id": who.IdentityID, "email": who.IdentityEmail},
		"person":          map[string]any{"id": who.PersonID, "name": who.Name, "email": who.Email},
		"profile_created": created != nil,
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
	fmt.Fprintln(w, r.Muted.Render(fmt.Sprintf("Profile: %s · Account: %s · Access: %s · Token: %s", name, account, scope, tokenLine)))
	if created != nil {
		line := fmt.Sprintf("Created profile %q for account %s", name, account)
		if isDefault {
			line += " (default)"
		}
		fmt.Fprintln(w, r.Muted.Render(line))
	}
	return nil
}

// accountGivenExplicitly reports whether the effective account came from
// this invocation (--account or BASECAMP_ACCOUNT_ID) rather than a config
// file: a new bot profile must not inherit whatever account the operator's
// own configuration happens to name.
func accountGivenExplicitly(app *appctx.App) bool {
	src := config.Source(app.Config.Sources["account_id"])
	return app.Config.AccountID != "" && (src == config.SourceFlag || src == config.SourceEnv)
}

// requireNumericAccount mirrors App.RequireAccount for an account the login
// is about to bind a profile to: anything but ASCII digits would leave a
// profile every account-scoped command rejects.
func requireNumericAccount(account string) error {
	if account == "" {
		return output.ErrUsage("Account ID required. Set via --account flag or BASECAMP_ACCOUNT_ID env.")
	}
	for _, c := range account {
		if c < '0' || c > '9' {
			return output.ErrUsage(fmt.Sprintf("Invalid account ID %q: must contain only digits", account))
		}
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

	// A pipe delivers the token with one line ending (`op read`, `echo`, a
	// CRLF file on Windows); that one is stripped and nothing else is —
	// any other whitespace is not a token, and quietly trimming it would
	// hide a malformed secret rather than the secret store's exact value.
	token := strings.TrimSuffix(string(data), "\n")
	token = strings.TrimSuffix(token, "\r")
	if token == "" {
		return "", output.ErrUsageHint("No token on stdin",
			"Pipe it in from a secret store: `op read \"op://<vault>/<item>/credential\" | basecamp auth login --with-token -P <profile> --account <id>`.")
	}
	if strings.IndexFunc(token, func(c rune) bool { return unicode.IsSpace(c) || unicode.IsControl(c) }) >= 0 {
		return "", output.ErrUsage("Token on stdin must be a single line with no whitespace or control characters (one trailing line ending is allowed)")
	}
	return token, nil
}

// machineOutputFlagSet reports whether an explicit output flag asked for a
// machine format. The config-driven formats are deliberately excluded: a
// configured format=json must not lock a person out of an interactive login.
func machineOutputFlagSet(app *appctx.App) bool {
	return app.Flags.Agent || app.Flags.JSON || app.Flags.Quiet || app.Flags.IDsOnly || app.Flags.Count
}

// parseExpectIdentity parses --expect-identity: 0 when absent, the identity
// ID otherwise. Identity IDs are positive, so 0 is free to mean "none".
func parseExpectIdentity(raw string) (int64, error) {
	if raw == "" {
		return 0, nil
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, output.ErrUsage(fmt.Sprintf("Invalid --expect-identity %q: expected a numeric identity ID", raw))
	}
	return id, nil
}

// errEnvTokenShadows names the reason BASECAMP_TOKEN blocks a verified
// login: every request, the verification included, would carry the
// environment token instead of the credential being checked.
func errEnvTokenShadows(msg string) error {
	return output.ErrUsageHint(msg,
		"Every request, including the identity check, would use the environment token instead of the new credential. Unset it and retry.")
}

// loginIdentity is who a credential authenticates as: the account-independent
// identity from the authorization endpoint — what --expect-identity compares
// against — and the person within the verified account.
type loginIdentity struct {
	IdentityID    int64
	IdentityEmail string
	PersonID      int64
	Name          string
	Email         string
	Scope         string
	// ExpiresAt is the expiry the authorization document reported for the
	// credential; zero when it reported none.
	ExpiresAt time.Time
}

// label renders the identity for a one-line terminal sink. Name and email
// are server-supplied, so they are reduced to single lines first.
func (l *loginIdentity) label() string {
	label := richtext.SanitizeSingleLine(l.Name)
	if email := richtext.SanitizeSingleLine(l.Email); email != "" {
		label += " <" + email + ">"
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
	return strings.TrimSpace(label)
}

// loginVerifier proves a credential before it is stored. verify runs as
// LoginOptions.Verify (and directly for token imports) with a client bound
// to the candidate token, so nothing it learns comes from a stored
// credential or from BASECAMP_TOKEN.
//
// Strict mode is the assertive login: the authorization endpoint must
// answer with an identity, the effective account (when there is one) must
// be among the accounts the token can reach and its person record must
// resolve, and the identity must match any expectation. Non-strict mode
// keeps the informational "Logged in as" line best-effort: a lookup failure
// leaves who nil or without a person, and the login proceeds. A scope the
// CLI cannot store and a stated expectation are refused in either mode.
type loginVerifier struct {
	app            *appctx.App
	expectIdentity int64
	account        string
	strict         bool

	who *loginIdentity
}

func (v *loginVerifier) verify(ctx context.Context, accessToken, oauthType string) error {
	client := v.app.SDKClientFor(&basecamp.StaticTokenProvider{Token: accessToken})

	endpoint, err := v.app.Auth.AuthorizationEndpointFor(oauthType)
	if err != nil {
		return err
	}
	info, err := client.Authorization().GetInfo(ctx, &basecamp.GetInfoOptions{Endpoint: endpoint, FilterProduct: "bc3"})
	if err != nil {
		if !v.strict {
			return nil
		}
		return output.ErrAuth(fmt.Sprintf("Could not verify the new credential: %v", err))
	}

	who := &loginIdentity{
		IdentityID:    info.Identity.ID,
		IdentityEmail: info.Identity.EmailAddress,
		Email:         info.Identity.EmailAddress,
		Name:          strings.TrimSpace(info.Identity.FirstName + " " + info.Identity.LastName),
		Scope:         info.Scope,
	}
	if expiry, ok := info.Expiry(); ok {
		who.ExpiresAt = expiry
	}
	if who.Scope != "" && who.Scope != "read" && who.Scope != "full" {
		return output.ErrAuth(fmt.Sprintf("The server reports scope %q for this credential; only read or full can be stored", richtext.SanitizeSingleLine(who.Scope)))
	}
	if v.strict && who.IdentityID <= 0 {
		return output.ErrAuth("The authorization endpoint did not report an identity for the new credential; nothing was stored")
	}
	// An imported token has nothing to refresh with, so one already inside
	// the refresh window could not serve a single command once stored.
	if v.strict && !who.ExpiresAt.IsZero() && time.Until(who.ExpiresAt) <= auth.RefreshWindow {
		return output.ErrAuth(fmt.Sprintf("The credential expires at %s, within the %s the CLI keeps clear of expiry; nothing was stored — mint a fresh token", who.ExpiresAt.UTC().Format(time.RFC3339), auth.RefreshWindow))
	}
	if v.expectIdentity != 0 && who.IdentityID != v.expectIdentity {
		return output.ErrAuth(fmt.Sprintf("Authenticated as %s, not identity %d; nothing was stored", who.label(), v.expectIdentity))
	}

	// The person record is account-scoped (/{account}/my/profile.json), so
	// it doubles as the proof the token reaches the account it is about to
	// be used for. The authorization document is checked first: a missing
	// account there is a clearer answer than a 404 from the person lookup.
	if v.account != "" {
		if !authorizesAccount(info, v.account) {
			if !v.strict {
				v.who = who
				return nil
			}
			return output.ErrAuth(fmt.Sprintf("%s cannot access account %s (authorized: %s); nothing was stored", who.label(), v.account, authorizedAccountIDs(info)))
		}
		person, err := client.ForAccount(v.account).People().Me(ctx)
		if err != nil {
			if !v.strict {
				v.who = who
				return nil
			}
			return output.ErrAuth(fmt.Sprintf("Could not verify %s on account %s: %v", who.label(), v.account, err))
		}
		who.PersonID = person.ID
		who.Name = person.Name
		if person.EmailAddress != "" {
			who.Email = person.EmailAddress
		}
	}

	v.who = who
	return nil
}

// authorizesAccount reports whether the authorization document names the
// account as reachable (and not expired) by the credential.
func authorizesAccount(info *basecamp.AuthorizationInfo, account string) bool {
	for _, acct := range info.Accounts {
		if !acct.Expired && accountIDsEqual(strconv.FormatInt(acct.ID, 10), account) {
			return true
		}
	}
	return false
}

func authorizedAccountIDs(info *basecamp.AuthorizationInfo) string {
	ids := make([]string, 0, len(info.Accounts))
	for _, acct := range info.Accounts {
		if !acct.Expired {
			ids = append(ids, strconv.FormatInt(acct.ID, 10))
		}
	}
	if len(ids) == 0 {
		return "none"
	}
	return strings.Join(ids, ", ")
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
