package richtext

import (
	"fmt"
	"strings"
	"testing"
)

func TestMarkdownToHTML(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "empty string",
			input:    "",
			expected: "",
		},
		{
			name:     "plain text",
			input:    "Hello world",
			expected: "<p>Hello world</p>",
		},
		{
			name:     "h1 heading",
			input:    "# Hello",
			expected: "<h1>Hello</h1>",
		},
		{
			name:     "h2 heading",
			input:    "## Hello",
			expected: "<h2>Hello</h2>",
		},
		{
			name:     "h3 heading",
			input:    "### Hello",
			expected: "<h3>Hello</h3>",
		},
		{
			name:     "bold with asterisks",
			input:    "This is **bold** text",
			expected: "<p>This is <strong>bold</strong> text</p>",
		},
		{
			name:     "bold with underscores",
			input:    "This is __bold__ text",
			expected: "<p>This is <strong>bold</strong> text</p>",
		},
		{
			name:     "italic with asterisk",
			input:    "This is *italic* text",
			expected: "<p>This is <em>italic</em> text</p>",
		},
		{
			name:     "inline code",
			input:    "Use `code` here",
			expected: "<p>Use <code>code</code> here</p>",
		},
		{
			name:     "link",
			input:    "Check [this link](https://example.com)",
			expected: `<p>Check <a href="https://example.com">this link</a></p>`,
		},
		{
			name:     "unordered list",
			input:    "- Item 1\n- Item 2\n- Item 3",
			expected: "<ul>\n<li>Item 1</li>\n<li>Item 2</li>\n<li>Item 3</li>\n</ul>",
		},
		{
			name:     "ordered list",
			input:    "1. First\n2. Second\n3. Third",
			expected: "<ol>\n<li>First</li>\n<li>Second</li>\n<li>Third</li>\n</ol>",
		},
		{
			name:     "ordered list with multi-line items and blank lines",
			input:    "1. First item\n   Description here\n\n2. Second item\n   Another description",
			expected: "<ol>\n<li>First item<br>\nDescription here</li>\n<li>Second item<br>\nAnother description</li>\n</ol>",
		},
		{
			name:     "ordered list with trailing spaces and descriptions",
			input:    "1. **Item** - [Link](url) (time)  \n   Description here\n\n2. **Next** - [Link](url)",
			expected: "<ol>\n<li><strong>Item</strong> - <a href=\"url\">Link</a> (time)  <br>\nDescription here</li>\n<li><strong>Next</strong> - <a href=\"url\">Link</a></li>\n</ol>",
		},
		{
			name:     "list followed by blank line then paragraph",
			input:    "- Item 1\n- Item 2\n\nFollowing paragraph.",
			expected: "<ul>\n<li>Item 1</li>\n<li>Item 2</li>\n</ul>\n<br>\n<p>Following paragraph.</p>",
		},
		{
			name:     "blank between list items does not leak break after list",
			input:    "- One\n\n- Two\nAfter",
			expected: "<ul>\n<li>One</li>\n<li>Two</li>\n</ul>\n<p>After</p>",
		},
		{
			name:     "blockquote",
			input:    "> This is a quote",
			expected: "<blockquote>This is a quote</blockquote>",
		},
		{
			name:     "code block",
			input:    "```go\nfunc main() {}\n```",
			expected: `<pre><code class="language-go">func main() {}</code></pre>`,
		},
		{
			name:     "code block without language",
			input:    "```\nsome code\n```",
			expected: "<pre><code>some code</code></pre>",
		},
		{
			name:     "horizontal rule with dashes",
			input:    "---",
			expected: "<hr>",
		},
		{
			name:     "horizontal rule with asterisks",
			input:    "***",
			expected: "<hr>",
		},
		{
			name:     "strikethrough",
			input:    "This is ~~deleted~~ text",
			expected: "<p>This is <del>deleted</del> text</p>",
		},
		{
			name:     "mixed formatting",
			input:    "# Title\n\nThis is **bold** and *italic* and `code`.",
			expected: "<h1>Title</h1>\n<br>\n<p>This is <strong>bold</strong> and <em>italic</em> and <code>code</code>.</p>",
		},
		{
			name:     "escapes HTML",
			input:    "Use <script> tags",
			expected: "<p>Use &lt;script&gt; tags</p>",
		},
		{
			name:     "escapes ampersand",
			input:    "Tom & Jerry",
			expected: "<p>Tom &amp; Jerry</p>",
		},
		{
			name:     "paragraph spacing with blank line",
			input:    "First paragraph\n\nSecond paragraph",
			expected: "<p>First paragraph</p>\n<br>\n<p>Second paragraph</p>",
		},
		{
			name:     "multiple blank lines collapse to one break",
			input:    "First\n\n\n\nSecond",
			expected: "<p>First</p>\n<br>\n<p>Second</p>",
		},
		{
			name:     "consecutive lines join into one paragraph",
			input:    "Line one\nLine two",
			expected: "<p>Line one Line two</p>",
		},
		{
			name:     "blank line before list",
			input:    "Intro\n\n- Item 1\n- Item 2",
			expected: "<p>Intro</p>\n<br>\n<ul>\n<li>Item 1</li>\n<li>Item 2</li>\n</ul>",
		},
		{
			name:     "blank line before code block",
			input:    "Intro\n\n```\ncode\n```",
			expected: "<p>Intro</p>\n<br>\n<pre><code>code</code></pre>",
		},
		{
			name:     "leading blank lines ignored",
			input:    "\n\nHello",
			expected: "<p>Hello</p>",
		},
		{
			name:     "blank line before blockquote",
			input:    "Intro\n\n> A quote",
			expected: "<p>Intro</p>\n<br>\n<blockquote>A quote</blockquote>",
		},
		{
			name:     "blank line before horizontal rule",
			input:    "Intro\n\n---",
			expected: "<p>Intro</p>\n<br>\n<hr>",
		},
		{
			name:     "heading flushes accumulated paragraph",
			input:    "Text\n# Heading",
			expected: "<p>Text</p>\n<h1>Heading</h1>",
		},
		{
			name:     "list flushes accumulated paragraph",
			input:    "Text\n- Item",
			expected: "<p>Text</p>\n<ul>\n<li>Item</li>\n</ul>",
		},
		{
			name:     "blockquote flushes accumulated paragraph",
			input:    "Text\n> Quote",
			expected: "<p>Text</p>\n<blockquote>Quote</blockquote>",
		},
		{
			name:     "code fence flushes accumulated paragraph",
			input:    "Text\n```go\nx\n```",
			expected: "<p>Text</p>\n<pre><code class=\"language-go\">x</code></pre>",
		},
		{
			name:     "horizontal rule flushes accumulated paragraph",
			input:    "Text\n---",
			expected: "<p>Text</p>\n<hr>",
		},
		{
			name:     "code span containing HTML tag is converted not passthrough",
			input:    "the `<div>` container",
			expected: "<p>the <code>&lt;div&gt;</code> container</p>",
		},
		{
			name:     "fenced code block containing HTML tags is converted",
			input:    "intro\n\n```\n<div>hello</div>\n```",
			expected: "<p>intro</p>\n<br>\n<pre><code>&lt;div&gt;hello&lt;/div&gt;</code></pre>",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := MarkdownToHTML(tt.input)
			if result != tt.expected {
				t.Errorf("MarkdownToHTML(%q)\ngot:  %q\nwant: %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestHTMLToMarkdown(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		contains []string // Use contains for more flexible matching
	}{
		{
			name:     "empty string",
			input:    "",
			contains: []string{},
		},
		{
			name:     "paragraph",
			input:    "<p>Hello world</p>",
			contains: []string{"Hello world"},
		},
		{
			name:     "h1 heading",
			input:    "<h1>Title</h1>",
			contains: []string{"# Title"},
		},
		{
			name:     "h2 heading",
			input:    "<h2>Subtitle</h2>",
			contains: []string{"## Subtitle"},
		},
		{
			name:     "bold strong tag",
			input:    "<p>This is <strong>bold</strong> text</p>",
			contains: []string{"**bold**"},
		},
		{
			name:     "bold b tag",
			input:    "<p>This is <b>bold</b> text</p>",
			contains: []string{"**bold**"},
		},
		{
			name:     "italic em tag",
			input:    "<p>This is <em>italic</em> text</p>",
			contains: []string{"*italic*"},
		},
		{
			name:     "italic i tag",
			input:    "<p>This is <i>italic</i> text</p>",
			contains: []string{"*italic*"},
		},
		{
			name:     "inline code",
			input:    "<p>Use <code>code</code> here</p>",
			contains: []string{"`code`"},
		},
		{
			name:     "link",
			input:    `<p>Check <a href="https://example.com">this link</a></p>`,
			contains: []string{"[this link](https://example.com)"},
		},
		{
			name:     "unordered list",
			input:    "<ul><li>Item 1</li><li>Item 2</li></ul>",
			contains: []string{"- Item 1", "- Item 2"},
		},
		{
			name:     "ordered list",
			input:    "<ol><li>First</li><li>Second</li></ol>",
			contains: []string{"1. First", "2. Second"},
		},
		{
			name:     "blockquote",
			input:    "<blockquote>This is a quote</blockquote>",
			contains: []string{"> This is a quote"},
		},
		{
			name:     "code block",
			input:    `<pre><code class="language-go">func main() {}</code></pre>`,
			contains: []string{"```go", "func main() {}", "```"},
		},
		{
			name:     "horizontal rule",
			input:    "<hr>",
			contains: []string{"---"},
		},
		{
			name:     "strikethrough del",
			input:    "<p>This is <del>deleted</del> text</p>",
			contains: []string{"~~deleted~~"},
		},
		{
			name:     "strikethrough s",
			input:    "<p>This is <s>deleted</s> text</p>",
			contains: []string{"~~deleted~~"},
		},
		{
			name:     "unescapes entities",
			input:    "<p>Tom &amp; Jerry</p>",
			contains: []string{"Tom & Jerry"},
		},
		{
			name:     "image with alt",
			input:    `<img src="https://example.com/img.png" alt="My image">`,
			contains: []string{"![My image](https://example.com/img.png)"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := HTMLToMarkdown(tt.input)
			for _, expected := range tt.contains {
				if !strings.Contains(result, expected) {
					t.Errorf("HTMLToMarkdown(%q)\ngot:  %q\nmissing: %q", tt.input, result, expected)
				}
			}
		})
	}
}

func TestRenderMarkdown(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{
			name:    "empty string",
			input:   "",
			wantErr: false,
		},
		{
			name:    "simple text",
			input:   "Hello world",
			wantErr: false,
		},
		{
			name:    "heading",
			input:   "# Hello",
			wantErr: false,
		},
		{
			name:    "bold text",
			input:   "This is **bold**",
			wantErr: false,
		},
		{
			name:    "code block",
			input:   "```go\nfunc main() {}\n```",
			wantErr: false,
		},
		{
			name:    "list",
			input:   "- Item 1\n- Item 2",
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := RenderMarkdown(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("RenderMarkdown() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			// Empty input should return empty output
			if tt.input == "" && result != "" {
				t.Errorf("RenderMarkdown(%q) = %q, want empty string", tt.input, result)
			}
			// Non-empty input should return non-empty output
			if tt.input != "" && result == "" {
				t.Errorf("RenderMarkdown(%q) returned empty string", tt.input)
			}
		})
	}
}

func TestRenderMarkdownWithWidth(t *testing.T) {
	input := "This is a very long line that should be wrapped at a specific width for testing purposes."

	result80, err := RenderMarkdownWithWidth(input, 80)
	if err != nil {
		t.Fatalf("RenderMarkdownWithWidth failed: %v", err)
	}

	result40, err := RenderMarkdownWithWidth(input, 40)
	if err != nil {
		t.Fatalf("RenderMarkdownWithWidth failed: %v", err)
	}

	// Both should produce output
	if result80 == "" || result40 == "" {
		t.Error("RenderMarkdownWithWidth returned empty string")
	}
}

func TestIsMarkdown(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{
			name:     "empty string",
			input:    "",
			expected: false,
		},
		{
			name:     "plain text",
			input:    "Hello world",
			expected: false,
		},
		{
			name:     "heading",
			input:    "# Hello",
			expected: true,
		},
		{
			name:     "bold",
			input:    "This is **bold** text",
			expected: true,
		},
		{
			name:     "italic",
			input:    "This is *italic* text",
			expected: true,
		},
		{
			name:     "link",
			input:    "Check [this](https://example.com)",
			expected: true,
		},
		{
			name:     "code block",
			input:    "```go\ncode\n```",
			expected: true,
		},
		{
			name:     "unordered list",
			input:    "- Item",
			expected: true,
		},
		{
			name:     "ordered list",
			input:    "1. Item",
			expected: true,
		},
		{
			name:     "blockquote",
			input:    "> Quote",
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsMarkdown(tt.input)
			if result != tt.expected {
				t.Errorf("IsMarkdown(%q) = %v, want %v", tt.input, result, tt.expected)
			}
		})
	}
}

