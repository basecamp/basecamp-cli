package commands

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"github.com/basecamp/bcq/internal/appctx"
	"github.com/basecamp/bcq/internal/output"
)

// NewAPICmd creates the api command for raw API access.
func NewAPICmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "api <verb> <path>",
		Short: "Raw API access",
		Long:  "Make raw API requests to any Basecamp endpoint. Useful for operations not covered by dedicated commands.",
	}

	cmd.AddCommand(
		newAPIGetCmd(),
		newAPIPostCmd(),
		newAPIPutCmd(),
		newAPIDeleteCmd(),
	)

	return cmd
}

func newAPIGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <path>",
		Short: "GET request to API",
		Long:  "Make a raw GET request to any Basecamp API endpoint.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := app.RequireAccount(); err != nil {
				return err
			}

			path := parsePath(args[0])
			resp, err := app.Account().Get(cmd.Context(), path)
			if err != nil {
				return convertSDKError(err)
			}

			summary := apiSummary(resp.Data)
			breadcrumbs := apiBreadcrumbs(path)

			return app.OK(resp.Data,
				output.WithSummary(summary),
				output.WithBreadcrumbs(breadcrumbs...),
			)
		},
	}
}

func newAPIPostCmd() *cobra.Command {
	var data string

	cmd := &cobra.Command{
		Use:   "post <path>",
		Short: "POST request to API",
		Long:  "Make a raw POST request to any Basecamp API endpoint.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := app.RequireAccount(); err != nil {
				return err
			}

			if data == "" {
				return output.ErrUsage("--data is required")
			}

			path := parsePath(args[0])

			// Parse JSON data
			var body any
			if err := json.Unmarshal([]byte(data), &body); err != nil {
				return output.ErrUsageHint(
					"Invalid JSON data",
					fmt.Sprintf("JSON parse error: %v", err),
				)
			}

			resp, err := app.Account().Post(cmd.Context(), path, body)
			if err != nil {
				return err
			}

			summary := fmt.Sprintf("POST %s: %s", path, apiSummary(resp.Data))

			return app.OK(resp.Data,
				output.WithSummary(summary),
			)
		},
	}

	cmd.Flags().StringVarP(&data, "data", "d", "", "JSON request body (required)")
	_ = cmd.MarkFlagRequired("data") // Error only if flag doesn't exist

	return cmd
}

func newAPIPutCmd() *cobra.Command {
	var data string

	cmd := &cobra.Command{
		Use:   "put <path>",
		Short: "PUT request to API",
		Long:  "Make a raw PUT request to any Basecamp API endpoint.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := app.RequireAccount(); err != nil {
				return err
			}

			if data == "" {
				return output.ErrUsage("--data is required")
			}

			path := parsePath(args[0])

			// Parse JSON data
			var body any
			if err := json.Unmarshal([]byte(data), &body); err != nil {
				return output.ErrUsageHint(
					"Invalid JSON data",
					fmt.Sprintf("JSON parse error: %v", err),
				)
			}

			resp, err := app.Account().Put(cmd.Context(), path, body)
			if err != nil {
				return err
			}

			summary := fmt.Sprintf("PUT %s: %s", path, apiSummary(resp.Data))

			return app.OK(resp.Data,
				output.WithSummary(summary),
			)
		},
	}

	cmd.Flags().StringVarP(&data, "data", "d", "", "JSON request body (required)")
	_ = cmd.MarkFlagRequired("data") // Error only if flag doesn't exist

	return cmd
}

func newAPIDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <path>",
		Short: "DELETE request to API",
		Long:  "Make a raw DELETE request to any Basecamp API endpoint.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := app.RequireAccount(); err != nil {
				return err
			}

			path := parsePath(args[0])
			resp, err := app.Account().Delete(cmd.Context(), path)
			if err != nil {
				return err
			}

			// Handle empty response (204 No Content)
			data := resp.Data
			if len(data) == 0 {
				data = []byte("{}")
			}

			summary := fmt.Sprintf("DELETE %s", path)

			return app.OK(data,
				output.WithSummary(summary),
			)
		},
	}
}

// parsePath extracts and normalizes the API path.
// Handles full URLs, relative paths, and auto-adds leading slash.
func parsePath(input string) string {
	// Extract path from full URL
	// Matches: https://3.basecampapi.com/12345/projects.json
	urlPattern := regexp.MustCompile(`^https?://[^/]+/[0-9]+(/.*)`)
	if matches := urlPattern.FindStringSubmatch(input); len(matches) > 1 {
		return matches[1]
	}

	// Ensure leading slash
	if !strings.HasPrefix(input, "/") {
		input = "/" + input
	}

	return input
}

// apiSummary generates a summary from the API response.
func apiSummary(data []byte) string {
	// Check if array response
	var arr []any
	if err := json.Unmarshal(data, &arr); err == nil {
		return fmt.Sprintf("%d items", len(arr))
	}

	// Single object - try to get type/title
	var obj map[string]any
	if err := json.Unmarshal(data, &obj); err != nil {
		return "API response"
	}

	itemType, _ := obj["type"].(string)
	title := ""
	for _, key := range []string{"title", "name", "subject"} {
		if v, ok := obj[key].(string); ok && v != "" {
			title = v
			break
		}
	}

	// Truncate title if too long
	if len(title) > 50 {
		title = title[:47] + "..."
	}

	if itemType != "" && title != "" {
		return fmt.Sprintf("%s: %s", itemType, title)
	}
	if itemType != "" {
		return itemType
	}
	if title != "" {
		return title
	}
	return "API response"
}

// apiBreadcrumbs generates contextual breadcrumbs based on the path.
func apiBreadcrumbs(path string) []output.Breadcrumb {
	var breadcrumbs []output.Breadcrumb

	// Projects list
	if strings.HasSuffix(path, "/projects.json") {
		breadcrumbs = append(breadcrumbs,
			output.Breadcrumb{
				Action:      "details",
				Cmd:         "bcq api get /projects/<id>.json",
				Description: "Get project details",
			},
			output.Breadcrumb{
				Action:      "list",
				Cmd:         "bcq projects",
				Description: "List projects with formatting",
			},
		)
	}

	// Card table
	cardTablePattern := regexp.MustCompile(`/buckets/(\d+)/card_tables/(\d+)\.json`)
	if matches := cardTablePattern.FindStringSubmatch(path); len(matches) > 1 {
		bucket := matches[1]
		breadcrumbs = append(breadcrumbs,
			output.Breadcrumb{
				Action:      "cards",
				Cmd:         fmt.Sprintf("bcq cards --in %s", bucket),
				Description: "List cards",
			},
			output.Breadcrumb{
				Action:      "columns",
				Cmd:         fmt.Sprintf("bcq cards columns --in %s", bucket),
				Description: "List columns",
			},
		)
		return breadcrumbs
	}

	// Bucket path
	bucketPattern := regexp.MustCompile(`/buckets/(\d+)`)
	if matches := bucketPattern.FindStringSubmatch(path); len(matches) > 1 {
		bucket := matches[1]
		breadcrumbs = append(breadcrumbs,
			output.Breadcrumb{
				Action:      "project",
				Cmd:         fmt.Sprintf("bcq api get /projects/%s.json", bucket),
				Description: "Get project details",
			},
			output.Breadcrumb{
				Action:      "todos",
				Cmd:         fmt.Sprintf("bcq todos --in %s", bucket),
				Description: "List todos",
			},
		)
	}

	return breadcrumbs
}
