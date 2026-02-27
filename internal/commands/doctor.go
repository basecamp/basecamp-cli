// Package commands implements the CLI commands.
package commands

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
	"github.com/zalando/go-keyring"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/config"
	"github.com/basecamp/basecamp-cli/internal/output"
	"github.com/basecamp/basecamp-cli/internal/version"
)

// Check represents a single diagnostic check result.
type Check struct {
	Name    string `json:"name"`
	Status  string `json:"status"` // "pass", "fail", "skip", "warn"
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`
}

// DoctorResult holds the complete diagnostic results.
type DoctorResult struct {
	Checks  []Check `json:"checks"`
	Passed  int     `json:"passed"`
	Failed  int     `json:"failed"`
	Warned  int     `json:"warned"`
	Skipped int     `json:"skipped"`
}

// Summary returns a human-readable summary of the results.
func (r *DoctorResult) Summary() string {
	if r.Failed == 0 && r.Warned == 0 && r.Passed > 0 {
		if r.Skipped > 0 {
			return fmt.Sprintf("All %d checks passed, %d skipped", r.Passed, r.Skipped)
		}
		return fmt.Sprintf("All %d checks passed", r.Passed)
	}
	parts := []string{}
	if r.Passed > 0 {
		parts = append(parts, fmt.Sprintf("%d passed", r.Passed))
	}
	if r.Failed > 0 {
		parts = append(parts, fmt.Sprintf("%d failed", r.Failed))
	}
	if r.Warned > 0 {
		parts = append(parts, fmt.Sprintf("%d %s", r.Warned, pluralize(r.Warned, "warning", "warnings")))
	}
	if r.Skipped > 0 {
		parts = append(parts, fmt.Sprintf("%d skipped", r.Skipped))
	}
	return strings.Join(parts, ", ")
}

// NewDoctorCmd creates the doctor command.
func NewDoctorCmd() *cobra.Command {
	var verbose bool

	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check CLI health and diagnose issues",
		Long: `Run diagnostic checks on authentication, configuration, and API connectivity.

The doctor command helps troubleshoot common issues by checking:
  - CLI version (and whether updates are available)
  - Configuration files (existence and validity)
  - Authentication credentials
  - Token validity and expiration
  - API connectivity
  - Cache directory health
  - Shell completion status

Examples:
  basecamp doctor              # Run all diagnostic checks
  basecamp doctor --json       # Output results as JSON
  basecamp doctor --verbose    # Show additional debug information`,
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if app == nil {
				return fmt.Errorf("app not initialized")
			}

			checks := runDoctorChecks(cmd.Context(), app, verbose)
			result := summarizeChecks(checks)

			// For styled/TTY output, render a human-friendly format
			if app.Output.EffectiveFormat() == output.FormatStyled {
				renderDoctorStyled(cmd.OutOrStdout(), result)
				return nil
			}

			// Build breadcrumbs based on failures
			breadcrumbs := buildDoctorBreadcrumbs(checks)

			opts := []output.ResponseOption{
				output.WithSummary(result.Summary()),
			}
			if len(breadcrumbs) > 0 {
				opts = append(opts, output.WithBreadcrumbs(breadcrumbs...))
			}

			return app.OK(result, opts...)
		},
	}

	cmd.Flags().BoolVar(&verbose, "verbose", false, "Show additional debug information")

	return cmd
}

