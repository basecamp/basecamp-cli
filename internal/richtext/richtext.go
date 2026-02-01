// Package richtext provides utilities for converting between Markdown and HTML.
// It uses glamour for terminal-friendly Markdown rendering.
package richtext

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/charmbracelet/glamour"
)

// MarkdownToHTML converts Markdown text to HTML suitable for Basecamp's rich text fields.
// It handles common Markdown syntax: headings, bold, italic, links, lists, code blocks, and blockquotes.
func MarkdownToHTML(md string) string {
	if md == "" {
		return ""
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

	for i := 0; i < len(lines); i++ {
		line := lines[i]

		// Handle code blocks
		if strings.HasPrefix(line, "```") {
			if inCodeBlock {
				// End code block
				code := strings.Join(codeLines, "\n")
				code = escapeHTML(code)
				if codeBlockLang != "" {
					result.WriteString("<pre><code class=\"language-" + codeBlockLang + "\">" + code + "</code></pre>\n")
				} else {
					result.WriteString("<pre><code>" + code + "</code></pre>\n")
				}
				inCodeBlock = false
				codeLines = nil
				codeBlockLang = ""
			} else {
				// Start code block
				flushList()
				inCodeBlock = true
				codeBlockLang = strings.TrimPrefix(line, "```")
			}
			continue
		}

		if inCodeBlock {
			codeLines = append(codeLines, line)
			continue
		}

		// Check for list items
		ulMatch := regexp.MustCompile(`^(\s*)[-*+]\s+(.*)$`).FindStringSubmatch(line)
		olMatch := regexp.MustCompile(`^(\s*)\d+\.\s+(.*)$`).FindStringSubmatch(line)

		if ulMatch != nil {
			if !inList || listType != "ul" {
				flushList()
				inList = true
				listType = "ul"
			}
			listItems = append(listItems, convertInline(ulMatch[2]))
			continue
		}

		if olMatch != nil {
			if !inList || listType != "ol" {
				flushList()
				inList = true
				listType = "ol"
			}
			listItems = append(listItems, convertInline(olMatch[2]))
			continue
		}

		// Not a list item, flush any pending list
		flushList()

		// Empty line
		if strings.TrimSpace(line) == "" {
			continue
		}

		// Headings
		if strings.HasPrefix(line, "######") {
			result.WriteString("<h6>" + convertInline(strings.TrimSpace(strings.TrimPrefix(line, "######"))) + "</h6>\n")
			continue
		}
		if strings.HasPrefix(line, "#####") {
			result.WriteString("<h5>" + convertInline(strings.TrimSpace(strings.TrimPrefix(line, "#####"))) + "</h5>\n")
			continue
		}
		if strings.HasPrefix(line, "####") {
			result.WriteString("<h4>" + convertInline(strings.TrimSpace(strings.TrimPrefix(line, "####"))) + "</h4>\n")
			continue
		}
		if strings.HasPrefix(line, "###") {
			result.WriteString("<h3>" + convertInline(strings.TrimSpace(strings.TrimPrefix(line, "###"))) + "</h3>\n")
			continue
		}
		if strings.HasPrefix(line, "##") {
			result.WriteString("<h2>" + convertInline(strings.TrimSpace(strings.TrimPrefix(line, "##"))) + "</h2>\n")
			continue
		}
		if strings.HasPrefix(line, "#") {
			result.WriteString("<h1>" + convertInline(strings.TrimSpace(strings.TrimPrefix(line, "#"))) + "</h1>\n")
			continue
		}

		// Blockquote
		if strings.HasPrefix(line, ">") {
			quote := strings.TrimSpace(strings.TrimPrefix(line, ">"))
			result.WriteString("<blockquote>" + convertInline(quote) + "</blockquote>\n")
			continue
		}

		// Horizontal rule
		trimmed := strings.TrimSpace(line)
		if len(trimmed) >= 3 && (allChars(trimmed, '-') || allChars(trimmed, '*') || allChars(trimmed, '_')) {
			result.WriteString("<hr>\n")
			continue
		}

		// Regular paragraph
		result.WriteString("<p>" + convertInline(line) + "</p>\n")
	}

	// Flush any remaining list
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
func convertInline(text string) string {
	// Escape HTML entities first (but preserve our conversions)
	text = escapeHTML(text)

	// Code (backticks) - must be done before other inline elements
	text = regexp.MustCompile("`([^`]+)`").ReplaceAllString(text, "<code>$1</code>")

	// Bold with ** or __
	text = regexp.MustCompile(`\*\*([^*]+)\*\*`).ReplaceAllString(text, "<strong>$1</strong>")
	text = regexp.MustCompile(`__([^_]+)__`).ReplaceAllString(text, "<strong>$1</strong>")

	// Italic with * or _ (but not inside words for _)
	text = regexp.MustCompile(`\*([^*]+)\*`).ReplaceAllString(text, "<em>$1</em>")
	text = regexp.MustCompile(`(?:^|[^a-zA-Z0-9])_([^_]+)_(?:[^a-zA-Z0-9]|$)`).ReplaceAllStringFunc(text, func(s string) string {
		// Extract the content and convert
		inner := regexp.MustCompile(`_([^_]+)_`).FindStringSubmatch(s)
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

	// Links [text](url)
	text = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`).ReplaceAllString(text, `<a href="$2">$1</a>`)

	// Images ![alt](url)
	text = regexp.MustCompile(`!\[([^\]]*)\]\(([^)]+)\)`).ReplaceAllString(text, `<img src="$2" alt="$1">`)

	// Strikethrough ~~text~~
	text = regexp.MustCompile(`~~([^~]+)~~`).ReplaceAllString(text, "<del>$1</del>")

	return text
}

// escapeHTML escapes special HTML characters.
func escapeHTML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
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

// RenderMarkdown renders Markdown for terminal display using glamour.
// It returns styled output suitable for CLI display.
func RenderMarkdown(md string) (string, error) {
	if md == "" {
		return "", nil
	}

	r, err := glamour.NewTermRenderer(
		glamour.WithAutoStyle(),
		glamour.WithWordWrap(80),
	)
	if err != nil {
		return "", err
	}

	out, err := r.Render(md)
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(out), nil
}

// RenderMarkdownWithWidth renders Markdown for terminal display with a custom width.
func RenderMarkdownWithWidth(md string, width int) (string, error) {
	if md == "" {
		return "", nil
	}

	r, err := glamour.NewTermRenderer(
		glamour.WithAutoStyle(),
		glamour.WithWordWrap(width),
	)
	if err != nil {
		return "", err
	}

	out, err := r.Render(md)
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(out), nil
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
	html = regexp.MustCompile(`(?i)<h1[^>]*>(.*?)</h1>`).ReplaceAllString(html, "# $1\n\n")
	html = regexp.MustCompile(`(?i)<h2[^>]*>(.*?)</h2>`).ReplaceAllString(html, "## $1\n\n")
	html = regexp.MustCompile(`(?i)<h3[^>]*>(.*?)</h3>`).ReplaceAllString(html, "### $1\n\n")
	html = regexp.MustCompile(`(?i)<h4[^>]*>(.*?)</h4>`).ReplaceAllString(html, "#### $1\n\n")
	html = regexp.MustCompile(`(?i)<h5[^>]*>(.*?)</h5>`).ReplaceAllString(html, "##### $1\n\n")
	html = regexp.MustCompile(`(?i)<h6[^>]*>(.*?)</h6>`).ReplaceAllString(html, "###### $1\n\n")

	// Blockquotes
	html = regexp.MustCompile(`(?i)<blockquote[^>]*>(.*?)</blockquote>`).ReplaceAllStringFunc(html, func(s string) string {
		inner := regexp.MustCompile(`(?i)<blockquote[^>]*>(.*?)</blockquote>`).FindStringSubmatch(s)
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

	// Code blocks
	html = regexp.MustCompile(`(?i)<pre[^>]*><code[^>]*(?:class="language-([^"]*)")?[^>]*>(.*?)</code></pre>`).ReplaceAllStringFunc(html, func(s string) string {
		langMatch := regexp.MustCompile(`class="language-([^"]*)"`).FindStringSubmatch(s)
		lang := ""
		if len(langMatch) >= 2 {
			lang = langMatch[1]
		}
		codeMatch := regexp.MustCompile(`(?i)<code[^>]*>(.*?)</code>`).FindStringSubmatch(s)
		if len(codeMatch) >= 2 {
			code := unescapeHTML(codeMatch[1])
			return "```" + lang + "\n" + code + "\n```\n\n"
		}
		return s
	})

	// Unordered lists
	html = regexp.MustCompile(`(?is)<ul[^>]*>(.*?)</ul>`).ReplaceAllStringFunc(html, func(s string) string {
		inner := regexp.MustCompile(`(?is)<ul[^>]*>(.*?)</ul>`).FindStringSubmatch(s)
		if len(inner) >= 2 {
			items := regexp.MustCompile(`(?is)<li[^>]*>(.*?)</li>`).FindAllStringSubmatch(inner[1], -1)
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
	html = regexp.MustCompile(`(?is)<ol[^>]*>(.*?)</ol>`).ReplaceAllStringFunc(html, func(s string) string {
		inner := regexp.MustCompile(`(?is)<ol[^>]*>(.*?)</ol>`).FindStringSubmatch(s)
		if len(inner) >= 2 {
			items := regexp.MustCompile(`(?is)<li[^>]*>(.*?)</li>`).FindAllStringSubmatch(inner[1], -1)
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
	html = regexp.MustCompile(`(?i)<p[^>]*>(.*?)</p>`).ReplaceAllString(html, "$1\n\n")

	// Line breaks
	html = regexp.MustCompile(`(?i)<br\s*/?\s*>`).ReplaceAllString(html, "\n")

	// Horizontal rules
	html = regexp.MustCompile(`(?i)<hr\s*/?\s*>`).ReplaceAllString(html, "\n---\n\n")

	// Inline elements
	// Bold
	html = regexp.MustCompile(`(?i)<strong[^>]*>(.*?)</strong>`).ReplaceAllString(html, "**$1**")
	html = regexp.MustCompile(`(?i)<b[^>]*>(.*?)</b>`).ReplaceAllString(html, "**$1**")

	// Italic
	html = regexp.MustCompile(`(?i)<em[^>]*>(.*?)</em>`).ReplaceAllString(html, "*$1*")
	html = regexp.MustCompile(`(?i)<i[^>]*>(.*?)</i>`).ReplaceAllString(html, "*$1*")

	// Inline code
	html = regexp.MustCompile(`(?i)<code[^>]*>(.*?)</code>`).ReplaceAllString(html, "`$1`")

	// Links
	html = regexp.MustCompile(`(?i)<a[^>]*href="([^"]*)"[^>]*>(.*?)</a>`).ReplaceAllString(html, "[$2]($1)")

	// Images
	html = regexp.MustCompile(`(?i)<img[^>]*src="([^"]*)"[^>]*alt="([^"]*)"[^>]*/?\s*>`).ReplaceAllString(html, "![$2]($1)")
	html = regexp.MustCompile(`(?i)<img[^>]*alt="([^"]*)"[^>]*src="([^"]*)"[^>]*/?\s*>`).ReplaceAllString(html, "![$1]($2)")
	html = regexp.MustCompile(`(?i)<img[^>]*src="([^"]*)"[^>]*/?\s*>`).ReplaceAllString(html, "![]($1)")

	// Strikethrough
	html = regexp.MustCompile(`(?i)<del[^>]*>(.*?)</del>`).ReplaceAllString(html, "~~$1~~")
	html = regexp.MustCompile(`(?i)<s[^>]*>(.*?)</s>`).ReplaceAllString(html, "~~$1~~")
	html = regexp.MustCompile(`(?i)<strike[^>]*>(.*?)</strike>`).ReplaceAllString(html, "~~$1~~")

	// Remove remaining HTML tags
	html = regexp.MustCompile(`<[^>]+>`).ReplaceAllString(html, "")

	// Unescape HTML entities
	html = unescapeHTML(html)

	// Clean up multiple newlines
	html = regexp.MustCompile(`\n{3,}`).ReplaceAllString(html, "\n\n")

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

	// Check for common Markdown patterns
	patterns := []string{
		`^#{1,6}\s`,           // Headings
		`\*\*[^*]+\*\*`,       // Bold
		`\*[^*]+\*`,           // Italic
		`\[[^\]]+\]\([^)]+\)`, // Links
		"```",                 // Code blocks
		`^[-*+]\s`,            // Unordered list
		`^\d+\.\s`,            // Ordered list
		`^>\s`,                // Blockquote
	}

	for _, pattern := range patterns {
		if matched, _ := regexp.MatchString(pattern, s); matched {
			return true
		}
	}

	return false
}

// IsHTML attempts to detect if the input string contains HTML.
func IsHTML(s string) bool {
	if s == "" {
		return false
	}

	// Check for common HTML tags
	return regexp.MustCompile(`<[a-zA-Z][^>]*>`).MatchString(s)
}
