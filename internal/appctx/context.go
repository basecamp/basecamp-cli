// Package appctx provides application context helpers.
package appctx

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/bcq/internal/auth"
	"github.com/basecamp/bcq/internal/config"
	"github.com/basecamp/bcq/internal/names"
	"github.com/basecamp/bcq/internal/observability"
	"github.com/basecamp/bcq/internal/output"
	"github.com/basecamp/bcq/internal/resilience"
	"github.com/basecamp/bcq/internal/tui/resolve"
)

// contextKey is a private type for context keys.
type contextKey string

const appKey contextKey = "app"

// App holds the shared application context for all commands.
type App struct {
	Config *config.Config
	Auth   *auth.Manager
	SDK    *basecamp.Client
	Names  *names.Resolver
	Output *output.Writer

	// Observability
	Collector *observability.SessionCollector
	Hooks     *observability.CLIHooks

	// Flags holds the global flag values
	Flags GlobalFlags
}

// GlobalFlags holds values for global CLI flags.
type GlobalFlags struct {
	// Output format flags
	JSON    bool
	Quiet   bool
	MD      bool // Literal Markdown syntax output
	Styled  bool // Force ANSI styled output (even when piped)
	IDsOnly bool
	Count   bool
	Agent   bool

	// Context flags
	Project  string
	Account  string
	Todolist string
	Host     string // Basecamp host with smart protocol detection

	// Behavior flags
	Verbose  int // 0=off, 1=operations, 2=operations+requests (stacks with -v -v or -vv)
	Stats    bool
	CacheDir string
}

// authAdapter wraps auth.Manager to implement basecamp.TokenProvider.
type authAdapter struct {
	mgr *auth.Manager
}

func (a *authAdapter) AccessToken(ctx context.Context) (string, error) {
	return a.mgr.AccessToken(ctx)
}

// NewApp creates a new App with the given configuration.
func NewApp(cfg *config.Config) *App {
	// Create HTTP client for auth manager (OAuth discovery, token refresh)
	httpClient := &http.Client{Timeout: 30 * time.Second}
	authMgr := auth.NewManager(cfg, httpClient)

	// Create observability components
	// Collector always runs to gather stats; hooks control output verbosity
	// Level 0 initially; ApplyFlags sets the actual level from -v flags
	collector := observability.NewSessionCollector()
	traceWriter := observability.NewTraceWriter()
	cliHooks := observability.NewCLIHooks(0, collector, traceWriter)

	// Create resilience components for cross-process state coordination
	// State is stored in <cacheDir>/resilience/state.json
	// If CacheDir is empty, NewStore uses the default (~/.cache/bcq/resilience/)
	var resilienceDir string
	if cfg.CacheDir != "" {
		resilienceDir = filepath.Join(cfg.CacheDir, resilience.DefaultDirName)
	}
	resilienceStore := resilience.NewStore(resilienceDir)
	resilienceCfg := resilience.DefaultConfig()
	gatingHooks := resilience.NewGatingHooksFromConfig(resilienceStore, resilienceCfg)

	// Chain hooks: gating hooks first (to gate requests), then CLI hooks (for observability)
	// Note: resilience.GatingHooks implements basecamp.GatingHooks, while CLIHooks implements basecamp.Hooks
	hooks := basecamp.NewChainHooks(gatingHooks, cliHooks)

	// Create SDK client with auth adapter and chained hooks
	// Note: AccountID is NOT set here - use app.Account() for account-scoped operations
	sdkCfg := &basecamp.Config{
		BaseURL:      cfg.BaseURL,
		ProjectID:    cfg.ProjectID,
		TodolistID:   cfg.TodolistID,
		CacheDir:     cfg.CacheDir,
		CacheEnabled: cfg.CacheEnabled,
	}
	sdkClient := basecamp.NewClient(sdkCfg, &authAdapter{mgr: authMgr}, basecamp.WithHooks(hooks))

	// Create name resolver using SDK client and account ID
	nameResolver := names.NewResolver(sdkClient, authMgr, cfg.AccountID)

	// Determine output format from config (default to auto)
	format := output.FormatAuto
	switch cfg.Format {
	case "json":
		format = output.FormatJSON
	case "markdown", "md":
		format = output.FormatMarkdown
	case "quiet":
		format = output.FormatQuiet
	}

	return &App{
		Config:    cfg,
		Auth:      authMgr,
		SDK:       sdkClient,
		Names:     nameResolver,
		Collector: collector,
		Hooks:     cliHooks,
		Output: output.New(output.Options{
			Format: format,
			Writer: os.Stdout,
		}),
	}
}