// runDoctorChecks executes all diagnostic checks.
func runDoctorChecks(ctx context.Context, app *appctx.App, verbose bool) []Check {
	checks := []Check{}

	// 1. Version check
	checks = append(checks, checkVersion(verbose))

	// 2. SDK provenance
	checks = append(checks, checkSDKProvenance(verbose))

	// 3. Go runtime info (verbose only, always passes)
	if verbose {
		checks = append(checks, checkRuntime())
	}

	// 4. Config files check
	checks = append(checks, checkConfigFiles(app, verbose)...)

	// 5. Credentials check
	credCheck := checkCredentials(app, verbose)
	checks = append(checks, credCheck)

	// 6. Authentication check (only if credentials exist)
	var canTestAPI bool
	if credCheck.Status == "pass" || credCheck.Status == "warn" {
		authCheck := checkAuthentication(ctx, app, verbose)
		checks = append(checks, authCheck)
		canTestAPI = authCheck.Status == "pass" || authCheck.Status == "warn"
	} else {
		checks = append(checks, Check{
			Name:    "Authentication",
			Status:  "skip",
			Message: "Skipped (no credentials)",
			Hint:    "Run: basecamp auth login",
		})
	}

	// 7. API connectivity (only if authenticated)
	if canTestAPI {
		checks = append(checks, checkAPIConnectivity(ctx, app, verbose))
	} else {
		checks = append(checks, Check{
			Name:    "API Connectivity",
			Status:  "skip",
			Message: "Skipped (not authenticated)",
		})
	}

	// 8. Account access (only if API works)
	if canTestAPI && app.Config.AccountID != "" {
		checks = append(checks, checkAccountAccess(ctx, app, verbose))
	} else if app.Config.AccountID == "" {
		checks = append(checks, Check{
			Name:    "Account Access",
			Status:  "skip",
			Message: "Skipped (no account configured)",
			Hint:    "Set account_id in config or use --account flag",
		})
	} else {
		checks = append(checks, Check{
			Name:    "Account Access",
			Status:  "skip",
			Message: "Skipped (API not available)",
		})
	}

	// 9. Cache health
	checks = append(checks, checkCacheHealth(app, verbose))

	// 10. Shell completion
	checks = append(checks, checkShellCompletion(verbose))

	// 11. Legacy bcq detection
	if legacyCheck := checkLegacyInstall(); legacyCheck != nil {
		checks = append(checks, *legacyCheck)
	}

	return checks
}

// checkVersion checks the CLI version.
func checkVersion(verbose bool) Check {
	check := Check{
		Name:   "CLI Version",
		Status: "pass",
	}

	v := version.Version
	if v == "dev" {
		check.Message = "dev (built from source)"
		if verbose {
			check.Message += fmt.Sprintf(" [commit: %s, date: %s]", version.Commit, version.Date)
		}
		return check
	}

	check.Message = v

	// Try to check for latest version (non-blocking, best effort)
	latest, err := fetchLatestVersion()
	if err == nil && latest != "" && latest != v {
		check.Status = "warn"
		check.Message = fmt.Sprintf("%s (update available: %s)", v, latest)
		check.Hint = "Run: basecamp upgrade"
	}

	return check
}

// checkSDKProvenance reports the embedded SDK version and revision.
func checkSDKProvenance(verbose bool) Check {
	return formatSDKProvenance(version.GetSDKProvenance(), verbose)
}

// formatSDKProvenance formats SDK provenance into a doctor check result.
func formatSDKProvenance(p *version.SDKProvenance, verbose bool) Check {
	check := Check{
		Name:   "SDK",
		Status: "pass",
	}

	if p == nil {
		check.Status = "warn"
		check.Message = "Provenance data unavailable"
		return check
	}

	if p.SDK.Version == "" {
		check.Status = "warn"
		check.Message = "Provenance data incomplete (missing version)"
		return check
	}

	if verbose {
		parts := []string{p.SDK.Version}

		metaParts := []string{}
		if p.SDK.Revision != "" {
			metaParts = append(metaParts, fmt.Sprintf("revision: %s", p.SDK.Revision))
		}
		if p.SDK.UpdatedAt != "" {
			// Show just the date portion
			date := p.SDK.UpdatedAt
			if len(date) >= 10 {
				date = date[:10]
			}
			metaParts = append(metaParts, fmt.Sprintf("updated: %s", date))
		}

		if len(metaParts) > 0 {
			parts = append(parts, fmt.Sprintf("[%s]", strings.Join(metaParts, ", ")))
		}
		check.Message = strings.Join(parts, " ")
	} else {
		if p.SDK.Revision != "" {
			check.Message = fmt.Sprintf("%s (%s)", p.SDK.Version, p.SDK.Revision)
		} else {
			check.Message = p.SDK.Version
		}
	}

	return check
}

// fetchLatestVersion attempts to fetch the latest release version from GitHub.
func fetchLatestVersion() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", "https://api.github.com/repos/basecamp/basecamp-cli/releases/latest", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}

	var release struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return "", err
	}

	// Strip "v" prefix if present
	return strings.TrimPrefix(release.TagName, "v"), nil
}

