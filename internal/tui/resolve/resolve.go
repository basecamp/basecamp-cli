// Package resolve provides interactive prompts for resolving missing CLI options.
// When required options like --account or --project are not specified, the resolver
// interactively prompts the user to select from available options.
package resolve

import (
	"context"
	"os"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/bcq/internal/auth"
	"github.com/basecamp/bcq/internal/config"
	"github.com/basecamp/bcq/internal/tui"
)

// Resolver provides interactive prompts for resolving missing CLI options.
// It wraps the SDK client and auth manager to fetch available options,
// then uses TUI components to let the user select interactively.
type Resolver struct {
	sdk    *basecamp.Client
	auth   *auth.Manager
	config *config.Config
	styles *tui.Styles

	// flags holds the current CLI flag values for checking what's already set
	flags *Flags
}

// Flags holds the relevant CLI flag values for resolution.
type Flags struct {
	Account  string
	Project  string
	Todolist string

	// Machine output flags - when any of these are set, interactive prompts are disabled
	Agent   bool
	JSON    bool
	Quiet   bool
	IDsOnly bool
	Count   bool
}

// Option configures a Resolver.
type Option func(*Resolver)

// WithFlags sets the CLI flags for the resolver.
func WithFlags(flags *Flags) Option {
	return func(r *Resolver) {
		r.flags = flags
	}
}

// WithStyles sets custom TUI styles for the resolver.
func WithStyles(styles *tui.Styles) Option {
	return func(r *Resolver) {
		r.styles = styles
	}
}

// New creates a new Resolver with the given SDK client, auth manager, and config.
func New(sdk *basecamp.Client, auth *auth.Manager, cfg *config.Config, opts ...Option) *Resolver {
	r := &Resolver{
		sdk:    sdk,
		auth:   auth,
		config: cfg,
		styles: tui.NewStyles(),
		flags:  &Flags{},
	}

	for _, opt := range opts {
		opt(r)
	}

	return r
}

// SDK returns the underlying SDK client for fetching data.
func (r *Resolver) SDK() *basecamp.Client {
	return r.sdk
}

// Auth returns the auth manager for authentication operations.
func (r *Resolver) Auth() *auth.Manager {
	return r.auth
}

// Config returns the current config.
func (r *Resolver) Config() *config.Config {
	return r.config
}

// Styles returns the TUI styles.
func (r *Resolver) Styles() *tui.Styles {
	return r.styles
}

// Flags returns the current CLI flags.
func (r *Resolver) Flags() *Flags {
	return r.flags
}

// IsInteractive returns true if interactive prompts can be shown.
// This checks both TTY status and machine-output flags.
// Returns false if any machine-output flag is set (--agent, --json, --quiet, --ids-only, --count)
// or if stdout is not a terminal.
func (r *Resolver) IsInteractive() bool {
	// Check machine-output flags first
	if r.flags != nil {
		if r.flags.Agent || r.flags.JSON || r.flags.Quiet || r.flags.IDsOnly || r.flags.Count {
			return false
		}
	}

	// Check if stdout is a terminal
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

// ResolvedValue represents a value that was resolved, along with metadata
// about how it was resolved.
type ResolvedValue struct {
	Value  string
	Source ResolvedSource
}

// ResolvedSource indicates where a resolved value came from.
type ResolvedSource int

const (
	// SourceFlag means the value came from a CLI flag.
	SourceFlag ResolvedSource = iota
	// SourceConfig means the value came from the config file.
	SourceConfig
	// SourcePrompt means the value was selected interactively.
	SourcePrompt
	// SourceDefault means a default value was used.
	SourceDefault
)

// String returns a human-readable description of the source.
func (s ResolvedSource) String() string {
	switch s {
	case SourceFlag:
		return "flag"
	case SourceConfig:
		return "config"
	case SourcePrompt:
		return "prompt"
	case SourceDefault:
		return "default"
	default:
		return "unknown"
	}
}

// Context is a helper type for passing context through resolution chains.
type Context struct {
	context.Context
	resolver *Resolver
}

// NewContext creates a new resolution context.
func NewContext(ctx context.Context, r *Resolver) *Context {
	return &Context{
		Context:  ctx,
		resolver: r,
	}
}

// Resolver returns the resolver from the context.
func (c *Context) Resolver() *Resolver {
	return c.resolver
}
