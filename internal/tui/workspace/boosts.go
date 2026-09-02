package workspace

import (
	"context"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/tui"
)

// boost is one reaction under a comment: who left it and what they left.
type boost struct {
	id      int64
	content string
	by      person

	// mine says the reader left this one, which is the only kind they may take
	// back.
	mine bool
}

// boostsMsg is the reactions under one comment.
type boostsMsg struct {
	comment int64
	boosts  []boost
	err     error
}

func loadBoosts(ctx context.Context, app *appctx.App, me int64, commentID int64) tea.Cmd {
	return func() tea.Msg {
		if err := app.RequireAccount(); err != nil {
			return boostsMsg{comment: commentID, err: err}
		}
		result, err := app.Account().Boosts().ListRecording(ctx, commentID, nil)
		if err != nil {
			return boostsMsg{comment: commentID, err: err}
		}

		boosts := make([]boost, 0, len(result.Boosts))
		for _, left := range result.Boosts {
			boosts = append(boosts, toBoost(left, me))
		}
		return boostsMsg{comment: commentID, boosts: boosts}
	}
}

func toBoost(left basecamp.Boost, me int64) boost {
	out := boost{id: left.ID, content: left.Content}
	if left.Booster != nil {
		out.by = toPerson(left.Booster)
		out.mine = me != 0 && left.Booster.ID == me
	}
	return out
}

// boostRows draws the reactions under a comment: each one behind the initials of
// whoever left it.
//
// Initials rather than a face. A face is four columns at its smallest here — a
// cell is twice as tall as it is wide, so anything narrower is a letterbox — and
// four columns per reaction turns a row of them into a wall. Two letters say who
// in the room the web gives a 20-pixel circle.
//
// One line, wrapped by the caller's width, so they read as a row of small things
// rather than a list.
func boostRows(styles *tui.Styles, boosts []boost, width int) []string {
	if len(boosts) == 0 {
		return nil
	}

	theme := styles.Theme()
	chip := lipgloss.NewStyle().Foreground(theme.Muted)
	mine := lipgloss.NewStyle().Foreground(theme.Primary)
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(theme.Border).
		Padding(0, 1)

	// One pill each, the way the web draws them, laid along the row until the
	// column runs out and then onto the next.
	//
	// A pill is three rows tall against the one its text needs, so they go side
	// by side rather than stacked: five reactions down the page would bury the
	// comment they belong to.
	var rows []string
	var line []string
	spent := 0
	for _, left := range boosts {
		style := chip
		if left.mine {
			style = mine
		}
		pill := box.Render(style.Render(left.by.initials() + " " + left.content))
		wide := lipgloss.Width(pill)

		if len(line) > 0 && spent+1+wide > width {
			rows = append(rows, strings.Split(lipgloss.JoinHorizontal(lipgloss.Top, line...), "\n")...)
			line, spent = nil, 0
		}
		if len(line) > 0 {
			line = append(line, " ")
			spent++
		}
		line = append(line, pill)
		spent += wide
	}
	if len(line) > 0 {
		rows = append(rows, strings.Split(lipgloss.JoinHorizontal(lipgloss.Top, line...), "\n")...)
	}
	return rows
}