// checkRuntime returns Go runtime information.
func checkRuntime() Check {
	return Check{
		Name:    "Runtime",
		Status:  "pass",
		Message: fmt.Sprintf("Go %s (%s/%s)", runtime.Version(), runtime.GOOS, runtime.GOARCH),
	}
}

// checkConfigFiles checks for configuration file existence and validity.
func checkConfigFiles(app *appctx.App, verbose bool) []Check {
	checks := []Check{}

	// Global config
	globalPath := config.GlobalConfigDir()
	configPath := filepath.Join(globalPath, "config.json")

	if _, err := os.Stat(configPath); err == nil {
		// File exists, try to parse it
		data, readErr := os.ReadFile(configPath)
		if readErr != nil {
			checks = append(checks, Check{
				Name:    "Global Config",
				Status:  "fail",
				Message: fmt.Sprintf("Cannot read: %s", configPath),
				Hint:    fmt.Sprintf("Check file permissions: %v", readErr),
			})
		} else {
			var cfg map[string]any
			if jsonErr := json.Unmarshal(data, &cfg); jsonErr != nil {
				checks = append(checks, Check{
					Name:    "Global Config",
					Status:  "fail",
					Message: fmt.Sprintf("Invalid JSON: %s", configPath),
					Hint:    fmt.Sprintf("JSON error: %v", jsonErr),
				})
			} else {
				msg := configPath
				if verbose {
					msg = fmt.Sprintf("%s (%d keys)", configPath, len(cfg))
				}
				checks = append(checks, Check{
					Name:    "Global Config",
					Status:  "pass",
					Message: msg,
				})
			}
		}
	} else {
		checks = append(checks, Check{
			Name:    "Global Config",
			Status:  "warn",
			Message: "Not found (using defaults)",
			Hint:    fmt.Sprintf("Create %s to persist settings", configPath),
		})
	}

	// Repo config (at git root)
	repoConfigPath := findRepoConfig()
	if repoConfigPath != "" {
		check := validateConfigFile(repoConfigPath, "Repo Config", verbose)
		checks = append(checks, check)
	} else if verbose {
		checks = append(checks, Check{
			Name:    "Repo Config",
			Status:  "skip",
			Message: "Not found",
			Hint:    "Create .basecamp/config.json at repo root for team settings",
		})
	}

	// Local config (in current directory or parents, excluding repo config)
	localConfigPath := findLocalConfig(repoConfigPath)
	if localConfigPath != "" {
		check := validateConfigFile(localConfigPath, "Local Config", verbose)
		checks = append(checks, check)
	} else if verbose {
		checks = append(checks, Check{
			Name:    "Local Config",
			Status:  "skip",
			Message: "Not found",
			Hint:    "Create .basecamp/config.json for directory-specific settings",
		})
	}

	// Show effective config values in verbose mode
	if verbose && app.Config != nil {
		details := []string{}
		if app.Config.BaseURL != "" {
			src := app.Config.Sources["base_url"]
			if src == "" {
				src = "default"
			}
			details = append(details, fmt.Sprintf("base_url=%s [%s]", app.Config.BaseURL, src))
		}
		if app.Config.AccountID != "" {
			src := app.Config.Sources["account_id"]
			if src == "" {
				src = "default"
			}
			details = append(details, fmt.Sprintf("account_id=%s [%s]", app.Config.AccountID, src))
		}
		if app.Config.ProjectID != "" {
			src := app.Config.Sources["project_id"]
			if src == "" {
				src = "default"
			}
			details = append(details, fmt.Sprintf("project_id=%s [%s]", app.Config.ProjectID, src))
		}
		if len(details) > 0 {
			checks = append(checks, Check{
				Name:    "Effective Config",
				Status:  "pass",
				Message: strings.Join(details, ", "),
			})
		}
	}

	return checks
}