func TestIsHTML(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{
			name:     "empty string",
			input:    "",
			expected: false,
		},
		{
			name:     "plain text",
			input:    "Hello world",
			expected: false,
		},
		{
			name:     "paragraph tag",
			input:    "<p>Hello</p>",
			expected: true,
		},
		{
			name:     "div tag",
			input:    "<div>Content</div>",
			expected: true,
		},
		{
			name:     "self-closing tag",
			input:    "<br />",
			expected: true,
		},
		{
			name:     "tag with attributes",
			input:    `<a href="url">link</a>`,
			expected: true,
		},
		{
			name:     "angle brackets in text",
			input:    "5 < 10",
			expected: false,
		},
		{
			name:     "markdown with asterisks",
			input:    "This is **bold**",
			expected: false,
		},
		{
			name:     "bc-attachment mention",
			input:    `<bc-attachment sgid="BAh7CEkiCG" content-type="application/vnd.basecamp.mention">@Alice</bc-attachment>`,
			expected: true,
		},
		{
			name:     "bc-attachment file",
			input:    `<bc-attachment sgid="BAh7" content-type="application/pdf" filename="report.pdf"></bc-attachment>`,
			expected: true,
		},
		{
			name:     "HTML tag inside backtick code span",
			input:    "the `<div>` container",
			expected: false,
		},
		{
			name:     "HTML tag inside multi-word code span",
			input:    "use `<strong>bold</strong>` for emphasis",
			expected: false,
		},
		{
			name:     "real HTML with code span containing tag",
			input:    `<p>the <code>&lt;div&gt;</code> container</p>`,
			expected: true,
		},
		{
			name:     "HTML tag inside fenced code block",
			input:    "```\n<div>hello</div>\n```",
			expected: false,
		},
		{
			name:     "mixed markdown with code span tag",
			input:    "Check `<br>` and **bold** text",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsHTML(tt.input)
			if result != tt.expected {
				t.Errorf("IsHTML(%q) = %v, want %v", tt.input, result, tt.expected)
			}
		})
	}
}

