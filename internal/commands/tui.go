package commands

import (
	"fmt"

	"github.com/spf13/cobra"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace/data"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace/views"
)

// NewTUICmd creates the tui command for the persistent workspace.
func NewTUICmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tui",
		Short: "Launch the Basecamp workspace",
		Long:  "Launch a persistent, full-screen terminal workspace for Basecamp.",
		PreRunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if app == nil {
				return fmt.Errorf("app not initialized")
			}
			return ensureAccount(cmd, app)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if app == nil {
				return fmt.Errorf("app not initialized")
			}

			session := workspace.NewSession(app)
			defer session.Shutdown()
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
func viewFactory(target workspace.ViewTarget, session *workspace.Session, store *data.Store, scope workspace.Scope) workspace.View {
	switch target {
	case workspace.ViewProjects:
		return views.NewProjects(session, store)
	case workspace.ViewDock:
		return views.NewDock(session, store, scope.ProjectID)
	case workspace.ViewTodos:
		return views.NewTodos(session, store)
	case workspace.ViewCampfire:
		return views.NewCampfire(session, store)
	case workspace.ViewCards:
		return views.NewCards(session, store)
	case workspace.ViewMessages:
		return views.NewMessages(session, store)
	case workspace.ViewSearch:
		return views.NewSearch(session, store)
	case workspace.ViewMyStuff:
		return views.NewMyStuff(session, store)
	case workspace.ViewPeople:
		return views.NewPeople(session)
	case workspace.ViewHey:
		return views.NewHey(session, store)
	case workspace.ViewSchedule:
		return views.NewSchedule(session)
	case workspace.ViewDocsFiles:
		return views.NewDocsFiles(session)
	case workspace.ViewCheckins:
		return views.NewCheckins(session)
	case workspace.ViewForwards:
		return views.NewForwards(session)
	case workspace.ViewDetail:
		return views.NewDetail(session, scope.RecordingID, scope.RecordingType)
	case workspace.ViewPulse:
		return views.NewPulse(session, store)
	case workspace.ViewAssignments:
		return views.NewAssignments(session, store)
	case workspace.ViewPings:
		return views.NewPings(session, store)
	case workspace.ViewCompose:
		return views.NewCompose(session, store)
	case workspace.ViewHome:
		return views.NewHome(session, store)
	default:
		return views.NewPlaceholder(session, target)
	}
}
