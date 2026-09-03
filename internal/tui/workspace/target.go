package workspace

import (
	"context"

	tea "charm.land/bubbletea/v2"

	"github.com/basecamp/basecamp-cli/internal/appctx"
)

// Target is where `basecamp tui <url>` asks the workspace to open. The zero
// value is home, which is where it opens without one.
//
// Kind is the path segment Basecamp puts in the URL — "card_tables", "cards",
// "chats" — rather than the dock's own name for the tool. The two disagree, and
// which one a caller has depends on whether it read a URL or a dock.
type Target struct {
	AccountID string
	ProjectID int64
	Kind      string
	ID        int64
}

// dockKinds maps the segment a URL names a tool by to the name the dock gives
// it, which is what openTool routes on.
var dockKinds = map[string]string{
	"card_tables":    cardTableKind,
	"message_boards": messageBoardKind,
	"chats":          chatKind,
	"todosets":       "todoset",
	"vaults":         "vault",
	"schedules":      "schedule",
	"questionnaires": "questionnaire",
	"inboxes":        "inbox",
}

// targetResolvedMsg is a target worked out down to a project and one tool on its
// dock. A URL that named only a project resolves with no tool.
type targetResolvedMsg struct {
	project int64
	tool    int64
	kind    string

	// open is the recording the URL actually named, when walking up to the tool
	// meant reading it anyway. A pasted card URL should land on the card, not
	// beside it.
	open *card

	err error
}

// WithTarget tells the workspace to open somewhere other than home.
func WithTarget(target Target) Option {
	return func(m *model) { m.target = &target }
}

// resolveTarget works a URL's target down to a tool on a project's dock.
//
// Most URLs name the tool outright. A card or a column does not: it names
// something inside a card table, and the only way to the table is up through the
// parents — a column's parent is its board, and a card's is its column. Two
// reads at worst, which is what a pasted card URL costs.
func resolveTarget(ctx context.Context, app *appctx.App, target Target) tea.Cmd {
	return func() tea.Msg {
		if err := app.RequireAccount(); err != nil {
			return targetResolvedMsg{err: err}
		}

		resolved := targetResolvedMsg{project: target.ProjectID}
		switch target.Kind {
		case "cards":
			named, column, err := parentOfCard(ctx, app, target.ID)
			if err != nil {
				return targetResolvedMsg{err: err}
			}
			resolved.open = &named
			target = Target{ProjectID: target.ProjectID, Kind: "columns", ID: column}
			fallthrough

		case "columns":
			board, err := parentOfColumn(ctx, app, target.ID)
			if err != nil {
				return targetResolvedMsg{err: err}
			}
			resolved.tool, resolved.kind = board, cardTableKind
			return resolved

		default:
			resolved.tool, resolved.kind = target.ID, dockKinds[target.Kind]
			return resolved
		}
	}
}

// parentOfCard reads a card and answers with both the card and the column it
// sits in. The read has to happen to find the column, so the card comes back
// with it rather than being read a second time by the screen behind it.
func parentOfCard(ctx context.Context, app *appctx.App, cardID int64) (card, int64, error) {
	found, err := app.Account().Cards().Get(ctx, cardID)
	if err != nil {
		return card{}, 0, err
	}
	if found.Parent == nil {
		return toCard(*found), 0, nil
	}
	return toCard(*found), found.Parent.ID, nil
}

func parentOfColumn(ctx context.Context, app *appctx.App, columnID int64) (int64, error) {
	found, err := app.Account().CardColumns().Get(ctx, columnID)
	if err != nil {
		return 0, err
	}
	if found.Parent == nil {
		return 0, nil
	}
	return found.Parent.ID, nil
}

// openTarget walks in to where a URL pointed: the project first, so the trail
// reads Home › CLIs › Card Table, and then the tool once the project's dock has
// arrived and can say what kind it is.
func (m *model) openTarget(msg targetResolvedMsg) tea.Cmd {
	m.target = nil
	if msg.err != nil {
		return notifyError("Could not open that link", msg.err)
	}
	if msg.project == 0 {
		return nil
	}

	if msg.tool != 0 {
		m.pending, m.pendingCard = openToolMsg{tool: tool{id: msg.tool, kind: msg.kind}}, msg.open
	}
	return m.openProject(project{id: msg.project})
}

// openPending pushes the tool a URL named, now that the project screen has read
// the dock and can say what the tool is called.
//
// The dock is where the name comes from: a URL says a card table's id and
// nothing about it being called "Basecamp CLI", and the breadcrumb needs the
// name the reader gave it.
func (m *model) openPending(msg projectLoadedMsg) tea.Cmd {
	if m.pending.tool.id == 0 || msg.err != nil {
		return nil
	}

	wanted, named := m.pending.tool, m.pendingCard
	m.pending, m.pendingCard = openToolMsg{}, nil

	for _, on := range msg.tools {
		if on.id != wanted.id {
			continue
		}
		opened := m.openTool(on, msg.project)
		if named == nil {
			return opened
		}
		// The URL named the card rather than the table it is on, so the table
		// goes in the trail and the card goes on top of it.
		return tea.Batch(opened, m.openCard(openCardMsg{card: *named}))
	}
	// The tool is not on the dock — turned off, or the URL named something in
	// another project. The project is open, which is as far in as this goes.
	return notify("That tool is not on this project's dock")
}
