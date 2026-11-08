package richtext

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func styleChecks(findings []StyleFinding) []string {
	checks := make([]string, 0, len(findings))
	for _, f := range findings {
		checks = append(checks, f.Check)
	}
	return checks
}

func words(n int) string {
	return strings.TrimSpace(strings.Repeat("word ", n))
}

func TestCheckStyle(t *testing.T) {
	mention := func(sgid string) string {
		return `<bc-attachment sgid="` + sgid + `" content-type="application/vnd.basecamp.mention">@Someone</bc-attachment>`
	}
	tests := []struct {
		name string
		html string
		want []string
	}{
		// Bare URLs
		{"raw URL in prose", "<p>See https://example.com/a for details.</p>", []string{StyleBareURL}},
		{"raw URL in plain text without tags", "See https://example.com/a for details.", []string{StyleBareURL}},
		{"URL as a link's words", `<p>See <a href="https://example.com/a">https://example.com/a</a> now.</p>`, []string{}},
		{"URL as a link's href", `<p>See <a href="https://example.com/a">the report</a>.</p>`, []string{}},
		{"URL alone in a block is an embed", "<p>Intro.</p><p>https://example.com/a</p><p>Outro.</p>", []string{}},
		{"URL alone in a div", "<div>https://example.com/a</div>", []string{}},
		{"URL alone without tags", "https://example.com/a", []string{}},
		{"URL in inline code", "<p>Run <code>curl https://example.com/a</code> to check.</p>", []string{}},
		{"URL in a code block", "<pre><code>curl https://example.com/a\n</code></pre>", []string{}},
		{"same URL twice reported once", "<p>https://example.com/a and https://example.com/a.</p>", []string{StyleBareURL}},
		{"two URLs", "<p>See https://a.example and https://b.example.</p>", []string{StyleBareURL, StyleBareURL}},
		{"URL beside an embed line in one block", "<p>Intro\nhttps://example.com/a</p>", []string{StyleBareURL}},

		// Flat bodies
		{"long single paragraph", "<p>" + words(150) + "</p>", []string{StyleFlatBody}},
		{"long text without tags", words(150), []string{StyleFlatBody}},
		{"short single paragraph", "<p>" + words(120) + "</p>", []string{}},
		{"two paragraphs", "<p>" + words(80) + "</p><p>" + words(80) + "</p>", []string{}},
		{"markdown spacer between paragraphs", "<p>" + words(80) + "</p>\n<p><br></p>\n<p>" + words(80) + "</p>", []string{}},
		{"line break", "<div>" + words(80) + "<br>" + words(80) + "</div>", []string{}},
		{"list", "<p>Intro:</p><ul><li>" + words(80) + "</li><li>" + words(80) + "</li></ul>", []string{}},
		{"heading and paragraph", "<h2>Plan</h2><p>" + words(150) + "</p>", []string{}},

		// Mentions at the start
		{"one mention", "<p>" + mention("A") + " can you decide?</p>", []string{}},
		{"two mentions in the first block", "<p>" + mention("A") + " " + mention("B") + " can you decide?</p>", []string{StyleLeadMentions}},
		{"same person twice", "<p>" + mention("A") + " and again " + mention("A") + "</p>", []string{}},
		{"second mention in a later block", "<p>" + mention("A") + " can you decide?</p><p>cc " + mention("B") + "</p>", []string{}},
		{"mention links from markdown", `<p><a href="person:1">@Ann</a> <a href="mention:XYZ">@Bob</a> please look</p>`, []string{StyleLeadMentions}},
		{"@Name text the CLI resolves", "<p>@Ann and @Bob.Smith, please look</p>", []string{StyleLeadMentions}},
		{"@sgid text", "<p>@sgid:AAA @sgid:BBB please look</p>", []string{StyleLeadMentions}},
		{"email is not a mention", "<p>@Ann, mail bob@example.com</p>", []string{}},
		{"spacer before the first block", "<p><br></p><p>@Ann @Bob</p>", []string{StyleLeadMentions}},
		{"mention in code is not a mention", "<p>@Ann run <code>npm i @types/node</code></p>", []string{}},

		{"empty", "", []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, styleChecks(CheckStyle(tt.html)))
		})
	}
}

func TestCheckStyleMessages(t *testing.T) {
	findings := CheckStyle("<p>@Ann @Bob see https://example.com/" + strings.Repeat("x", 80) + " " + words(130) + "</p>")
	assert.Equal(t, []string{StyleBareURL, StyleFlatBody, StyleLeadMentions}, styleChecks(findings))
	assert.Contains(t, findings[0].Message, "never as a raw URL")
	assert.Contains(t, findings[0].Message, "…)", "a long URL is shortened")
	assert.Contains(t, findings[1].Message, "134 words")
	assert.Contains(t, findings[2].Message, "2 people mentioned at the start")
}
