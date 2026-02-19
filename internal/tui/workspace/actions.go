package workspace

import (
	"fmt"
	"os/exec"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// ScopeRequirement defines what scope context an action needs.
type ScopeRequirement int

const (
	// ScopeAny means the action works anywhere.
	ScopeAny ScopeRequirement = iota
	// ScopeAccount means the action needs an account selected.
	ScopeAccount
	// ScopeProject means the action needs a project selected.
	ScopeProject
)

// Action represents a registered command/action in the workspace.
type Action struct {
	Name        string
	Aliases     []string
	Description string
	Category    string           // "navigation", "project", "mutation", etc.
	Scope       ScopeRequirement // what scope context is needed
	Execute     func(session *Session) tea.Cmd
}

// Registry holds all registered actions.
type Registry struct {
	actions []Action
}

// NewRegistry creates an empty action registry.
func NewRegistry() *Registry {
	return &Registry{}
}

// Register adds an action to the registry.
func (r *Registry) Register(action Action) {
	r.actions = append(r.actions, action)
}

// All returns every registered action.
func (r *Registry) All() []Action {
	out := make([]Action, len(r.actions))
	copy(out, r.actions)
	return out
}

// Search returns actions whose name, aliases, or description match the query.
// An empty query returns all actions.
func (r *Registry) Search(query string) []Action {
	if query == "" {
		return r.All()
	}
	q := strings.ToLower(query)
	var matches []Action
	for _, a := range r.actions {
		if fuzzyMatch(q, a) {
			matches = append(matches, a)
		}
	}
	return matches
}

// ForScope returns actions available for the given scope.
func (r *Registry) ForScope(scope Scope) []Action {
	var matches []Action
	for _, a := range r.actions {
		if scopeSatisfied(a.Scope, scope) {
			matches = append(matches, a)
		}
	}
	return matches
}

// fuzzyMatch checks whether query appears as a substring in any of the
// action's searchable fields (name, aliases, description).
func fuzzyMatch(query string, a Action) bool {
	if strings.Contains(strings.ToLower(a.Name), query) {
		return true
	}
	for _, alias := range a.Aliases {
		if strings.Contains(strings.ToLower(alias), query) {
			return true
		}
	}
	return strings.Contains(strings.ToLower(a.Description), query)
}

// scopeSatisfied returns true when the given scope meets the requirement.
func scopeSatisfied(req ScopeRequirement, scope Scope) bool {
	switch req {
	case ScopeAccount:
		return scope.AccountID != ""
	case ScopeProject:
		return scope.ProjectID != 0
	default:
		return true
	}
}

// DefaultActions returns a registry pre-populated with the standard navigation actions.
func DefaultActions() *Registry {
	r := NewRegistry()

	r.Register(Action{
		Name:        ":projects",
		Aliases:     []string{"home", "dashboard"},
		Description: "Navigate to projects",
		Category:    "navigation",
		Scope:       ScopeAny,
		Execute: func(s *Session) tea.Cmd {
			return Navigate(ViewProjects, s.Scope())
		},
	})
	r.Register(Action{
		Name:        ":todos",
		Aliases:     []string{"todolists", "tasks"},
		Description: "Navigate to to-dos",
		Category:    "project",
		Scope:       ScopeProject,
		Execute: func(s *Session) tea.Cmd {
			return Navigate(ViewTodos, s.Scope())
		},
	})
	r.Register(Action{
		Name:        ":campfire",
		Aliases:     []string{"chat", "fire"},
		Description: "Navigate to campfire",
		Category:    "project",
		Scope:       ScopeProject,
		Execute: func(s *Session) tea.Cmd {
			return Navigate(ViewCampfire, s.Scope())
		},
	})
	r.Register(Action{
		Name:        ":messages",
		Aliases:     []string{"message board", "posts"},
		Description: "Navigate to message board",
		Category:    "project",
		Scope:       ScopeProject,
		Execute: func(s *Session) tea.Cmd {
			return Navigate(ViewMessages, s.Scope())
		},
	})
	r.Register(Action{
		Name:        ":cards",
		Aliases:     []string{"card table", "kanban"},
		Description: "Navigate to card table",
		Category:    "project",
		Scope:       ScopeProject,
		Execute: func(s *Session) tea.Cmd {
			return Navigate(ViewCards, s.Scope())
		},
	})
	r.Register(Action{
		Name:        ":search",
		Aliases:     []string{"find", "lookup"},
		Description: "Open search",
		Category:    "navigation",
		Scope:       ScopeAny,
		Execute: func(s *Session) tea.Cmd {
			return Navigate(ViewSearch, s.Scope())
		},
	})
	r.Register(Action{
		Name:        ":hey",
		Aliases:     []string{"inbox", "notifications"},
		Description: "Open Hey! inbox",
		Category:    "navigation",
		Scope:       ScopeAny,
		Execute: func(s *Session) tea.Cmd {
			return Navigate(ViewHey, s.Scope())
		},
	})
	r.Register(Action{
		Name:        ":me",
		Aliases:     []string{"mystuff", "my stuff"},
		Description: "Open My Stuff",
		Category:    "navigation",
		Scope:       ScopeAny,
		Execute: func(s *Session) tea.Cmd {
			return Navigate(ViewMyStuff, s.Scope())
		},
	})
	r.Register(Action{
		Name:        ":people",
		Aliases:     []string{"team", "users"},
		Description: "Open people list",
		Category:    "navigation",
		Scope:       ScopeAny,
		Execute: func(s *Session) tea.Cmd {
			return Navigate(ViewPeople, s.Scope())
		},
	})
	r.Register(Action{
		Name:        ":pulse",
		Aliases:     []string{"activity", "recent"},
		Description: "Activity across all accounts",
		Category:    "navigation",
		Scope:       ScopeAny,
		Execute: func(s *Session) tea.Cmd {
			return Navigate(ViewPulse, s.Scope())
		},
	})
	r.Register(Action{
		Name:        ":assignments",
		Aliases:     []string{"assigned", "my todos"},
		Description: "My todo assignments",
		Category:    "navigation",
		Scope:       ScopeAny,
		Execute: func(s *Session) tea.Cmd {
			return Navigate(ViewAssignments, s.Scope())
		},
	})
	r.Register(Action{
		Name:        ":pings",
		Aliases:     []string{"dm", "direct messages"},
		Description: "Direct messages (pings)",
		Category:    "navigation",
		Scope:       ScopeAny,
		Execute: func(s *Session) tea.Cmd {
			return Navigate(ViewPings, s.Scope())
		},
	})
	r.Register(Action{
		Name:        ":open",
		Aliases:     []string{"browser", "web"},
		Description: "Open in browser",
		Category:    "navigation",
		Scope:       ScopeAccount,
		Execute: func(s *Session) tea.Cmd {
			return openInBrowser(s.Scope())
		},
	})
	r.Register(Action{
		Name:        ":quit",
		Aliases:     []string{"exit", "close"},
		Description: "Quit bcq",
		Category:    "navigation",
		Scope:       ScopeAny,
		Execute: func(_ *Session) tea.Cmd {
			return tea.Quit
		},
	})

	return r
}

// openInBrowser builds a Basecamp URL from scope and opens it in the default browser.
func openInBrowser(scope Scope) tea.Cmd {
	var url string
	switch {
	case scope.RecordingID != 0 && scope.ProjectID != 0:
		url = fmt.Sprintf("https://3.basecamp.com/%s/buckets/%d/recordings/%d",
			scope.AccountID, scope.ProjectID, scope.RecordingID)
	case scope.ProjectID != 0:
		url = fmt.Sprintf("https://3.basecamp.com/%s/projects/%d",
			scope.AccountID, scope.ProjectID)
	default:
		url = fmt.Sprintf("https://3.basecamp.com/%s", scope.AccountID)
	}
	return func() tea.Msg {
		if err := exec.Command("open", url).Start(); err != nil { //nolint:gosec,noctx
			return ErrorMsg{Context: "open", Err: err}
		}
		return StatusMsg{Text: "Opened in browser"}
	}
}
