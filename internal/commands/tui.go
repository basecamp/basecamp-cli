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
	"github.com/basecamp/basecamp-cli/internal/tui/workspace"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace/views"
	"github.com/basecamp/basecamp-cli/internal/version"
)

// NewTUICmd creates the tui command for the persistent workspace.
func NewTUICmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tui [url]",
		Short: "Launch the Basecamp workspace [dev]",
		Long: "Launch a persistent, full-screen terminal workspace for Basecamp.\n" +
			"Optionally pass a Basecamp URL to jump directly to a project or recording.\n\n" +
			"This feature is under active development and may change between releases.",
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
			return ensureAccount(cmd, app)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if app == nil {
				return fmt.Errorf("app not initialized")
			}

			trace, _ := cmd.Flags().GetBool("trace")

			if trace {
				if app.Tracer == nil {
					// No tracer yet — create one with all categories
					t, err := observability.NewTracer(observability.TraceAll,
						observability.TracePath(app.Config.CacheDir))
					if err != nil {
						fmt.Fprintf(os.Stderr, "Warning: failed to start tracer: %v\n", err)
					} else {
						app.Tracer = t
						if app.Hooks != nil {
							app.Hooks.SetTracer(t)
						}
					}
				} else {
					// Env tracer exists but may be narrower (e.g. BASECAMP_TRACE=http).
					// Widen to all categories so TUI events are captured too.
					app.Tracer.EnableCategories(observability.TraceAll)
				}
			}

			// Print trace path so devtools scripts can find it
			if app.Tracer != nil {
				fmt.Fprintf(os.Stderr, "Trace: %s\n", app.Tracer.Path())
			}

			// Suppress stderr TraceWriter during TUI (TUI owns stderr)
			if app.Hooks != nil {
				app.Hooks.SetLevel(0)
			}

			// Wire bubbletea debug logging to a separate file (plain text,
			// not the structured JSON trace) so both remain parseable.
			if app.Tracer != nil && app.Tracer.Enabled(observability.TraceTUI) {
				debugPath := strings.TrimSuffix(app.Tracer.Path(), ".log") + ".debug.log"
				f, err := tea.LogToFile(debugPath, "bubbletea")
				if err == nil {
					defer f.Close()
				}
			}

			session := workspace.NewSession(app)
			defer session.Shutdown()

			// Deep-link: parse URL argument and set initial navigation target.
			if len(args) > 0 {
				target, scope, err := parseBasecampURL(args[0])
				if err != nil {
					return err
				}
				session.SetInitialView(target, scope)
			}

			// Pass tracer to workspace
			var wsOpts []workspace.Option
			if app.Tracer != nil {
				wsOpts = append(wsOpts, workspace.WithTracer(app.Tracer))
			}
			model := workspace.New(session, viewFactory, poolMonitorFactory(session), wsOpts...)

			p := tea.NewProgram(model)

			_, err := p.Run()
			model.CloseWatcher()
			return err
		},
	}

	cmd.Flags().Bool("trace", false, "Enable trace logging to file")

	return cmd
}

// poolMonitorFactory returns a factory that creates pool monitor views.
func poolMonitorFactory(session *workspace.Session) func() workspace.View {
	return func() workspace.View {
		hub := session.Hub()
		if hub == nil {
			return nil
		}
		m := hub.Metrics()
		return views.NewPoolMonitor(session.Styles(), m.PoolStatsList, m.Apdex, m.RecentEvents)
	}
}

// viewFactory creates views for navigation targets.
func viewFactory(target workspace.ViewTarget, session *workspace.Session, scope workspace.Scope) workspace.View {
	switch target {
	case workspace.ViewProjects:
		return views.NewProjects(session)
	case workspace.ViewDock:
		return views.NewDock(session, scope.ProjectID)
	case workspace.ViewTodos:
		return views.NewTodos(session)
	case workspace.ViewChat:
		return views.NewChat(session)
	case workspace.ViewCards:
		return views.NewCards(session)
	case workspace.ViewMessages:
		return views.NewMessages(session)
	case workspace.ViewSearch:
		return views.NewSearch(session)
	case workspace.ViewMyStuff:
		return views.NewMyStuff(session)
	case workspace.ViewPeople:
		return views.NewPeople(session)
	case workspace.ViewHey:
		return views.NewHey(session)
	case workspace.ViewSchedule:
		return views.NewSchedule(session)
	case workspace.ViewDocsFiles:
		return views.NewDocsFiles(session)
	case workspace.ViewCheckins:
		return views.NewCheckins(session)
	case workspace.ViewForwards:
		return views.NewForwards(session)
	case workspace.ViewDetail:
		return views.NewDetail(session, scope.RecordingID, scope.RecordingType,
			scope.OriginView, scope.OriginHint)
	case workspace.ViewPulse:
		return views.NewPulse(session)
	case workspace.ViewAssignments:
		return views.NewAssignments(session)
	case workspace.ViewPings:
		return views.NewPings(session)
	case workspace.ViewCompose:
		return views.NewCompose(session)
	case workspace.ViewHome:
		return views.NewHome(session)
	case workspace.ViewActivity:
		return views.NewActivity(session)
	case workspace.ViewTimeline:
		return views.NewTimeline(session, scope.ProjectID)
	case workspace.ViewBonfire:
		return views.NewRiver(session)
	case workspace.ViewFrontPage:
		return views.NewFrontPage(session)
	case workspace.ViewBonfireSidebar:
		return views.NewBonfireSidebar(session)
	default:
		return views.NewHome(session)
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
