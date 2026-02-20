package commands

import (
	"fmt"

	"github.com/spf13/cobra"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace"
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
		return views.NewDetail(session, scope.RecordingID, scope.RecordingType)
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
	default:
		return views.NewPlaceholder(session, target)
	}
}