// findRepoConfig looks for .basecamp/config.json at the git root.
func findRepoConfig() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}

	for {
		gitPath := filepath.Join(dir, ".git")
		if _, err := os.Stat(gitPath); err == nil {
			cfgPath := filepath.Join(dir, ".basecamp", "config.json")
			if _, err := os.Stat(cfgPath); err == nil {
				return cfgPath
			}
			return ""
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// findLocalConfig looks for .basecamp/config.json in current directory or parents.
func findLocalConfig(excludePath string) string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}

	for {
		cfgPath := filepath.Join(dir, ".basecamp", "config.json")
		if _, err := os.Stat(cfgPath); err == nil && cfgPath != excludePath {
			return cfgPath
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// validateConfigFile checks if a config file is valid JSON.
func validateConfigFile(path, name string, verbose bool) Check {
	data, err := os.ReadFile(path)
	if err != nil {
		return Check{
			Name:    name,
			Status:  "fail",
			Message: fmt.Sprintf("Cannot read: %s", path),
			Hint:    fmt.Sprintf("Check file permissions: %v", err),
		}
	}

	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Check{
			Name:    name,
			Status:  "fail",
			Message: fmt.Sprintf("Invalid JSON: %s", path),
			Hint:    fmt.Sprintf("JSON error: %v", err),
		}
	}

	msg := path
	if verbose {
		msg = fmt.Sprintf("%s (%d keys)", path, len(cfg))
	}
	return Check{
		Name:    name,
		Status:  "pass",
		Message: msg,
	}
}

// checkCredentials checks for stored credentials.
func checkCredentials(app *appctx.App, verbose bool) Check {
	check := Check{
		Name: "Credentials",
	}

	// Check for BASECAMP_TOKEN env var first
	if envToken := os.Getenv("BASECAMP_TOKEN"); envToken != "" {
		check.Status = "pass"
		check.Message = "Using BASECAMP_TOKEN environment variable"
		return check
	}

	// Check if authenticated (works for both keyring and file storage)
	if !app.Auth.IsAuthenticated() {
		check.Status = "fail"
		check.Message = "No credentials found"
		check.Hint = "Run: basecamp auth login"
		return check
	}

	// Try to load credentials for details
	credKey := app.Auth.CredentialKey()
	store := app.Auth.GetStore()
	creds, err := store.Load(credKey)
	if err != nil {
		// Authenticated but can't load details - still pass but note the issue
		check.Status = "pass"
		check.Message = "Stored (via system keyring)"
		return check
	}

	check.Status = "pass"
	if store.UsingKeyring() {
		if verbose {
			check.Message = fmt.Sprintf("Stored in keyring (scope: %s, type: %s)", creds.Scope, creds.OAuthType)
		} else {
			check.Message = "Stored in system keyring"
		}
	} else {
		credsPath := filepath.Join(config.GlobalConfigDir(), "credentials.json")
		if verbose {
			check.Message = fmt.Sprintf("%s (scope: %s, type: %s)", credsPath, creds.Scope, creds.OAuthType)
		} else {
			check.Message = credsPath
		}
	}
	return check
}

// checkAuthentication checks token validity.
func checkAuthentication(ctx context.Context, app *appctx.App, verbose bool) Check {
	check := Check{
		Name: "Authentication",
	}

	// Using env token - can't check expiration
	if envToken := os.Getenv("BASECAMP_TOKEN"); envToken != "" {
		check.Status = "pass"
		check.Message = "Valid (via BASECAMP_TOKEN)"
		return check
	}

	// Load credentials to check expiration
	credKey := app.Auth.CredentialKey()
	store := app.Auth.GetStore()
	creds, err := store.Load(credKey)
	if err != nil {
		check.Status = "fail"
		check.Message = "Cannot load credentials"
		check.Hint = "Run: basecamp auth login"
		return check
	}

	// Check token expiration
	if creds.ExpiresAt > 0 {
		expiresIn := time.Until(time.Unix(creds.ExpiresAt, 0))

		if expiresIn < 0 {
			// Token expired - try to refresh
			if err := app.Auth.Refresh(ctx); err != nil {
				check.Status = "fail"
				check.Message = "Token expired and refresh failed"
				check.Hint = "Run: basecamp auth login"
				return check
			}
			check.Status = "pass"
			check.Message = "Valid (auto-refreshed)"
			return check
		}

		if expiresIn < 5*time.Minute {
			check.Status = "warn"
			check.Message = fmt.Sprintf("Token expires in %s", expiresIn.Round(time.Second))
			check.Hint = "Token will auto-refresh on next API call"
			return check
		}

		check.Status = "pass"
		msg := "Valid"
		if verbose {
			msg = fmt.Sprintf("Valid (expires in %s)", expiresIn.Round(time.Minute))
		}
		check.Message = msg
		return check
	}

	// No expiration info
	check.Status = "pass"
	check.Message = "Valid"
	return check
}

// checkAPIConnectivity tests API connectivity via the authorization endpoint.
func checkAPIConnectivity(ctx context.Context, app *appctx.App, verbose bool) Check {
	check := Check{
		Name: "API Connectivity",
	}

	start := time.Now()
	_, err := app.SDK.Authorization().GetInfo(ctx, nil)
	latency := time.Since(start)

	if err != nil {
		check.Status = "fail"
		check.Message = "Cannot connect to Basecamp API"
		check.Hint = fmt.Sprintf("Error: %v", err)
		return check
	}

	check.Status = "pass"
	if verbose {
		check.Message = fmt.Sprintf("Basecamp API reachable (%dms)", latency.Milliseconds())
	} else {
		check.Message = "Basecamp API reachable"
	}
	return check
}

// checkAccountAccess verifies access to the configured account.
func checkAccountAccess(ctx context.Context, app *appctx.App, verbose bool) Check {
	check := Check{
		Name: "Account Access",
	}

	// Validate account ID before calling Account() which panics on non-numeric IDs
	if err := app.RequireAccount(); err != nil {
		check.Status = "fail"
		check.Message = "Invalid account configuration"
		check.Hint = err.Error()
		return check
	}

	// Try to list projects (simple account-scoped operation)
	start := time.Now()
	result, err := app.Account().Projects().List(ctx, nil)
	latency := time.Since(start)

	if err != nil {
		check.Status = "fail"
		check.Message = fmt.Sprintf("Cannot access account %s", app.Config.AccountID)
		check.Hint = fmt.Sprintf("Error: %v", err)
		return check
	}

	check.Status = "pass"
	msg := fmt.Sprintf("Account %s accessible", app.Config.AccountID)
	if verbose {
		msg = fmt.Sprintf("Account %s accessible (%d projects, %dms)", app.Config.AccountID, len(result.Projects), latency.Milliseconds())
	}
	check.Message = msg
	return check
}

// checkCacheHealth checks the cache directory.
func checkCacheHealth(app *appctx.App, verbose bool) Check {
	check := Check{
		Name: "Cache",
	}

	cacheDir := app.Config.CacheDir
	if cacheDir == "" {
		check.Status = "warn"
		check.Message = "Cache directory not configured"
		return check
	}

	if !app.Config.CacheEnabled {
		check.Status = "pass"
		check.Message = "Disabled"
		return check
	}

	info, err := os.Stat(cacheDir)
	if err != nil {
		if os.IsNotExist(err) {
			check.Status = "pass"
			check.Message = fmt.Sprintf("%s (will be created on first use)", cacheDir)
			return check
		}
		check.Status = "warn"
		check.Message = fmt.Sprintf("Cannot access: %s", cacheDir)
		check.Hint = fmt.Sprintf("Error: %v", err)
		return check
	}

	if !info.IsDir() {
		check.Status = "fail"
		check.Message = fmt.Sprintf("%s exists but is not a directory", cacheDir)
		return check
	}

	// Count cache entries and size
	var totalSize int64
	var entryCount int
	_ = filepath.WalkDir(cacheDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // Best-effort counting, continue on errors
		}
		if !d.IsDir() {
			if info, err := d.Info(); err == nil {
				totalSize += info.Size()
			}
			entryCount++
		}
		return nil
	})

	check.Status = "pass"
	msg := cacheDir
	if verbose || entryCount > 0 {
		sizeMB := float64(totalSize) / (1024 * 1024)
		msg = fmt.Sprintf("%s (%.1f MB, %d entries)", cacheDir, sizeMB, entryCount)
	}
	check.Message = msg
	return check
}

