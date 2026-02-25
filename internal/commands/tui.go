package commands

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace/views"
	"github.com/basecamp/basecamp-cli/internal/version"
)

// NewTUICmd creates the tui command for the persistent workspace.
func NewTUICmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tui [url]",
		Short: "Launch the Basecamp workspace [experimental]",
		Long: "Launch a persistent, full-screen terminal workspace for Basecamp.\n" +
			"Optionally pass a Basecamp URL to jump directly to a project or recording.\n\n" +
			"This feature is under active development and may change between releases.",
		Annotations: map[string]string{"experimental": "true"},
		Args:        cobra.MaximumNArgs(1),
		PreRunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if app == nil {
				return fmt.Errorf("app not initialized")
			}
			printExperimentalNotice(app.Config.CacheDir)
			return ensureAccount(cmd, app)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if app == nil {
				return fmt.Errorf("app not initialized")
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

			model := workspace.New(session, viewFactory)

			p := tea.NewProgram(
				model,
				tea.WithAltScreen(),
				tea.WithMouseCellMotion(),
			)

			_, err := p.Run()
			return err
		},
	}

	return cmd
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
	case workspace.ViewCampfire:
		return views.NewCampfire(session)
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
	default:
		return views.NewHome(session)
	}
}

// printExperimentalNotice prints a one-time-per-version advisory to stderr.
// The sentinel file resets on version upgrade so the notice resurfaces when
// experimental features are most likely to have changed.
func printExperimentalNotice(cacheDir string) {
	if cacheDir == "" {
		return
	}
	v := version.Version
	sentinel := filepath.Join(cacheDir, "experimental-tui-"+v)

	if _, err := os.Stat(sentinel); err == nil {
		return // already shown for this version
	}

	_, _ = fmt.Fprintf(os.Stderr,
		"Note: The TUI workspace is experimental in %s.\n"+
			"Behavior may change between releases. Report issues at https://github.com/basecamp/basecamp-cli/issues\n\n",
		v)

	// Best-effort write — ignore errors (e.g. read-only filesystem).
	_ = os.MkdirAll(cacheDir, 0o700)
	_ = os.WriteFile(sentinel, []byte(v), 0o600)
}
