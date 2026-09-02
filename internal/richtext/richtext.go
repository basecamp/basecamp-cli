// Package richtext provides utilities for converting between Markdown and HTML.
// Rendering Markdown for a terminal is internal/markdown's job.
package richtext

import (
	"bytes"
	"errors"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	east "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	gmhtml "github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// Pre-compiled regexes for IsHTML detection (code span stripping)
var reCodeSpan = regexp.MustCompile("`([^`]+)`")

// Pre-compiled regexes for HTMLToMarkdown (HTML → Markdown block elements)
var (
	reH1         = regexp.MustCompile(`(?i)<h1[^>]*>(.*?)</h1>`)
	reH2         = regexp.MustCompile(`(?i)<h2[^>]*>(.*?)</h2>`)
	reH3         = regexp.MustCompile(`(?i)<h3[^>]*>(.*?)</h3>`)
	reH4         = regexp.MustCompile(`(?i)<h4[^>]*>(.*?)</h4>`)
	reH5         = regexp.MustCompile(`(?i)<h5[^>]*>(.*?)</h5>`)
	reH6         = regexp.MustCompile(`(?i)<h6[^>]*>(.*?)</h6>`)
	reBlockquote = regexp.MustCompile(`(?is)<blockquote[^>]*>(.*?)</blockquote>`)
	reCodeBlock  = regexp.MustCompile(`(?is)<pre[^>]*><code[^>]*(?:class="language-([^"]*)")?[^>]*>(.*?)</code></pre>`)
	reCodeLang   = regexp.MustCompile(`class="language-([^"]*)"`)
	rePreLang    = regexp.MustCompile(`(?i)<pre[^>]*\s+language="([^"]*)"`)
	reCodeInner  = regexp.MustCompile(`(?is)<code[^>]*>([\s\S]*?)</code>`)
	// Tag-match patterns use (?:\s[^>]*)? to require whitespace or `>` after the
	// tag name, preventing false matches against longer tag names with the same
	// prefix (e.g. <p> vs <pre>, <b> vs <br>, <em> vs <embed>, <i> vs <img>,
	// <s> vs <script>, <del> vs <details>, <a> vs <abbr>).
	reP  = regexp.MustCompile(`(?is)<p(?:\s[^>]*)?>(.*?)</p>`)
	reBR = regexp.MustCompile(`(?i)<br\s*/?\s*>`)
	reHR = regexp.MustCompile(`(?i)<hr\s*/?\s*>`)
	// reNbsp matches non-breaking-space entities used as visual filler in
	// otherwise-empty paragraphs (&nbsp;, &#160;, &#xa0; / &#xA0;).
	reNbsp = regexp.MustCompile(`(?i)&nbsp;|&#160;|&#xa0;`)
)

// Pre-compiled regexes for HTMLToMarkdown inline elements
var (
	reHTMLStrong        = regexp.MustCompile(`(?i)<strong(?:\s[^>]*)?>(.*?)</strong>`)
	reHTMLB             = regexp.MustCompile(`(?i)<b(?:\s[^>]*)?>(.*?)</b>`)
	reHTMLEm            = regexp.MustCompile(`(?i)<em(?:\s[^>]*)?>(.*?)</em>`)
	reHTMLI             = regexp.MustCompile(`(?i)<i(?:\s[^>]*)?>(.*?)</i>`)
	reHTMLCode          = regexp.MustCompile(`(?i)<code(?:\s[^>]*)?>(.*?)</code>`)
	reHTMLLink          = regexp.MustCompile(`(?i)<a\s[^>]*href="([^"]*)"[^>]*>(.*?)</a>`)
	reHTMLImgSA         = regexp.MustCompile(`(?i)<img\s[^>]*src="([^"]*)"[^>]*alt="([^"]*)"[^>]*/?\s*>`)
	reHTMLImgAS         = regexp.MustCompile(`(?i)<img\s[^>]*alt="([^"]*)"[^>]*src="([^"]*)"[^>]*/?\s*>`)
	reHTMLImgS          = regexp.MustCompile(`(?i)<img\s[^>]*src="([^"]*)"[^>]*/?\s*>`)
	reHTMLDel           = regexp.MustCompile(`(?i)<del(?:\s[^>]*)?>(.*?)</del>`)
	reHTMLS             = regexp.MustCompile(`(?i)<s(?:\s[^>]*)?>(.*?)</s>`)
	reHTMLStrike        = regexp.MustCompile(`(?i)<strike(?:\s[^>]*)?>(.*?)</strike>`)
	reMentionAttachment = regexp.MustCompile(`(?is)<bc-attachment[^>]*content-type="application/vnd\.basecamp\.mention"[^>]*>(.*?)</bc-attachment>`)
	reMentionFigcaption = regexp.MustCompile(`(?is)<figcaption[^>]*>(.*?)</figcaption>`)
	reMentionImgAlt     = regexp.MustCompile(`(?is)<img[^>]*alt="([^"]+)"[^>]*>`)
	reAttachElement     = regexp.MustCompile(`(?is)<bc-attachment((?:\s[^>]*)?)>.*?</bc-attachment\s*>`)
	reAttachment        = regexp.MustCompile(`(?i)<bc-attachment[^>]*filename="([^"]*)"[^>]*/?\s*>`)
	reAttachNoFile      = regexp.MustCompile(`(?i)<bc-attachment[^>]*/?\s*>`)
	reAttachClose       = regexp.MustCompile(`(?i)</bc-attachment>`)
	// Single quotes as well as double: an embed's own markup goes in a
	// content='…' attribute, because the markup is full of double quotes.
	reAttachAttr   = regexp.MustCompile(`(?is)\b([a-z-]+)=(?:"([^"]*)"|'([^']*)')`)
	reAttachHref   = regexp.MustCompile(`(?is)href="([^"]*)"`)
	reStripTags    = regexp.MustCompile(`<[^>]+>`)
	reMultiNewline = regexp.MustCompile(`\n{3,}`)
)

// reMentionInput matches @Name or @First.Last in user input.
// Group 1: prefix character (whitespace, >, (, [, ", ', or empty at start of string).
// Group 2: the @mention itself.
// Uses Unicode letter/digit classes to support non-ASCII names (e.g., @José, @Zoë).
// Does not match mid-word (e.g., user@example.com).
var reMentionInput = regexp.MustCompile(`(^|[\s>(\["'])(@[\pL\pN_]+(?:\.[\pL\pN_]+)*)`)

// reMentionAnchor matches Markdown-style mention anchors after HTML conversion.
// Group 1: scheme (mention or person).
// Group 2: value (SGID for mention:, person ID for person:).
// Group 3: display text (may include leading @).
var reMentionAnchor = regexp.MustCompile(`<a href="(mention|person):([^"]+)">([^<]*)</a>`)

// reMentionMarkdownLink matches a literal Markdown mention link that was never
// converted to an <a> anchor — this happens when the link was embedded inside an
// author-supplied HTML block, which MarkdownToHTML passes through verbatim.
// Group 1: display text including the leading @.
// Group 2: scheme (mention or person).
// Group 3: value (SGID for mention:, person ID for person:).
// SGIDs and person IDs use the same base64-safe character set as inline SGIDs.
// Excluding '<' from display text prevents a match from spanning HTML elements.
var reMentionMarkdownLink = regexp.MustCompile(`\[(@[^\]<]+)\]\((mention|person):([\w+=/-]+)\)`)

// reSGIDMention matches inline @sgid:VALUE syntax.
// Group 1: prefix character.
// Group 2: the full @sgid:VALUE token.
// Group 3: the SGID value (base64-safe characters).
var reSGIDMention = regexp.MustCompile(`(^|[\s>(\["'])(@sgid:([\w+=/-]+))`)

// Pre-compiled regexes for IsHTML detection
var (
	reSafeTag     = regexp.MustCompile(`<(p|div|span|a|strong|b|em|i|code|pre|ul|ol|li|h[1-6]|blockquote|br|hr|img|table|bc-attachment)\b[^>]*>`)
	reFencedBlock = regexp.MustCompile("(?m)^```[^\n]*\n[\\s\\S]*?^```")
)

// Pre-compiled regexes for IsMarkdown detection
var reMarkdownPatterns = []*regexp.Regexp{
	regexp.MustCompile(`^#{1,6}\s`),
	regexp.MustCompile(`\*\*[^*]+\*\*`),
	regexp.MustCompile(`\*[^*]+\*`),
	regexp.MustCompile(`\[[^\]]+\]\([^)]+\)`),
	regexp.MustCompile("```"),
	regexp.MustCompile(`^[-*+]\s`),
	regexp.MustCompile(`^\d+\.\s`),
	regexp.MustCompile(`^>\s`),
}

// mdConverter is the goldmark Markdown-to-HTML converter configured for Trix compatibility.
var mdConverter = goldmark.New(
	goldmark.WithExtensions(
		extension.Strikethrough,
		// Emit the whitelisted `align` attribute rather than a `style` attribute:
		// BC3's sanitizer keeps `align` but strips inline `style` (only color and
		// background-color survive), so GFM column alignment is preserved through
		// sanitization. Bare <table> is correct — BC3's WrapTablesFilter wraps it.
		extension.NewTable(
			extension.WithTableCellAlignMethod(extension.TableCellAlignAttribute),
		),
	),
	goldmark.WithRendererOptions(gmhtml.WithUnsafe()),
	goldmark.WithParserOptions(
		parser.WithInlineParsers(
			util.Prioritized(&escapedAtParser{}, 900),
		),
		parser.WithASTTransformers(
			util.Prioritized(&trixTransformer{}, 100),
		),
	),
	goldmark.WithRendererOptions(
		renderer.WithNodeRenderers(
			util.Prioritized(&trixRenderer{}, 500),
		),
	),
)

// paragraphSeparator is the blank line Basecamp's editor itself stores between
// two blocks. A bare top-level <br> is not a block in the editor's document
// model, so it is discarded the first time someone edits the content and the
// spacing disappears; an empty paragraph survives the round trip.
const paragraphSeparator = "<p><br></p>"

// TrixBreak is a custom block node that renders the blank line between blocks:
// an empty paragraph at the top level, a <br> inside a block (see
// renderTrixBreak).
type TrixBreak struct{ ast.BaseBlock }

// KindTrixBreak is the node kind for TrixBreak.
var KindTrixBreak = ast.NewNodeKind("TrixBreak")

func (n *TrixBreak) Kind() ast.NodeKind            { return KindTrixBreak }
func (n *TrixBreak) Dump(source []byte, level int) { ast.DumpHelper(n, source, level, nil, nil) }

// EscapedAt is a custom inline node that renders as literal \@.
type EscapedAt struct{ ast.BaseInline }

// KindEscapedAt is the node kind for EscapedAt.
var KindEscapedAt = ast.NewNodeKind("EscapedAt")

func (n *EscapedAt) Kind() ast.NodeKind            { return KindEscapedAt }
func (n *EscapedAt) Dump(source []byte, level int) { ast.DumpHelper(n, source, level, nil, nil) }

// escapedAtParser intercepts \@ before goldmark's standard backslash escape handling.
type escapedAtParser struct{}

func (p *escapedAtParser) Trigger() []byte { return []byte{'\\'} }

func (p *escapedAtParser) Parse(_ ast.Node, block text.Reader, _ parser.Context) ast.Node {
	line, _ := block.PeekLine()
	if len(line) < 2 || line[0] != '\\' || line[1] != '@' {
		return nil
	}
	block.Advance(2)
	return &EscapedAt{}
}

// trixTransformer modifies the AST for Trix-compatible HTML output.
type trixTransformer struct{}

func (t *trixTransformer) Transform(node *ast.Document, reader text.Reader, pc parser.Context) {
	// Phase 1: Force tight lists, convert soft breaks to hard in list items,
	// and unwrap blockquote paragraphs
	_ = ast.Walk(node, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch v := n.(type) {
		case *ast.List:
			v.IsTight = true
			for li := v.FirstChild(); li != nil; li = li.NextSibling() {
				replaceParagraphsWithTextBlocks(li)
				convertSoftBreaksToHard(li)
			}
		case *ast.Blockquote:
			replaceParagraphsWithTextBlocks(v)
			convertSoftBreaksToHard(v)
			insertBreaksBetweenTextBlocks(v)
		}
		return ast.WalkContinue, nil
	})

	// Phase 2: Insert TrixBreak nodes before blank-line-separated top-level blocks
	for child := node.FirstChild(); child != nil; child = child.NextSibling() {
		if child.PreviousSibling() != nil && hasBlankPreviousLines(child, reader.Source()) {
			br := &TrixBreak{}
			node.InsertBefore(node, child, br)
		}
	}
}