func TestRoundTrip(t *testing.T) {
	// Test that converting Markdown -> HTML -> Markdown preserves meaning
	tests := []struct {
		name     string
		markdown string
	}{
		{
			name:     "heading",
			markdown: "# Hello",
		},
		{
			name:     "bold text",
			markdown: "This is **bold** text",
		},
		{
			name:     "link",
			markdown: "[link](https://example.com)",
		},
		{
			name:     "unordered list",
			markdown: "- Item 1\n- Item 2",
		},
		{
			name:     "consecutive lines merge into single paragraph",
			markdown: "Line 1\nLine 2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			html := MarkdownToHTML(tt.markdown)
			if html == "" {
				t.Errorf("MarkdownToHTML(%q) returned empty", tt.markdown)
				return
			}

			back := HTMLToMarkdown(html)
			if back == "" {
				t.Errorf("HTMLToMarkdown(%q) returned empty", html)
				return
			}

			// The round-trip should preserve the basic structure
			// Note: exact equality is not expected due to formatting differences
			t.Logf("Original: %q", tt.markdown)
			t.Logf("HTML: %q", html)
			t.Logf("Back: %q", back)
		})
	}

	// Consecutive lines should round-trip through a single paragraph
	t.Run("consecutive lines round-trip as single paragraph", func(t *testing.T) {
		input := "Line 1\nLine 2"
		html := MarkdownToHTML(input)
		back := HTMLToMarkdown(html)
		if strings.Contains(back, "\n\n") {
			t.Errorf("round-trip produced two paragraphs, want one\nhtml: %q\nback: %q", html, back)
		}
		if !strings.Contains(back, "Line 1") || !strings.Contains(back, "Line 2") {
			t.Errorf("round-trip lost content\nhtml: %q\nback: %q", html, back)
		}
	})
}