// checkShellCompletion checks if shell completion is installed.
func checkShellCompletion(verbose bool) Check {
	check := Check{
		Name: "Shell Completion",
	}

	shell := detectShell()
	if shell == "" {
		check.Status = "skip"
		check.Message = "Could not detect shell"
		return check
	}

	// Check if completion is likely installed based on shell
	var completionInstalled bool
	var completionPath string

	switch shell {
	case "bash":
		// Check common bash completion paths
		paths := []string{
			"/opt/homebrew/etc/bash_completion.d/basecamp",
			"/usr/local/etc/bash_completion.d/basecamp",
			"/etc/bash_completion.d/basecamp",
			filepath.Join(os.Getenv("HOME"), ".local/share/bash-completion/completions/basecamp"),
		}
		for _, p := range paths {
			if _, err := os.Stat(p); err == nil {
				completionInstalled = true
				completionPath = p
				break
			}
		}
	case "zsh":
		// Check common zsh completion paths
		paths := []string{
			"/opt/homebrew/share/zsh/site-functions/_basecamp",
			"/usr/local/share/zsh/site-functions/_basecamp",
			filepath.Join(os.Getenv("HOME"), ".zsh/completions/_basecamp"),
		}
		for _, p := range paths {
			if _, err := os.Stat(p); err == nil {
				completionInstalled = true
				completionPath = p
				break
			}
		}
		// Check for eval-based installation in .zshrc
		if !completionInstalled {
			if zshrcHasCompletionEval() {
				completionInstalled = true
				completionPath = "~/.zshrc (via eval)"
			}
		}
	case "fish":
		completionPath = filepath.Join(os.Getenv("HOME"), ".config/fish/completions/basecamp.fish")
		if _, err := os.Stat(completionPath); err == nil {
			completionInstalled = true
		}
	}

	if completionInstalled {
		check.Status = "pass"
		msg := fmt.Sprintf("%s (installed)", shell)
		if verbose && completionPath != "" {
			msg = fmt.Sprintf("%s (%s)", shell, completionPath)
		}
		check.Message = msg
	} else {
		check.Status = "warn"
		check.Message = fmt.Sprintf("%s (not installed)", shell)
		check.Hint = fmt.Sprintf("Run: basecamp completion %s --help", shell)
	}

	return check
}

