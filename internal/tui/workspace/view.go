package workspace

import (
	"context"

	tea "charm.land/bubbletea/v2"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/tui"
)

// Context is what every view is handed: the API, the palette, and the size of
// the area it has to draw in. One instance is shared, so a view reads the
// current styles and dimensions rather than keeping copies that go stale on a
// resize or a theme switch.
type Context struct {
	app         *appctx.App
	ctx         context.Context
	styles      *tui.Styles
	accountName string
	width       int
	height      int
}

// SDK is the Basecamp client every read goes through.
func (c *Context) SDK() *basecamp.Client { return c.app.SDK }

// AccountID is the account the workspace is reading.
func (c *Context) AccountID() string { return c.app.Config.AccountID }

// AccountName is that account by name, which is what the header shows. Config
// holds only the id, so until the accounts have been read the id stands in for
// the name rather than leaving the header blank.
func (c *Context) AccountName() string {
	if c.accountName != "" {
		return c.accountName
	}
	return c.AccountID()
}

// Ctx is the context that lives as long as the workspace does. Every read a
// view starts takes it, so quitting cancels what is still in flight.
func (c *Context) Ctx() context.Context { return c.ctx }

// Styles is the active palette.
func (c *Context) Styles() *tui.Styles { return c.styles }

// Size is the content area: the rows and columns left after the header and the
// help footer have taken theirs.
func (c *Context) Size() (width, height int) { return c.width, c.height }

// View is one screen in the workspace. Views stack: home sits at the bottom and
// esc pops whatever is on top of it.
type View interface {
	// Init returns the command to run when the view is pushed onto the stack.
	Init() tea.Cmd

	// Update handles a message and reports whether it consumed it. A message it
	// does not recognize goes back to the model.
	Update(msg tea.Msg) (tea.Cmd, bool)

	// View renders the content area.
	View() string

	// Title is the view's name in the breadcrumb trail.
	Title() string

	// HandleKey handles a key press the model did not claim for itself.
	HandleKey(msg tea.KeyPressMsg) tea.Cmd

	// HelpBindings are the view's own entries in the help bar, appended to the
	// model's.
	HelpBindings() []helpBinding

	// Resize lays the view out for a new content area.
	Resize(width, height int)

	// Loading reports whether the view is waiting on a read.
	Loading() bool
}

// inputCapturer is implemented by a view that can open a text field. While
// CapturingInput is true every key press goes to HandleKey — esc, tab, letters
// and all — instead of being read as a shortcut. The ctrl+c quit chord is
// handled before this and is the one key a view never sees.
type inputCapturer interface {
	CapturingInput() bool
}

// fullWidth is implemented by a view that wants the whole terminal rather than a
// column beside the sidebar. A chooser is one: it is what the reader is doing,
// not something they are doing beside anything else.
type fullWidth interface {
	WantsFullWidth() bool
}

// popBlocker is implemented by a view that has something of its own to close
// before esc should pop it off the stack: a filter, a picker, a confirmation.
// It answers true when it handled the esc itself.
type popBlocker interface {
	HandleBack() bool
}