// hasBlankPreviousLines reports whether a top-level block was separated from
// the previous block by a blank line. For most blocks this is the parser's own
// flag. Tables are the exception: goldmark's table extension builds the Table
// node inside a paragraph transformer that replaces the source paragraph
// without carrying its blank-previous-lines flag over, so the flag is always
// false and a blank-line-separated table would lose its separator. Recover the
// answer from the source instead: the table's Pos is the start of the replaced
// paragraph's first line, so the table was blank-line-separated exactly when
// the line above that position is blank.
//
// A table that interrupted a paragraph ("Intro.\n| a | b |\n|---|---|") is the
// case the flag being false is right about: the transformer leaves the leading
// lines behind as a paragraph sharing the table's Pos, so the line above Pos
// belongs to whatever preceded that paragraph, not to the table. Detect it by
// that shared Pos and keep the table attached.
func hasBlankPreviousLines(child ast.Node, source []byte) bool {
	table, ok := child.(*east.Table)
	if !ok {
		return child.HasBlankPreviousLines()
	}
	if p, isPara := child.PreviousSibling().(*ast.Paragraph); isPara && p.Pos() == table.Pos() {
		return false
	}
	return precedingLineIsBlank(source, table.Pos())
}

// precedingLineIsBlank reports whether the line immediately above pos in
// source is blank — empty or whitespace-only.
func precedingLineIsBlank(source []byte, pos int) bool {
	i := pos - 1
	if i >= 0 && source[i] == '\n' {
		i--
	}
	if i >= 0 && source[i] == '\r' {
		i--
	}
	for ; i >= 0 && source[i] != '\n'; i-- {
		if c := source[i]; c != ' ' && c != '\t' && c != '\r' {
			return false
		}
	}
	return true
}

func replaceParagraphsWithTextBlocks(parent ast.Node) {
	for child := parent.FirstChild(); child != nil; {
		next := child.NextSibling()
		if p, ok := child.(*ast.Paragraph); ok {
			tb := ast.NewTextBlock()
			for gc := p.FirstChild(); gc != nil; {
				gnext := gc.NextSibling()
				tb.AppendChild(tb, gc)
				gc = gnext
			}
			tb.SetLines(p.Lines())
			parent.ReplaceChild(parent, p, tb)
		}
		child = next
	}
}

func convertSoftBreaksToHard(parent ast.Node) {
	_ = ast.Walk(parent, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		if t, ok := n.(*ast.Text); ok && t.SoftLineBreak() {
			t.SetSoftLineBreak(false)
			t.SetHardLineBreak(true)
		}
		return ast.WalkContinue, nil
	})
}

func insertBreaksBetweenTextBlocks(parent ast.Node) {
	for child := parent.FirstChild(); child != nil; child = child.NextSibling() {
		if _, ok := child.(*ast.TextBlock); ok {
			if next := child.NextSibling(); next != nil {
				if _, ok := next.(*ast.TextBlock); ok {
					br := &TrixBreak{}
					parent.InsertAfter(parent, child, br)
				}
			}
		}
	}
}

// trixRenderer provides custom rendering for Trix-compatible HTML output.
type trixRenderer struct{}

func (r *trixRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(ast.KindRawHTML, r.renderRawHTML)
	reg.Register(ast.KindHTMLBlock, r.renderHTMLBlock)
	reg.Register(ast.KindBlockquote, r.renderBlockquote)
	reg.Register(ast.KindFencedCodeBlock, r.renderFencedCodeBlock)
	reg.Register(KindTrixBreak, r.renderTrixBreak)
	reg.Register(KindEscapedAt, r.renderEscapedAt)
}

func (r *trixRenderer) renderBlockquote(w util.BufWriter, _ []byte, _ ast.Node, entering bool) (ast.WalkStatus, error) {
	if entering {
		_, _ = w.WriteString("<blockquote>")
	} else {
		_, _ = w.WriteString("</blockquote>\n")
	}
	return ast.WalkContinue, nil
}

func (r *trixRenderer) renderRawHTML(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	n, ok := node.(*ast.RawHTML)
	if !ok {
		return ast.WalkContinue, nil
	}
	for i := 0; i < n.Segments.Len(); i++ {
		seg := n.Segments.At(i)
		_, _ = w.Write(util.EscapeHTML(seg.Value(source)))
	}
	return ast.WalkContinue, nil
}

func (r *trixRenderer) renderHTMLBlock(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	n, ok := node.(*ast.HTMLBlock)
	if !ok {
		return ast.WalkContinue, nil
	}
	lines := n.Lines()
	parts := make([]string, 0, lines.Len()+1)
	for i := 0; i < lines.Len(); i++ {
		seg := lines.At(i)
		escaped := strings.TrimRight(string(util.EscapeHTML(seg.Value(source))), "\n")
		parts = append(parts, escaped)
	}
	if n.HasClosure() {
		escaped := strings.TrimRight(string(util.EscapeHTML(n.ClosureLine.Value(source))), "\n")
		parts = append(parts, escaped)
	}
	_, _ = w.WriteString("<p>" + strings.Join(parts, " ") + "</p>\n")
	return ast.WalkContinue, nil
}

// renderFencedCodeBlock emits <pre language="X"><code>...</code></pre> for syntax
// highlighting in BC5. The SyntaxHighlightFilter looks for the language attribute
// on <pre>, not class="language-X" on <code> (the CommonMark default).
func (r *trixRenderer) renderFencedCodeBlock(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	n, ok := node.(*ast.FencedCodeBlock)
	if !ok {
		return ast.WalkContinue, nil
	}
	if entering {
		if language := n.Language(source); language != nil {
			_, _ = w.WriteString(`<pre language="`)
			_, _ = w.Write(util.EscapeHTML(language))
			_, _ = w.WriteString(`"><code>`)
		} else {
			_, _ = w.WriteString("<pre><code>")
		}
		lines := n.Lines()
		for i := 0; i < lines.Len(); i++ {
			line := lines.At(i)
			_, _ = w.Write(util.EscapeHTML(line.Value(source)))
		}
	} else {
		_, _ = w.WriteString("</code></pre>\n")
	}
	return ast.WalkContinue, nil
}

// renderTrixBreak emits an empty paragraph for a top-level break and a <br> for
// one inside a block. Only the top level needs a block-level separator:
// a <br> nested in a blockquote is inline content, which survives editing.
func (r *trixRenderer) renderTrixBreak(w util.BufWriter, _ []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	if parent := node.Parent(); parent != nil && parent.Kind() == ast.KindDocument {
		_, _ = w.WriteString(paragraphSeparator + "\n")
	} else {
		_, _ = w.WriteString("<br>\n")
	}
	return ast.WalkContinue, nil
}

func (r *trixRenderer) renderEscapedAt(w util.BufWriter, _ []byte, _ ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	_, _ = w.WriteString(`\@`)
	return ast.WalkContinue, nil
}

// MarkdownToHTML converts Markdown text to HTML suitable for Basecamp's rich text fields.
// It uses goldmark with custom AST transformations for Trix editor compatibility.
// If the input already appears to be HTML, it is passed through with existing
// formatting preserved, except that a separator is inserted between directly
// adjacent paragraph blocks (see insertParagraphSeparators).
func MarkdownToHTML(md string) string {
	if md == "" {
		return ""
	}

	if IsHTML(md) {
		return insertParagraphSeparators(md)
	}

	md = strings.ReplaceAll(md, "\r\n", "\n")
	md = strings.ReplaceAll(md, "\r", "\n")

	var buf bytes.Buffer
	if err := mdConverter.Convert([]byte(md), &buf); err != nil {
		return "<p>" + html.EscapeString(md) + "</p>"
	}

	return strings.TrimSpace(buf.String())
}

