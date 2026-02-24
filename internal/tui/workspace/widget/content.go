package widget

import (
	"strings"

	"github.com/basecamp/basecamp-cli/internal/richtext"
	"github.com/basecamp/basecamp-cli/internal/tui"
)

// Content renders HTML or Markdown content as terminal-styled text.
// It handles the HTML→Markdown→glamour pipeline and provides scrolling.
type Content struct {
	styles *tui.Styles
	width  int
	height int

	raw      string   // original HTML or Markdown
	rendered string   // terminal-rendered output
	lines    []string // rendered lines for scrolling
	offset   int      // scroll offset
}

// NewContent creates a new content renderer.
func NewContent(styles *tui.Styles) *Content {
	return &Content{styles: styles}
}

// SetContent sets the raw HTML or Markdown content and renders it.
// Skips re-render if content is unchanged.
func (c *Content) SetContent(html string) {
	if html == c.raw {
		return
	}
	c.raw = html
	c.offset = 0
	c.render()
}

// SetSize updates dimensions and re-renders only if width changed.
// Height changes affect the viewport but don't require re-rendering.
func (c *Content) SetSize(w, h int) {
	widthChanged := w != c.width
	c.height = h
	if widthChanged {
		c.width = w
		c.render()
	}
}

// ScrollDown scrolls the content down by n lines.
func (c *Content) ScrollDown(n int) {
	maxOffset := len(c.lines) - c.height
	if maxOffset < 0 {
		maxOffset = 0
	}
	c.offset += n
	if c.offset > maxOffset {
		c.offset = maxOffset
	}
}

// ScrollUp scrolls the content up by n lines.
func (c *Content) ScrollUp(n int) {
	c.offset -= n
	if c.offset < 0 {
		c.offset = 0
	}
}

// View renders the visible portion of the content.
func (c *Content) View() string {
	if c.width <= 0 || c.height <= 0 {
		return ""
	}

	if len(c.lines) == 0 {
		return ""
	}

	end := c.offset + c.height
	if end > len(c.lines) {
		end = len(c.lines)
	}

	visible := c.lines[c.offset:end]
	return strings.Join(visible, "\n")
}

func (c *Content) render() {
	if c.raw == "" || c.width <= 0 {
		c.rendered = ""
		c.lines = nil
		return
	}

	// HTML → Markdown → glamour
	md := richtext.HTMLToMarkdown(c.raw)
	rendered, err := richtext.RenderMarkdownWithWidth(md, c.width)
	if err != nil {
		// Fallback: render as plain text
		rendered = md
	}

	c.rendered = rendered
	c.lines = strings.Split(rendered, "\n")
	// Glamour appends a trailing newline; remove the empty last element
	if len(c.lines) > 0 && c.lines[len(c.lines)-1] == "" {
		c.lines = c.lines[:len(c.lines)-1]
	}

	// Clamp offset after render — if new content is shorter, stale offset
	// would produce an empty or incorrect view window.
	maxOffset := len(c.lines) - c.height
	if maxOffset < 0 {
		maxOffset = 0
	}
	if c.offset > maxOffset {
		c.offset = maxOffset
	}
}
