// Package appctx provides application context helpers.
package appctx

import (
	"context"
	"log/slog"
	"os"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/bcq/internal/auth"
	"github.com/basecamp/bcq/internal/config"
	"github.com/basecamp/bcq/internal/names"
	"github.com/basecamp/bcq/internal/output"
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
	BaseURL  string

	// Behavior flags
	Verbose  bool
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
	authMgr := auth.NewManager(cfg, nil)

	// Create SDK client with auth adapter
	sdkCfg := &basecamp.Config{
		BaseURL:      cfg.BaseURL,
		AccountID:    cfg.AccountID,
		ProjectID:    cfg.ProjectID,
		TodolistID:   cfg.TodolistID,
		CacheDir:     cfg.CacheDir,
		CacheEnabled: cfg.CacheEnabled,
	}
	sdkClient := basecamp.NewClient(sdkCfg, &authAdapter{mgr: authMgr})

	// Create name resolver using SDK client
	nameResolver := names.NewResolver(sdkClient, authMgr)

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
		Config: cfg,
		Auth:   authMgr,
		SDK:    sdkClient,
		Names:  nameResolver,
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

	// Apply verbose mode - enable debug logging via slog
	if a.Flags.Verbose {
		debugLogger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
			Level: slog.LevelDebug,
		}))
		a.SDK.SetLogger(debugLogger)
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
