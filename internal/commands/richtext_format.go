package commands

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/harness"
	"github.com/basecamp/basecamp-cli/internal/output"
	"github.com/basecamp/basecamp-cli/internal/richtext"
)

// richTextFormat is the value of --format: how a command reads the rich text
// it sends. It is stated, never inferred from the content.
type richTextFormat string

const (
	richTextMarkdown richTextFormat = "markdown"
	richTextHTML     richTextFormat = "html"
)

func (f *richTextFormat) String() string {
	if *f == "" {
		return string(richTextMarkdown)
	}
	return string(*f)
}

func (f *richTextFormat) Set(v string) error {
	switch richTextFormat(strings.ToLower(v)) {
	case richTextMarkdown:
		*f = richTextMarkdown
	case richTextHTML:
		*f = richTextHTML
	default:
		return fmt.Errorf(`must be "markdown" or "html"`)
	}
	return nil
}

func (f *richTextFormat) Type() string { return "string" }

// addRichTextFormatFlag gives cmd the --format and --as-written flags
// richTextToHTML reads.
func addRichTextFormatFlag(cmd *cobra.Command) {
	cmd.Flags().Var(new(richTextFormat), "format",
		"Rich-text input format: markdown (converted to HTML; a URL alone on its line becomes an embed) or html (sent without conversion)")
	_ = cmd.RegisterFlagCompletionFunc("format",
		cobra.FixedCompletions([]string{string(richTextMarkdown), string(richTextHTML)}, cobra.ShellCompDirectiveNoFileComp))
	cmd.Flags().Bool("as-written", false,
		"Send rich text that breaks the writing-style checks (raw URLs, one long block, several people mentioned at the start) without refusing or warning")
}

// inAgentSession is harness.InAgentSession, replaceable in tests.
var inAgentSession = harness.InAgentSession

// richTextToHTML is the one place a command turns rich-text input into the
// HTML Basecamp stores. With --format html the content is returned exactly as
// given. Otherwise it is read as Markdown, whatever it contains.
//
// Content that holds HTML tags was sent as written before Markdown became the
// only default, so when --format was left unset and the Markdown keeps raw HTML
// (richtext.HasRichTextHTML), a one-line warning on stderr says how to get that
// result back. The check only words the warning; it does not choose the format.
//
// The HTML is then held to the writing style (checkRichTextStyle), which can
// refuse the write: callers return the error before sending anything.
func richTextToHTML(cmd *cobra.Command, content string) (string, error) {
	flag := cmd.Flags().Lookup("format")
	if flag == nil {
		panic(cmd.CommandPath() + " reads rich text but has no --format flag; call addRichTextFormatFlag")
	}
	if flag.Value.String() == string(richTextHTML) {
		return content, checkRichTextStyle(cmd, content)
	}
	if !flag.Changed && richtext.HasRichTextHTML(content) {
		fmt.Fprintln(cmd.ErrOrStderr(), "Warning: content contains HTML tags and was read as Markdown, the default. "+
			"Supported tags are kept and the Markdown around them is converted. "+
			"To send HTML without Markdown conversion, pass --format html.")
	}
	html := richtext.MarkdownToHTMLWithEmbeds(content, embedLinks(cmd))
	return html, checkRichTextStyle(cmd, html)
}

// checkRichTextStyle holds the HTML a write is about to send to three
// structural writing rules (richtext.CheckStyle): no raw URLs, no single long
// block, at most one person mentioned at the start. Agents are told to read
// `basecamp instructions` before writing, and many post without it; this is
// the one door every rich-text write passes, so the rules that can be checked
// without judgment are checked here.
//
// It runs only when writing instructions apply to this write (the baseline or
// the active profile's), and not with --as-written. Inside an agent session a
// finding refuses the write, since the agent can rewrite and resend; outside
// one, a person may be posting on purpose, so the findings are a warning on
// stderr and the write goes ahead.
func checkRichTextStyle(cmd *cobra.Command, html string) error {
	if asWritten, _ := cmd.Flags().GetBool("as-written"); asWritten {
		return nil
	}
	app := appctx.FromContext(cmd.Context())
	if app == nil || app.Config == nil || len(instructionsLayers(app)) == 0 {
		return nil
	}
	findings := richtext.CheckStyle(html)
	if len(findings) == 0 {
		return nil
	}
	var list strings.Builder
	for _, f := range findings {
		list.WriteString("\n  - " + f.Message)
	}
	if inAgentSession() {
		return output.ErrUsageHint(
			"Not sent: this breaks the writing instructions set for Basecamp on this computer:"+list.String(),
			"Rewrite it (run `basecamp instructions` for the rules), or pass --as-written to send it anyway.")
	}
	fmt.Fprintln(cmd.ErrOrStderr(), "Warning: this breaks the writing instructions set for Basecamp on this computer:"+
		list.String()+"\nSent as written; pass --as-written to skip this check.")
	return nil
}