func TestMarkdownToHTMLListVariants(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "dash list",
			input:    "- Item",
			expected: "<ul>\n<li>Item</li>\n</ul>",
		},
		{
			name:     "asterisk list",
			input:    "* Item",
			expected: "<ul>\n<li>Item</li>\n</ul>",
		},
		{
			name:     "plus list",
			input:    "+ Item",
			expected: "<ul>\n<li>Item</li>\n</ul>",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := MarkdownToHTML(tt.input)
			if result != tt.expected {
				t.Errorf("MarkdownToHTML(%q)\ngot:  %q\nwant: %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestAttachmentToHTML(t *testing.T) {
	got := AttachmentToHTML("BAh123==", "report.pdf", "application/pdf")
	want := `<bc-attachment sgid="BAh123==" content-type="application/pdf" filename="report.pdf"></bc-attachment>`
	if got != want {
		t.Errorf("AttachmentToHTML\ngot:  %s\nwant: %s", got, want)
	}
}

func TestAttachmentToHTMLEscapes(t *testing.T) {
	got := AttachmentToHTML(`bad"sgid`, `file"name.pdf`, `type"bad`)
	if !strings.Contains(got, "&quot;") {
		t.Errorf("AttachmentToHTML should escape quotes, got: %s", got)
	}
}

func TestEmbedAttachments(t *testing.T) {
	html := "<p>Hello</p>"
	refs := []AttachmentRef{
		{SGID: "abc", Filename: "doc.pdf", ContentType: "application/pdf"},
		{SGID: "def", Filename: "img.png", ContentType: "image/png"},
	}
	got := EmbedAttachments(html, refs)
	if !strings.Contains(got, "<p>Hello</p>") {
		t.Error("EmbedAttachments should preserve original HTML")
	}
	if !strings.Contains(got, `filename="doc.pdf"`) {
		t.Error("EmbedAttachments should include first attachment")
	}
	if !strings.Contains(got, `filename="img.png"`) {
		t.Error("EmbedAttachments should include second attachment")
	}
}

func TestEmbedAttachmentsEmpty(t *testing.T) {
	html := "<p>Hello</p>"
	got := EmbedAttachments(html, nil)
	if got != html {
		t.Errorf("EmbedAttachments(nil) should return input unchanged, got: %s", got)
	}
}

func TestExtractAttachments(t *testing.T) {
	tests := []struct {
		name string
		html string
		want []InlineAttachment
	}{
		{
			name: "empty input",
			html: "",
			want: nil,
		},
		{
			name: "no attachments",
			html: "<p>Hello world</p>",
			want: nil,
		},
		{
			name: "mention excluded",
			html: `<bc-attachment sgid="BAh7" content-type="application/vnd.basecamp.mention" href="https://example.com">@Alice</bc-attachment>`,
			want: nil,
		},
		{
			name: "attachment without href excluded",
			html: `<bc-attachment sgid="BAh7" content-type="application/pdf" filename="report.pdf"></bc-attachment>`,
			want: nil,
		},
		{
			name: "single file attachment",
			html: `<bc-attachment sgid="BAh7CEk" content-type="application/pdf" href="https://storage.3.basecamp.com/123/blobs/abc/download/report.pdf" filename="report.pdf" filesize="12345"></bc-attachment>`,
			want: []InlineAttachment{
				{
					Href:        "https://storage.3.basecamp.com/123/blobs/abc/download/report.pdf",
					Filename:    "report.pdf",
					Filesize:    "12345",
					ContentType: "application/pdf",
					SGID:        "BAh7CEk",
				},
			},
		},
		{
			name: "multiple mixed: mention + file",
			html: `<p>Hey <bc-attachment sgid="m1" content-type="application/vnd.basecamp.mention" href="https://example.com">@Alice</bc-attachment> see ` +
				`<bc-attachment sgid="f1" content-type="message/rfc822" href="https://storage.3.basecamp.com/123/blobs/def/download/email.eml" filename="email.eml" filesize="9999"></bc-attachment></p>`,
			want: []InlineAttachment{
				{
					Href:        "https://storage.3.basecamp.com/123/blobs/def/download/email.eml",
					Filename:    "email.eml",
					Filesize:    "9999",
					ContentType: "message/rfc822",
					SGID:        "f1",
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractAttachments(tt.html)
			if len(got) != len(tt.want) {
				t.Fatalf("ExtractAttachments() returned %d attachments, want %d", len(got), len(tt.want))
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("attachment[%d]\ngot:  %+v\nwant: %+v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestHTMLToMarkdownBcAttachment(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		contains string
	}{
		{
			name:     "attachment with filename",
			input:    `<p>Here's the doc</p><bc-attachment sgid="BAh" content-type="application/pdf" filename="report.pdf"></bc-attachment>`,
			contains: "📎 report.pdf",
		},
		{
			name:     "attachment self-closing",
			input:    `<bc-attachment sgid="x" filename="img.png" content-type="image/png"/>`,
			contains: "📎 img.png",
		},
		{
			name:     "multiple attachments",
			input:    `<bc-attachment sgid="a" filename="one.pdf" content-type="application/pdf"></bc-attachment><bc-attachment sgid="b" filename="two.zip" content-type="application/zip"></bc-attachment>`,
			contains: "📎 one.pdf",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := HTMLToMarkdown(tt.input)
			if !strings.Contains(result, tt.contains) {
				t.Errorf("HTMLToMarkdown(%q)\ngot:  %q\nmissing: %q", tt.input, result, tt.contains)
			}
		})
	}
}

func TestHTMLToMarkdown_Mention(t *testing.T) {
	input := `<p>Hey <bc-attachment sgid="BAh7CEkiCG" content-type="application/vnd.basecamp.mention">@Alice</bc-attachment> check this</p>`
	result := HTMLToMarkdown(input)
	if !strings.Contains(result, "**@Alice**") {
		t.Errorf("mention not rendered as bold\ngot: %q", result)
	}
	if strings.Contains(result, "bc-attachment") {
		t.Errorf("bc-attachment tag leaked through\ngot: %q", result)
	}
}

func TestHTMLToMarkdown_AttachmentNoFilename(t *testing.T) {
	input := `<bc-attachment sgid="BAh7" content-type="image/png"></bc-attachment>`
	result := HTMLToMarkdown(input)
	if !strings.Contains(result, "📎 attachment") {
		t.Errorf("attachment without filename not rendered\ngot: %q", result)
	}
}

func TestHTMLToMarkdownPreservesContent(t *testing.T) {
	// Test that complex HTML structures are handled
	input := `<h1>Main Title</h1>
<p>This is a paragraph with <strong>bold</strong> and <em>italic</em> text.</p>
<ul>
<li>First item</li>
<li>Second item</li>
</ul>
<p>Check out <a href="https://example.com">this link</a>.</p>`

	result := HTMLToMarkdown(input)

	// Check key elements are present
	checks := []string{
		"# Main Title",
		"**bold**",
		"*italic*",
		"- First item",
		"- Second item",
		"[this link](https://example.com)",
	}

	for _, check := range checks {
		if !strings.Contains(result, check) {
			t.Errorf("HTMLToMarkdown result missing %q\nFull result: %q", check, result)
		}
	}
}

func TestMentionToHTML(t *testing.T) {
	got := MentionToHTML("sgid-abc123", "John Doe")
	expected := `<bc-attachment sgid="sgid-abc123" content-type="application/vnd.basecamp.mention">@John Doe</bc-attachment>`
	if got != expected {
		t.Errorf("MentionToHTML() = %q, want %q", got, expected)
	}
}

func TestResolveMentions(t *testing.T) {
	lookup := func(name string) (sgid, displayName string, err error) {
		people := map[string][2]string{
			"John":          {"sgid-john", "John Doe"},
			"John Doe":      {"sgid-john", "John Doe"},
			"Igor":          {"sgid-igor", "Igor Logachev"},
			"Igor Logachev": {"sgid-igor", "Igor Logachev"},
			"José":          {"sgid-jose", "José García"},
		}
		if p, ok := people[name]; ok {
			return p[0], p[1], nil
		}
		return "", "", fmt.Errorf("not found: %s", name)
	}

	tests := []struct {
		name     string
		input    string
		expected string
		wantErr  bool
	}{
		{
			name:     "single mention",
			input:    `<p>Hey @John, check this</p>`,
			expected: `<p>Hey ` + MentionToHTML("sgid-john", "John Doe") + `, check this</p>`,
		},
		{
			name:     "first.last mention",
			input:    `<p>Hey @Igor.Logachev, check this</p>`,
			expected: `<p>Hey ` + MentionToHTML("sgid-igor", "Igor Logachev") + `, check this</p>`,
		},
		{
			name:     "multiple mentions",
			input:    `<p>@John and @Igor please review</p>`,
			expected: `<p>` + MentionToHTML("sgid-john", "John Doe") + ` and ` + MentionToHTML("sgid-igor", "Igor Logachev") + ` please review</p>`,
		},
		{
			name:     "no mentions",
			input:    `<p>Hello world</p>`,
			expected: `<p>Hello world</p>`,
		},
		{
			name:     "mention at start of line",
			input:    `@John hello`,
			expected: MentionToHTML("sgid-john", "John Doe") + ` hello`,
		},
		{
			name:     "email not treated as mention",
			input:    `<p>Send to user@John.com</p>`,
			expected: `<p>Send to user@John.com</p>`,
		},
		{
			name:    "unresolved mention is error",
			input:   `<p>Hey @Unknown</p>`,
			wantErr: true,
		},
		{
			name:     "mention inside HTML tag is skipped",
			input:    `<a href="@John">link</a>`,
			expected: `<a href="@John">link</a>`,
		},
		{
			name:     "mention inside existing bc-attachment is skipped",
			input:    `<bc-attachment sgid="x" content-type="application/vnd.basecamp.mention">@John</bc-attachment>`,
			expected: `<bc-attachment sgid="x" content-type="application/vnd.basecamp.mention">@John</bc-attachment>`,
		},
		{
			name:     "unicode name mention",
			input:    `<p>Hey @José, check this</p>`,
			expected: `<p>Hey ` + MentionToHTML("sgid-jose", "José García") + `, check this</p>`,
		},
		{
			name:     "mention inside code block is skipped",
			input:    `<p>Use <code>@John</code> syntax</p>`,
			expected: `<p>Use <code>@John</code> syntax</p>`,
		},
		{
			name:     "mention inside pre block is skipped",
			input:    `<pre>@John example</pre>`,
			expected: `<pre>@John example</pre>`,
		},
		{
			name:     "mention after self-closing bc-attachment is resolved",
			input:    `<bc-attachment sgid="x" content-type="image/png"/> @John check this`,
			expected: `<bc-attachment sgid="x" content-type="image/png"/> ` + MentionToHTML("sgid-john", "John Doe") + ` check this`,
		},
		{
			name:     "mention inside pre after preview tag is skipped",
			input:    `<preview>stuff</preview><pre>@John example</pre>`,
			expected: `<preview>stuff</preview><pre>@John example</pre>`,
		},
		// Expanded prefix tests
		{
			name:     "mention after open paren",
			input:    `<p>(@John) check this</p>`,
			expected: `<p>(` + MentionToHTML("sgid-john", "John Doe") + `) check this</p>`,
		},
		{
			name:     "mention after open bracket",
			input:    `<p>[@John] check this</p>`,
			expected: `<p>[` + MentionToHTML("sgid-john", "John Doe") + `] check this</p>`,
		},
		{
			name:     "mention after double quote",
			input:    `<p>"@John" check this</p>`,
			expected: `<p>"` + MentionToHTML("sgid-john", "John Doe") + `" check this</p>`,
		},
		{
			name:     "mention after single quote",
			input:    `<p>'@John' check this</p>`,
			expected: `<p>'` + MentionToHTML("sgid-john", "John Doe") + `' check this</p>`,
		},
		// Trailing-character bailout tests
		{
			name:     "hyphen bailout",
			input:    `<p>Hey @John-Doe</p>`,
			expected: `<p>Hey @John-Doe</p>`,
			wantErr:  false,
		},
		{
			name:     "apostrophe letter bailout",
			input:    `<p>Hey @John's stuff</p>`,
			expected: `<p>Hey @John's stuff</p>`,
			wantErr:  false,
		},
		{
			name:     "apostrophe then non-letter is not bailout",
			input:    `<p>'@John' said hi</p>`,
			expected: `<p>'` + MentionToHTML("sgid-john", "John Doe") + `' said hi</p>`,
		},
		// Case-insensitive bc-attachment guard
		{
			name:     "uppercase BC-ATTACHMENT skips inner mention",
			input:    `<BC-ATTACHMENT sgid="x" content-type="application/vnd.basecamp.mention">@John</BC-ATTACHMENT>`,
			expected: `<BC-ATTACHMENT sgid="x" content-type="application/vnd.basecamp.mention">@John</BC-ATTACHMENT>`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ResolveMentions(tt.input, lookup, nil)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result.HTML != tt.expected {
				t.Errorf("ResolveMentions() =\n  %q\nwant:\n  %q", result.HTML, tt.expected)
			}
		})
	}
}

func TestResolveMentions_MentionSGID(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "mention scheme — zero API calls",
			input:    `<a href="mention:BAh7CEkiCG">@Jane Smith</a>`,
			expected: MentionToHTML("BAh7CEkiCG", "Jane Smith"),
		},
		{
			name:     "mention scheme without @ in link text",
			input:    `<a href="mention:BAh7CEkiCG">Jane Smith</a>`,
			expected: MentionToHTML("BAh7CEkiCG", "Jane Smith"),
		},
		{
			name:     "mention in paragraph",
			input:    `<p>Hey <a href="mention:BAh7CEkiCG">@Jane Smith</a>, check this</p>`,
			expected: `<p>Hey ` + MentionToHTML("BAh7CEkiCG", "Jane Smith") + `, check this</p>`,
		},
		{
			name:     "mention inside code block is skipped",
			input:    `<code><a href="mention:BAh7">@Jane</a></code>`,
			expected: `<code><a href="mention:BAh7">@Jane</a></code>`,
		},
		{
			name:     "mention inside pre block is skipped",
			input:    `<pre><a href="mention:BAh7">@Jane</a></pre>`,
			expected: `<pre><a href="mention:BAh7">@Jane</a></pre>`,
		},
		{
			name:     "mention inside bc-attachment is skipped",
			input:    `<bc-attachment sgid="x" content-type="text/plain"><a href="mention:BAh7">@Jane</a></bc-attachment>`,
			expected: `<bc-attachment sgid="x" content-type="text/plain"><a href="mention:BAh7">@Jane</a></bc-attachment>`,
		},
		{
			name:     "uppercase BC-ATTACHMENT around mention anchor is skipped",
			input:    `<BC-ATTACHMENT sgid="x" content-type="text/plain"><a href="mention:BAh7">@Jane</a></BC-ATTACHMENT>`,
			expected: `<BC-ATTACHMENT sgid="x" content-type="text/plain"><a href="mention:BAh7">@Jane</a></BC-ATTACHMENT>`,
		},
		{
			name:     "normal link scheme is not intercepted by anchor regex",
			input:    `<a href="http://example.com">link text</a>`,
			expected: `<a href="http://example.com">link text</a>`,
		},
		{
			name:     "mention scheme with HTML entities in link text does not double-escape",
			input:    `<a href="mention:sgid-att">@AT&amp;T</a>`,
			expected: MentionToHTML("sgid-att", "AT&T"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ResolveMentions(tt.input, nil, nil)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result.HTML != tt.expected {
				t.Errorf("ResolveMentions() =\n  %q\nwant:\n  %q", result.HTML, tt.expected)
			}
		})
	}
}

func TestResolveMentions_PersonID(t *testing.T) {
	lookupByID := func(id string) (string, string, error) {
		people := map[string][2]string{
			"42000": {"sgid-jane", "Jane Smith"},
			"42001": {"sgid-bob", "Bob Jones"},
		}
		if p, ok := people[id]; ok {
			return p[0], p[1], nil
		}
		return "", "", fmt.Errorf("person not found: %s", id)
	}

	tests := []struct {
		name     string
		input    string
		expected string
		wantErr  bool
	}{
		{
			name:     "person scheme — uses canonical name",
			input:    `<a href="person:42000">@Wrong Name</a>`,
			expected: MentionToHTML("sgid-jane", "Jane Smith"),
		},
		{
			name:     "person scheme in paragraph",
			input:    `<p>Hey <a href="person:42000">@Jane</a>, check this</p>`,
			expected: `<p>Hey ` + MentionToHTML("sgid-jane", "Jane Smith") + `, check this</p>`,
		},
		{
			name:    "person scheme — not pingable",
			input:   `<a href="person:99999">@Nobody</a>`,
			wantErr: true,
		},
		{
			name:     "person inside code block is skipped",
			input:    `<code><a href="person:42000">@Jane</a></code>`,
			expected: `<code><a href="person:42000">@Jane</a></code>`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ResolveMentions(tt.input, nil, lookupByID)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result.HTML != tt.expected {
				t.Errorf("ResolveMentions() =\n  %q\nwant:\n  %q", result.HTML, tt.expected)
			}
		})
	}
}

func TestResolveMentions_SGIDInline(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "sgid inline — direct embed",
			input:    `<p>Hey @sgid:BAh7CEkiCG, check this</p>`,
			expected: `<p>Hey ` + MentionToHTML("BAh7CEkiCG", "BAh7CEkiCG") + `, check this</p>`,
		},
		{
			name:     "sgid at start of line",
			input:    `@sgid:BAh7CEkiCG check this`,
			expected: MentionToHTML("BAh7CEkiCG", "BAh7CEkiCG") + ` check this`,
		},
		{
			name:     "sgid with base64 chars",
			input:    `<p>Hey @sgid:BAh7+CG/k=, check</p>`,
			expected: `<p>Hey ` + MentionToHTML("BAh7+CG/k=", "BAh7+CG/k=") + `, check</p>`,
		},
		{
			name:     "sgid inside code is skipped",
			input:    `<code>@sgid:BAh7CEkiCG</code>`,
			expected: `<code>@sgid:BAh7CEkiCG</code>`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ResolveMentions(tt.input, nil, nil)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result.HTML != tt.expected {
				t.Errorf("ResolveMentions() =\n  %q\nwant:\n  %q", result.HTML, tt.expected)
			}
		})
	}
}

