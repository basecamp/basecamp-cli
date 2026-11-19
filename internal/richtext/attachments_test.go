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
