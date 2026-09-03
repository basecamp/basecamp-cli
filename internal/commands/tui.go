//go:build dev

package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	tea "charm.land/bubbletea/v2"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/observability"
	"github.com/basecamp/basecamp-cli/internal/output"
	"github.com/basecamp/basecamp-cli/internal/tui"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace"
	"github.com/basecamp/basecamp-cli/internal/version"
)

// NewTUICmd creates the tui command for the persistent workspace.
func NewTUICmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tui [url]",
		Short: "Launch the Basecamp workspace [dev]",
		Long: "Launch a persistent, full-screen terminal workspace for Basecamp.\n\n" +
			"Pass a Basecamp URL to open a project, or one of the tools on its dock,\n" +
			"instead of the home screen. This feature is under active development and\n" +
			"may change between releases.",
		Annotations: map[string]string{"dev_only": "true"},
		Args:        cobra.MaximumNArgs(1),
		PreRunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if app == nil {
				return fmt.Errorf("app not initialized")
			}
			if !app.Config.IsExperimental("tui") {
				return output.ErrUsage(
					`experimental feature "tui" is not enabled; run: basecamp config set experimental.tui true --global`)
			}
			printDevNotice(app.Config.CacheDir)

			// A URL names the account it belongs to, and that is the account the
			// workspace should open — not whatever the config was left on.
			if len(args) > 0 {
				target, err := parseTUITarget(args[0])
				if err != nil {
					return err
				}
				if target.AccountID != "" {
					app.Config.AccountID = target.AccountID
				}
			}
			return settleAccount(app)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if app == nil {
				return fmt.Errorf("app not initialized")
			}

			if trace, _ := cmd.Flags().GetBool("trace"); trace {
				startTracing(app)
			}
			if app.Tracer != nil {
				fmt.Fprintf(os.Stderr, "Trace: %s\n", app.Tracer.Path())
			}

			// The workspace owns stderr for as long as it runs, so the trace
			// writer that shares it has to go quiet.
			if app.Hooks != nil {
				app.Hooks.SetLevel(0)
			}

			// Bubble Tea's own debug log goes to a file beside the trace rather
			// than into it: one is plain text, the other structured JSON, and
			// both stay parseable apart.
			if app.Tracer != nil && app.Tracer.Enabled(observability.TraceTUI) {
				debugPath := strings.TrimSuffix(app.Tracer.Path(), ".log") + ".debug.log"
				if f, err := tea.LogToFile(debugPath, "bubbletea"); err == nil {
					defer f.Close()
				}
			}

			// Ask the terminal how wide it draws the clusters no width table
			// agrees on — Devanagari and Thai combining marks, joined emoji —
			// and whether it draws pictures at all. Both are asked before Bubble
			// Tea owns the terminal and the answers are no longer readable, and
			// both give up quietly: the widths keep their safe guesses, and a
			// terminal that says nothing about pictures is shown none.
			tui.CalibrateWidths(os.Stdin, os.Stdout)
			tui.DetectImageSupport(os.Stdin, os.Stdout)

			options := []workspace.Option{workspace.WithReadingsWatcher(liveReadings(app))}
			if len(args) > 0 {
				// PreRunE already read it and refused an argument that is not a
				// Basecamp URL, so this cannot fail.
				target, _ := parseTUITarget(args[0])
				options = append(options, workspace.WithTarget(target))
			}

			model, shutdown := workspace.New(app, options...)
			defer shutdown()

			_, err := tea.NewProgram(model).Run()
			return err
		},
	}

	cmd.Flags().Bool("trace", false, "Enable trace logging to file")

	return cmd
}

// settleAccount takes the account the CLI can work out on its own — the config,
// which the --account flag and BASECAMP_ACCOUNT_ID have already been folded
// into, or the account a BC5 token is bound to by its resource indicator.
//
// It deliberately does not fall through to ensureAccount's prompt. The
// workspace has a picker of its own, with the logo over it and the accounts
// searchable; prompting here would stand a second one in front of it. An
// account it cannot settle is left unset, which is the picker's cue.
func settleAccount(app *appctx.App) error {
	if app.Config.AccountID == "" {
		app.Config.AccountID = app.Auth.AccountID()
	}
	if app.Config.AccountID == "" {
		return nil
	}

	if err := app.RequireAccount(); err != nil {
		return err
	}
	app.Names.SetAccountID(app.Config.AccountID)
	return nil
}

// startTracing widens an existing tracer to every category, or starts one when
// there is none. An env tracer may be narrower — BASECAMP_TRACE=http — and the
// workspace's own events would fall outside it.
func startTracing(app *appctx.App) {
	if app.Tracer != nil {
		app.Tracer.EnableCategories(observability.TraceAll)
		return
	}

	tracer, err := observability.NewTracer(observability.TraceAll, observability.TracePath(app.Config.CacheDir))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to start tracer: %v\n", err)
		return
	}
	app.Tracer = tracer
	if app.Hooks != nil {
		app.Hooks.SetTracer(tracer)
	}
}

// printDevNotice prints a one-time-per-version advisory to stderr.
// The sentinel file resets on version upgrade so the notice resurfaces when
// the TUI is most likely to have changed.
func printDevNotice(cacheDir string) {
	if cacheDir == "" {
		return
	}
	v := version.Version
	sentinel := filepath.Join(cacheDir, "dev-tui-"+v)

	if _, err := os.Stat(sentinel); err == nil {
		return // already shown for this version
	}

	_, _ = fmt.Fprintf(os.Stderr,
		"Note: The TUI workspace is a development preview in %s.\n"+
			"Behavior may change between releases. Report issues at https://github.com/basecamp/basecamp-cli/issues\n\n",
		v)

	// Best-effort write — ignore errors (e.g. read-only filesystem).
	_ = os.MkdirAll(cacheDir, 0o700)
	_ = os.WriteFile(sentinel, []byte(v), 0o600)
}
