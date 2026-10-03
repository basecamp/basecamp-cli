package commands

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

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

// addRichTextFormatFlag gives cmd the --format flag richTextToHTML reads.
func addRichTextFormatFlag(cmd *cobra.Command) {
	cmd.Flags().Var(new(richTextFormat), "format",
		"Rich-text input format: markdown (converted to HTML) or html (sent without conversion)")
	_ = cmd.RegisterFlagCompletionFunc("format",
		cobra.FixedCompletions([]string{string(richTextMarkdown), string(richTextHTML)}, cobra.ShellCompDirectiveNoFileComp))
}

// richTextToHTML is the one place a command turns rich-text input into the
// HTML Basecamp stores. With --format html the content is returned exactly as
// given. Otherwise it is read as Markdown, whatever it contains.
//
// Content that holds HTML tags was sent as written before Markdown became the
// only default, so when --format was left unset and the Markdown keeps raw HTML
// (richtext.HasRichTextHTML), a one-line warning on stderr says how to get that
// result back. The check only words the warning; it does not choose the format.
func richTextToHTML(cmd *cobra.Command, content string) string {
	flag := cmd.Flags().Lookup("format")
	if flag == nil {
		panic(cmd.CommandPath() + " reads rich text but has no --format flag; call addRichTextFormatFlag")
	}
	if flag.Value.String() == string(richTextHTML) {
		return content
	}
	if !flag.Changed && richtext.HasRichTextHTML(content) {
		fmt.Fprintln(cmd.ErrOrStderr(), "Warning: content contains HTML tags and was read as Markdown, the default. "+
			"Supported tags are kept and the Markdown around them is converted. "+
			"To send HTML without Markdown conversion, pass --format html.")
	}
	return richtext.MarkdownToHTML(content)
}
