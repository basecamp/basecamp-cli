package workspace

import (
	"strings"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/richtext"
)

// body is a recording's rich text ready to draw: its prose and its pictures in
// the order they appear, and where each of those pictures can be read from.
//
// Every screen that shows rich text holds one of these — a message, a card, a
// comment — so Basecamp's markup is read the same way wherever it turns up.
//
// The split settles when the text arrives rather than when the window changes
// size: it is a property of the words, not of the column they are drawn in.
type body struct {
	parts []bodyPart

	// sources maps a picture's address in the text to the address it can be read
	// from. The two differ: see imageSources.
	sources map[string]string
}

// newBody reads Basecamp's markup as something a terminal can draw.
func newBody(html string, attachments []basecamp.RichTextAttachment) body {
	return body{
		parts:   splitBody(strings.TrimSpace(richtext.HTMLToMarkdown(html))),
		sources: imageSources(attachments),
	}
}

// newBodyFromMarkdown is the same thing for text that has already been
// converted, which is how a message arrives: its board reads the markup once and
// carries the Markdown.
func newBodyFromMarkdown(markdown string, sources map[string]string) body {
	return body{parts: splitBody(markdown), sources: sources}
}

func (b body) empty() bool { return len(b.parts) == 0 }

// text is the whole thing back as one string of Markdown, which is what a form
// that edits it starts from. splitBody cuts on exact boundaries, so joining the
// parts with nothing between them is the text it was split from.
func (b body) text() string {
	pieces := make([]string, 0, len(b.parts))
	for _, part := range b.parts {
		if part.isImage() {
			pieces = append(pieces, part.markdown())
			continue
		}
		pieces = append(pieces, part.text)
	}
	return strings.Join(pieces, "")
}

// source is where a picture in the text is read from, and empty for a part that
// is not a picture or is one with no attachment behind it.
func (b body) source(part bodyPart) string {
	if !part.isImage() {
		return ""
	}
	return b.sources[part.url]
}

// imageSources maps a picture's address in the text to the address it can be
// read from.
//
// The Markdown points at the preview host, which is where a browser reads a
// picture from and is not somewhere this will: accountImageReader reads from the
// account's API host and nowhere else. The same attachment appears in the
// recording's attachments with a download_url on that host, so the text says one
// address and the read uses the other.
func imageSources(attachments []basecamp.RichTextAttachment) map[string]string {
	sources := make(map[string]string, len(attachments))
	for _, file := range attachments {
		if !strings.HasPrefix(strings.ToLower(file.ContentType), "image/") || file.DownloadURL == "" {
			continue
		}
		if file.PreviewURL != "" {
			sources[file.PreviewURL] = file.DownloadURL
		}
		sources[file.DownloadURL] = file.DownloadURL
	}
	return sources
}
