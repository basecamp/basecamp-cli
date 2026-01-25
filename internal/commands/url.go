package commands

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"github.com/basecamp/bcq/internal/appctx"
	"github.com/basecamp/bcq/internal/output"
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

			// Handle "bcq url parse <url>" or "bcq url <url>"
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
	if !strings.Contains(url, "basecamp.com") {
		return output.ErrUsageHint(
			fmt.Sprintf("Not a Basecamp URL: %s", url),
			"Expected URL like: https://3.basecamp.com/...",
		)
	}

	// Extract fragment if present
	var fragment string
	urlPath := url
	if idx := strings.Index(url, "#"); idx != -1 {
		fragment = url[idx+1:]
		urlPath = url[:idx]
	}

	// Remove protocol and domain
	pathOnly := urlPath
	if idx := strings.Index(urlPath, "://"); idx != -1 {
		pathOnly = urlPath[idx+3:]
		// Remove domain
		if slashIdx := strings.Index(pathOnly, "/"); slashIdx != -1 {
			pathOnly = pathOnly[slashIdx:]
		}
	}

	var accountID, bucketID, recordingType, recordingID, commentID string

	// Regex patterns for different URL formats
	cardPattern := regexp.MustCompile(`^/(\d+)/buckets/(\d+)/card_tables/cards/(\d+)`)
	columnPattern := regexp.MustCompile(`^/(\d+)/buckets/(\d+)/card_tables/(?:columns|lists)/(\d+)`)
	stepPattern := regexp.MustCompile(`^/(\d+)/buckets/(\d+)/card_tables/steps/(\d+)`)
	fullRecordingPattern := regexp.MustCompile(`^/(\d+)/buckets/(\d+)/([^/]+)/(\d+)`)
	typeListPattern := regexp.MustCompile(`^/(\d+)/buckets/(\d+)/([^/]+)/?$`)
	projectPattern := regexp.MustCompile(`^/(\d+)/projects/(\d+)`)
	accountOnlyPattern := regexp.MustCompile(`^/(\d+)`)

	if matches := cardPattern.FindStringSubmatch(pathOnly); matches != nil {
		// Card URL: /{account}/buckets/{bucket}/card_tables/cards/{id}
		accountID = matches[1]
		bucketID = matches[2]
		recordingType = "cards"
		recordingID = matches[3]
	} else if matches := columnPattern.FindStringSubmatch(pathOnly); matches != nil {
		// Column URL: /{account}/buckets/{bucket}/card_tables/columns/{id} or lists/{id}
		accountID = matches[1]
		bucketID = matches[2]
		recordingType = "columns"
		recordingID = matches[3]
	} else if matches := stepPattern.FindStringSubmatch(pathOnly); matches != nil {
		// Step URL: /{account}/buckets/{bucket}/card_tables/steps/{id}
		accountID = matches[1]
		bucketID = matches[2]
		recordingType = "steps"
		recordingID = matches[3]
	} else if matches := fullRecordingPattern.FindStringSubmatch(pathOnly); matches != nil {
		// Full recording URL: /{account}/buckets/{bucket}/{type}/{id}
		accountID = matches[1]
		bucketID = matches[2]
		recordingType = matches[3]
		recordingID = matches[4]
	} else if matches := typeListPattern.FindStringSubmatch(pathOnly); matches != nil {
		// Type list URL: /{account}/buckets/{bucket}/{type}
		accountID = matches[1]
		bucketID = matches[2]
		recordingType = matches[3]
	} else if matches := projectPattern.FindStringSubmatch(pathOnly); matches != nil {
		// Project URL: /{account}/projects/{project}
		accountID = matches[1]
		bucketID = matches[2]
		recordingType = "project"
	} else if matches := accountOnlyPattern.FindStringSubmatch(pathOnly); matches != nil {
		// Account-only URL
		accountID = matches[1]
	} else {
		return output.ErrUsageHint(
			fmt.Sprintf("Could not parse URL path: %s", pathOnly),
			"Expected Basecamp URL format",
		)
	}

	// Parse fragment for comment ID
	// Fragment format: __recording_{id} or just {id}
	if fragment != "" {
		commentPattern := regexp.MustCompile(`__recording_(\d+)`)
		if matches := commentPattern.FindStringSubmatch(fragment); matches != nil {
			commentID = matches[1]
		} else if regexp.MustCompile(`^\d+$`).MatchString(fragment) {
			commentID = fragment
		}
	}

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
					Cmd:         fmt.Sprintf("bcq cards column show %s --in %s", recordingID, bucketID),
					Description: "View the column",
				},
				output.Breadcrumb{
					Action:      "columns",
					Cmd:         fmt.Sprintf("bcq cards columns --in %s", bucketID),
					Description: "List all columns",
				},
			)
		case "step":
			breadcrumbs = append(breadcrumbs,
				output.Breadcrumb{
					Action:      "complete",
					Cmd:         fmt.Sprintf("bcq cards step complete %s --in %s", recordingID, bucketID),
					Description: "Complete the step",
				},
				output.Breadcrumb{
					Action:      "uncomplete",
					Cmd:         fmt.Sprintf("bcq cards step uncomplete %s --in %s", recordingID, bucketID),
					Description: "Uncomplete the step",
				},
			)
		default:
			// Standard recording types
			breadcrumbs = append(breadcrumbs,
				output.Breadcrumb{
					Action:      "show",
					Cmd:         fmt.Sprintf("bcq show %s %s --in %s", typeSingular, recordingID, bucketID),
					Description: fmt.Sprintf("View the %s", typeSingular),
				},
				output.Breadcrumb{
					Action:      "comment",
					Cmd:         fmt.Sprintf("bcq comment --content \"text\" --on %s --in %s", recordingID, bucketID),
					Description: "Add a comment",
				},
				output.Breadcrumb{
					Action:      "comments",
					Cmd:         fmt.Sprintf("bcq comments --on %s --in %s", recordingID, bucketID),
					Description: "List comments",
				},
			)

			if commentID != "" {
				breadcrumbs = append(breadcrumbs,
					output.Breadcrumb{
						Action:      "show-comment",
						Cmd:         fmt.Sprintf("bcq comments show %s --in %s", commentID, bucketID),
						Description: "View the comment",
					},
				)
			}
		}
	}

	return app.Output.OK(result,
		output.WithSummary(summary),
		output.WithBreadcrumbs(breadcrumbs...),
	)
}
