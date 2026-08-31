package workspace

import (
	"context"
	"sort"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/richtext"
	"github.com/basecamp/basecamp-cli/internal/tui"
)

const (
	// The box is a fixed width so a wide terminal does not stretch a list of
	// six names across the whole screen, and narrows only when the terminal
	// gives it no choice.
	pickerBoxWidth = 52

	// Two border rows, the search field, and the rule beneath it.
	pickerChrome = 4

	// Below this many rows the logo gives up its space: the list is the point
	// of the screen.
	pickerMinRows = 3
)

type account struct {
	id   string
	name string
}

// accountsLoadedMsg is the answer to the one read this screen makes.
type accountsLoadedMsg struct {
	accounts []account
	err      error
}

// accountChosenMsg says which account the reader picked. Applying it is the
// model's: the account is the whole workspace's, not this screen's.
type accountChosenMsg struct {
	account account
}

// pickerCanceledMsg closes the picker. It cannot pop itself — the stack belongs
// to the model.
type pickerCanceledMsg struct{}

// accountPicker is the list of accounts the credential can open: the logo, then
// a box with a search field over the names.
//
// It is the opening screen when no account has been settled, and ctrl+a brings
// it back to switch. Those differ in one respect only: with no account there is
// nothing behind this screen, so esc has nowhere to go.
type accountPicker struct {
	ctx *Context

	search   textinput.Model
	accounts []account
	filtered []account
	cursor   int
	offset   int

	current   string
	canCancel bool
	loading   bool
	notice    string

	width  int
	height int
}

func newAccountPicker(ctx *Context, canCancel bool) *accountPicker {
	search := textinput.New()
	search.Placeholder = "Search accounts"
	search.Prompt = ""
	search.Focus()

	return &accountPicker{
		ctx:       ctx,
		search:    search,
		current:   ctx.AccountID(),
		canCancel: canCancel,
		loading:   true,
	}
}

func (p *accountPicker) Init() tea.Cmd {
	return tea.Batch(loadAccounts(p.ctx.Ctx(), p.ctx.app), textinput.Blink)
}

// loadAccounts reads the accounts the credential is good for. That list belongs
// to the authorization rather than to an account, so it is the same read
// whichever account is currently open.
func loadAccounts(ctx context.Context, app *appctx.App) tea.Cmd {
	return func() tea.Msg {
		authorized, err := app.Resolve().ListAccounts(ctx)
		if err != nil {
			return accountsLoadedMsg{err: err}
		}

		accounts := make([]account, 0, len(authorized))
		for _, a := range authorized {
			accounts = append(accounts, account{
				id:   strconv.FormatInt(a.ID, 10),
				name: richtext.SanitizeSingleLine(a.Name),
			})
		}
		sort.Slice(accounts, func(i, j int) bool { return accounts[i].name < accounts[j].name })
		return accountsLoadedMsg{accounts: accounts}
	}
}

func (p *accountPicker) Update(msg tea.Msg) (tea.Cmd, bool) {
	if loaded, ok := msg.(accountsLoadedMsg); ok {
		p.loading = false
		if loaded.err != nil {
			p.notice = errorNotice("Could not load the accounts", loaded.err)
			return nil, true
		}
		p.accounts = loaded.accounts
		p.notice = ""
		p.refilter()
		p.cursor = p.indexOf(p.current)
		p.scrollToCursor()
		return nil, true
	}

	// The cursor blink and the rest of the text field's own messages.
	search, cmd := p.search.Update(msg)
	p.search = search
	return cmd, true
}

func (p *accountPicker) HandleKey(msg tea.KeyPressMsg) tea.Cmd {
	// A read that failed leaves the opening picker with no accounts and no esc
	// to fall back on, so there has to be a way to ask again.
	if msg.String() == "ctrl+r" && !p.loading {
		p.loading = true
		p.notice = ""
		return p.Init()
	}

	switch msg.Key().Code {
	case tea.KeyEscape:
		if p.canCancel {
			return func() tea.Msg { return pickerCanceledMsg{} }
		}
		return nil
	case tea.KeyEnter:
		if p.loading || len(p.filtered) == 0 {
			return nil
		}
		chosen := p.filtered[p.cursor]
		return func() tea.Msg { return accountChosenMsg{account: chosen} }
	case tea.KeyUp:
		p.moveCursor(-1)
		return nil
	case tea.KeyDown:
		p.moveCursor(1)
		return nil
	}

	search, cmd := p.search.Update(msg)
	p.search = search
	p.refilter()
	return cmd
}

// refilter narrows the list to what the reader has typed, matching either the
// name or the id, and leaves the cursor on something that still exists.
func (p *accountPicker) refilter() {
	query := strings.ToLower(strings.TrimSpace(p.search.Value()))
	p.filtered = p.filtered[:0]
	for _, a := range p.accounts {
		if query == "" || strings.Contains(strings.ToLower(a.name), query) || strings.Contains(a.id, query) {
			p.filtered = append(p.filtered, a)
		}
	}
	p.cursor = min(p.cursor, max(len(p.filtered)-1, 0))
	p.scrollToCursor()
}

func (p *accountPicker) moveCursor(by int) {
	p.cursor = max(min(p.cursor+by, len(p.filtered)-1), 0)
	p.scrollToCursor()
}

// scrollToCursor slides the window until the cursor is inside it.
func (p *accountPicker) scrollToCursor() {
	rows := p.visibleRows()
	p.offset = min(p.offset, p.cursor)
	if p.cursor >= p.offset+rows {
		p.offset = p.cursor - rows + 1
	}
	p.offset = max(min(p.offset, max(len(p.filtered)-rows, 0)), 0)
}