// insertParagraphSeparators puts an empty separator paragraph between directly
// adjacent, non-empty paragraph blocks so that HTML supplied to the CLI renders
// with visible paragraph spacing.
//
// Basecamp's rich text relies on explicit separator nodes for paragraph
// spacing, not CSS margins: contiguous <p>A</p><p>B</p> renders squished. The
// Markdown pipeline already inserts a separator between blank-line-separated
// paragraphs (via TrixBreak), so this brings the raw-HTML passthrough into line
// with that behavior. Unlike Basecamp's editor, the CLI has no concept of an
// intentionally-tight single-line-break paragraph (its edit loop collapses that
// distinction), so contiguous paragraphs from HTML input are treated as
// separate paragraphs.
//
// The transform is byte-preserving apart from the inserted separators and is
// idempotent: a boundary that already carries a separator — a bare <br> between
// the paragraphs, or an empty separator paragraph (<p><br></p> or <p></p>) on
// either side — is left untouched, so running it on already-separated content
// (including Basecamp editor output) is a no-op. Separators the caller supplied
// are left as they came: this matching is not nesting-aware, and a <br> the
// caller put between two paragraphs inside a blockquote is legal inline content
// that survives editing. Only directly adjacent <p> blocks are separated;
// anything else between them, such as a heading, list, or attachment, already
// provides its own break and is left alone.
func insertParagraphSeparators(s string) string {
	locs := reP.FindAllStringIndex(s, -1)
	if len(locs) < 2 {
		return s
	}

	empty := make([]bool, len(locs))
	for i, loc := range locs {
		empty[i] = isEmptyParagraph(s[loc[0]:loc[1]])
	}

	var b strings.Builder
	b.Grow(len(s) + len(locs)*4)
	cursor := 0
	for i := 0; i < len(locs); i++ {
		end := locs[i][1]
		b.WriteString(s[cursor:end])
		cursor = end

		if i+1 == len(locs) {
			break
		}

		nextStart := locs[i+1][0]
		gap := s[end:nextStart]
		if !empty[i] && !empty[i+1] && strings.TrimSpace(gap) == "" {
			b.WriteString(gap)
			b.WriteString(paragraphSeparator)
			cursor = nextStart
		}
	}
	b.WriteString(s[cursor:])
	return b.String()
}

// isEmptyParagraph reports whether a <p>...</p> block has no visible content —
// i.e. it is empty or contains only <br> tags and whitespace, including
// non-breaking-space entities (&nbsp;, &#160;, &#xa0;) that rich text editors
// commonly use for blank separator lines. Such paragraphs act as separators, so
// no additional one is inserted adjacent to them.
func isEmptyParagraph(block string) bool {
	m := reP.FindStringSubmatch(block)
	if m == nil {
		return false
	}
	inner := reBR.ReplaceAllString(m[1], "")
	inner = reNbsp.ReplaceAllString(inner, "")
	return strings.TrimSpace(inner) == ""
}

// PlainToHTML serializes literal plain text as Basecamp rich text. HTML-special
// characters are escaped so they render as typed (not interpreted as markup),
// and the line structure is kept in the one shape Basecamp's editor preserves
// across an edit: each run of non-blank lines becomes a <p> with single line
// breaks as <br> between its lines, and each blank line between runs becomes an
// empty paragraph (paragraphSeparator).
//
// The editor drops a root-level <br> on import and keeps a <br> only when it
// sits between two text runs inside a block, so neither bare <br> between
// lines nor <br><br> for a blank line survives the first edit in Basecamp.
// Leading and trailing blank lines are dropped — they have no paragraphs to
// separate — matching the Markdown path; a whitespace-only line counts as
// blank. Windows CRLF and bare CR are normalized to LF first. Use this when the
// caller wants the text delivered verbatim to an endpoint that always stores
// rich text.
func PlainToHTML(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	lines := trimBlankLines(strings.Split(escapeHTML(s), "\n"))

	var b strings.Builder
	var run []string
	flush := func() {
		if len(run) > 0 {
			b.WriteString("<p>" + strings.Join(run, "<br>") + "</p>")
			run = run[:0]
		}
	}
	for _, line := range lines {
		if isBlankLine(line) {
			flush()
			b.WriteString(paragraphSeparator)
		} else {
			run = append(run, line)
		}
	}
	flush()
	return b.String()
}

// trimBlankLines drops leading and trailing blank lines.
func trimBlankLines(lines []string) []string {
	start, end := 0, len(lines)
	for start < end && isBlankLine(lines[start]) {
		start++
	}
	for end > start && isBlankLine(lines[end-1]) {
		end--
	}
	return lines[start:end]
}

func isBlankLine(line string) bool {
	return strings.TrimSpace(line) == ""
}

// escapeHTML escapes special HTML characters.
func escapeHTML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

// escapeAttr escapes characters for use in HTML attributes, including quotes.
func escapeAttr(s string) string {
	s = escapeHTML(s)
	s = strings.ReplaceAll(s, `"`, "&quot;")
	s = strings.ReplaceAll(s, "'", "&#39;")
	return s
}

// HTMLToMarkdown converts HTML content to Markdown.
// This is useful for displaying Basecamp's rich text content in the terminal.
func HTMLToMarkdown(html string) string {
	if html == "" {
		return ""
	}

	// Normalize whitespace
	html = strings.TrimSpace(html)

	// Handle block elements first (order matters)
	// Headings
	html = reH1.ReplaceAllString(html, "# $1\n\n")
	html = reH2.ReplaceAllString(html, "## $1\n\n")
	html = reH3.ReplaceAllString(html, "### $1\n\n")
	html = reH4.ReplaceAllString(html, "#### $1\n\n")
	html = reH5.ReplaceAllString(html, "##### $1\n\n")
	html = reH6.ReplaceAllString(html, "###### $1\n\n")

	// Blockquotes — convert inner block elements (lists, code, paragraphs) to
	// Markdown first, then prefix each line with >. Loop handles nesting:
	// the lazy regex matches outermost open → innermost close, so each pass
	// converts one level and the next pass handles the enclosing level.
	convertBlockquote := func(s string) string {
		inner := reBlockquote.FindStringSubmatch(s)
		if len(inner) >= 2 {
			content := blockquoteInnerToMarkdown(inner[1])
			lines := strings.Split(content, "\n")
			result := make([]string, 0, len(lines))
			for _, line := range lines {
				if line == "" {
					result = append(result, ">")
				} else {
					result = append(result, "> "+line)
				}
			}
			return strings.Join(result, "\n") + "\n\n"
		}
		return s
	}
	for reBlockquote.MatchString(html) {
		html = reBlockquote.ReplaceAllStringFunc(html, convertBlockquote)
	}

	// Code blocks
	html = reCodeBlock.ReplaceAllStringFunc(html, func(s string) string {
		return convertCodeBlockHTML(s) + "\n\n"
	})

	// Lists — use balanced-tag replacement to handle nesting correctly.
	// Runs before the parked table pass below: list items convert their
	// tables inline (see formatListItem) so each pipe row picks up the item
	// indent a parked one-line placeholder would deny it.
	html = replaceBalancedListBlocks(html)

	// Tables — convert to GFM pipe tables while row and cell tags are still
	// intact. Must run before the paragraph, line-break, and tag-stripping
	// passes below, which would otherwise smear cell text together. The
	// emitted Markdown is parked behind placeholders until every later pass
	// has run: cell text is fully entity-decoded and escaped, so the
	// document-level unescape would double-decode it (and could conjure
	// unescaped pipes out of encoded ones).
	var tables []string
	html = reTableBlock.ReplaceAllStringFunc(html, func(s string) string {
		md := convertTableHTML(s)
		if md == "" {
			return "\n\n"
		}
		tables = append(tables, md)
		return "\x00tbl" + strconv.Itoa(len(tables)-1) + "\x00\n\n"
	})

	// Paragraphs
	html = reP.ReplaceAllString(html, "$1\n\n")

	// Line breaks
	html = reBR.ReplaceAllString(html, "\n")

	// Horizontal rules
	html = reHR.ReplaceAllString(html, "\n---\n\n")

	// Inline elements
	// Bold
	html = reHTMLStrong.ReplaceAllString(html, "**$1**")
	html = reHTMLB.ReplaceAllString(html, "**$1**")

	// Italic
	html = reHTMLEm.ReplaceAllString(html, "*$1*")
	html = reHTMLI.ReplaceAllString(html, "*$1*")

	// Inline code
	html = reHTMLCode.ReplaceAllString(html, "`$1`")

	// Links
	html = reHTMLLink.ReplaceAllString(html, "[$2]($1)")

	// Images
	html = reHTMLImgSA.ReplaceAllString(html, "![$2]($1)")
	html = reHTMLImgAS.ReplaceAllString(html, "![$1]($2)")
	html = reHTMLImgS.ReplaceAllString(html, "![]($1)")

	// Strikethrough
	html = reHTMLDel.ReplaceAllString(html, "~~$1~~")
	html = reHTMLS.ReplaceAllString(html, "~~$1~~")
	html = reHTMLStrike.ReplaceAllString(html, "~~$1~~")

	// @-mentions: extract display text, render as bold (must fire before general attachment regex)
	html = reMentionAttachment.ReplaceAllStringFunc(html, mentionMarkdown)

	// Basecamp attachments that wrap a <figure>: converted whole, from their own
	// attributes, before the tag-stripping pass below can get at their insides.
	html = reAttachElement.ReplaceAllStringFunc(html, attachmentMarkdown)

	// Basecamp attachments: <bc-attachment ... filename="report.pdf"> → 📎 report.pdf
	html = reAttachment.ReplaceAllString(html, "\n📎 $1\n")
	// Closing bc-attachment tags (e.g. </bc-attachment>)
	html = reAttachClose.ReplaceAllString(html, "")
	// Remaining bc-attachment tags without filename
	html = reAttachNoFile.ReplaceAllString(html, "\n📎 attachment\n")

	// Remove remaining HTML tags
	html = reStripTags.ReplaceAllString(html, "")

	// Unescape HTML entities
	html = unescapeHTML(html)

	// Clean up multiple newlines
	html = reMultiNewline.ReplaceAllString(html, "\n\n")

	// Restore the parked tables now that no pass can touch their content.
	for i, table := range tables {
		html = strings.Replace(html, "\x00tbl"+strconv.Itoa(i)+"\x00", table, 1)
	}

	return strings.TrimSpace(html)
}

