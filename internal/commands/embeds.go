package commands

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/output"
	"github.com/basecamp/basecamp-cli/internal/richtext"
)

type embedClient interface {
	Create(ctx context.Context, url string) (*basecamp.Embed, error)
	Get(ctx context.Context, sgid string) (*basecamp.Embed, error)
}

var (
	embedPollInterval = time.Second
	embedWait         = 10 * time.Second
)

type EmbedResult struct {
	*basecamp.Embed
	Attachment string `json:"attachment,omitempty"`
}

func NewEmbedsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "embeds",
		Short: "Embed videos, charts and other links in rich text",
		Long: `Embed content from another site, such as a YouTube video or a Dash chart,
in a card, comment, message, document or to-do.

With Markdown content (the default), you don't need this command: put the URL
on a line by itself and Basecamp embeds it, as the editor does when you paste
a link. Write [title](url) to keep a link instead.

Use 'create' when you write HTML (--format html): it prints the
<bc-attachment> tag to put in your content.`,
		Annotations: map[string]string{"agent_notes": "A URL alone on its line in Markdown content becomes an embed automatically\nWrite [title](url) to keep it a link\nURLs Basecamp can't embed stay links, with no warning\nWith --format html, use embeds create and paste its attachment tag"},
	}

	cmd.AddCommand(
		newEmbedsCreateCmd(),
		newEmbedsShowCmd(),
	)

	return cmd
}

func newEmbedsCreateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "create <url>",
		Short: "Create an embed for a URL",
		Long: `Create an embed for a URL and wait until Basecamp has it from the provider.

Prints the embed, with the <bc-attachment> tag to put in HTML rich text.
A URL no provider embeds is refused; link to it instead.`,
		Example: `  basecamp embeds create https://www.youtube.com/watch?v=dQw4w9WgXcQ
  basecamp embeds create https://dash.37signals.com/widgets/abc --json`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 || strings.TrimSpace(args[0]) == "" {
				return missingArg(cmd, "<url>")
			}

			app := appctx.FromContext(cmd.Context())
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			embed, err := createEmbed(cmd.Context(), app.Account().Embeds(), args[0])
			if err != nil {
				return err
			}
			return app.OK(embedResult(embed), output.WithSummary(embedSummary(embed)))
		},
	}
}

func newEmbedsShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <sgid>",
		Short: "Show an embed",
		Long:  "Show an embed by the sgid that 'embeds create' printed.",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 || strings.TrimSpace(args[0]) == "" {
				return missingArg(cmd, "<sgid>")
			}

			app := appctx.FromContext(cmd.Context())
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			embed, err := app.Account().Embeds().Get(cmd.Context(), args[0])
			if err != nil {
				return convertSDKError(err)
			}
			return app.OK(embedResult(embed), output.WithSummary(embedSummary(embed)))
		},
	}
}

func createEmbed(ctx context.Context, client embedClient, url string) (*basecamp.Embed, error) {
	embed, err := client.Create(ctx, url)
	if err != nil {
		if httpStatus(err) == http.StatusUnprocessableEntity {
			return nil, output.ErrUsageHint("Basecamp can't embed "+url, "Link to it instead")
		}
		return nil, convertSDKError(err)
	}
	return waitForEmbed(ctx, client, embed)
}

func waitForEmbed(ctx context.Context, client embedClient, embed *basecamp.Embed) (*basecamp.Embed, error) {
	ctx, cancel := context.WithTimeout(ctx, embedWait)
	defer cancel()

	for embed.Status == "pending" && pauseForEmbed(ctx) {
		refreshed, err := client.Get(ctx, embed.SGID)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			return nil, convertSDKError(err)
		}
		embed = refreshed
	}
	return embed, nil
}

func pauseForEmbed(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(embedPollInterval):
		return true
	}
}

func embedResult(embed *basecamp.Embed) EmbedResult {
	result := EmbedResult{Embed: embed}
	if embed.Status == "active" {
		result.Attachment = richtext.EmbedToHTML(richtext.EmbedRef{SGID: embed.SGID, ContentType: embed.ContentType})
	}
	return result
}

func embedSummary(embed *basecamp.Embed) string {
	switch embed.Status {
	case "active":
		return "Embed ready for " + embed.URL
	case "pending":
		return "Embed still pending for " + embed.URL + "; check again with basecamp embeds show"
	default:
		return "Basecamp found nothing to embed at " + embed.URL + "; link to it instead"
	}
}

func embedLinks(cmd *cobra.Command) richtext.EmbedFunc {
	app := appctx.FromContext(cmd.Context())
	if app == nil || app.SDK == nil || app.RequireAccount() != nil {
		return nil
	}
	return func(urls []string) map[string]richtext.EmbedRef {
		return resolveEmbeds(cmd.Context(), app.Account().Embeds(), urls, cmd.ErrOrStderr())
	}
}

func resolveEmbeds(ctx context.Context, client embedClient, urls []string, warnings io.Writer) map[string]richtext.EmbedRef {
	ctx, cancel := context.WithTimeout(ctx, embedWait)
	defer cancel()

	var created []string
	embeds := map[string]*basecamp.Embed{}
	for i, url := range urls {
		embed, err := client.Create(ctx, url)
		if err != nil {
			if status := httpStatus(err); status == http.StatusUnprocessableEntity || status == http.StatusNotFound {
				continue
			}
			fmt.Fprintf(warnings, "Warning: couldn't embed %s, so it stays a link: %v\n", url, err)
			switch rest := len(urls) - i - 1; rest {
			case 0:
			case 1:
				fmt.Fprintln(warnings, "Warning: the URL after it stays a link too.")
			default:
				fmt.Fprintf(warnings, "Warning: the %d URLs after it stay links too.\n", rest)
			}
			break
		}
		created = append(created, url)
		embeds[url] = embed
	}

	var pollErr error
	for pollErr == nil && anyPendingEmbed(created, embeds) && pauseForEmbed(ctx) {
		for _, url := range created {
			if embeds[url].Status != "pending" {
				continue
			}
			refreshed, err := client.Get(ctx, embeds[url].SGID)
			if err != nil {
				if ctx.Err() == nil {
					pollErr = err
				}
				break
			}
			embeds[url] = refreshed
		}
	}

	refs := map[string]richtext.EmbedRef{}
	for _, url := range created {
		switch embed := embeds[url]; {
		case embed.Status == "active":
			refs[url] = richtext.EmbedRef{SGID: embed.SGID, ContentType: embed.ContentType}
		case embed.Status == "pending" && pollErr != nil:
			fmt.Fprintf(warnings, "Warning: couldn't embed %s, so it stays a link: %v\n", url, pollErr)
		case embed.Status == "pending":
			fmt.Fprintf(warnings, "Warning: %s took too long to answer, so it stays a link.\n", url)
		default:
			fmt.Fprintf(warnings, "Warning: Basecamp found nothing to embed at %s, so it stays a link.\n", url)
		}
	}
	return refs
}

func anyPendingEmbed(urls []string, embeds map[string]*basecamp.Embed) bool {
	for _, url := range urls {
		if embeds[url].Status == "pending" {
			return true
		}
	}
	return false
}

func httpStatus(err error) int {
	var apiErr *basecamp.Error
	if errors.As(err, &apiErr) {
		return apiErr.HTTPStatus
	}
	return 0
}