func (p *accountPicker) indexOf(id string) int {
	for index, a := range p.filtered {
		if a.id == id {
			return index
		}
	}
	return 0
}

// --- Rendering ---

func (p *accountPicker) View() string {
	block := p.box()
	if p.showsLogo() {
		logo := tui.RenderSnowglobe(p.ctx.Styles().Theme())
		block = lipgloss.JoinVertical(lipgloss.Center, logo, "", block)
	}
	return lipgloss.Place(p.width, p.height, lipgloss.Center, lipgloss.Center, block)
}

// showsLogo is whether the Basecamp mark goes over the box. It belongs to the
// front door — the picker the workspace opens on — and not to a switch made
// with ctrl+a partway through, where it would only push the list down the
// screen. Then only if the screen has room for it and still leaves enough rows
// to choose from.
func (p *accountPicker) showsLogo() bool {
	return !p.canCancel &&
		p.width >= tui.SnowglobeWidth &&
		p.height-tui.SnowglobeLines-1-pickerChrome >= pickerMinRows
}

func (p *accountPicker) box() string {
	theme := p.ctx.Styles().Theme()
	inner := p.innerWidth()

	var b strings.Builder
	b.WriteString(p.search.View())
	b.WriteString("\n")
	b.WriteString(lipgloss.NewStyle().Foreground(theme.Border).Render(strings.Repeat("─", inner)))
	for _, row := range p.rows(inner) {
		b.WriteString("\n" + row)
	}

	// Width is the whole block's, border and padding included, so it is the
	// content width plus the four columns of chrome around it.
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(theme.Border).
		Padding(0, 1).
		Width(inner + 4).
		Render(b.String())
}

// rows are the account lines. A list with nothing in it says so rather than
// drawing a hole.
func (p *accountPicker) rows(inner int) []string {
	styles := p.ctx.Styles()
	switch {
	case p.notice != "":
		lines := wrapText(p.notice, inner)
		rows := make([]string, 0, len(lines)+2)
		for _, line := range lines {
			rows = append(rows, styles.Error.Render(line))
		}
		return append(rows, "", styles.Muted.Render("ctrl+r to try again"))
	case p.loading:
		return []string{styles.Muted.Render("Loading accounts…")}
	case len(p.accounts) == 0:
		return []string{styles.Muted.Render("No accounts on this login")}
	case len(p.filtered) == 0:
		return []string{styles.Muted.Render("Nothing matches that")}
	}

	end := min(p.offset+p.visibleRows(), len(p.filtered))
	rows := make([]string, 0, end-p.offset)
	for index := p.offset; index < end; index++ {
		rows = append(rows, p.row(p.filtered[index], index == p.cursor, inner))
	}
	return rows
}

// row is one account: the name on the left, its id on the right, the cursor
// marked with an arrow and the account already open marked with a dot.
func (p *accountPicker) row(a account, selected bool, inner int) string {
	styles := p.ctx.Styles()
	theme := styles.Theme()

	marker := "  "
	if a.id == p.current {
		marker = "• "
	}
	name := lipgloss.NewStyle().Foreground(theme.Foreground)
	id := styles.Muted
	if selected {
		marker = "› "
		name = lipgloss.NewStyle().Foreground(theme.Primary).Bold(true)
		id = lipgloss.NewStyle().Foreground(theme.Primary)
	}

	width := max(inner-tui.DisplayWidth(marker), 1)
	gap := width - tui.DisplayWidth(a.name) - tui.DisplayWidth(a.id)
	if gap < 2 {
		return marker + name.Render(truncateToWidth(a.name, width))
	}
	return marker + name.Render(a.name) + strings.Repeat(" ", gap) + id.Render(a.id)
}

// visibleRows is how many accounts the box has room for, once the logo has
// taken its share.
func (p *accountPicker) visibleRows() int {
	rows := p.height - pickerChrome
	if p.showsLogo() {
		rows -= tui.SnowglobeLines + 1
	}
	return max(min(rows, max(len(p.filtered), 1)), 1)
}

// innerWidth is the box's content width: what is left of the terminal after the
// border and the padding, never more than the box's own width.
func (p *accountPicker) innerWidth() int {
	return max(min(pickerBoxWidth, p.width)-4, 1)
}

// --- View plumbing ---

func (p *accountPicker) Title() string { return "Accounts" }

func (p *accountPicker) HelpBindings() []helpBinding {
	if p.notice != "" {
		bindings := []helpBinding{{"ctrl+r", "try again"}}
		if p.canCancel {
			bindings = append(bindings, helpBinding{"esc", "cancel"})
		}
		return bindings
	}

	bindings := []helpBinding{{"↑↓", "account"}, {"enter", "open"}}
	if p.canCancel {
		bindings = append(bindings, helpBinding{"esc", "cancel"})
	}
	return bindings
}

func (p *accountPicker) Resize(width, height int) {
	p.width = width
	p.height = height
	p.search.SetWidth(p.innerWidth())
	p.scrollToCursor()
}

// Loading is false while the accounts are being read: the box says so itself,
// on the screen the reader is already looking at. Answering true would swap the
// whole screen for a spinner and take the logo with it.
func (p *accountPicker) Loading() bool { return false }

// CapturingInput is always true. Every key that is not a cursor move or a
// choice is search text.
func (p *accountPicker) CapturingInput() bool { return true }

// WantsFullWidth keeps the sidebar off this screen. Choosing an account is what
// the reader is doing, not something they are doing beside something else — and
// the front door has no account to have a sidebar about yet.
func (p *accountPicker) WantsFullWidth() bool { return true }
