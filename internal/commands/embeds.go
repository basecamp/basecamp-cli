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
	return waitForEmbed(ctx, client, embed, time.Now().Add(embedWait))
}

func waitForEmbed(ctx context.Context, client embedClient, embed *basecamp.Embed, deadline time.Time) (*basecamp.Embed, error) {
	for embed.Status == "pending" && time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(embedPollInterval):
		}

		refreshed, err := client.Get(ctx, embed.SGID)
		if err != nil {
			return nil, convertSDKError(err)
		}
		embed = refreshed
	}
	return embed, nil
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
	created := map[string]*basecamp.Embed{}
	for _, url := range urls {
		embed, err := client.Create(ctx, url)
		if err != nil {
			if status := httpStatus(err); status != http.StatusUnprocessableEntity && status != http.StatusNotFound {
				fmt.Fprintf(warnings, "Warning: couldn't embed %s, so it stays a link: %v\n", url, err)
			}
			continue
		}
		created[url] = embed
	}

	refs := map[string]richtext.EmbedRef{}
	deadline := time.Now().Add(embedWait)
	for _, url := range urls {
		embed, ok := created[url]
		if !ok {
			continue
		}

		embed, err := waitForEmbed(ctx, client, embed, deadline)
		switch {
		case err != nil:
			fmt.Fprintf(warnings, "Warning: couldn't embed %s, so it stays a link: %v\n", url, err)
		case embed.Status == "active":
			refs[url] = richtext.EmbedRef{SGID: embed.SGID, ContentType: embed.ContentType}
		case embed.Status == "pending":
			fmt.Fprintf(warnings, "Warning: %s took too long to answer, so it stays a link.\n", url)
		default:
			fmt.Fprintf(warnings, "Warning: Basecamp found nothing to embed at %s, so it stays a link.\n", url)
		}
	}
	return refs
}

func httpStatus(err error) int {
	var apiErr *basecamp.Error
	if errors.As(err, &apiErr) {
		return apiErr.HTTPStatus
	}
	return 0
}
