// Package richtext provides utilities for converting between Markdown and HTML.
// It uses glamour for terminal-friendly Markdown rendering.
package richtext

import (
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
)

// Pre-compiled regexes for MarkdownToHTML list detection
var (
	ulPattern = regexp.MustCompile(`^(\s*)[-*+]\s+(.*)$`)
	olPattern = regexp.MustCompile(`^(\s*)\d+\.\s+(.*)$`)
)

// Pre-compiled regexes for convertInline (Markdown → HTML inline elements)
var (
	reCodeSpan      = regexp.MustCompile("`([^`]+)`")
	reBoldStar      = regexp.MustCompile(`\*\*([^*]+)\*\*`)
	reBoldUnder     = regexp.MustCompile(`__([^_]+)__`)
	reItalicStar    = regexp.MustCompile(`\*([^*]+)\*`)
	reItalicUnder   = regexp.MustCompile(`(?:^|[^a-zA-Z0-9])_([^_]+)_(?:[^a-zA-Z0-9]|$)`)
	reItalicInner   = regexp.MustCompile(`_([^_]+)_`)
	reImage         = regexp.MustCompile(`!\[([^\]]*)\]\(([^)]+)\)`)
	reLink          = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`)
	reStrikethrough = regexp.MustCompile(`~~([^~]+)~~`)
)

// Pre-compiled regexes for HTMLToMarkdown (HTML → Markdown block elements)
var (
	reH1         = regexp.MustCompile(`(?i)<h1[^>]*>(.*?)</h1>`)
	reH2         = regexp.MustCompile(`(?i)<h2[^>]*>(.*?)</h2>`)
	reH3         = regexp.MustCompile(`(?i)<h3[^>]*>(.*?)</h3>`)
	reH4         = regexp.MustCompile(`(?i)<h4[^>]*>(.*?)</h4>`)
	reH5         = regexp.MustCompile(`(?i)<h5[^>]*>(.*?)</h5>`)
	reH6         = regexp.MustCompile(`(?i)<h6[^>]*>(.*?)</h6>`)
	reBlockquote = regexp.MustCompile(`(?i)<blockquote[^>]*>(.*?)</blockquote>`)
	reCodeBlock  = regexp.MustCompile(`(?is)<pre[^>]*><code[^>]*(?:class="language-([^"]*)")?[^>]*>(.*?)</code></pre>`)
	reCodeLang   = regexp.MustCompile(`class="language-([^"]*)"`)
	reCodeInner  = regexp.MustCompile(`(?is)<code[^>]*>([\s\S]*?)</code>`)
	reUL         = regexp.MustCompile(`(?is)<ul[^>]*>(.*?)</ul>`)
	reOL         = regexp.MustCompile(`(?is)<ol[^>]*>(.*?)</ol>`)
	reLI         = regexp.MustCompile(`(?is)<li[^>]*>(.*?)</li>`)
	reP          = regexp.MustCompile(`(?i)<p[^>]*>(.*?)</p>`)
	reBR         = regexp.MustCompile(`(?i)<br\s*/?\s*>`)
	reHR         = regexp.MustCompile(`(?i)<hr\s*/?\s*>`)
)

