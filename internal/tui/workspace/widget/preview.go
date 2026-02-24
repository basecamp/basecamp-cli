package widget

import (
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/basecamp/basecamp-cli/internal/tui"
)

// PreviewField is a key-value pair shown in the preview header.
type PreviewField struct {
	Key   string
	Value string
}

// Preview renders a detail pane with key-value fields and an optional body.
type Preview struct {
	styles *tui.Styles
	width  int
	height int

	title   string
	fields  []PreviewField
	content *Content
}

// NewPreview creates a new preview pane.
func NewPreview(styles *tui.Styles) *Preview {
	return &Preview{
		styles:  styles,
		content: NewContent(styles),
	}
}

// SetTitle sets the preview title.
func (p *Preview) SetTitle(title string) {
	p.title = title
}

// SetFields sets the key-value metadata fields.
func (p *Preview) SetFields(fields []PreviewField) {
	p.fields = fields
}

// Fields returns the current metadata fields.
func (p *Preview) Fields() []PreviewField {
	return p.fields
}

// SetBody sets the HTML/Markdown body content.
func (p *Preview) SetBody(html string) {
	p.content.SetContent(html)
}

// SetSize updates dimensions.
func (p *Preview) SetSize(w, h int) {
	p.width = w
	p.height = h

	// Content gets remaining height after title + fields
	headerHeight := 1 // title
	if len(p.fields) > 0 {
		headerHeight += len(p.fields) + 1 // fields + blank line
	}
	contentHeight := h - headerHeight
	if contentHeight < 1 {
		contentHeight = 1
	}
	p.content.SetSize(w, contentHeight)
}

// ScrollDown scrolls the body content down.
func (p *Preview) ScrollDown(n int) {
	p.content.ScrollDown(n)
}

// ScrollUp scrolls the body content up.
func (p *Preview) ScrollUp(n int) {
	p.content.ScrollUp(n)
}

// View renders the preview pane.
func (p *Preview) View() string {
	if p.width <= 0 || p.height <= 0 {
		return ""
	}

	theme := p.styles.Theme()
	var sections []string

	// Title
	if p.title != "" {
		sections = append(sections, lipgloss.NewStyle().
			Bold(true).
			Foreground(theme.Primary).
			Width(p.width).
			Render(p.title))
	}

	// Fields — align keys by padding to the widest key
	if len(p.fields) > 0 {
		maxKeyWidth := 0
		for _, f := range p.fields {
			if w := lipgloss.Width(f.Key); w > maxKeyWidth {
				maxKeyWidth = w
			}
		}
		var fieldLines []string
		keyStyle := lipgloss.NewStyle().Foreground(theme.Muted).Width(maxKeyWidth + 1).Align(lipgloss.Right)
		valStyle := lipgloss.NewStyle().Foreground(theme.Foreground)
		for _, f := range p.fields {
			line := keyStyle.Render(f.Key+":") + " " + valStyle.Render(f.Value)
			fieldLines = append(fieldLines, lipgloss.NewStyle().MaxWidth(p.width).Render(line))
		}
		sections = append(sections, strings.Join(fieldLines, "\n"))
	}

	// Body
	body := p.content.View()
	if body != "" {
		sections = append(sections, body)
	}

	return lipgloss.JoinVertical(lipgloss.Left, sections...)
}
