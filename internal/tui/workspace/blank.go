package workspace

import (
	tea "charm.land/bubbletea/v2"
)

// blank is a screen with a name and nothing on it yet: the frame is drawn, the
// content is still to come. Every section the menu opens is one, until it grows
// something of its own to show.
type blank struct {
	ctx    *Context
	title  string
	width  int
	height int
}

func newBlank(ctx *Context, title string) *blank {
	return &blank{ctx: ctx, title: title}
}

// homeKey goes back to the bottom of the stack from wherever the reader is, and
// homeHintText is how the menu says so beside it.
const (
	homeKey      = "H"
	homeHintText = "Shift + H"
)

func (b *blank) Init() tea.Cmd { return nil }

func (b *blank) Update(tea.Msg) (tea.Cmd, bool) { return nil, false }

func (b *blank) View() string { return "" }

func (b *blank) Title() string { return b.title }

func (b *blank) HandleKey(tea.KeyPressMsg) tea.Cmd { return nil }

func (b *blank) HelpBindings() []helpBinding { return nil }

func (b *blank) Resize(width, height int) {
	b.width = width
	b.height = height
}

func (b *blank) Loading() bool { return false }