func TestResolveMentions_Mixed(t *testing.T) {
	lookup := func(name string) (string, string, error) {
		if name == "John" || name == "John Doe" {
			return "sgid-john", "John Doe", nil
		}
		return "", "", fmt.Errorf("not found: %s", name)
	}

	t.Run("markdown mention resolved first then fuzzy", func(t *testing.T) {
		input := `<p><a href="mention:BAh7">@Jane</a> and @John</p>`
		expected := `<p>` + MentionToHTML("BAh7", "Jane") + ` and ` + MentionToHTML("sgid-john", "John Doe") + `</p>`
		result, err := ResolveMentions(input, lookup, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.HTML != expected {
			t.Errorf("ResolveMentions() =\n  %q\nwant:\n  %q", result.HTML, expected)
		}
	})
}

func TestResolveMentions_PersonSchemeNilLookup(t *testing.T) {
	input := `<a href="person:42000">@Jane</a>`
	_, err := ResolveMentions(input, nil, nil)
	if err == nil {
		t.Error("expected error for person: scheme with nil lookupByID")
	}
}

func TestResolveMentions_ErrMentionSkip(t *testing.T) {
	lookup := func(name string) (string, string, error) {
		people := map[string][2]string{
			"John":     {"sgid-john", "John Doe"},
			"John Doe": {"sgid-john", "John Doe"},
			"Beth":     {"sgid-beth", "Beth Smith"},
		}
		if p, ok := people[name]; ok {
			return p[0], p[1], nil
		}
		return "", "", fmt.Errorf("%w: not found: %s", ErrMentionSkip, name)
	}

	t.Run("mixed valid and invalid mentions", func(t *testing.T) {
		input := `<p>Hey @John, @Beth, and @Bobby are you around</p>`
		result, err := ResolveMentions(input, lookup, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		expected := `<p>Hey ` + MentionToHTML("sgid-john", "John Doe") + `, ` +
			MentionToHTML("sgid-beth", "Beth Smith") + `, and @Bobby are you around</p>`
		if result.HTML != expected {
			t.Errorf("HTML =\n  %q\nwant:\n  %q", result.HTML, expected)
		}
		if len(result.Unresolved) != 1 || result.Unresolved[0] != "@Bobby" {
			t.Errorf("Unresolved = %v, want [@Bobby]", result.Unresolved)
		}
	})

	t.Run("all invalid fuzzy mentions preserves input order", func(t *testing.T) {
		input := `<p>Hey @Unknown and @Nobody</p>`
		result, err := ResolveMentions(input, lookup, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if result.HTML != input {
			t.Errorf("HTML should be unchanged, got:\n  %q", result.HTML)
		}
		want := []string{"@Unknown", "@Nobody"}
		if len(result.Unresolved) != len(want) {
			t.Fatalf("Unresolved = %v, want %v", result.Unresolved, want)
		}
		for i, name := range want {
			if result.Unresolved[i] != name {
				t.Errorf("Unresolved[%d] = %q, want %q", i, result.Unresolved[i], name)
			}
		}
	})

	t.Run("all valid mentions returns empty unresolved", func(t *testing.T) {
		input := `<p>Hey @John and @Beth</p>`
		result, err := ResolveMentions(input, lookup, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(result.Unresolved) != 0 {
			t.Errorf("Unresolved should be empty, got %v", result.Unresolved)
		}
	})

	t.Run("non-skip error still fails", func(t *testing.T) {
		failLookup := func(name string) (string, string, error) {
			return "", "", fmt.Errorf("network timeout")
		}
		input := `<p>Hey @John</p>`
		_, err := ResolveMentions(input, failLookup, nil)
		if err == nil {
			t.Error("expected error for non-skip failure")
		}
	})

	t.Run("person ID scheme still hard fails", func(t *testing.T) {
		lookupByID := func(id string) (string, string, error) {
			return "", "", fmt.Errorf("not pingable")
		}
		input := `<a href="person:99999">@Ghost</a>`
		_, err := ResolveMentions(input, lookup, lookupByID)
		if err == nil {
			t.Error("expected error for person:ID resolution failure")
		}
	})
}

func TestParseAttachments(t *testing.T) {
	tests := []struct {
		name     string
		html     string
		expected int
		check    func(*testing.T, []ParsedAttachment)
	}{
		{
			name:     "no attachments",
			html:     `<p>Just some regular HTML content</p>`,
			expected: 0,
		},
		{
			name:     "empty string",
			html:     "",
			expected: 0,
		},
		{
			name: "single image with nested figure",
			html: `<bc-attachment sgid="BAh7CEkiCG" content-type="image/jpeg" width="2560" height="1536" url="https://example.com/image.jpg" href="https://example.com/image.jpg" filename="photo.jpg" caption="My photo">
  <figure><img src="..."><figcaption>My photo</figcaption></figure>
</bc-attachment>`,
			expected: 1,
			check: func(t *testing.T, atts []ParsedAttachment) {
				a := atts[0]
				if a.SGID != "BAh7CEkiCG" {
					t.Errorf("SGID = %q, want BAh7CEkiCG", a.SGID)
				}
				if a.ContentType != "image/jpeg" {
					t.Errorf("ContentType = %q, want image/jpeg", a.ContentType)
				}
				if a.Filename != "photo.jpg" {
					t.Errorf("Filename = %q, want photo.jpg", a.Filename)
				}
				if a.Caption != "My photo" {
					t.Errorf("Caption = %q, want My photo", a.Caption)
				}
				if a.Width != "2560" || a.Height != "1536" {
					t.Errorf("Dimensions = %sx%s, want 2560x1536", a.Width, a.Height)
				}
				if !a.IsImage() {
					t.Error("IsImage() = false, want true")
				}
				if a.DisplayName() != "My photo" {
					t.Errorf("DisplayName() = %q, want My photo", a.DisplayName())
				}
			},
		},
		{
			name: "multiple attachments",
			html: `<div>
  <bc-attachment sgid="SGIDone" content-type="image/png" filename="first.png" url="https://example.com/first.png"></bc-attachment>
  <bc-attachment sgid="SGIDtwo" content-type="image/gif" filename="second.gif" url="https://example.com/second.gif"></bc-attachment>
</div>`,
			expected: 2,
			check: func(t *testing.T, atts []ParsedAttachment) {
				if atts[0].Filename != "first.png" {
					t.Errorf("first filename = %q, want first.png", atts[0].Filename)
				}
				if atts[1].Filename != "second.gif" {
					t.Errorf("second filename = %q, want second.gif", atts[1].Filename)
				}
			},
		},
		{
			name:     "self-closing tag",
			html:     `<bc-attachment sgid="TEST123" content-type="application/pdf" filename="document.pdf" url="https://example.com/doc.pdf" />`,
			expected: 1,
			check: func(t *testing.T, atts []ParsedAttachment) {
				a := atts[0]
				if a.ContentType != "application/pdf" {
					t.Errorf("ContentType = %q, want application/pdf", a.ContentType)
				}
				if a.IsImage() {
					t.Error("IsImage() = true, want false for PDF")
				}
			},
		},
		{
			name: "mentions filtered out",
			html: `<bc-attachment sgid="MENTION1" content-type="application/vnd.basecamp.mention">@Jane</bc-attachment>
<bc-attachment sgid="FILE1" content-type="image/png" filename="real.png"></bc-attachment>`,
			expected: 1,
			check: func(t *testing.T, atts []ParsedAttachment) {
				if atts[0].Filename != "real.png" {
					t.Errorf("Filename = %q, want real.png", atts[0].Filename)
				}
			},
		},
		{
			name:     "apostrophe in double-quoted attribute value",
			html:     `<bc-attachment sgid="APO1" content-type="image/jpeg" filename="Brian's Report.jpg" caption="It's done"></bc-attachment>`,
			expected: 1,
			check: func(t *testing.T, atts []ParsedAttachment) {
				a := atts[0]
				if a.Filename != "Brian's Report.jpg" {
					t.Errorf("Filename = %q, want %q", a.Filename, "Brian's Report.jpg")
				}
				if a.Caption != "It's done" {
					t.Errorf("Caption = %q, want %q", a.Caption, "It's done")
				}
			},
		},
		{
			name:     "single-quoted attribute values",
			html:     `<bc-attachment sgid='SQ1' content-type='image/png' filename='single.png' url='https://example.com/single.png'></bc-attachment>`,
			expected: 1,
			check: func(t *testing.T, atts []ParsedAttachment) {
				a := atts[0]
				if a.SGID != "SQ1" {
					t.Errorf("SGID = %q, want SQ1", a.SGID)
				}
				if a.Filename != "single.png" {
					t.Errorf("Filename = %q, want single.png", a.Filename)
				}
			},
		},
		{
			name:     "url attr not confused with data-url",
			html:     `<bc-attachment sgid="BD1" content-type="image/png" data-url="https://wrong.com" url="https://right.com" filename="boundary.png"></bc-attachment>`,
			expected: 1,
			check: func(t *testing.T, atts []ParsedAttachment) {
				a := atts[0]
				if a.URL != "https://right.com" {
					t.Errorf("URL = %q, want https://right.com", a.URL)
				}
			},
		},
		{
			name:     "HTML entities decoded in attributes",
			html:     `<bc-attachment sgid="ENT1" content-type="image/png" filename="O&#39;Brien &amp; Co.png" url="https://example.com/file?a=1&amp;b=2"></bc-attachment>`,
			expected: 1,
			check: func(t *testing.T, atts []ParsedAttachment) {
				a := atts[0]
				if a.Filename != "O'Brien & Co.png" {
					t.Errorf("Filename = %q, want %q", a.Filename, "O'Brien & Co.png")
				}
				if a.URL != "https://example.com/file?a=1&b=2" {
					t.Errorf("URL = %q, want decoded URL", a.URL)
				}
			},
		},
		{
			name:     "tag boundary prevents false match on bc-attachment-foo",
			html:     `<bc-attachment-custom sgid="NOPE" content-type="image/png" filename="nope.png"></bc-attachment-custom>`,
			expected: 0,
		},
		{
			name: "case-insensitive mention filtering",
			html: `<bc-attachment sgid="M1" content-type="Application/Vnd.Basecamp.Mention">@Jane</bc-attachment>
<bc-attachment sgid="F1" content-type="image/png" filename="real.png"></bc-attachment>`,
			expected: 1,
			check: func(t *testing.T, atts []ParsedAttachment) {
				if atts[0].Filename != "real.png" {
					t.Errorf("Filename = %q, want real.png", atts[0].Filename)
				}
			},
		},
		{
			name:     "mixed-case image content type",
			html:     `<bc-attachment sgid="MC1" content-type="Image/PNG" filename="mixed.png"></bc-attachment>`,
			expected: 1,
			check: func(t *testing.T, atts []ParsedAttachment) {
				if !atts[0].IsImage() {
					t.Error("IsImage() = false for mixed-case Image/PNG, want true")
				}
			},
		},
		{
			name:     "uppercase tag name",
			html:     `<BC-ATTACHMENT sgid="UP1" content-type="image/png" filename="upper.png"></BC-ATTACHMENT>`,
			expected: 1,
			check: func(t *testing.T, atts []ParsedAttachment) {
				if atts[0].Filename != "upper.png" {
					t.Errorf("Filename = %q, want upper.png", atts[0].Filename)
				}
			},
		},
		{
			name:     "bare tag with no attributes",
			html:     `<bc-attachment>content</bc-attachment>`,
			expected: 1,
		},
		{
			name:     "missing attributes handled gracefully",
			html:     `<bc-attachment sgid="BARE"></bc-attachment>`,
			expected: 1,
			check: func(t *testing.T, atts []ParsedAttachment) {
				a := atts[0]
				if a.SGID != "BARE" {
					t.Errorf("SGID = %q, want BARE", a.SGID)
				}
				if a.Filename != "" {
					t.Errorf("Filename = %q, want empty", a.Filename)
				}
				if a.DisplayName() != "Unnamed attachment" {
					t.Errorf("DisplayName() = %q, want Unnamed attachment", a.DisplayName())
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			atts := ParseAttachments(tt.html)
			if len(atts) != tt.expected {
				t.Fatalf("got %d attachments, want %d", len(atts), tt.expected)
			}
			if tt.check != nil {
				tt.check(t, atts)
			}
		})
	}
}

func TestParsedAttachmentDisplayName(t *testing.T) {
	tests := []struct {
		name     string
		att      ParsedAttachment
		expected string
	}{
		{"caption wins", ParsedAttachment{Caption: "My Caption", Filename: "file.jpg"}, "My Caption"},
		{"filename fallback", ParsedAttachment{Filename: "document.pdf"}, "document.pdf"},
		{"unnamed fallback", ParsedAttachment{}, "Unnamed attachment"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.att.DisplayName(); got != tt.expected {
				t.Errorf("DisplayName() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestParsedAttachmentDisplayURL(t *testing.T) {
	tests := []struct {
		name     string
		att      ParsedAttachment
		expected string
	}{
		{"URL wins", ParsedAttachment{URL: "https://a.com", Href: "https://b.com"}, "https://a.com"},
		{"href fallback", ParsedAttachment{Href: "https://b.com"}, "https://b.com"},
		{"empty", ParsedAttachment{}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.att.DisplayURL(); got != tt.expected {
				t.Errorf("DisplayURL() = %q, want %q", got, tt.expected)
			}
		})
	}
}