// attachmentMarkdown converts one whole <bc-attachment> element to Markdown,
// reading its own attributes rather than what it wraps.
//
// The element carries everything worth saying — content-type, url, filename,
// caption — and wraps a <figure> holding an <img> and a <figcaption> that
// repeat it. Converting the element as a unit drops that duplication, and it
// drops the whitespace with it: the editor pretty-prints the figure, and four
// spaces of HTML indentation left behind by a tag-stripping pass is an indented
// code block in Markdown, which is why an image used to print as its own
// literal ![](…).
//
// An image becomes an image, so a terminal that draws pictures has something to
// draw and one that doesn't shows the caption. Everything else is a paperclip
// and a filename, which is all a terminal can offer for a PDF.
func attachmentMarkdown(s string) string {
	attrs := map[string]string{}
	if match := reAttachElement.FindStringSubmatch(s); len(match) >= 2 {
		for _, attr := range reAttachAttr.FindAllStringSubmatch(match[1], -1) {
			value := attr[2]
			if value == "" {
				value = attr[3]
			}
			attrs[strings.ToLower(attr[1])] = unescapeHTML(value)
		}
	}

	filename := strings.TrimSpace(attrs["filename"])
	caption := strings.TrimSpace(attrs["caption"])

	// An embedded post — a tweet, a video, a Figma frame — is an iframe on the
	// web and nothing a terminal can draw. What it is is a link, so that is what
	// it becomes.
	if strings.HasPrefix(attrs["content-type"], "embed/") {
		return embedMarkdown(attrs["content"], caption)
	}

	if source := strings.TrimSpace(attrs["url"]); source != "" && strings.HasPrefix(attrs["content-type"], "image/") {
		// The alt text is the caption when there is one and the filename
		// otherwise, so the line says something either way.
		alt := caption
		if alt == "" {
			alt = filename
		}
		return "\n![" + escapeAltText(alt) + "](" + source + ")\n"
	}

	if filename == "" {
		filename = "attachment"
	}
	if caption != "" && caption != filename {
		return "\n📎 " + filename + " — " + caption + "\n"
	}
	return "\n📎 " + filename + "\n"
}

// embedMarkdown turns an embedded post into a link to it.
//
// The address comes out of the embed's own markup rather than off the element:
// the element's iframe points at Basecamp's embed proxy, which is not somewhere
// a reader wants to go. An oEmbed blockquote carries the post's text and ends
// with a link to the post itself, so the last link that is not a shortener is
// the permalink — a t.co is a link to another link.
func embedMarkdown(content, caption string) string {
	var address string
	for _, href := range reAttachHref.FindAllStringSubmatch(content, -1) {
		found := strings.TrimSpace(unescapeHTML(href[1]))
		if !strings.HasPrefix(found, "http") || isLinkShortener(found) {
			continue
		}
		address = found
	}
	if address == "" {
		return "\n🔗 embed\n"
	}

	label := caption
	if label == "" {
		label = strings.TrimSpace(reWhitespaceRun.ReplaceAllString(
			unescapeHTML(reStripTags.ReplaceAllString(unescapeHTML(content), " ")), " "))
	}
	if label == "" {
		label = address
	}
	return "\n🔗 [" + escapeAltText(truncateWords(label, 120)) + "](" + address + ")\n"
}

// isLinkShortener reports whether an address only stands for another one, which
// makes it the wrong thing to show a reader.
func isLinkShortener(address string) bool {
	parsed, err := url.Parse(address)
	if err != nil {
		return false
	}
	switch strings.ToLower(strings.TrimPrefix(parsed.Host, "www.")) {
	case "t.co", "bit.ly", "buff.ly", "ow.ly", "tinyurl.com", "goo.gl":
		return true
	}
	return false
}

// truncateWords cuts a label at a word rather than mid-word, so a long embed
// reads as a sentence that stops rather than one that breaks.
func truncateWords(s string, most int) string {
	if len(s) <= most {
		return s
	}

	cut := s[:most]
	// Already on a boundary, so the cut takes no word with it. Backing up here
	// would throw away a word that fitted exactly.
	if s[most] != ' ' {
		if at := strings.LastIndex(cut, " "); at > most/2 {
			cut = cut[:at]
		}
	}
	return strings.TrimSpace(cut) + "…"
}

// escapeAltText keeps a caption from closing the image's own brackets.
func escapeAltText(alt string) string {
	alt = strings.ReplaceAll(alt, `\`, `\\`)
	alt = strings.ReplaceAll(alt, "[", `\[`)
	return strings.ReplaceAll(alt, "]", `\]`)
}

// mentionMarkdown converts one <bc-attachment> mention element to bold
// Markdown, extracting the display name from the figcaption, the image alt,
// or the remaining text, in that order.
func mentionMarkdown(s string) string {
	inner := ""
	if match := reMentionAttachment.FindStringSubmatch(s); len(match) >= 2 {
		inner = match[1]
	}

	name := ""
	if match := reMentionFigcaption.FindStringSubmatch(inner); len(match) >= 2 {
		name = strings.TrimSpace(unescapeHTML(reStripTags.ReplaceAllString(match[1], "")))
	}
	if name == "" {
		if match := reMentionImgAlt.FindStringSubmatch(inner); len(match) >= 2 {
			name = strings.TrimSpace(unescapeHTML(match[1]))
		}
	}
	if name == "" {
		name = strings.TrimSpace(unescapeHTML(reStripTags.ReplaceAllString(inner, "")))
	}
	if name == "" {
		name = "mention"
	}
	if !strings.HasPrefix(name, "@") {
		name = "@" + name
	}
	return "**" + name + "**"
}

// Pre-compiled regexes for HTML table conversion. BC3 rich text is sanitized
// editor output, not arbitrary email HTML: tables are always flat
// editor-authored grids (no nesting, no layout tables), so a non-greedy block
// match is safe and every <table> converts — none are skipped.
var (
	reTableBlock     = regexp.MustCompile(`(?is)<table(?:\s[^>]*)?>.*?</table\s*>`)
	reTableRowHTML   = regexp.MustCompile(`(?is)<tr(?:\s[^>]*)?>(.*?)</tr\s*>`)
	reTableCellHTML  = regexp.MustCompile(`(?is)<t[hd]((?:\s[^>]*)?)>(.*?)</t[hd]\s*>`)
	reTableCellAlign = regexp.MustCompile(`(?i)\balign="(left|center|right)"`)
	reTableCaption   = regexp.MustCompile(`(?is)<caption(?:\s[^>]*)?>(.*?)</caption\s*>`)
	reWhitespaceRun  = regexp.MustCompile(`\s+`)
)

// convertTableHTML converts one <table> block to a GFM pipe table. The first
// row is the header whether its cells are <th> or <td> (GFM has no headerless
// tables), with its align attributes — what MarkdownToHTML emits for GFM
// column alignment — mapped back to :--- / :---: / ---: markers. The widest
// row sizes the table and narrower rows are padded with empty cells. Cells
// carrying colspan/rowspan emit as ordinary cells: a merged grid displays
// better flattened than smeared, and editing such tables stays guarded by
// HasComplexTableHTML.
func convertTableHTML(table string) string {
	caption := ""
	if m := reTableCaption.FindStringSubmatch(table); m != nil {
		caption = cellMarkdown(m[1])
	}

	var rows [][]string
	var aligns []string
	for _, row := range reTableRowHTML.FindAllStringSubmatch(table, -1) {
		cells := reTableCellHTML.FindAllStringSubmatch(row[1], -1)
		if len(cells) == 0 {
			continue
		}
		texts := make([]string, 0, len(cells))
		for _, cell := range cells {
			texts = append(texts, cellMarkdown(cell[2]))
		}
		if rows == nil {
			aligns = make([]string, 0, len(cells))
			for _, cell := range cells {
				aligns = append(aligns, cellAlign(cell[1]))
			}
		}
		rows = append(rows, texts)
	}
	if rows == nil {
		return caption
	}

	// Size the table to its widest row, not just the header: truncating a
	// wider later row would silently drop cell data.
	width := 0
	for _, row := range rows {
		width = max(width, len(row))
	}
	separators := make([]string, width)
	for i := range separators {
		switch {
		case i < len(aligns) && aligns[i] == "left":
			separators[i] = ":---"
		case i < len(aligns) && aligns[i] == "center":
			separators[i] = ":---:"
		case i < len(aligns) && aligns[i] == "right":
			separators[i] = "---:"
		default:
			separators[i] = "---"
		}
	}

	pipeRow := func(cells []string) string {
		for len(cells) < width {
			cells = append(cells, "")
		}
		return "| " + strings.Join(cells, " | ") + " |"
	}
	lines := make([]string, 0, len(rows)+1)
	lines = append(lines, pipeRow(rows[0]), pipeRow(separators))
	for _, row := range rows[1:] {
		lines = append(lines, pipeRow(row))
	}
	out := strings.Join(lines, "\n")
	// GFM has no table captions; emit the text as a paragraph above the grid
	// rather than dropping user-visible content. Caption-bearing tables stay
	// display-only (HasComplexTableHTML), so this never has to round-trip.
	if caption != "" {
		out = caption + "\n\n" + out
	}
	return out
}

// cellAlign extracts the whitelisted align attribute value from a cell's
// open-tag attributes, or "" when unaligned.
func cellAlign(attrs string) string {
	if m := reTableCellAlign.FindStringSubmatch(attrs); m != nil {
		return strings.ToLower(m[1])
	}
	return ""
}

// reCellEscape matches the characters that must be backslash-escaped in cell
// text. Pipes would split the row. Backslashes must double: GFM processes
// escapes left to right, so a lone literal `\` before an escaped pipe would
// swallow its backslash and turn the pipe back into a delimiter. Ampersands
// would let decoded text that still looks like an entity (say a literal
// &#124;) be decoded a second time by goldmark on the next render.
var reCellEscape = regexp.MustCompile(`[\\|&]`)

// reCodeNonSpaceWS matches whitespace other than plain spaces. Code-span cell
// content must be one line, but interior spaces are significant in GFM code
// spans, so only these collapse.
var reCodeNonSpaceWS = regexp.MustCompile(`[\t\n\r\f\v]+`)

// reAdjacentCode matches the boundary between two code elements with nothing
// separating them.
var reAdjacentCode = regexp.MustCompile(`(?i)</code><code(?:\s[^>]*)?>`)

// codeSpanMarkdown wraps code content in a backtick fence long enough to
// survive backticks in the content, with CommonMark's space padding when the
// content begins or ends with a backtick — or with a space, since the parser
// strips exactly one leading/trailing space pair from padded spans and would
// otherwise eat a significant edge space. All-space content needs no padding:
// the strip rule exempts it.
func codeSpanMarkdown(content string) string {
	longest, run := 0, 0
	for _, r := range content {
		if r == '`' {
			run++
			longest = max(longest, run)
		} else {
			run = 0
		}
	}
	delim := strings.Repeat("`", longest+1)
	pad := ""
	edgeSpace := (strings.HasPrefix(content, " ") || strings.HasSuffix(content, " ")) &&
		strings.TrimSpace(content) != ""
	if strings.HasPrefix(content, "`") || strings.HasSuffix(content, "`") || edgeSpace {
		pad = " "
	}
	return delim + pad + content + pad + delim
}