// Pre-compiled regexes for HTMLToMarkdown inline elements
var (
	reHTMLStrong        = regexp.MustCompile(`(?i)<strong[^>]*>(.*?)</strong>`)
	reHTMLB             = regexp.MustCompile(`(?i)<b[^>]*>(.*?)</b>`)
	reHTMLEm            = regexp.MustCompile(`(?i)<em[^>]*>(.*?)</em>`)
	reHTMLI             = regexp.MustCompile(`(?i)<i[^>]*>(.*?)</i>`)
	reHTMLCode          = regexp.MustCompile(`(?i)<code[^>]*>(.*?)</code>`)
	reHTMLLink          = regexp.MustCompile(`(?i)<a[^>]*href="([^"]*)"[^>]*>(.*?)</a>`)
	reHTMLImgSA         = regexp.MustCompile(`(?i)<img[^>]*src="([^"]*)"[^>]*alt="([^"]*)"[^>]*/?\s*>`)
	reHTMLImgAS         = regexp.MustCompile(`(?i)<img[^>]*alt="([^"]*)"[^>]*src="([^"]*)"[^>]*/?\s*>`)
	reHTMLImgS          = regexp.MustCompile(`(?i)<img[^>]*src="([^"]*)"[^>]*/?\s*>`)
	reHTMLDel           = regexp.MustCompile(`(?i)<del[^>]*>(.*?)</del>`)
	reHTMLS             = regexp.MustCompile(`(?i)<s[^>]*>(.*?)</s>`)
	reHTMLStrike        = regexp.MustCompile(`(?i)<strike[^>]*>(.*?)</strike>`)
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

// MarkdownToHTML converts Markdown text to HTML suitable for Basecamp's rich text fields.
// It handles common Markdown syntax: headings, bold, italic, links, lists, code blocks, and blockquotes.
// If the input already appears to be HTML, it is returned unchanged to preserve existing formatting.
func MarkdownToHTML(md string) string {
	if md == "" {
		return ""
	}

	// If input is already HTML, return unchanged to preserve existing content
	if IsHTML(md) {
		return md
	}

	// Normalize line endings
	md = strings.ReplaceAll(md, "\r\n", "\n")
	md = strings.ReplaceAll(md, "\r", "\n")

	var result strings.Builder
	lines := strings.Split(md, "\n")

	var inCodeBlock bool
	var codeBlockLang string
	var codeLines []string
	var inList bool
	var listItems []string
	var listType string // "ul" or "ol"
	var pendingBreak bool
	var paraLines []string

	flushPendingBreak := func() {
		if pendingBreak {
			result.WriteString("<br>\n")
			pendingBreak = false
		}
	}

	flushParagraph := func() {
		if len(paraLines) > 0 {
			flushPendingBreak()
			text := strings.Join(paraLines, " ")
			result.WriteString("<p>" + convertInline(text) + "</p>\n")
			paraLines = nil
		}
	}

	flushList := func() {
		if len(listItems) > 0 {
			result.WriteString("<" + listType + ">\n")
			for _, item := range listItems {
				result.WriteString("<li>" + item + "</li>\n")
			}
			result.WriteString("</" + listType + ">\n")
			listItems = nil
			inList = false
		}
	}

	for i := range lines {
		line := lines[i]

		// Handle code blocks
		if after, ok := strings.CutPrefix(line, "```"); ok {
			if inCodeBlock {
				// End code block
				code := strings.Join(codeLines, "\n")
				code = escapeHTML(code)
				if codeBlockLang != "" {
					// Sanitize language to prevent attribute injection
					safeLang := sanitizeLanguage(codeBlockLang)
					result.WriteString("<pre><code class=\"language-" + safeLang + "\">" + code + "</code></pre>\n")
				} else {
					result.WriteString("<pre><code>" + code + "</code></pre>\n")
				}
				inCodeBlock = false
				codeLines = nil
				codeBlockLang = ""
			} else {
				// Start code block
				flushParagraph()
				flushList()
				flushPendingBreak()
				inCodeBlock = true
				codeBlockLang = after
			}
			continue
		}

		if inCodeBlock {
			codeLines = append(codeLines, line)
			continue
		}

		// Check for list items (using precompiled regexes)
		ulMatch := ulPattern.FindStringSubmatch(line)
		olMatch := olPattern.FindStringSubmatch(line)

		if ulMatch != nil {
			flushParagraph()
			if !inList || listType != "ul" {
				flushList()
				flushPendingBreak()
				inList = true
				listType = "ul"
			}
			pendingBreak = false // blank was between items, not after the list
			listItems = append(listItems, convertInline(ulMatch[2]))
			continue
		}

		if olMatch != nil {
			flushParagraph()
			if !inList || listType != "ol" {
				flushList()
				flushPendingBreak()
				inList = true
				listType = "ol"
			}
			pendingBreak = false // blank was between items, not after the list
			listItems = append(listItems, convertInline(olMatch[2]))
			continue
		}

		// Empty line - handle differently based on context
		if strings.TrimSpace(line) == "" {
			if inList {
				// In a list: empty lines between items create spacing but don't break the list.
				// Record pending break so content after the list gets proper separation.
				pendingBreak = true
				continue
			}
			// Not in a list: flush paragraph and record break
			flushParagraph()
			if result.Len() > 0 {
				pendingBreak = true
			}
			continue
		}

		// Check for list continuation lines (indented text that continues previous list item)
		if inList && len(listItems) > 0 {
			// Check if line is indented (starts with spaces or tabs)
			if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
				// This is a continuation of the last list item
				trimmedLine := strings.TrimSpace(line)
				// Append to last list item with <br> separator
				lastItemIndex := len(listItems) - 1
				listItems[lastItemIndex] = listItems[lastItemIndex] + "<br>\n" + convertInline(trimmedLine)
				pendingBreak = false // blank was before continuation, not after the list
				continue
			}
		}

		// Not a list item or continuation, flush any pending list
		flushList()

		// Headings
		if strings.HasPrefix(line, "#") {
			flushParagraph()
			flushPendingBreak()
		}
		if after, ok := strings.CutPrefix(line, "######"); ok {
			result.WriteString("<h6>" + convertInline(strings.TrimSpace(after)) + "</h6>\n")
			continue
		}
		if after, ok := strings.CutPrefix(line, "#####"); ok {
			result.WriteString("<h5>" + convertInline(strings.TrimSpace(after)) + "</h5>\n")
			continue
		}
		if after, ok := strings.CutPrefix(line, "####"); ok {
			result.WriteString("<h4>" + convertInline(strings.TrimSpace(after)) + "</h4>\n")
			continue
		}
		if after, ok := strings.CutPrefix(line, "###"); ok {
			result.WriteString("<h3>" + convertInline(strings.TrimSpace(after)) + "</h3>\n")
			continue
		}
		if after, ok := strings.CutPrefix(line, "##"); ok {
			result.WriteString("<h2>" + convertInline(strings.TrimSpace(after)) + "</h2>\n")
			continue
		}
		if after, ok := strings.CutPrefix(line, "#"); ok {
			result.WriteString("<h1>" + convertInline(strings.TrimSpace(after)) + "</h1>\n")
			continue
		}

		// Blockquote
		if strings.HasPrefix(line, ">") {
			flushParagraph()
			flushPendingBreak()
		}
		if after, ok := strings.CutPrefix(line, ">"); ok {
			quote := strings.TrimSpace(after)
			result.WriteString("<blockquote>" + convertInline(quote) + "</blockquote>\n")
			continue
		}

		// Horizontal rule
		trimmed := strings.TrimSpace(line)
		if len(trimmed) >= 3 && (allChars(trimmed, '-') || allChars(trimmed, '*') || allChars(trimmed, '_')) {
			flushParagraph()
			flushPendingBreak()
			result.WriteString("<hr>\n")
			continue
		}

		// Accumulate paragraph lines
		paraLines = append(paraLines, line)
	}

	// Flush any remaining paragraph or list
	flushParagraph()
	flushList()

	// Handle unclosed code block
	if inCodeBlock && len(codeLines) > 0 {
		code := strings.Join(codeLines, "\n")
		code = escapeHTML(code)
		result.WriteString("<pre><code>" + code + "</code></pre>\n")
	}

	return strings.TrimSpace(result.String())
}

