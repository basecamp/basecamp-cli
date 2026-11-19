package richtext

import (
	"regexp"
	"sort"
	"strings"

	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

// reAttachmentElement matches a <bc-attachment> element that has a closing tag,
// capturing its opening tag's attributes. Attribute values are HTML-escaped in
// what the API returns, so none holds a literal '>'.
var reAttachmentElement = regexp.MustCompile(`(?is)<bc-attachment(\s[^>]*)?>.*?</bc-attachment\s*>`)

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
// content itself (a horizontal rule, say) and is left as written.
func CollapseAttachments(html string) string {
	if !strings.Contains(strings.ToLower(html), "<bc-attachment") {
		return html
	}
	return reAttachmentElement.ReplaceAllStringFunc(html, collapseAttachment)
}

// CollapseAttachmentsInMarkdown is CollapseAttachments for Markdown: it
// leaves alone an attachment written inside a code span or code block, which
// shows the markup as text rather than writing it.
func CollapseAttachmentsInMarkdown(md string) string {
	if !strings.Contains(strings.ToLower(md), "<bc-attachment") {
		return md
	}
	code := markdownCodeSpans(md)
	var b strings.Builder
	last := 0
	for _, m := range reAttachmentElement.FindAllStringIndex(md, -1) {
		if code.contains(m[0]) {
			continue
		}
		b.WriteString(md[last:m[0]])
		b.WriteString(collapseAttachment(md[m[0]:m[1]]))
		last = m[1]
	}
	b.WriteString(md[last:])
	return b.String()
}

// collapseAttachment reduces one <bc-attachment> element to its write form,
// or returns it unchanged when it names no record.
func collapseAttachment(element string) string {
	open := element[:strings.IndexByte(element, '>')]
	attrs := reTagAttribute.FindAllString(open[len("<bc-attachment"):], -1)

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

// markdownCodeSpans returns the byte ranges of md that the Markdown parser
// reads as code: code spans, and fenced and indented code blocks.
func markdownCodeSpans(md string) textSpanIndex {
	source := []byte(md)
	doc := mdConverter.Parser().Parse(text.NewReader(source))

	var index textSpanIndex
	add := func(lines *text.Segments) {
		for i := 0; i < lines.Len(); i++ {
			seg := lines.At(i)
			index = append(index, textSpan{start: seg.Start, end: seg.Stop})
		}
	}
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch node := n.(type) {
		case *ast.FencedCodeBlock, *ast.CodeBlock:
			add(node.Lines())
			return ast.WalkSkipChildren, nil
		case *ast.CodeSpan:
			for c := node.FirstChild(); c != nil; c = c.NextSibling() {
				if t, ok := c.(*ast.Text); ok {
					index = append(index, textSpan{start: t.Segment.Start, end: t.Segment.Stop})
				}
			}
			return ast.WalkSkipChildren, nil
		}
		return ast.WalkContinue, nil
	})
	sort.Slice(index, func(i, j int) bool { return index[i].start < index[j].start })
	return index
}
