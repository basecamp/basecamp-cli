package workspace

import (
	"strings"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// composer is the field rich text is written in, wherever that happens: a chat
// message, a new message on a board, an edit to a comment. Every one of them is
// Markdown on the way out, so every one of them reads the same on the way in.
type composer struct {
	textarea.Model
}

// newComposer is a field with nothing between the reader and what they typed: no
// prompt, no line numbers, no band under the cursor line. What is in it is
// Markdown, and rows is what says so.
func newComposer(placeholder string) composer {
	field := textarea.New()
	field.Prompt = ""
	field.ShowLineNumbers = false
	field.Placeholder = placeholder
	field.SetStyles(composerStyles())
	return composer{field}
}

// growWith lets the field start at one row and grow with what is typed, up to a
// ceiling. A chat composer does this so a one-line message takes one line; a
// form's body field keeps the box it was given.
func (c *composer) growWith(rows int) {
	c.DynamicHeight = true
	c.MinHeight = 1
	c.MaxHeight = rows
	c.SetHeight(1)
}

// edit hands a key press or a cursor tick to the field. textarea answers with a
// new model rather than mutating, which every caller would otherwise have to
// remember to put back.
func (c *composer) edit(msg tea.Msg) tea.Cmd {
	field, cmd := c.Update(msg)
	c.Model = field
	return cmd
}

// rows is the field, restyled so the Markdown reads the way it will arrive and
// its delimiters dim. Only styling is added — every character the field drew, the
// cursor included, stays where it was.
func (c *composer) rows() []string {
	return strings.Split(styleInlineMarkdown(c.View()), "\n")
}

// composerStyles leave the text alone. styleInlineMarkdown is what colors it, and
// a band under the cursor line or a color on the text itself would fight that.
// The cursor says where the cursor is.
func composerStyles() textarea.Styles {
	plain := lipgloss.NewStyle()
	muted := lipgloss.NewStyle().Faint(true)

	focused := textarea.StyleState{
		Base:             plain,
		Text:             plain,
		LineNumber:       muted,
		CursorLineNumber: plain,
		CursorLine:       plain,
		EndOfBuffer:      muted,
		Placeholder:      muted,
		Prompt:           plain,
		Selection:        lipgloss.NewStyle().Reverse(true),
	}
	return textarea.Styles{Focused: focused, Blurred: focused}
}
