// Package richtext provides utilities for converting between Markdown and HTML.
// It uses glamour for terminal-friendly Markdown rendering.
package richtext

import (
	"bytes"
	"errors"
	"fmt"
	"html"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/glamour"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
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
	reHR = regexp.MustCompile(`(?i)<hr\s*/?\s*>`)

	// Trix-native block shapes. Trix (the editor behind Basecamp) stores
	// paragraphs as <div>...</div> and blank-line separators as
	// <div><br></div>. HTMLToMarkdown needs to unwrap both so content
	// typed in Basecamp round-trips cleanly, and so the CLI's own output
	// (which MarkdownToHTML emits in the same shape) can be re-parsed.
	reDivEmpty = regexp.MustCompile(`(?i)<div(?:\s[^>]*)?>\s*<br\s*/?\s*>\s*</div>`)
	reDiv      = regexp.MustCompile(`(?is)<div(?:\s[^>]*)?>(.*?)</div>`)
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
	reAttachment        = regexp.MustCompile(`(?i)<bc-attachment[^>]*filename="([^"]*)"[^>]*/?\s*>`)
	reAttachNoFile      = regexp.MustCompile(`(?i)<bc-attachment[^>]*/?\s*>`)
	reAttachClose       = regexp.MustCompile(`(?i)</bc-attachment>`)
	reStripTags         = regexp.MustCompile(`<[^>]+>`)
	reMultiNewline      = regexp.MustCompile(`\n{3,}`)
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

// reSGIDMention matches inline @sgid:VALUE syntax.
// Group 1: prefix character.
// Group 2: the full @sgid:VALUE token.
// Group 3: the SGID value (base64-safe characters).
var reSGIDMention = regexp.MustCompile(`(^|[\s>(\["'])(@sgid:([\w+=/-]+))`)

// Pre-compiled regexes for IsHTML detection
var (
	reSafeTag     = regexp.MustCompile(`<(p|div|span|a|strong|b|em|i|code|pre|ul|ol|li|h[1-6]|blockquote|br|hr|img|bc-attachment)\b[^>]*>`)
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
	goldmark.WithExtensions(extension.Strikethrough),
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

// TrixBreak is a custom block node that renders as <br>\n for Trix paragraph spacing.
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
	// Phase 0: Convert soft line breaks to hard across the entire document.
	// In Markdown `"Line 1\nLine 2"` (no blank line) is authored as a single
	// paragraph with an intentional visible line break. goldmark's default
	// renders the soft break as a literal newline, which browsers collapse
	// to whitespace. Trix (the editor behind Basecamp) represents the same
	// "single Enter" as <br> inside the paragraph's <div>, so emit <br>.
	// convertSoftBreaksToHard is idempotent, so later per-list / per-blockquote
	// re-invocations stay a no-op.
	convertSoftBreaksToHard(node)

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
		if child.HasBlankPreviousLines() && child.PreviousSibling() != nil {
			br := &TrixBreak{}
			node.InsertBefore(node, child, br)
		}
	}
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
	reg.Register(ast.KindParagraph, r.renderParagraph)
	reg.Register(ast.KindRawHTML, r.renderRawHTML)
	reg.Register(ast.KindHTMLBlock, r.renderHTMLBlock)
	reg.Register(ast.KindBlockquote, r.renderBlockquote)
	reg.Register(ast.KindFencedCodeBlock, r.renderFencedCodeBlock)
	reg.Register(KindTrixBreak, r.renderTrixBreak)
	reg.Register(KindEscapedAt, r.renderEscapedAt)
}

