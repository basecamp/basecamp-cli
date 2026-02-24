package richtext

import (
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
			expected: "<h1>Title</h1>\n<p>This is <strong>bold</strong> and <em>italic</em> and <code>code</code>.</p>",
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