// cellMarkdown converts a table cell's inner HTML to single-line Markdown:
// inline elements convert as usual, block boundaries (<p>, <br>, and any
// other leftover tag) collapse to spaces, and backslashes and pipes are
// escaped so cell text can't break the row. Code spans pass through as
// placeholders: backslashes are literal inside code, so only their pipes are
// escaped (GFM's row splitting honors `\|` inside code spans). Entities are
// fully decoded here, after tags are gone and before escaping — escaping
// must see the real characters (an encoded pipe is still a pipe, and
// goldmark would decode it on the next render) — which is why HTMLToMarkdown
// parks the emitted table out of reach of its document-level unescape pass.
func cellMarkdown(inner string) string {
	s := reMentionAttachment.ReplaceAllStringFunc(inner, mentionMarkdown)

	// Adjacent code elements with no gap coalesce into one span: GFM cannot
	// express them separately (`a``b` pairs the outer fences instead), and
	// the rendered output of one span is identical.
	s = reAdjacentCode.ReplaceAllString(s, "")

	var codes []string
	s = reHTMLCode.ReplaceAllStringFunc(s, func(m string) string {
		codes = append(codes, reHTMLCode.FindStringSubmatch(m)[1])
		return "\x00" + strconv.Itoa(len(codes)-1) + "\x00"
	})

	s = reHTMLStrong.ReplaceAllString(s, "**$1**")
	s = reHTMLB.ReplaceAllString(s, "**$1**")
	s = reHTMLEm.ReplaceAllString(s, "*$1*")
	s = reHTMLI.ReplaceAllString(s, "*$1*")
	s = reHTMLLink.ReplaceAllString(s, "[$2]($1)")
	s = reHTMLImgSA.ReplaceAllString(s, "![$2]($1)")
	s = reHTMLImgAS.ReplaceAllString(s, "![$1]($2)")
	s = reHTMLImgS.ReplaceAllString(s, "![]($1)")
	s = reHTMLDel.ReplaceAllString(s, "~~$1~~")
	s = reHTMLS.ReplaceAllString(s, "~~$1~~")
	s = reHTMLStrike.ReplaceAllString(s, "~~$1~~")
	s = reAttachment.ReplaceAllString(s, "📎 $1")
	s = reAttachClose.ReplaceAllString(s, "")
	s = reAttachNoFile.ReplaceAllString(s, "📎 attachment")
	s = reStripTags.ReplaceAllString(s, " ")
	s = strings.ReplaceAll(html.UnescapeString(s), "\u00a0", " ")
	s = reCellEscape.ReplaceAllString(s, `\${0}`)
	s = strings.TrimSpace(reWhitespaceRun.ReplaceAllString(s, " "))

	for i, code := range codes {
		// Decode before collapsing so entity-encoded newlines (&#10;) can't
		// slip through and split the row. No TrimSpace: edge spaces are
		// significant in code spans — codeSpanMarkdown pads so the parser's
		// strip restores them.
		code = reCodeNonSpaceWS.ReplaceAllString(html.UnescapeString(code), " ")
		code = strings.ReplaceAll(code, "|", `\|`)
		s = strings.Replace(s, "\x00"+strconv.Itoa(i)+"\x00", codeSpanMarkdown(code), 1)
	}
	return s
}

// reBRLine matches a <br> tag followed by an optional newline, collapsing
// the pair to a single \n. goldmark's hard-break output is <br>\n; Trix API
// content may have standalone <br>.
var reBRLine = regexp.MustCompile(`(?i)<br\s*/?\s*>\n?`)

// formatListItem converts a list item's HTML content to Markdown, handling
// <br> tags as indented continuation lines.
func formatListItem(prefix, indent, content string) string {
	// Tables inside list items convert here, like quoted tables convert in
	// the blockquote pass: the indentation below applies per line, so the
	// pipe rows must already be in place. Editing stays blocked by
	// HasComplexTableHTML.
	content = reTableBlock.ReplaceAllStringFunc(content, func(s string) string {
		if md := convertTableHTML(s); md != "" {
			return "\n" + md + "\n"
		}
		return ""
	})
	content = strings.TrimSpace(content)
	content = reBRLine.ReplaceAllString(content, "\n")
	lines := strings.Split(content, "\n")
	var parts []string
	for i, line := range lines {
		if i == 0 {
			parts = append(parts, prefix+strings.TrimSpace(line))
		} else {
			// Preserve existing indentation from nested list conversion
			parts = append(parts, indent+line)
		}
	}
	return strings.Join(parts, "\n")
}

// convertCodeBlockHTML converts a <pre><code>...</code></pre> match to Markdown.
// Entities are left escaped so that later regex passes (reP, reStripTags) don't
// corrupt code content like &lt;p&gt;. The global unescapeHTML at the end of
// HTMLToMarkdown converts them.
func convertCodeBlockHTML(s string) string {
	lang := ""
	// Prefer <pre language="X"> (Trix/BC5 format). Fall back to
	// <code class="language-X"> for CommonMark-formatted content (e.g. legacy
	// stored HTML or output from other markdown renderers).
	if match := rePreLang.FindStringSubmatch(s); len(match) >= 2 {
		lang = match[1]
	} else if match := reCodeLang.FindStringSubmatch(s); len(match) >= 2 {
		lang = match[1]
	}
	codeMatch := reCodeInner.FindStringSubmatch(s)
	if len(codeMatch) >= 2 {
		code := strings.TrimSuffix(codeMatch[1], "\n")
		return "```" + lang + "\n" + code + "\n```"
	}
	return s
}

// reLIOpen matches an opening <li> tag (with optional attributes).
// (?:\s[^>]*)? requires whitespace or `>` after `li` so tags like <link> don't
// over-match and break extractListItems depth tracking.
var reLIOpen = regexp.MustCompile(`(?i)<li(?:\s[^>]*)?>`)

// hasPrefixFold checks if s starts with prefix using ASCII case-insensitive
// comparison. Safe for HTML tag matching without ToLower index desync.
func hasPrefixFold(s, prefix string) bool {
	return len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix)
}

// extractListItems extracts top-level <li> content by tracking nesting depth,
// correctly handling nested <li> tags that trip up regex-based extraction.
// Nested <ul>/<ol> inside items are recursively converted to Markdown.
func extractListItems(html string) []string {
	var items []string
	i := 0
	for {
		// Find next top-level <li> opening tag (regex is case-insensitive)
		loc := reLIOpen.FindStringIndex(html[i:])
		if loc == nil {
			break
		}
		contentStart := i + loc[1]

		// Walk forward tracking <li> depth to find the matching </li>.
		// Jump to next '<' to avoid quadratic byte-by-byte scanning.
		depth := 1
		j := contentStart
		for j < len(html) && depth > 0 {
			idx := strings.IndexByte(html[j:], '<')
			if idx == -1 {
				j = len(html)
				break
			}
			j += idx
			if hasPrefixFold(html[j:], "</li>") {
				depth--
				if depth == 0 {
					content := html[contentStart:j]
					content = replaceBalancedListBlocks(content)
					items = append(items, content)
					j += 5
					break
				}
				j += 5
			} else if loc := reLIOpen.FindStringIndex(html[j:]); loc != nil && loc[0] == 0 {
				depth++
				j += loc[1]
			} else {
				j++
			}
		}
		i = j
	}
	return items
}

// reListOpen matches an opening <ul> or <ol> tag. (?:\s[^>]*)? requires
// whitespace or `>` after the tag name so `<ultra>` or other long-prefix tags
// don't trigger replaceBalancedListBlocks.
var reListOpen = regexp.MustCompile(`(?i)<(ul|ol)(?:\s[^>]*)?>`)