// renderParagraph wraps top-level paragraphs in <div>...</div> instead of
// goldmark's default <p>...</p>. Trix (the editor behind Basecamp's rich
// text fields) uses <div> blocks natively; <p> blocks get rendered with
// extra margins, producing double-spaced paragraphs in Basecamp.
// Paragraphs inside lists and blockquotes are converted to TextBlocks by
// trixTransformer before reaching the renderer, so this only fires for
// top-level paragraphs.
func (r *trixRenderer) renderParagraph(w util.BufWriter, _ []byte, _ ast.Node, entering bool) (ast.WalkStatus, error) {
	if entering {
		_, _ = w.WriteString("<div>")
	} else {
		_, _ = w.WriteString("</div>\n")
	}
	return ast.WalkContinue, nil
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
	_, _ = w.WriteString("<div>" + strings.Join(parts, " ") + "</div>\n")
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

func (r *trixRenderer) renderTrixBreak(w util.BufWriter, _ []byte, _ ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	// <div><br></div> is Trix's native empty-line representation.
	// A bare <br> between <p>/<div> blocks produces double spacing in
	// Basecamp because the <br> comes in addition to the block margins.
	_, _ = w.WriteString("<div><br></div>\n")
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
// If the input already appears to be HTML, it is returned unchanged to preserve existing formatting.
func MarkdownToHTML(md string) string {
	if md == "" {
		return ""
	}

	if IsHTML(md) {
		return md
	}

	md = strings.ReplaceAll(md, "\r\n", "\n")
	md = strings.ReplaceAll(md, "\r", "\n")

	var buf bytes.Buffer
	if err := mdConverter.Convert([]byte(md), &buf); err != nil {
		return "<div>" + html.EscapeString(md) + "</div>"
	}

	return strings.TrimSpace(buf.String())
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

// glamourCache caches glamour renderers by width to avoid repeated construction.
var (
	glamourMu    sync.Mutex
	glamourCache = make(map[int]*glamour.TermRenderer)
)

func cachedRenderer(width int) (*glamour.TermRenderer, error) {
	glamourMu.Lock()
	defer glamourMu.Unlock()

	if r, ok := glamourCache[width]; ok {
		return r, nil
	}
	r, err := glamour.NewTermRenderer(
		glamour.WithAutoStyle(),
		glamour.WithWordWrap(width),
	)
	if err != nil {
		return nil, err
	}
	glamourCache[width] = r
	return r, nil
}

// RenderMarkdown renders Markdown for terminal display using glamour.
// It returns styled output suitable for CLI display.
func RenderMarkdown(md string) (string, error) {
	if md == "" {
		return "", nil
	}

	r, err := cachedRenderer(80)
	if err != nil {
		return "", err
	}

	out, err := r.Render(md)
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(LinkifyURLs(out)), nil
}

// RenderMarkdownWithWidth renders Markdown for terminal display with a custom width.
func RenderMarkdownWithWidth(md string, width int) (string, error) {
	if md == "" {
		return "", nil
	}

	r, err := cachedRenderer(width)
	if err != nil {
		return "", err
	}

	out, err := r.Render(md)
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(LinkifyURLs(out)), nil
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
	html = replaceBalancedListBlocks(html)

	// Trix-native paragraphs: <div><br></div> is a blank-line separator,
	// <div>...</div> is a paragraph. Handle before <p> so a document that
	// mixes both (rare, but possible when Basecamp content is copied from
	// a non-Trix source) degrades cleanly.
	html = reDivEmpty.ReplaceAllString(html, "\n\n")
	html = reDiv.ReplaceAllString(html, "$1\n\n")

	// Paragraphs
	html = reP.ReplaceAllString(html, "$1\n\n")

	// Line breaks. Use reBRLine so a trailing newline after <br> (goldmark's
	// hard-break output is "<br>\n") collapses with the <br> to a single
	// "\n" rather than "\n\n", which would be misread as a paragraph break.
	html = reBRLine.ReplaceAllString(html, "\n")

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
	html = reMentionAttachment.ReplaceAllStringFunc(html, func(s string) string {
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
	})

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

	return strings.TrimSpace(html)
}

// reBRLine matches a <br> tag followed by an optional newline, collapsing
// the pair to a single \n. goldmark's hard-break output is <br>\n; Trix API
// content may have standalone <br>.
var reBRLine = regexp.MustCompile(`(?i)<br\s*/?\s*>\n?`)

// formatListItem converts a list item's HTML content to Markdown, handling
// <br> tags as indented continuation lines.
func formatListItem(prefix, indent, content string) string {
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
	content = replaceBalancedListBlocks(content)
	// Trix-native paragraph separator inside a blockquote: <div><br></div>
	// between two <div> text blocks. Collapse to a single newline so the
	// outer blockquote-line splitter sees it as a blank line (which gets
	// the ">" prefix alone). Without this we produce multiple ">" lines.
	content = reDivEmpty.ReplaceAllString(content, "\n")
	// Replace </p> / </div> with double newline (paragraph break) to separate
	// adjacent blocks, then strip <p> / <div> openers. Two passes so
	// <p>para1</p><p>para2</p> produces "para1\n\npara2" (blank line =
	// > separator) rather than "para1para2".
	content = reClosingP.ReplaceAllString(content, "\n\n")
	content = reOpeningP.ReplaceAllString(content, "")
	content = reClosingDiv.ReplaceAllString(content, "\n\n")
	content = reOpeningDiv.ReplaceAllString(content, "")
	content = reBRLine.ReplaceAllString(content, "\n")
	content = reMultiNewline.ReplaceAllString(content, "\n\n")
	return strings.TrimSpace(content)
}

var (
	reOpeningP   = regexp.MustCompile(`(?i)<p(?:\s[^>]*)?>`)
	reClosingP   = regexp.MustCompile(`(?i)</p>`)
	reOpeningDiv = regexp.MustCompile(`(?i)<div(?:\s[^>]*)?>`)
	reClosingDiv = regexp.MustCompile(`(?i)</div>`)
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

	return false
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

// ResolveMentions processes mention syntax in HTML in three passes:
//  1. Markdown mention anchors: <a href="mention:SGID">@Name</a> and <a href="person:ID">@Name</a>
//  2. Inline @sgid:VALUE syntax
//  3. Fuzzy @Name and @First.Last patterns
//
// Each pass replaces matches with <bc-attachment> tags. Subsequent passes skip regions
// already converted by earlier passes via isInsideBcAttachment.
//
// lookupByID may be nil if person:ID syntax is not needed; encountering a person:ID
// anchor with a nil lookupByID returns an error.
func ResolveMentions(html string, lookup MentionLookupFunc, lookupByID PersonByIDFunc) (MentionResult, error) {
	// Pass 1: Markdown mention anchors
	var err error
	html, err = resolveMentionAnchors(html, lookupByID)
	if err != nil {
		return MentionResult{}, err
	}

	// Pass 2: @sgid:VALUE
	html = resolveSGIDMentions(html)

	// Pass 3: fuzzy @Name (skip when no lookup function provided)
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
	matches := reMentionAnchor.FindAllStringSubmatchIndex(html, -1)
	if len(matches) == 0 {
		return html, nil
	}

	htmlLower := strings.ToLower(html)
	result := html
	for i := len(matches) - 1; i >= 0; i-- {
		m := matches[i]
		fullStart, fullEnd := m[0], m[1]

		// Skip anchors inside code blocks, existing bc-attachments, or HTML tags
		if isInsideHTMLTag(html, fullStart) || isInsideCodeBlock(htmlLower, fullStart) || isInsideBcAttachment(htmlLower, fullStart) {
			continue
		}

		scheme := html[m[2]:m[3]]
		value := html[m[4]:m[5]]
		displayText := html[m[6]:m[7]]

		var tag string
		switch scheme {
		case "mention":
			// Zero API calls — use value as SGID, link text as display name (caller-trusted).
			// Unescape HTML because goldmark already escaped the link text (e.g. & → &amp;)
			// and MentionToHTML will re-escape — without this we'd double-encode.
			name := unescapeHTML(strings.TrimPrefix(displayText, "@"))
			tag = MentionToHTML(value, name)

		case "person":
			// One API lookup — ID → SGID via pingable set
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

// resolveSGIDMentions processes inline @sgid:VALUE syntax.
func resolveSGIDMentions(html string) string {
	matches := reSGIDMention.FindAllStringSubmatchIndex(html, -1)
	if len(matches) == 0 {
		return html
	}

	htmlLower := strings.ToLower(html)
	result := html
	for i := len(matches) - 1; i >= 0; i-- {
		m := matches[i]
		// Group 2: full @sgid:VALUE token
		tokenStart, tokenEnd := m[4], m[5]
		// Group 3: SGID value
		sgid := html[m[6]:m[7]]

		if isInsideHTMLTag(html, tokenStart) || isInsideCodeBlock(htmlLower, tokenStart) || isInsideBcAttachment(htmlLower, tokenStart) {
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
	htmlLower := strings.ToLower(html)
	var unresolved []string
	for i := len(matches) - 1; i >= 0; i-- {
		m := matches[i]
		mentionStart, mentionEnd := m[4], m[5]

		// Skip mentions inside HTML tags, code blocks, or existing <bc-attachment> elements
		if isInsideHTMLTag(html, mentionStart) || isInsideCodeBlock(htmlLower, mentionStart) || isInsideBcAttachment(htmlLower, mentionStart) {
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

// isInsideHTMLTag checks if position pos is inside an HTML tag (between < and >).
func isInsideHTMLTag(s string, pos int) bool {
	// Walk backwards from pos looking for < or >
	for i := pos - 1; i >= 0; i-- {
		if s[i] == '>' {
			return false // closed tag before us
		}
		if s[i] == '<' {
			return true // inside a tag
		}
	}
	return false
}

// isInsideCodeBlock checks if position pos is inside a <code> or <pre> element.
// s must be pre-lowercased by the caller.
func isInsideCodeBlock(s string, pos int) bool {
	prefix := s[:pos]
	for _, tag := range []string{"code", "pre"} {
		open := "<" + tag
		searchIn := prefix
		for {
			openIdx := strings.LastIndex(searchIn, open)
			if openIdx == -1 {
				break
			}
			// Verify tag boundary: next char must be '>', ' ', tab, or newline
			// to avoid matching partial names like <preview> for <pre>
			nextPos := openIdx + len(open)
			if nextPos < len(prefix) && prefix[nextPos] != '>' && prefix[nextPos] != ' ' && prefix[nextPos] != '\t' && prefix[nextPos] != '\n' {
				// Not a real tag, keep searching earlier in the string
				searchIn = prefix[:openIdx]
				continue
			}
			between := prefix[openIdx:]
			if !strings.Contains(between, "</"+tag+">") {
				return true
			}
			break
		}
	}
	return false
}

// isInsideBcAttachment checks if position pos is inside a <bc-attachment>...</bc-attachment> element.
// s must be pre-lowercased by the caller for case-insensitive matching.
func isInsideBcAttachment(s string, pos int) bool {
	// Find the last <bc-attachment before pos
	prefix := s[:pos]
	openIdx := strings.LastIndex(prefix, "<bc-attachment")
	if openIdx == -1 {
		return false
	}
	between := s[openIdx:pos]
	// Self-closing tag (e.g., <bc-attachment ... />) — mention is after it, not inside
	if strings.Contains(between, "/>") {
		return false
	}
	// Check for closing tag between the open and pos
	if strings.Contains(between, "</bc-attachment>") {
		return false
	}
	return true
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
func (a *ParsedAttachment) DisplayURL() string {
	if a.URL != "" {
		return a.URL
	}
	return a.Href
}
