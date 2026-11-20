package richtext

import (
	"regexp"
	"sort"
	"strings"

	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
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
func CollapseAttachments(html string) string {
	return collapseAttachments(html, nil)
}

// CollapseAttachmentsInMarkdown is CollapseAttachments for Markdown: it
// leaves alone an attachment written inside a code span or code block, or
// escaped with a backslash, which shows the markup as text rather than
// writing it.
func CollapseAttachmentsInMarkdown(md string) string {
	if !hasAttachmentTag(md) {
		return md
	}
	code := markdownCodeSpans(md)
	return collapseAttachments(md, func(i int) bool {
		return code.contains(i) || escapedInMarkdown(md, i)
	})
}

func hasAttachmentTag(s string) bool {
	return strings.Contains(strings.ToLower(s), "<bc-attachment")
}

// escapedInMarkdown reports whether the '<' at i follows an odd run of
// backslashes, which makes it a literal character in Markdown.
func escapedInMarkdown(s string, i int) bool {
	n := 0
	for j := i - 1; j >= 0 && s[j] == '\\'; j-- {
		n++
	}
	return n%2 == 1
}

// collapseAttachments collapses every attachment element in s. A tag that
// starts where literal says is text, or inside an HTML comment, is not one.
func collapseAttachments(s string, literal func(int) bool) string {
	if !hasAttachmentTag(s) {
		return s
	}
	if literal == nil {
		literal = func(int) bool { return false }
	}
	comments := htmlComments(s, literal)
	tags := attachmentTags{s: s, lower: asciiLower(s), literal: func(i int) bool {
		return literal(i) || comments.contains(i)
	}}

	var b strings.Builder
	last, pos := 0, 0
	for {
		el, ok := tags.nextElement(pos)
		if !ok {
			break
		}
		pos = el.end
		if el.selfClosing {
			continue
		}
		b.WriteString(s[last:el.start])
		b.WriteString(collapseAttachment(s[el.start:el.end], s[el.start:el.openEnd]))
		last = el.end
	}
	b.WriteString(s[last:])
	return b.String()
}

// htmlComments returns the byte ranges of s inside HTML comments, an
// unterminated one running to the end. A "<!--" that literal says is text
// opens none.
func htmlComments(s string, literal func(int) bool) textSpanIndex {
	var index textSpanIndex
	for from := 0; ; {
		i := strings.Index(s[from:], "<!--")
		if i < 0 {
			return index
		}
		start := from + i
		if literal(start) {
			from = start + 1
			continue
		}
		end := len(s)
		if j := strings.Index(s[start+4:], "-->"); j >= 0 {
			end = start + 4 + j + 3
		}
		index = append(index, textSpan{start: start, end: end})
		from = end
	}
}

// attachmentElement locates one <bc-attachment> in a string: where it starts,
// where its opening tag ends, and where the element ends. A self-closing tag
// ends where its opening tag does.
type attachmentElement struct {
	start, openEnd, end int
	selfClosing         bool
}

const (
	attachmentOpen  = "<bc-attachment"
	attachmentClose = "</bc-attachment"
)

// attachmentTags finds attachment tags in s. lower is s with its ASCII
// letters lowered, byte for byte, and a tag starting where literal says is
// text is passed by.
type attachmentTags struct {
	s, lower string
	literal  func(int) bool
}

// next returns the start of the first tag beginning with prefix at or after
// from, followed by the end of its name or the string.
func (t attachmentTags) next(prefix string, from int) (int, bool) {
	for from < len(t.s) {
		i := strings.Index(t.lower[from:], prefix)
		if i < 0 {
			return 0, false
		}
		start := from + i
		after := start + len(prefix)
		if (after == len(t.s) || isTagNameEnd(t.s[after])) && !t.literal(start) {
			return start, true
		}
		from = after
	}
	return 0, false
}

// nextElement finds the first <bc-attachment> element at or after from. The
// opening tag is read with its quoted attribute values, so a '>' inside one
// does not end it. An element that is opened and never closed, or is followed
// by another before its closing tag, is not one: the search moves past its
// opening tag.
func (t attachmentTags) nextElement(from int) (attachmentElement, bool) {
	for {
		start, ok := t.next(attachmentOpen, from)
		if !ok {
			return attachmentElement{}, false
		}
		after := start + len(attachmentOpen)
		openEnd, ok := openingTagEnd(t.s, after)
		if !ok {
			return attachmentElement{}, false
		}
		if strings.HasSuffix(strings.TrimRight(t.s[after:openEnd-1], " \t\n\r"), "/") {
			return attachmentElement{start: start, openEnd: openEnd, end: openEnd, selfClosing: true}, true
		}
		closeStart, ok := t.next(attachmentClose, openEnd)
		if !ok {
			return attachmentElement{}, false
		}
		if nested, ok := t.next(attachmentOpen, openEnd); ok && nested < closeStart {
			from = openEnd
			continue
		}
		closeEnd := strings.IndexByte(t.s[closeStart:], '>')
		if closeEnd < 0 {
			return attachmentElement{}, false
		}
		return attachmentElement{start: start, openEnd: openEnd, end: closeStart + closeEnd + 1}, true
	}
}

// asciiLower lowers only ASCII letters, so every index into the result is the
// same index into s. strings.ToLower can change a string's length: 'İ' grows
// by a byte.
func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if 'A' <= c && c <= 'Z' {
			b[i] = c + ('a' - 'A')
		}
	}
	return string(b)
}

func isTagNameEnd(c byte) bool {
	return c == '>' || c == '/' || c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f'
}

// openingTagEnd returns the index just past the '>' that ends the tag whose
// attributes start at i, skipping quoted attribute values.
func openingTagEnd(s string, i int) (int, bool) {
	var quote byte
	for ; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '>':
			return i + 1, true
		}
	}
	return 0, false
}

// collapseAttachment reduces one <bc-attachment> element to its write form,
// or returns it unchanged when it names no record.
func collapseAttachment(element, open string) string {
	attrs := reTagAttribute.FindAllString(open[len(attachmentOpen):len(open)-1], -1)

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
