package richtext

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCollapseAttachmentsReducesAnExpandedMention(t *testing.T) {
	input := `<p>Hi <bc-attachment sgid="BAh7" content-type="application/vnd.basecamp.mention" content="&lt;bc-mention&gt;
  &lt;span&gt;Jo&lt;/span&gt;&lt;/bc-mention&gt;"><figure>
  <img class="avatar" alt="Jo" src="https://example.com/a">
  <figcaption>Jo</figcaption>
</figure></bc-attachment> there.</p>`

	assert.Equal(t,
		`<p>Hi <bc-attachment sgid="BAh7" content-type="application/vnd.basecamp.mention"></bc-attachment> there.</p>`,
		CollapseAttachments(input))
}

func TestCollapseAttachmentsKeepsAFileAttachmentsOwnAttributes(t *testing.T) {
	input := `<bc-attachment sgid="BAh8" content-type="image/png" filename="shot.png" caption="The bug" presentation="gallery"><figure><img src="https://example.com/shot.png"><figcaption>The bug</figcaption></figure></bc-attachment>`

	assert.Equal(t,
		`<bc-attachment sgid="BAh8" content-type="image/png" filename="shot.png" caption="The bug" presentation="gallery"></bc-attachment>`,
		CollapseAttachments(input))
}

func TestCollapseAttachmentsLeavesWhatNamesNoRecord(t *testing.T) {
	for _, input := range []string{
		`<bc-attachment content-type="vnd.basecamp.horizontal-rule.html" content="&lt;hr&gt;"><figure><hr></figure></bc-attachment>`,
		`<bc-attachment sgid="BAh9" content-type="application/pdf"/>`,
		`<bc-attachment sgid="BAh9" content-type="application/vnd.basecamp.mention"></bc-attachment>`,
		`<p>no attachments</p>`,
	} {
		assert.Equal(t, input, CollapseAttachments(input))
	}
}

func TestCollapseAttachmentsInMarkdownLeavesCode(t *testing.T) {
	expanded := `<bc-attachment sgid="BAh7" content-type="application/vnd.basecamp.mention"><figure><img alt="Jo"></figure></bc-attachment>`
	bare := `<bc-attachment sgid="BAh7" content-type="application/vnd.basecamp.mention"></bc-attachment>`
	input := "Hi " + expanded + " there.\n\n" +
		"`" + expanded + "`\n\n" +
		"```html\n" + expanded + "\n```\n\n" +
		"    " + expanded + "\n"

	assert.Equal(t,
		"Hi "+bare+" there.\n\n"+
			"`"+expanded+"`\n\n"+
			"```html\n"+expanded+"\n```\n\n"+
			"    "+expanded+"\n",
		CollapseAttachmentsInMarkdown(input))
}

// A self-closing attachment is not the opening tag of the next one: reading it
// that way dropped everything up to the next closing tag.
func TestCollapseAttachmentsSkipsASelfClosingAttachment(t *testing.T) {
	input := `<bc-attachment sgid="file"/>KEEP THIS TEXT<bc-attachment sgid="person"><figure>Jo</figure></bc-attachment>`

	assert.Equal(t,
		`<bc-attachment sgid="file"/>KEEP THIS TEXT<bc-attachment sgid="person"></bc-attachment>`,
		CollapseAttachments(input))
	assert.Equal(t,
		`<bc-attachment sgid="file"/>KEEP THIS TEXT<bc-attachment sgid="person"></bc-attachment>`,
		CollapseAttachmentsInMarkdown(input))
}

func TestCollapseAttachmentsReadsAQuotedGreaterThan(t *testing.T) {
	input := `<bc-attachment sgid="x" caption="a > b" title='c > d'><figure>a > b</figure></bc-attachment>`

	assert.Equal(t,
		`<bc-attachment sgid="x" caption="a > b" title='c > d'></bc-attachment>`,
		CollapseAttachments(input))
}

