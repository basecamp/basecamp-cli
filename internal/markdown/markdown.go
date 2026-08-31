// Package markdown renders Markdown for the terminal. It wraps glamour for the
// styling and adds OSC 8 hyperlinks so URLs stay clickable.
//
// Ported from hey-cli's internal/markdown, which is where the safety work in
// source.go and contain.go was done. What arrives here is other people's text —
// a chat line, a message, a comment — so it is treated as untrusted the whole
// way through: the source is rewritten before glamour reads it, and the output is
// checked before a terminal sees it.
package markdown

import (
	"strings"
	"sync"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/x/ansi"

	"github.com/basecamp/basecamp-cli/internal/richtext"
)

// DefaultWidth is the word wrap used when the caller has no width of its own.
const DefaultWidth = 80

// maxCachedRenderers bounds the renderers kept, one per wrap width. A TUI being
// resized by hand passes through many widths; a glamour renderer is not small.
const maxCachedRenderers = 8

var (
	renderersMutex sync.Mutex
	renderers      = map[int]*glamour.TermRenderer{}
)

// Render renders Markdown as styled terminal output wrapped to width. It falls
// back to the Markdown source when glamour cannot be set up, so a rendering
// problem costs formatting rather than the message itself.
//
// The Markdown is treated as untrusted on the way in and the output is checked on
// the way out: see prepareSource and contain. The one producer of the Markdown
// this is pointed at is richtext.HTMLToMarkdown, which decodes entities as it
// converts — so a `&` here stands for itself, and prepareSource keeps glamour's
// own decode from reading it as the start of something else.
func Render(md string, width int) string {
	safe, forGlamour, deep := prepareSource(md)
	if strings.TrimSpace(safe) == "" {
		return ""
	}
	if width <= 0 {
		width = DefaultWidth
	}

	renderer, err := cachedRenderer(width)
	if err != nil || deep {
		return contain(richtext.LinkifyURLs(safe))
	}

	out, err := renderer.Render(forGlamour)
	if err != nil {
		return contain(richtext.LinkifyURLs(safe))
	}

	return contain(trimBlankLines(richtext.LinkifyURLs(out)))
}

// trimBlankLines strips the padding glamour adds out to the wrap width, so a
// rendered body carries neither invisible whitespace nor lines that are nothing
// but leftover color codes.
func trimBlankLines(s string) string {
	var lines []string
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(ansi.Strip(line)) == "" {
			if len(lines) > 0 && lines[len(lines)-1] == "" {
				continue
			}
			lines = append(lines, "")
		} else {
			lines = append(lines, strings.TrimRight(line, " "))
		}
	}

	for len(lines) > 0 && lines[0] == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n")
}

func cachedRenderer(width int) (*glamour.TermRenderer, error) {
	renderersMutex.Lock()
	defer renderersMutex.Unlock()

	if renderer, ok := renderers[width]; ok {
		return renderer, nil
	}

	// Newlines are kept because the ones in what reaches here were put there by a
	// person. richtext.HTMLToMarkdown turns a <br> into a bare newline, and
	// CommonMark reads that as a space — so without this, a message written on two
	// lines arrives as one. Nothing is lost either way: the wrap still happens,
	// and a source with no newlines in it renders the same.
	renderer, err := glamour.NewTermRenderer(
		glamour.WithStyles(terminalStyle),
		glamour.WithWordWrap(width),
		glamour.WithPreservedNewLines(),
	)
	if err != nil {
		return nil, err
	}

	// The widths a resize passes through are transient, and there is no order
	// worth keeping among them: when the map is full, start it over rather than
	// grow without bound.
	if len(renderers) >= maxCachedRenderers {
		renderers = map[int]*glamour.TermRenderer{}
	}
	renderers[width] = renderer
	return renderer, nil
}