// detectShell returns the user's shell from $SHELL env var.
func detectShell() string {
	shell := os.Getenv("SHELL")
	if shell == "" {
		return ""
	}
	base := filepath.Base(shell)
	switch base {
	case "bash", "zsh", "fish":
		return base
	}
	return ""
}

// zshrcHasCompletionEval checks if ~/.zshrc contains an eval-based
// basecamp completion install (e.g., eval "$(basecamp completion zsh)").
func zshrcHasCompletionEval() bool {
	home := os.Getenv("HOME")
	if home == "" {
		return false
	}
	f, err := os.Open(filepath.Join(home, ".zshrc")) //nolint:gosec // G304: trusted path
	if err != nil {
		return false
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, "basecamp completion zsh") {
			return true
		}
	}
	return false
}

// summarizeChecks counts results by status.
func summarizeChecks(checks []Check) *DoctorResult {
	result := &DoctorResult{Checks: checks}
	for _, c := range checks {
		switch c.Status {
		case "pass":
			result.Passed++
		case "fail":
			result.Failed++
		case "warn":
			result.Warned++
		case "skip":
			result.Skipped++
		}
	}
	return result
}

// buildDoctorBreadcrumbs creates helpful next-step suggestions based on failures.
func buildDoctorBreadcrumbs(checks []Check) []output.Breadcrumb {
	var breadcrumbs []output.Breadcrumb

	for _, c := range checks {
		if c.Status != "fail" {
			continue
		}

		switch c.Name {
		case "Credentials", "Authentication":
			breadcrumbs = append(breadcrumbs, output.Breadcrumb{
				Action:      "login",
				Cmd:         "basecamp auth login",
				Description: "Authenticate with Basecamp",
			})
		case "API Connectivity":
			breadcrumbs = append(breadcrumbs, output.Breadcrumb{
				Action:      "status",
				Cmd:         "basecamp auth status",
				Description: "Check authentication status",
			})
		case "Account Access":
			breadcrumbs = append(breadcrumbs, output.Breadcrumb{
				Action:      "config",
				Cmd:         "basecamp config show",
				Description: "Review configuration",
			})
		}
	}

	// Deduplicate breadcrumbs
	seen := make(map[string]bool)
	unique := []output.Breadcrumb{}
	for _, b := range breadcrumbs {
		if !seen[b.Cmd] {
			seen[b.Cmd] = true
			unique = append(unique, b)
		}
	}

	return unique
}

// pluralize returns singular or plural form based on count.
func pluralize(n int, singular, plural string) string {
	if n == 1 {
		return singular
	}
	return plural
}