func TestCollapseAttachmentsLeavesAnUnclosedAttachment(t *testing.T) {
	for _, input := range []string{
		`<p>Hi <bc-attachment sgid="x"><figure>Jo</figure></p>`,
		`<p>Hi <bc-attachment sgid="x" caption="never closed`,
		`<bc-attachment-like sgid="x"><figure>Jo</figure></bc-attachment-like>`,
	} {
		assert.Equal(t, input, CollapseAttachments(input))
	}

	assert.Equal(t,
		`<bc-attachment sgid="a">KEEP <bc-attachment sgid="b"></bc-attachment>`,
		CollapseAttachments(`<bc-attachment sgid="a">KEEP <bc-attachment sgid="b"><figure>Jo</figure></bc-attachment>`))
}

// Lowering a string for a case-blind search must not move its bytes: 'İ'
// lowers to three bytes from two.
func TestCollapseAttachmentsAfterTextThatLowersLonger(t *testing.T) {
	input := `<p>İİ <BC-ATTACHMENT sgid="x"><figure>Jo</figure></BC-ATTACHMENT> ok</p>`

	assert.Equal(t, `<p>İİ <bc-attachment sgid="x"></bc-attachment> ok</p>`, CollapseAttachments(input))
}

// A backslash makes a tag literal Markdown text, so neither an escaped
// opening tag nor an escaped closing tag is markup.
func TestCollapseAttachmentsInMarkdownLeavesEscapedTags(t *testing.T) {
	for _, input := range []string{
		`Example: \<bc-attachment sgid="x">KEEP THIS TEXT\</bc-attachment>`,
		`Example: <bc-attachment sgid="x">KEEP THIS TEXT\</bc-attachment>`,
	} {
		assert.Equal(t, input, CollapseAttachmentsInMarkdown(input))
	}

	assert.Equal(t,
		`Example: \\<bc-attachment sgid="x"></bc-attachment>`,
		CollapseAttachmentsInMarkdown(`Example: \\<bc-attachment sgid="x"><figure>Jo</figure></bc-attachment>`))
}

// Markup inside an HTML comment is not markup: reading it as an attachment
// deleted the visible content between two comments.
func TestCollapseAttachmentsLeavesTagsInComments(t *testing.T) {
	input := `<!-- <bc-attachment sgid="x"> --><p>KEEP THIS TEXT</p><!-- </bc-attachment> -->`

	assert.Equal(t, input, CollapseAttachments(input))
	assert.Equal(t, input, CollapseAttachmentsInMarkdown(input))

	assert.Equal(t,
		`<!-- note --><bc-attachment sgid="x"></bc-attachment><!-- unterminated <bc-attachment sgid="y"><figure>Jo</figure></bc-attachment>`,
		CollapseAttachments(`<!-- note --><bc-attachment sgid="x"><figure>Jo</figure></bc-attachment><!-- unterminated <bc-attachment sgid="y"><figure>Jo</figure></bc-attachment>`))
}

// Attachment markup inside another tag's attribute value is the value's text,
// whether it would open an element or close a real one too early.
func TestCollapseAttachmentsIgnoresTagsInAttributeValues(t *testing.T) {
	quoted := `<span title='<bc-attachment sgid="x">'>KEEP THIS TEXT</span><span title='</bc-attachment>'>after</span>`
	assert.Equal(t, quoted, CollapseAttachments(quoted))
	assert.Equal(t, quoted, CollapseAttachmentsInMarkdown(quoted))

	alt := `<bc-attachment sgid="x"><figure><img alt="</bc-attachment>" src="a.png"><figcaption>Jo</figcaption></figure></bc-attachment>`
	assert.Equal(t, `<bc-attachment sgid="x"></bc-attachment>`, CollapseAttachments(alt))
	assert.Equal(t, `<bc-attachment sgid="x"></bc-attachment>`, CollapseAttachmentsInMarkdown(alt))
}

// The Markdown renderer shows an HTML block it does not support as escaped
// text, so an attachment written in one is text too.
func TestCollapseAttachmentsInMarkdownLeavesEscapedHTMLBlocks(t *testing.T) {
	input := `<section><bc-attachment sgid="x">KEEP THIS TEXT</bc-attachment></section>`

	assert.Equal(t, input, CollapseAttachmentsInMarkdown(input))
	assert.Contains(t, MarkdownToHTML(CollapseAttachmentsInMarkdown(input)), "KEEP THIS TEXT")
}