// convertInline converts inline Markdown elements (bold, italic, links, code) to HTML.
// Code spans are protected from further processing to preserve their literal content.
func convertInline(text string) string {
	// Escape HTML entities first
	text = escapeHTML(text)

	// Extract code spans and replace with placeholders to protect them
	var codeSpans []string
	text = reCodeSpan.ReplaceAllStringFunc(text, func(match string) string {
		inner := reCodeSpan.FindStringSubmatch(match)
		if len(inner) >= 2 {
			idx := len(codeSpans)
			codeSpans = append(codeSpans, inner[1])
			return "\x00CODE" + strconv.Itoa(idx) + "\x00"
		}
		return match
	})

	// Bold with ** or __
	text = reBoldStar.ReplaceAllString(text, "<strong>$1</strong>")
	text = reBoldUnder.ReplaceAllString(text, "<strong>$1</strong>")

	// Italic with * or _ (but not inside words for _)
	text = reItalicStar.ReplaceAllString(text, "<em>$1</em>")
	text = reItalicUnder.ReplaceAllStringFunc(text, func(s string) string {
		inner := reItalicInner.FindStringSubmatch(s)
		if len(inner) >= 2 {
			prefix := ""
			suffix := ""
			if len(s) > 0 && s[0] != '_' {
				prefix = string(s[0])
			}
			if len(s) > 0 && s[len(s)-1] != '_' {
				suffix = string(s[len(s)-1])
			}
			return prefix + "<em>" + inner[1] + "</em>" + suffix
		}
		return s
	})

	// Images ![alt](url) - MUST come before links since image syntax contains link syntax
	text = reImage.ReplaceAllStringFunc(text, func(match string) string {
		parts := reImage.FindStringSubmatch(match)
		if len(parts) >= 3 {
			alt := escapeAttr(parts[1])
			src := escapeAttr(parts[2])
			return `<img src="` + src + `" alt="` + alt + `">`
		}
		return match
	})

	// Links [text](url)
	text = reLink.ReplaceAllStringFunc(text, func(match string) string {
		parts := reLink.FindStringSubmatch(match)
		if len(parts) >= 3 {
			linkText := parts[1]
			href := escapeAttr(parts[2])
			return `<a href="` + href + `">` + linkText + `</a>`
		}
		return match
	})

	// Strikethrough ~~text~~
	text = reStrikethrough.ReplaceAllString(text, "<del>$1</del>")

	// Restore code spans
	for i, code := range codeSpans {
		placeholder := "\x00CODE" + strconv.Itoa(i) + "\x00"
		text = strings.Replace(text, placeholder, "<code>"+code+"</code>", 1)
	}

	return text
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

// sanitizeLanguage sanitizes a code block language identifier to prevent attribute injection.
// Only allows alphanumeric characters, hyphens, and underscores.
func sanitizeLanguage(lang string) string {
	var result strings.Builder
	for _, r := range lang {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' {
			result.WriteRune(r)
		}
	}
	return result.String()
}