// replaceBalancedListBlocks finds top-level <ul>/<ol> blocks by tracking tag
// depth and converts each to Markdown. Handles nesting correctly where regex
// lazy/greedy matching cannot.
func replaceBalancedListBlocks(html string) string {
	var result strings.Builder
	// Track last written byte to avoid materializing result.String() in the loop.
	var lastByte byte
	writeString := func(s string) {
		if len(s) > 0 {
			lastByte = s[len(s)-1]
			result.WriteString(s)
		}
	}
	writeByte := func(b byte) {
		lastByte = b
		result.WriteByte(b)
	}

	i := 0
	for {
		loc := reListOpen.FindStringSubmatchIndex(html[i:])
		if loc == nil {
			writeString(html[i:])
			break
		}
		matchStart := i + loc[0]
		tag := strings.ToLower(html[i+loc[2] : i+loc[3]]) // "ul" or "ol"
		contentStart := i + loc[1]

		writeString(html[i:matchStart])

		depth := 1
		j := contentStart
		for j < len(html) && depth > 0 {
			// Jump to next '<' to avoid quadratic byte-by-byte scanning
			idx := strings.IndexByte(html[j:], '<')
			if idx == -1 {
				j = len(html)
				break
			}
			j += idx
			// Decrement for any list close tag (handles mixed <ul>/<ol> nesting)
			if hasPrefixFold(html[j:], "</ul>") || hasPrefixFold(html[j:], "</ol>") {
				closeLen := 5 // len("</ul>") == len("</ol>")
				depth--
				if depth == 0 {
					inner := html[contentStart:j]
					var md string
					if tag == "ul" {
						md = convertULInner(inner)
					} else {
						md = convertOLInner(inner)
					}
					if lastByte != 0 && lastByte != '\n' {
						writeByte('\n')
					}
					writeString(md + "\n\n")
					j += closeLen
					break
				}
				j += closeLen
			} else if loc := reListOpen.FindStringSubmatchIndex(html[j:]); loc != nil && loc[0] == 0 {
				depth++
				j += loc[1]
			} else {
				j++
			}
		}
		if depth > 0 {
			// Unclosed tag — write original text
			writeString(html[matchStart:])
			break
		}
		i = j
	}
	return result.String()
}

// convertULInner converts inner <ul> content (between <ul> and </ul>) to Markdown.
func convertULInner(inner string) string {
	items := extractListItems(inner)
	result := make([]string, 0, len(items))
	for _, content := range items {
		result = append(result, formatListItem("- ", "  ", content))
	}
	return strings.Join(result, "\n")
}

// convertOLInner converts inner <ol> content (between <ol> and </ol>) to Markdown.
func convertOLInner(inner string) string {
	items := extractListItems(inner)
	result := make([]string, 0, len(items))
	for i, content := range items {
		prefix := strconv.Itoa(i+1) + ". "
		indent := strings.Repeat(" ", len(prefix))
		result = append(result, formatListItem(prefix, indent, content))
	}
	return strings.Join(result, "\n")
}

// blockquoteInnerToMarkdown converts the inner HTML of a blockquote to Markdown,
// handling nested block elements (lists, code blocks) before line-level operations.
func blockquoteInnerToMarkdown(inner string) string {
	content := strings.TrimSpace(inner)
	content = reCodeBlock.ReplaceAllStringFunc(content, func(s string) string {
		return convertCodeBlockHTML(s) + "\n\n"
	})
	// Quoted tables convert here, not in HTMLToMarkdown's parked table pass:
	// the quote prefixes each line below, so the pipe rows must already be in
	// place. The emitted cells then pass through the document-level unescape,
	// which is inert for them — cell text is decoded once here and its
	// ampersands are escaped, so no entity survives to decode again. Editing
	// quoted tables stays blocked by HasComplexTableHTML regardless.
	content = reTableBlock.ReplaceAllStringFunc(content, func(s string) string {
		md := convertTableHTML(s)
		if md == "" {
			return "\n\n"
		}
		return md + "\n\n"
	})
	content = replaceBalancedListBlocks(content)
	// Replace </p> with double newline (paragraph break) to separate adjacent blocks,
	// then strip <p> openers. Two passes so <p>para1</p><p>para2</p> produces
	// "para1\n\npara2" (blank line = > separator) rather than "para1para2".
	content = reClosingP.ReplaceAllString(content, "\n\n")
	content = reOpeningP.ReplaceAllString(content, "")
	content = reBRLine.ReplaceAllString(content, "\n")
	content = reMultiNewline.ReplaceAllString(content, "\n\n")
	return strings.TrimSpace(content)
}

var (
	reOpeningP = regexp.MustCompile(`(?i)<p(?:\s[^>]*)?>`)
	reClosingP = regexp.MustCompile(`(?i)</p>`)
)

// unescapeHTML converts HTML entities back to their characters.
func unescapeHTML(s string) string {
	s = strings.ReplaceAll(s, "&amp;", "&")
	s = strings.ReplaceAll(s, "&lt;", "<")
	s = strings.ReplaceAll(s, "&gt;", ">")
	s = strings.ReplaceAll(s, "&quot;", "\"")
	s = strings.ReplaceAll(s, "&#39;", "'")
	s = strings.ReplaceAll(s, "&apos;", "'")
	s = strings.ReplaceAll(s, "&nbsp;", " ")
	return s
}

// tableDetectParser parses input with the GFM table extension so table
// detection matches what MarkdownToHTML actually renders. Using the parser
// (rather than a regex) authoritatively handles single-column tables, CRLF
// line endings, and other pipe-row edge cases without pattern drift.
var tableDetectParser = goldmark.New(goldmark.WithExtensions(extension.Table)).Parser()

// hasMarkdownTable parses s and reports whether it contains a GFM table node.
func hasMarkdownTable(s string) bool {
	// Cheap-out before the full goldmark parse: a GFM table needs cell-delimiting
	// pipes and a delimiter row on its own line, so it can't exist without both a
	// '|' and a '\n'. IsMarkdown calls this on every input that matches none of the
	// regex patterns — typically plain text on TUI submit/editor return — so
	// skipping the parse in that common case avoids the cost. This matches
	// goldmark's own behavior exactly (it likewise treats a CR-only "table" with
	// no '\n' as not a table), so the guard introduces no false negatives.
	if !strings.Contains(s, "|") || !strings.Contains(s, "\n") {
		return false
	}
	doc := tableDetectParser.Parse(text.NewReader([]byte(s)))
	found := false
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if entering && n.Kind() == east.KindTable {
			found = true
			return ast.WalkStop, nil
		}
		return ast.WalkContinue, nil
	})
	return found
}

// IsMarkdown attempts to detect if the input string is Markdown rather than plain text or HTML.
// This is a heuristic and may not be 100% accurate.
func IsMarkdown(s string) bool {
	if s == "" {
		return false
	}

	for _, re := range reMarkdownPatterns {
		if re.MatchString(s) {
			return true
		}
	}

	return hasMarkdownTable(s)
}

// AttachmentRef holds the metadata needed to embed a <bc-attachment> in HTML.
type AttachmentRef struct {
	SGID        string
	Filename    string
	ContentType string
}

// AttachmentToHTML builds a <bc-attachment> tag for embedding in Trix-compatible HTML.
func AttachmentToHTML(sgid, filename, contentType string) string {
	return `<bc-attachment sgid="` + escapeAttr(sgid) +
		`" content-type="` + escapeAttr(contentType) +
		`" filename="` + escapeAttr(filename) +
		`"></bc-attachment>`
}

// EmbedAttachments appends <bc-attachment> tags to HTML content.
// Each attachment is added as a separate block after the main content.
func EmbedAttachments(html string, attachments []AttachmentRef) string {
	if len(attachments) == 0 {
		return html
	}
	var b strings.Builder
	b.WriteString(html)
	for _, a := range attachments {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(AttachmentToHTML(a.SGID, a.Filename, a.ContentType))
	}
	return b.String()
}

// MentionLookupFunc resolves a name to an attachable SGID and display name.
type MentionLookupFunc func(name string) (sgid, displayName string, err error)

// PersonByIDFunc resolves a person ID to an attachable SGID and canonical name.
// Used by the person:ID mention syntax.
type PersonByIDFunc func(id string) (sgid, canonicalName string, err error)

// ErrMentionSkip is a sentinel error that lookup functions can return to indicate
// that a fuzzy @Name mention should be left as plain text instead of failing the
// entire operation. Use this for recoverable errors like not-found or ambiguous.
var ErrMentionSkip = errors.New("mention skip")

// MentionResult holds the resolved HTML and any mentions that could not be resolved.
type MentionResult struct {
	HTML       string
	Unresolved []string
}

// MentionToHTML builds a <bc-attachment> mention tag.
func MentionToHTML(sgid, name string) string {
	return `<bc-attachment sgid="` + escapeAttr(sgid) +
		`" content-type="application/vnd.basecamp.mention">@` +
		escapeHTML(name) + `</bc-attachment>`
}

// ResolveMentions processes mention syntax in HTML in four passes:
//  1. Markdown mention anchors: <a href="mention:SGID">@Name</a> and <a href="person:ID">@Name</a>
//  2. Literal Markdown mention links [@Name](mention:SGID) / [@Name](person:ID) that were
//     never converted to anchors (e.g. embedded inside an author-supplied HTML block)
//  3. Inline @sgid:VALUE syntax
//  4. Fuzzy @Name and @First.Last patterns
//
// Each pass replaces matches with <bc-attachment> tags. Subsequent passes skip regions
// already converted by earlier passes via the mention exclusion index.
//
// lookupByID may be nil if person:ID syntax is not needed; encountering any
// person:ID syntax with a nil lookupByID returns an error.
func ResolveMentions(html string, lookup MentionLookupFunc, lookupByID PersonByIDFunc) (MentionResult, error) {
	// Pass 1: Markdown mention anchors
	var err error
	html, err = resolveMentionAnchors(html, lookupByID)
	if err != nil {
		return MentionResult{}, err
	}

	// Pass 2: literal Markdown mention links that were not converted to anchors
	html, err = resolveMentionMarkdownLinks(html, lookupByID)
	if err != nil {
		return MentionResult{}, err
	}

	// Pass 3: @sgid:VALUE
	html = resolveSGIDMentions(html)

	// Pass 4: fuzzy @Name (skip when no lookup function provided)
	var unresolved []string
	if lookup != nil {
		html, unresolved, err = resolveNameMentions(html, lookup)
		if err != nil {
			return MentionResult{}, err
		}
	}

	return MentionResult{HTML: html, Unresolved: unresolved}, nil
}

