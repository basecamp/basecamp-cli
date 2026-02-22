package commands

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/output"
	"github.com/basecamp/basecamp-cli/internal/urlarg"
)

// ParsedURL represents components extracted from a Basecamp URL.
type ParsedURL struct {
	URL          string  `json:"url"`
	AccountID    *string `json:"account_id"`
	BucketID     *string `json:"bucket_id"`
	Type         *string `json:"type"`
	TypeSingular *string `json:"type_singular"`
	RecordingID  *string `json:"recording_id"`
	CommentID    *string `json:"comment_id"`
}

// NewURLCmd creates the url command for parsing Basecamp URLs.
func NewURLCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "url [parse] <url>",
		Short: "Parse Basecamp URLs",
		Long: `Parse and work with Basecamp URLs.

Extracts components like account ID, project ID, type, and recording ID
from Basecamp URLs.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			// Handle "basecamp url parse <url>" or "basecamp url <url>"
			var url string
			if args[0] == "parse" {
				if len(args) < 2 {
					return output.ErrUsage("URL required")
				}
				url = args[1]
			} else {
				url = args[0]
			}

			return runURLParse(app, url)
		},
	}

	cmd.AddCommand(newURLParseCmd())

	return cmd
}

func newURLParseCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "parse <url>",
		Short: "Parse a Basecamp URL",
		Long: `Parse a Basecamp URL into its components.

Supported URL patterns:
  https://3.basecamp.com/{account}/buckets/{bucket}/{type}/{id}
  https://3.basecamp.com/{account}/buckets/{bucket}/{type}/{id}#__recording_{comment}
  https://3.basecamp.com/{account}/buckets/{bucket}/card_tables/cards/{id}
  https://3.basecamp.com/{account}/projects/{project}`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			return runURLParse(app, args[0])
		},
	}
}

func runURLParse(app *appctx.App, url string) error {
	// Validate it's a Basecamp URL
	if !urlarg.IsURL(url) {
		return output.ErrUsageHint(
			fmt.Sprintf("Not a Basecamp URL: %s", url),
			"Expected URL like: https://3.basecamp.com/...",
		)
	}

	parsed := urlarg.Parse(url)
	if parsed == nil {
		return output.ErrUsageHint(
			fmt.Sprintf("Could not parse URL: %s", url),
			"Expected Basecamp URL format",
		)
	}

	accountID := parsed.AccountID
	bucketID := parsed.ProjectID
	recordingType := parsed.Type
	recordingID := parsed.RecordingID
	commentID := parsed.CommentID

	// Normalize recording type (singular form)
	typeSingular := recordingType
	typeMap := map[string]string{
		"messages":         "message",
		"todos":            "todo",
		"todolists":        "todolist",
		"documents":        "document",
		"comments":         "comment",
		"uploads":          "upload",
		"cards":            "card",
		"columns":          "column",
		"lists":            "column",
		"steps":            "step",
		"chats":            "campfire",
		"campfires":        "campfire",
		"schedules":        "schedule",
		"schedule_entries": "schedule_entry",
		"vaults":           "vault",
	}
	if singular, ok := typeMap[recordingType]; ok {
		typeSingular = singular
	}

	// Build result
	result := ParsedURL{URL: url}
	if accountID != "" {
		result.AccountID = &accountID
	}
	if bucketID != "" {
		result.BucketID = &bucketID
	}
	if recordingType != "" {
		result.Type = &recordingType
		result.TypeSingular = &typeSingular
	}
	if recordingID != "" {
		result.RecordingID = &recordingID
	}
	if commentID != "" {
		result.CommentID = &commentID
	}

	// Build summary
	var summary string
	var typeCapitalized string
	if typeSingular != "" {
		typeCapitalized = strings.ToUpper(typeSingular[:1]) + typeSingular[1:]
	}

	if recordingID != "" {
		summary = fmt.Sprintf("%s #%s", typeCapitalized, recordingID)
		if bucketID != "" {
			summary += fmt.Sprintf(" in project #%s", bucketID)
		}
		if commentID != "" {
			summary += fmt.Sprintf(", comment #%s", commentID)
		}
	} else if bucketID != "" {
		if recordingType == "project" {
			summary = fmt.Sprintf("Project #%s", bucketID)
		} else {
			summary = fmt.Sprintf("%s list in project #%s", typeCapitalized, bucketID)
		}
	} else if accountID != "" {
		summary = fmt.Sprintf("Account #%s", accountID)
	} else {
		summary = "Basecamp URL"
	}

	// Build breadcrumbs
	var breadcrumbs []output.Breadcrumb
	if recordingID != "" && bucketID != "" {
		// Special handling for card table types (column, step)
		switch typeSingular {
		case "column":
			breadcrumbs = append(breadcrumbs,
				output.Breadcrumb{
					Action:      "show",
					Cmd:         fmt.Sprintf("basecamp cards column show %s --in %s", recordingID, bucketID),
					Description: "View the column",
				},
				output.Breadcrumb{
					Action:      "columns",
					Cmd:         fmt.Sprintf("basecamp cards columns --in %s", bucketID),
					Description: "List all columns",
				},
			)
		case "step":
			breadcrumbs = append(breadcrumbs,
				output.Breadcrumb{
					Action:      "complete",
					Cmd:         fmt.Sprintf("basecamp cards step complete %s --in %s", recordingID, bucketID),
					Description: "Complete the step",
				},
				output.Breadcrumb{
					Action:      "uncomplete",
					Cmd:         fmt.Sprintf("basecamp cards step uncomplete %s --in %s", recordingID, bucketID),
					Description: "Uncomplete the step",
				},
			)
		default:
			// Standard recording types
			breadcrumbs = append(breadcrumbs,
				output.Breadcrumb{
					Action:      "show",
					Cmd:         fmt.Sprintf("basecamp show %s %s --in %s", typeSingular, recordingID, bucketID),
					Description: fmt.Sprintf("View the %s", typeSingular),
				},
				output.Breadcrumb{
					Action:      "comment",
					Cmd:         fmt.Sprintf("basecamp comment --content \"text\" --on %s --in %s", recordingID, bucketID),
					Description: "Add a comment",
				},
				output.Breadcrumb{
					Action:      "comments",
					Cmd:         fmt.Sprintf("basecamp comments --on %s --in %s", recordingID, bucketID),
					Description: "List comments",
				},
			)

			if commentID != "" {
				breadcrumbs = append(breadcrumbs,
					output.Breadcrumb{
						Action:      "show-comment",
						Cmd:         fmt.Sprintf("basecamp comments show %s --in %s", commentID, bucketID),
						Description: "View the comment",
					},
				)
			}
		}
	}

	return app.OK(result,
		output.WithSummary(summary),
		output.WithBreadcrumbs(breadcrumbs...),
	)
}