// renderDoctorStyled outputs a human-friendly styled format for TTY.
func renderDoctorStyled(w io.Writer, result *DoctorResult) {
	r := output.NewRenderer(w, false)

	// Status icon styles
	iconPass := r.Success
	iconFail := r.Error
	iconWarn := r.Warning
	iconSkip := r.Muted

	nameStyle := lipgloss.NewStyle().Bold(true)
	hintStyle := r.Hint

	statusIcon := map[string]func(string) string{
		"pass": func(_ string) string { return iconPass.Render("✓") },
		"fail": func(_ string) string { return iconFail.Render("✗") },
		"warn": func(_ string) string { return iconWarn.Render("!") },
		"skip": func(_ string) string { return iconSkip.Render("○") },
	}

	statusMsg := map[string]lipgloss.Style{
		"pass": r.Success,
		"fail": r.Error,
		"warn": r.Warning,
		"skip": r.Muted,
	}

	fmt.Fprintln(w)
	fmt.Fprintln(w, r.Summary.Render("Basecamp CLI Doctor"))
	fmt.Fprintln(w)

	for _, check := range result.Checks {
		icon := statusIcon[check.Status]("")
		msgStyle := statusMsg[check.Status]

		fmt.Fprintf(w, "  %s %s %s\n",
			icon,
			nameStyle.Render(check.Name),
			msgStyle.Render(check.Message),
		)

		if check.Hint != "" && (check.Status == "fail" || check.Status == "warn") {
			fmt.Fprintf(w, "      %s\n", hintStyle.Render("↳ "+check.Hint))
		}
	}

	fmt.Fprintln(w)

	var summaryParts []string
	if result.Passed > 0 {
		summaryParts = append(summaryParts, r.Success.Render(fmt.Sprintf("%d passed", result.Passed)))
	}
	if result.Failed > 0 {
		summaryParts = append(summaryParts, r.Error.Render(fmt.Sprintf("%d failed", result.Failed)))
	}
	if result.Warned > 0 {
		summaryParts = append(summaryParts, r.Warning.Render(fmt.Sprintf("%d %s", result.Warned, pluralize(result.Warned, "warning", "warnings"))))
	}
	if result.Skipped > 0 {
		summaryParts = append(summaryParts, r.Muted.Render(fmt.Sprintf("%d skipped", result.Skipped)))
	}

	fmt.Fprintf(w, "  %s\n", strings.Join(summaryParts, "  "))
	fmt.Fprintln(w)
}

// checkLegacyInstall detects stale bcq artifacts and suggests migration.
// Returns nil if no legacy artifacts are found (to avoid noisy output).
func checkLegacyInstall() *Check {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}

	// Check marker first — if already migrated, skip
	configBase := os.Getenv("XDG_CONFIG_HOME")
	if configBase == "" {
		configBase = filepath.Join(home, ".config")
	}
	markerPath := filepath.Join(configBase, "basecamp", ".migrated")
	if _, err := os.Stat(markerPath); err == nil {
		return nil // Already migrated
	}

	cacheBase := os.Getenv("XDG_CACHE_HOME")
	if cacheBase == "" {
		cacheBase = filepath.Join(home, ".cache")
	}

	var found []string

	// Check for legacy cache dir
	legacyCache := filepath.Join(cacheBase, "bcq")
	if info, err := os.Stat(legacyCache); err == nil && info.IsDir() {
		found = append(found, legacyCache)
	}

	// Check for legacy theme dir
	legacyTheme := filepath.Join(configBase, "bcq", "theme")
	if info, err := os.Stat(legacyTheme); err == nil && info.IsDir() {
		found = append(found, legacyTheme)
	}

	// Check for legacy keyring entries (best-effort, probe all known origins)
	// Skip when BASECAMP_NO_KEYRING is set (headless/CI environments)
	if os.Getenv("BASECAMP_NO_KEYRING") == "" {
		configDir := filepath.Join(configBase, "basecamp")
		for _, origin := range collectKnownOrigins(configDir) {
			legacyKey := fmt.Sprintf("bcq::%s", origin)
			if _, err := keyring.Get("bcq", legacyKey); err == nil {
				found = append(found, "keyring(bcq::*)")
				break
			}
		}
	}

	if len(found) == 0 {
		return nil
	}

	return &Check{
		Name:    "Legacy Install",
		Status:  "warn",
		Message: fmt.Sprintf("Found legacy bcq data: %s", strings.Join(found, ", ")),
		Hint:    "Run: basecamp migrate",
	}
}