// resolveMentionAnchors processes <a href="mention:SGID">@Name</a> and
// <a href="person:ID">@Name</a> anchors produced by MarkdownToHTML.
func resolveMentionAnchors(html string, lookupByID PersonByIDFunc) (string, error) {
	return resolveDeterministicMentions(html, reMentionAnchor, mentionMatchGroups{
		scheme:  1,
		value:   2,
		display: 3,
	}, lookupByID)
}

// resolveMentionMarkdownLinks converts literal [@Name](mention:SGID) and
// [@Name](person:ID) Markdown links into <bc-attachment> mention tags.
//
// These survive as literal text when the link was authored inside an HTML block:
// MarkdownToHTML detects the input as HTML and passes it through verbatim, so
// goldmark never turns the link into an <a> anchor and resolveMentionAnchors
// cannot match it. Without this pass the fuzzy @Name matcher would match only the
// first name token (it stops at the first space) and leave the remainder — e.g.
// " Manrubia](mention:SGID)" — as garbage text next to a half-formed chip.
//
// Matches inside code blocks, existing bc-attachments, or HTML tags are skipped:
// those are documentation or already-resolved content, not live mentions.
func resolveMentionMarkdownLinks(html string, lookupByID PersonByIDFunc) (string, error) {
	return resolveDeterministicMentions(html, reMentionMarkdownLink, mentionMatchGroups{
		scheme:  2,
		value:   3,
		display: 1,
	}, lookupByID)
}

type mentionMatchGroups struct {
	scheme  int
	value   int
	display int
}

func resolveDeterministicMentions(html string, pattern *regexp.Regexp, groups mentionMatchGroups, lookupByID PersonByIDFunc) (string, error) {
	matches := pattern.FindAllStringSubmatchIndex(html, -1)
	if len(matches) == 0 {
		return html, nil
	}

	exclusions := buildMentionExclusionIndex(html)
	result := html
	for i := len(matches) - 1; i >= 0; i-- {
		m := matches[i]
		fullStart, fullEnd := m[0], m[1]

		if exclusions.contains(fullStart) {
			continue
		}

		scheme := submatch(html, m, groups.scheme)
		value := submatch(html, m, groups.value)
		displayText := submatch(html, m, groups.display)

		var tag string
		switch scheme {
		case "mention":
			// Both goldmark output and HTML-block passthrough may contain entities.
			name := unescapeHTML(strings.TrimPrefix(displayText, "@"))
			tag = MentionToHTML(value, name)

		case "person":
			if lookupByID == nil {
				return "", fmt.Errorf("person:%s syntax requires a person lookup function", value)
			}
			sgid, canonicalName, err := lookupByID(value)
			if err != nil {
				return "", fmt.Errorf("failed to resolve person:%s: %w", value, err)
			}
			tag = MentionToHTML(sgid, canonicalName)
		}

		result = result[:fullStart] + tag + result[fullEnd:]
	}

	return result, nil
}

func submatch(input string, indexes []int, group int) string {
	start := indexes[group*2]
	end := indexes[group*2+1]
	return input[start:end]
}

// resolveSGIDMentions processes inline @sgid:VALUE syntax.
func resolveSGIDMentions(html string) string {
	matches := reSGIDMention.FindAllStringSubmatchIndex(html, -1)
	if len(matches) == 0 {
		return html
	}

	exclusions := buildMentionExclusionIndex(html)
	result := html
	for i := len(matches) - 1; i >= 0; i-- {
		m := matches[i]
		// Group 2: full @sgid:VALUE token
		tokenStart, tokenEnd := m[4], m[5]
		// Group 3: SGID value
		sgid := html[m[6]:m[7]]

		if exclusions.contains(tokenStart) {
			continue
		}

		tag := MentionToHTML(sgid, sgid)
		result = result[:tokenStart] + tag + result[tokenEnd:]
	}

	return result
}

// resolveNameMentions processes fuzzy @Name and @First.Last patterns.
// When a lookup returns ErrMentionSkip (wrapped or direct), the mention is left
// as plain text and its name is collected in the unresolved slice.
func resolveNameMentions(html string, lookup MentionLookupFunc) (string, []string, error) {
	matches := reMentionInput.FindAllStringSubmatchIndex(html, -1)
	if len(matches) == 0 {
		return html, nil, nil
	}

	result := html
	exclusions := buildMentionExclusionIndex(html)
	var unresolved []string
	for i := len(matches) - 1; i >= 0; i-- {
		m := matches[i]
		mentionStart, mentionEnd := m[4], m[5]

		// Skip mentions inside HTML tags, code blocks, or existing <bc-attachment> elements
		if exclusions.contains(mentionStart) {
			continue
		}

		// Trailing-character bailout: skip if followed by hyphen or word-internal apostrophe
		if mentionEnd < len(result) {
			next := result[mentionEnd]
			if next == '-' {
				continue
			}
			if next == '\'' && mentionEnd+1 < len(result) {
				r, _ := utf8.DecodeRuneInString(result[mentionEnd+1:])
				if r != utf8.RuneError && (unicode.IsLetter(r) || unicode.IsDigit(r)) {
					continue
				}
			}
		}

		mention := html[mentionStart:mentionEnd]

		// Strip @ and convert dots to spaces for name lookup
		name := strings.ReplaceAll(mention[1:], ".", " ")

		sgid, displayName, err := lookup(name)
		if err != nil {
			if errors.Is(err, ErrMentionSkip) {
				unresolved = append(unresolved, mention)
				continue
			}
			return "", nil, fmt.Errorf("failed to resolve mention %s: %w", mention, err)
		}

		tag := MentionToHTML(sgid, displayName)
		result = result[:mentionStart] + tag + result[mentionEnd:]
	}

	slices.Reverse(unresolved)
	return result, unresolved, nil
}

type textSpan struct {
	start int
	end   int
}

type textSpanIndex []textSpan

type mentionExclusionIndex struct {
	tags      textSpanIndex
	protected textSpanIndex
}

func buildMentionExclusionIndex(s string) mentionExclusionIndex {
	tags := buildHTMLTagIndex(s)
	return mentionExclusionIndex{
		tags:      tags,
		protected: buildProtectedElementIndex(s, tags),
	}
}

func (index mentionExclusionIndex) contains(pos int) bool {
	return index.tags.contains(pos) || index.protected.contains(pos)
}

// buildHTMLTagIndex records tag spans in one pass so mention checks do not
// repeatedly scan the full HTML prefix. Quoted > characters do not end a tag.
func buildHTMLTagIndex(s string) textSpanIndex {
	var index textSpanIndex
	inTag := false
	tagStart := 0
	var quote byte
	for i := 0; i < len(s); i++ {
		if !inTag {
			if s[i] == '<' {
				inTag = true
				tagStart = i + 1
			}
			continue
		}

		if quote != 0 {
			if s[i] == quote {
				quote = 0
			}
			continue
		}

		switch s[i] {
		case '\'', '"':
			quote = s[i]
		case '>':
			index = append(index, textSpan{start: tagStart, end: i + 1})
			inTag = false
		}
	}
	if inTag {
		index = append(index, textSpan{start: tagStart, end: len(s) + 1})
	}
	return index
}

func buildProtectedElementIndex(s string, tags textSpanIndex) textSpanIndex {
	var index textSpanIndex
	var depths [3]int
	depth := 0
	protectedStart := 0

	for _, tag := range tags {
		tagEnd := min(tag.end-1, len(s))
		contents := strings.TrimSpace(s[tag.start:tagEnd])
		closing := strings.HasPrefix(contents, "/")
		if closing {
			contents = strings.TrimSpace(strings.TrimPrefix(contents, "/"))
		}

		nameEnd := strings.IndexAny(contents, " \t\r\n/")
		if nameEnd == -1 {
			nameEnd = len(contents)
		}
		kind := protectedElementKind(contents[:nameEnd])
		if kind == -1 {
			continue
		}

		if closing {
			if depths[kind] == 0 {
				continue
			}
			depths[kind]--
			depth--
			if depth == 0 {
				index = append(index, textSpan{start: protectedStart, end: tag.start})
			}
			continue
		}

		if strings.HasSuffix(contents, "/") {
			continue
		}
		if depth == 0 {
			protectedStart = tag.end
		}
		depths[kind]++
		depth++
	}

	if depth > 0 {
		index = append(index, textSpan{start: protectedStart, end: len(s) + 1})
	}
	return index
}

func protectedElementKind(name string) int {
	switch {
	case strings.EqualFold(name, "code"):
		return 0
	case strings.EqualFold(name, "pre"):
		return 1
	case strings.EqualFold(name, "bc-attachment"):
		return 2
	default:
		return -1
	}
}

func (index textSpanIndex) contains(pos int) bool {
	i := sort.Search(len(index), func(i int) bool {
		return index[i].end > pos
	})
	return i < len(index) && index[i].start <= pos
}

// IsHTML attempts to detect if the input string contains HTML.
// Only returns true for well-formed HTML with common content tags.
// Does not detect arbitrary tags like <script> to prevent XSS passthrough.
// Tags inside Markdown code spans (`...`) and fenced code blocks (```) are ignored.
func IsHTML(s string) bool {
	if s == "" {
		return false
	}

	// Strip fenced code blocks and backtick code spans so that HTML tags
	// appearing inside code contexts don't trigger a false positive.
	stripped := reFencedBlock.ReplaceAllString(s, "")
	stripped = reCodeSpan.ReplaceAllString(stripped, "")

	for _, match := range reSafeTag.FindAllStringIndex(stripped, -1) {
		if !isEscapedAt(stripped, match[0]) {
			return true
		}
	}

	return false
}

// reTableHTML matches a real <table> tag — with attributes (`<table …>`), bare
// (`<table>`), or self-closing (`<table/>`) — distinct from the Markdown table
// detector. The trailing class requires a boundary after the name so longer
// tags like <tablefoo> don't match.
var reTableHTML = regexp.MustCompile(`(?i)<table[\s/>]`)

// reTableClose matches a closing </table> tag.
var reTableClose = regexp.MustCompile(`(?i)</table\s*>`)

