package richtext

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

const (
	videoURL = "https://www.youtube.com/watch?v=dQw4w9WgXcQ"
	chartURL = "https://dash.37signals.com/widgets/abc"
)

func embedAll(seen *[]string) EmbedFunc {
	return func(urls []string) map[string]EmbedRef {
		*seen = append(*seen, urls...)
		refs := map[string]EmbedRef{}
		for _, url := range urls {
			refs[url] = EmbedRef{SGID: "sgid-" + url[len(url)-3:], ContentType: "embed/test"}
		}
		return refs
	}
}

func TestMarkdownToHTMLWithEmbedsEmbedsAURLAloneOnItsLine(t *testing.T) {
	var seen []string
	got := MarkdownToHTMLWithEmbeds(videoURL, embedAll(&seen))

	assert.Equal(t, []string{videoURL}, seen)
	assert.Equal(t, `<bc-attachment sgid="sgid-XcQ" content-type="embed/test"></bc-attachment>`, got)
}

func TestMarkdownToHTMLWithEmbedsSplitsAParagraphAroundEmbeddedLines(t *testing.T) {
	var seen []string
	md := "Charts for the incident:\n" + chartURL + "\n" + videoURL + "\nThat's all."
	got := MarkdownToHTMLWithEmbeds(md, embedAll(&seen))

	assert.Equal(t, []string{chartURL, videoURL}, seen)
	assert.Equal(t, "<p>Charts for the incident:</p>\n"+
		`<bc-attachment sgid="sgid-abc" content-type="embed/test"></bc-attachment>`+"\n"+
		`<bc-attachment sgid="sgid-XcQ" content-type="embed/test"></bc-attachment>`+"\n"+
		"<p>That's all.</p>", got)
}

func TestMarkdownToHTMLWithEmbedsKeepsBlankLineSpacing(t *testing.T) {
	var seen []string
	got := MarkdownToHTMLWithEmbeds("Intro\n\n"+videoURL+"\n\nOutro", embedAll(&seen))

	assert.Equal(t, "<p>Intro</p>\n<p><br></p>\n"+
		`<bc-attachment sgid="sgid-XcQ" content-type="embed/test"></bc-attachment>`+"\n"+
		"<p><br></p>\n<p>Outro</p>", got)
}

func TestMarkdownToHTMLWithEmbedsEmbedsAnAutolinkAloneOnItsLine(t *testing.T) {
	var seen []string
	got := MarkdownToHTMLWithEmbeds("<"+videoURL+">\nnext", embedAll(&seen))

	assert.Equal(t, []string{videoURL}, seen)
	assert.Equal(t, `<bc-attachment sgid="sgid-XcQ" content-type="embed/test"></bc-attachment>`+"\n<p>next</p>", got)
}

func TestMarkdownToHTMLWithEmbedsAsksOncePerURL(t *testing.T) {
	var seen []string
	got := MarkdownToHTMLWithEmbeds(videoURL+"\n\n"+videoURL, embedAll(&seen))

	assert.Equal(t, []string{videoURL}, seen)
	assert.Equal(t, `<bc-attachment sgid="sgid-XcQ" content-type="embed/test"></bc-attachment>`+"\n<p><br></p>\n"+
		`<bc-attachment sgid="sgid-XcQ" content-type="embed/test"></bc-attachment>`, got)
}

func TestMarkdownToHTMLWithEmbedsLeavesURLsItCanNotEmbed(t *testing.T) {
	refuse := func(urls []string) map[string]EmbedRef { return nil }
	md := "Intro\n" + videoURL + "\nOutro"

	assert.Equal(t, MarkdownToHTML(md), MarkdownToHTMLWithEmbeds(md, refuse))
}

func TestMarkdownToHTMLWithEmbedsEmbedsOnlyTheURLsItWasGiven(t *testing.T) {
	onlyChart := func(urls []string) map[string]EmbedRef {
		return map[string]EmbedRef{chartURL: {SGID: "chart"}}
	}
	got := MarkdownToHTMLWithEmbeds(videoURL+"\n"+chartURL, onlyChart)

	assert.Equal(t, "<p>"+videoURL+"</p>\n"+`<bc-attachment sgid="chart"></bc-attachment>`, got)
}

func TestMarkdownToHTMLWithEmbedsLeavesEverythingButAURLAloneOnALine(t *testing.T) {
	for name, md := range map[string]string{
		"in a sentence":         "Watch " + videoURL + " today",
		"a titled link":         "[The video](" + videoURL + ")",
		"in a list":             "- " + videoURL,
		"in a numbered list":    "1. " + videoURL,
		"in a quote":            "> " + videoURL,
		"in a code span":        "`" + videoURL + "`",
		"in a code block":       "```\n" + videoURL + "\n```",
		"in an indented block":  "    " + videoURL,
		"in an HTML block":      "<p>\n" + videoURL + "\n</p>",
		"in a heading":          "# " + videoURL,
		"emphasized":            "*" + videoURL + "*",
		"with an escape":        videoURL + `\_x`,
		"another scheme":        "ftp://example.com/video",
		"javascript":            "javascript:alert(1)",
		"a mail autolink":       "<someone@example.com>",
		"with markup in it":     "https://www.youtube.com/watch?v=<b>x</b>",
		"two URLs on one line":  videoURL + " " + chartURL,
		"in a table":            "| a |\n|---|\n| " + videoURL + " |",
		"followed by text":      videoURL + " is the one",
		"after a bc-attachment": `<bc-attachment sgid="x"></bc-attachment> ` + videoURL,
	} {
		t.Run(name, func(t *testing.T) {
			var seen []string
			got := MarkdownToHTMLWithEmbeds(md, embedAll(&seen))

			assert.Empty(t, seen)
			assert.Equal(t, MarkdownToHTML(md), got)
		})
	}
}

func TestMarkdownToHTMLWithEmbedsWithoutAnEmbedFuncIsMarkdownToHTML(t *testing.T) {
	md := "Intro\n" + videoURL + "\n\n- item"

	assert.Equal(t, MarkdownToHTML(md), MarkdownToHTMLWithEmbeds(md, nil))
	assert.Equal(t, "<p>Intro\n"+videoURL+"</p>\n<p><br></p>\n<ul>\n<li>item</li>\n</ul>", MarkdownToHTML(md))
}

func TestEmbedToHTMLEscapesItsAttributes(t *testing.T) {
	assert.Equal(t, `<bc-attachment sgid="a&quot;&gt;&lt;script&gt;"></bc-attachment>`,
		EmbedToHTML(EmbedRef{SGID: `a"><script>`}))
	assert.Equal(t, `<bc-attachment sgid="s" content-type="embed/&quot;x"></bc-attachment>`,
		EmbedToHTML(EmbedRef{SGID: "s", ContentType: `embed/"x`}))
}