// allChars returns true if the string consists entirely of the given character.
func allChars(s string, c byte) bool {
	for i := range len(s) {
		if s[i] != c && s[i] != ' ' {
			return false
		}
	}
	return true
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

	// Blockquotes
	html = reBlockquote.ReplaceAllStringFunc(html, func(s string) string {
		inner := reBlockquote.FindStringSubmatch(s)
		if len(inner) >= 2 {
			lines := strings.Split(strings.TrimSpace(inner[1]), "\n")
			result := make([]string, 0, len(lines))
			for _, line := range lines {
				result = append(result, "> "+strings.TrimSpace(line))
			}
			return strings.Join(result, "\n") + "\n\n"
		}
		return s
	})

	// Code blocks (use (?is) for case-insensitive and dotall mode to match multi-line content)
	html = reCodeBlock.ReplaceAllStringFunc(html, func(s string) string {
		langMatch := reCodeLang.FindStringSubmatch(s)
		lang := ""
		if len(langMatch) >= 2 {
			lang = langMatch[1]
		}
		codeMatch := reCodeInner.FindStringSubmatch(s)
		if len(codeMatch) >= 2 {
			code := unescapeHTML(codeMatch[1])
			return "```" + lang + "\n" + code + "\n```\n\n"
		}
		return s
	})

	// Unordered lists
	html = reUL.ReplaceAllStringFunc(html, func(s string) string {
		inner := reUL.FindStringSubmatch(s)
		if len(inner) >= 2 {
			items := reLI.FindAllStringSubmatch(inner[1], -1)
			var result []string
			for _, item := range items {
				if len(item) >= 2 {
					result = append(result, "- "+strings.TrimSpace(item[1]))
				}
			}
			return strings.Join(result, "\n") + "\n\n"
		}
		return s
	})

	// Ordered lists
	html = reOL.ReplaceAllStringFunc(html, func(s string) string {
		inner := reOL.FindStringSubmatch(s)
		if len(inner) >= 2 {
			items := reLI.FindAllStringSubmatch(inner[1], -1)
			var result []string
			for i, item := range items {
				if len(item) >= 2 {
					result = append(result, strconv.Itoa(i+1)+". "+strings.TrimSpace(item[1]))
				}
			}
			return strings.Join(result, "\n") + "\n\n"
		}
		return s
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
			// Unescape HTML because convertInline already escaped the link text (e.g. & → &amp;)
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

	return reSafeTag.MatchString(stripped)
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