// Complexity markers within a table block: merged cells (matched against a
// cell's open-tag attributes, so prose that merely mentions colspan= stays
// simple); block elements, captions, attachments, and images anywhere inside
// the table; header cells beyond the first row (GFM has exactly one header
// row); and cells whose content spans multiple paragraphs, divs, or lines.
// All are shapes a GFM pipe table cannot represent — cellMarkdown flattens
// them to single-line text for display, so resubmitting would lose the
// structure.
var (
	reTableMergedCell   = regexp.MustCompile(`(?i)\b(?:colspan|rowspan)\s*=`)
	reTableComplexInner = regexp.MustCompile(`(?i)<(?:ul|ol|pre|blockquote|h[1-6]|hr|caption|figure|img|bc-attachment)[\s/>]`)
	reTableTH           = regexp.MustCompile(`(?i)<th[\s/>]`)
	reOpeningDiv        = regexp.MustCompile(`(?i)<div(?:\s[^>]*)?>`)
	// BC3's sanitizer preserves color/background-color styles; conversion
	// strips them, so a styled tag anywhere in the table is data an edit
	// would lose.
	reTableStyledTag = regexp.MustCompile(`(?i)<[^>]*\bstyle\s*=`)
)

// reWholeCellWrapper matches cell content that is exactly one <p> or <div>
// element wrapping everything — the shape whose boundary costs nothing to
// flatten.
var reWholeCellWrapper = regexp.MustCompile(`(?is)^(?:<p(?:\s[^>]*)?>.*</p\s*>|<div(?:\s[^>]*)?>.*</div\s*>)$`)

// cellFlattensLineStructure reports whether a cell's inner HTML carries line
// structure cellMarkdown would flatten to spaces: any <br>, more than one
// block wrapper, or a single <p>/<div> that doesn't wrap the entire cell.
func cellFlattensLineStructure(inner string) bool {
	if reBR.MatchString(inner) {
		return true
	}
	blocks := len(reOpeningP.FindAllString(inner, -1)) + len(reOpeningDiv.FindAllString(inner, -1))
	if blocks > 1 {
		return true
	}
	return blocks == 1 && !reWholeCellWrapper.MatchString(strings.TrimSpace(inner))
}

// reTableContext matches the tags whose nesting decides whether a table sits
// inside another block container (a blockquote or a list item). GFM pipe
// tables are top-level only, so a table nested in either cannot round-trip.
var reTableContext = regexp.MustCompile(`(?i)<(/?)(blockquote|li|table)[\s/>]`)

// tableHasGrid reports whether a table block contains at least one row with
// at least one cell — the minimum structure convertTableHTML can emit.
func tableHasGrid(block string) bool {
	for _, row := range reTableRowHTML.FindAllStringSubmatch(block, -1) {
		if reTableCellHTML.MatchString(row[1]) {
			return true
		}
	}
	return false
}

// HasComplexTableHTML reports whether s contains a table that HTMLToMarkdown
// cannot round-trip as a GFM pipe table: merged cells (colspan/rowspan), a
// nested table, block content inside a cell, or the table itself nested in a
// blockquote or list. The TUI in-place editors use this to gate edits —
// HTMLToMarkdown still converts such tables for display, best-effort, but an
// edit-and-resubmit would flatten the structure, so those edits fail closed.
// Simple grids round-trip cleanly and stay editable.
func HasComplexTableHTML(s string) bool {
	depth := 0
	for _, m := range reTableContext.FindAllStringSubmatch(s, -1) {
		closing := m[1] == "/"
		if strings.EqualFold(m[2], "table") {
			if !closing && depth > 0 {
				return true
			}
		} else if closing {
			if depth > 0 {
				depth--
			}
		} else {
			depth++
		}
	}

	for {
		open := reTableHTML.FindStringIndex(s)
		if open == nil {
			return false
		}
		rest := s[open[1]:]
		closeTag := reTableClose.FindStringIndex(rest)
		// No closing tag: the converter can't parse the table, so nothing
		// about the edit loop is safe. Fail closed.
		if closeTag == nil {
			return true
		}
		block := rest[:closeTag[0]]
		if reTableHTML.MatchString(block) {
			return true
		}
		// A table the converter can't extract a grid from vanishes from the
		// Markdown; if it holds any text, that vanishing is data loss.
		if !tableHasGrid(block) && strings.TrimSpace(reStripTags.ReplaceAllString(block, "")) != "" {
			return true
		}
		// Mentions are the one rich element cells may keep: they convert to
		// **@Name** exactly as they do in body text. Strip them before
		// scanning for content the conversion would lose.
		stripped := reMentionAttachment.ReplaceAllString(block, "")
		if reTableComplexInner.MatchString(stripped) || reTableStyledTag.MatchString(stripped) {
			return true
		}
		var headerAligns []string
		for ri, row := range reTableRowHTML.FindAllStringSubmatch(block, -1) {
			// convertTableHTML promotes only the first row to the GFM header;
			// a <th> in any later row would be demoted to a plain cell.
			if ri > 0 && reTableTH.MatchString(row[1]) {
				return true
			}
			for ci, cell := range reTableCellHTML.FindAllStringSubmatch(row[1], -1) {
				if reTableMergedCell.MatchString(cell[1]) {
					return true
				}
				if cellFlattensLineStructure(cell[2]) {
					return true
				}
				// GFM alignment is a column property declared by the header
				// row; a later cell whose align differs from its column's
				// cannot round-trip. Our own MarkdownToHTML output aligns
				// every cell with its column, so real CLI tables pass.
				align := cellAlign(cell[1])
				if ri == 0 {
					headerAligns = append(headerAligns, align)
				} else {
					want := ""
					if ci < len(headerAligns) {
						want = headerAligns[ci]
					}
					if align != want {
						return true
					}
				}
			}
		}
		s = rest[closeTag[1]:]
	}
}

func isEscapedAt(s string, pos int) bool {
	backslashes := 0
	for i := pos - 1; i >= 0 && s[i] == '\\'; i-- {
		backslashes++
	}
	return backslashes%2 == 1
}

// ParsedAttachment holds metadata extracted from a <bc-attachment> tag in HTML content.
type ParsedAttachment struct {
	SGID        string `json:"sgid,omitempty"`
	Filename    string `json:"filename,omitempty"`
	ContentType string `json:"content_type,omitempty"`
	Filesize    string `json:"filesize,omitempty"`
	URL         string `json:"url,omitempty"`
	Href        string `json:"href,omitempty"`
	Width       string `json:"width,omitempty"`
	Height      string `json:"height,omitempty"`
	Caption     string `json:"caption,omitempty"`
}

// reBcAttachmentTag matches <bc-attachment> tags, both self-closing and wrapped.
// Group 1 captures the attributes string.
var reBcAttachmentTag = regexp.MustCompile(`(?si)<bc-attachment(\s[^>]*|)(?:>.*?</bc-attachment>|/>)`)

// ParseAttachments extracts file attachment metadata from HTML content.
// It finds all <bc-attachment> tags and returns their metadata, excluding
// mention attachments (content-type="application/vnd.basecamp.mention").
func ParseAttachments(content string) []ParsedAttachment {
	matches := reBcAttachmentTag.FindAllStringSubmatch(content, -1)
	attachments := make([]ParsedAttachment, 0, len(matches))

	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		attrs := match[1]

		contentType := extractAttr(attrs, "content-type")
		if strings.EqualFold(contentType, "application/vnd.basecamp.mention") {
			continue
		}

		attachments = append(attachments, ParsedAttachment{
			SGID:        extractAttr(attrs, "sgid"),
			Filename:    extractAttr(attrs, "filename"),
			ContentType: contentType,
			Filesize:    extractAttr(attrs, "filesize"),
			URL:         extractAttr(attrs, "url"),
			Href:        extractAttr(attrs, "href"),
			Width:       extractAttr(attrs, "width"),
			Height:      extractAttr(attrs, "height"),
			Caption:     extractAttr(attrs, "caption"),
		})
	}

	return attachments
}

// reAttrValue matches any HTML attribute as name="value" or name='value'.
// Group 1 = attribute name, group 2 = double-quoted value, group 3 = single-quoted value.
var reAttrValue = regexp.MustCompile(`(?:\s|^)([\w-]+)\s*=\s*(?:"([^"]*)"|'([^']*)')`)

// extractAttr extracts the value of an HTML attribute from an attribute string.
// Handles both double-quoted and single-quoted values independently so that
// an apostrophe inside a double-quoted value (or vice versa) is not treated
// as a delimiter. The attribute name must match as a whole word to avoid
// partial matches (e.g. "url" won't match "data-url").
func extractAttr(attrs, name string) string {
	for _, m := range reAttrValue.FindAllStringSubmatch(attrs, -1) {
		if !strings.EqualFold(m[1], name) {
			continue
		}
		val := m[2]
		if m[3] != "" {
			val = m[3]
		}
		val = html.UnescapeString(val)
		return strings.ReplaceAll(val, "\u00A0", " ")
	}
	return ""
}

// IsImage returns true if the attachment has an image content type.
func (a *ParsedAttachment) IsImage() bool {
	return len(a.ContentType) >= 6 && strings.EqualFold(a.ContentType[:6], "image/")
}

// DisplayName returns the best display name: caption, then filename, then fallback.
func (a *ParsedAttachment) DisplayName() string {
	if a.Caption != "" {
		return a.Caption
	}
	if a.Filename != "" {
		return a.Filename
	}
	return "Unnamed attachment"
}

// DisplayURL returns the best available URL for the attachment.
// Href is preferred because it points at the real blob download endpoint
// (storage.3.basecamp.com/.../download/<filename>). URL points at the
// preview endpoint (preview.3.basecamp.com/.../previews/full), which for
// non-image content types returns a generic SVG file-type icon instead of
// the real file. Every internal caller is a download path, so preferring
// Href is correct; URL is retained as a fallback for the rare case where
// an attachment has no downloadable blob (e.g. externally hosted images).
func (a *ParsedAttachment) DisplayURL() string {
	if a.Href != "" {
		return a.Href
	}
	return a.URL
}
