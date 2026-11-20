package richtext

import (
	"bytes"
	"regexp"
	"sort"
	"strings"

	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
	"golang.org/x/net/html"
)

// reTagAttribute matches one attribute of a tag: its name, and its value if it
// has one, quoted or not.
var reTagAttribute = regexp.MustCompile(`([^\s"'=<>/]+)(?:\s*=\s*(?:"[^"]*"|'[^']*'|[^\s"'=<>` + "`" + `]+))?`)

// CollapseAttachments returns html with every <bc-attachment> that names a
// record (it has an sgid) reduced to the form Basecamp documents for writing
// one: the tag with nothing inside it and without its content attribute.
//
// Basecamp returns a mention or a file expanded: the person's avatar or the
// file's preview as a <figure> inside the tag, and for a mention its rendered
// HTML in content. Written back as returned, the figure is stored as content
// of its own: it breaks the paragraph around a mention apart, and each edit
// adds another copy. Basecamp expands the bare tag again when it is read, so
// collapsing it loses nothing. An attachment without an sgid carries its
// content itself (a horizontal rule, say) and is left as written, and so is
// one that is self-closing or never closed.
func CollapseAttachments(s string) string {
	if !hasAttachmentTag(s) {
		return s
	}
	return collapseAttachments(s, attachmentTagsIn([]byte(s), []text.Segment{text.NewSegment(0, len(s))}))
}

// CollapseAttachmentsInMarkdown is CollapseAttachments for Markdown. Only an
// attachment tag the Markdown renderer writes as markup counts: one written
// in code, escaped with a backslash, or in an HTML block the renderer shows
// as text is left as written.
func CollapseAttachmentsInMarkdown(md string) string {
	if !hasAttachmentTag(md) {
		return md
	}
	return collapseAttachments(md, markdownAttachmentTags(md))
}

func hasAttachmentTag(s string) bool {
	return strings.Contains(strings.ToLower(s), "<bc-attachment")
}

// attachmentTag is one <bc-attachment> tag: where it starts and ends in the
// string it was read from, which kind of tag it is, and the tag as read. A
// tag can span lines whose prefixes (a blockquote's "> ") are not part of it,
// so the tag as read can differ from the source between start and end.
type attachmentTag struct {
	start, end int
	kind       html.TokenType
	raw        string
}

// collapseAttachments collapses the attachment elements tags pair up in s.
// An opening tag pairs with the next tag when that tag closes it. One that is
// never closed, or is followed by another attachment first, is left as
// written, and so is a self-closing one.
func collapseAttachments(s string, tags []attachmentTag) string {
	var b strings.Builder
	last := 0
	for i := 0; i+1 < len(tags); i++ {
		open, next := tags[i], tags[i+1]
		if open.kind != html.StartTagToken || next.kind != html.EndTagToken {
			continue
		}
		b.WriteString(s[last:open.start])
		b.WriteString(collapseAttachment(s[open.start:next.end], open.raw))
		last = next.end
		i++
	}
	b.WriteString(s[last:])
	return b.String()
}

// attachmentTagsIn reads the segments of source, joined, as HTML and
// returns their attachment tags, placed in source. A tokenizer reads them, so
// markup inside a comment or inside another tag's attribute value is not
// taken for a tag.
func attachmentTagsIn(source []byte, segments []text.Segment) []attachmentTag {
	var joined []byte
	var at []int // at[i] is where joined[i] is in source
	for _, seg := range segments {
		for range seg.Padding {
			joined = append(joined, ' ')
			at = append(at, seg.Start)
		}
		for i := seg.Start; i < seg.Stop; i++ {
			joined = append(joined, source[i])
			at = append(at, i)
		}
	}

	var tags []attachmentTag
	z := html.NewTokenizer(bytes.NewReader(joined))
	for pos := 0; ; {
		kind := z.Next()
		if kind == html.ErrorToken {
			return tags
		}
		size := len(z.Raw())
		isTag := kind == html.StartTagToken || kind == html.EndTagToken || kind == html.SelfClosingTagToken
		if name, _ := z.TagName(); isTag && bytes.Equal(name, []byte("bc-attachment")) {
			tags = append(tags, attachmentTag{
				start: at[pos], end: at[pos+size-1] + 1, kind: kind, raw: string(joined[pos : pos+size]),
			})
		}
		pos += size
	}
}

// markdownAttachmentTags returns the attachment tags in md that its renderer
// writes as markup: inline raw HTML, and the HTML blocks it passes through
// (isRichTextTag, as renderHTMLBlock decides), each read from its segments
// as the renderer reads it. Code, backslash escapes and the HTML blocks it
// escapes hold none.
func markdownAttachmentTags(md string) []attachmentTag {
	source := []byte(md)
	doc := mdConverter.Parser().Parse(text.NewReader(source))

	var tags []attachmentTag
	read := func(segments []text.Segment) {
		tags = append(tags, attachmentTagsIn(source, segments)...)
	}
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch node := n.(type) {
		case *ast.RawHTML:
			read(node.Segments.Sliced(0, node.Segments.Len()))
		case *ast.HTMLBlock:
			lines := node.Lines()
			if isRichTextTag(lines.Value(source)) {
				segments := lines.Sliced(0, lines.Len())
				if node.HasClosure() {
					segments = append(segments, node.ClosureLine)
				}
				read(segments)
			}
			return ast.WalkSkipChildren, nil
		}
		return ast.WalkContinue, nil
	})
	sort.Slice(tags, func(i, j int) bool { return tags[i].start < tags[j].start })
	return tags
}

// collapseAttachment reduces one <bc-attachment> element to its write form,
// or returns it unchanged when it names no record.
func collapseAttachment(element, open string) string {
	attrs := reTagAttribute.FindAllString(open[len("<bc-attachment"):len(open)-1], -1)

	kept := make([]string, 0, len(attrs))
	hasSGID := false
	for _, attr := range attrs {
		name := strings.ToLower(attr)
		if i := strings.IndexAny(name, " \t\n\r="); i >= 0 {
			name = name[:i]
		}
		switch name {
		case "sgid":
			hasSGID = true
		case "content":
			continue
		}
		kept = append(kept, attr)
	}
	if !hasSGID {
		return element
	}
	return "<bc-attachment " + strings.Join(kept, " ") + "></bc-attachment>"
}