// ApplyFlags applies global flag values to the app configuration.
func (a *App) ApplyFlags() {
	// Apply output format from flags (order matters: specific modes first)
	if a.Flags.Agent {
		// Agent mode = quiet JSON (data only, no envelope)
		a.Output = output.New(output.Options{
			Format: output.FormatQuiet,
			Writer: os.Stdout,
		})
	} else if a.Flags.IDsOnly {
		a.Output = output.New(output.Options{
			Format: output.FormatIDs,
			Writer: os.Stdout,
		})
	} else if a.Flags.Count {
		a.Output = output.New(output.Options{
			Format: output.FormatCount,
			Writer: os.Stdout,
		})
	} else if a.Flags.Quiet {
		a.Output = output.New(output.Options{
			Format: output.FormatQuiet,
			Writer: os.Stdout,
		})
	} else if a.Flags.JSON {
		a.Output = output.New(output.Options{
			Format: output.FormatJSON,
			Writer: os.Stdout,
		})
	} else if a.Flags.Styled {
		// Force ANSI styled output (even when piped)
		a.Output = output.New(output.Options{
			Format: output.FormatStyled,
			Writer: os.Stdout,
		})
	} else if a.Flags.MD {
		// Literal Markdown syntax (portable, pipeable to glow/bat)
		a.Output = output.New(output.Options{
			Format: output.FormatMarkdown,
			Writer: os.Stdout,
		})
	}

	// Determine verbosity level from flags and BCQ_DEBUG env var
	verboseLevel := a.Flags.Verbose
	if debugEnv := os.Getenv("BCQ_DEBUG"); debugEnv != "" {
		// BCQ_DEBUG can be "1", "2", or "true" (treated as 2 for full debug)
		if level, err := strconv.Atoi(debugEnv); err == nil {
			if level > verboseLevel {
				verboseLevel = level
			}
		} else if debugEnv == "true" {
			verboseLevel = 2 // Full debug output
		}
	}

	// Apply verbose level to hooks for trace output
	if a.Hooks != nil {
		a.Hooks.SetLevel(verboseLevel)
	}
}

// OK outputs a success response, automatically including stats if --stats flag is set.
func (a *App) OK(data any, opts ...output.ResponseOption) error {
	if a.Flags.Stats && a.Collector != nil {
		stats := a.Collector.Summary()
		opts = append(opts, output.WithStats(&stats))
	}
	return a.Output.OK(data, opts...)
}

// Err outputs an error response, printing stats to stderr if --stats flag is set.
func (a *App) Err(err error) error {
	// Print the error response first
	if outputErr := a.Output.Err(err); outputErr != nil {
		return outputErr
	}

	// Print stats to stderr if enabled, but not in machine-consumable modes
	// (agent, quiet, ids-only, count are meant for programmatic consumption)
	if a.Flags.Stats && a.Collector != nil && !a.isMachineOutput() {
		stats := a.Collector.Summary()
		a.printStatsToStderr(&stats)
	}
	return nil
}

// isMachineOutput returns true if the output mode is intended for programmatic consumption.
// Checks both flags and config-driven format settings.
func (a *App) isMachineOutput() bool {
	// Flag-driven machine output modes
	if a.Flags.Agent || a.Flags.Quiet || a.Flags.IDsOnly || a.Flags.Count {
		return true
	}
	// Config-driven quiet mode (format: "quiet" in config file)
	if a.Config != nil && a.Config.Format == "quiet" {
		return true
	}
	return false
}

// printStatsToStderr outputs a compact stats line to stderr.
func (a *App) printStatsToStderr(stats *observability.SessionMetrics) {
	if stats == nil {
		return
	}

	parts := stats.FormatParts()
	if len(parts) > 0 {
		fmt.Fprintf(os.Stderr, "\nStats: %s\n", strings.Join(parts, " | "))
	}
}

// IsInteractive returns true if the terminal supports interactive TUI.
func (a *App) IsInteractive() bool {
	// Not interactive if any non-interactive output mode is set
	if a.Flags.Agent || a.Flags.JSON || a.Flags.Quiet || a.Flags.IDsOnly || a.Flags.Count {
		return false
	}

	// Check if stdout is a terminal
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}

	return (fi.Mode() & os.ModeCharDevice) != 0
}

// WithApp stores the app in the context.
func WithApp(ctx context.Context, app *App) context.Context {
	return context.WithValue(ctx, appKey, app)
}

// FromContext retrieves the app from the context.
func FromContext(ctx context.Context) *App {
	app, _ := ctx.Value(appKey).(*App)
	return app
}

// Account returns an account-scoped client for the configured account.
// This is the preferred way to access the SDK for operations that require
// an account context (projects, todos, people, etc.).
//
// Call RequireAccount() first to validate the account is properly configured.
// ForAccount() will panic if accountID is empty or non-numeric.
//
// For account-agnostic operations (like Authorization().GetInfo()),
// use app.SDK directly.
func (a *App) Account() *basecamp.AccountClient {
	return a.SDK.ForAccount(a.Config.AccountID)
}

// RequireAccount validates that an account is configured and is a valid numeric ID.
// Returns an error if no account ID is set or if it contains non-digit characters.
// Commands that perform account-scoped operations should call this
// before using Account().
//
// Note: ForAccount() panics on invalid account IDs, so this validation must
// match exactly - only ASCII digits 0-9 are allowed (no signs, spaces, etc.).
func (a *App) RequireAccount() error {
	if a.Config == nil || a.Config.AccountID == "" {
		return output.ErrUsage("Account ID required. Set via --account flag, BCQ_ACCOUNT env, or config file.")
	}

	// Validate that account ID contains only digits (matches ForAccount requirements)
	for _, c := range a.Config.AccountID {
		if c < '0' || c > '9' {
			return output.ErrUsage(fmt.Sprintf("Invalid account ID %q: must contain only digits", a.Config.AccountID))
		}
	}

	return nil
}

// Resolve returns a Resolver for interactive prompts when CLI options are missing.
// The resolver uses the app's SDK, auth manager, and config to fetch available
// options and prompt the user to select interactively.
//
// Usage:
//
//	accountID, err := app.Resolve().Account(ctx)
//	if err != nil {
//	    return err
//	}
func (a *App) Resolve() *resolve.Resolver {
	return resolve.New(
		a.SDK,
		a.Auth,
		a.Config,
		resolve.WithFlags(&resolve.Flags{
			Account:  a.Flags.Account,
			Project:  a.Flags.Project,
			Todolist: a.Flags.Todolist,
			Host:     a.Flags.Host,
		}),
	)
}
