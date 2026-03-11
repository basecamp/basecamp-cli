package commands

import (
	"fmt"
	"html"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/output"
	"github.com/basecamp/basecamp-cli/internal/richtext"
)

// attachResult holds the structured output for a single attachment upload.
type attachResult struct {
	AttachableSGID string `json:"attachable_sgid"`
	Filename       string `json:"filename"`
	ContentType    string `json:"content_type"`
	HTML           string `json:"html"`
}

// NewAttachCmd creates the 'attach' command — a staging primitive for uploading files.
func NewAttachCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "attach <file> [<file2> ...]",
		Short: "Upload a file and return its attachment reference",
		Long: `Upload one or more files to Basecamp and return attachment references.

Each file is uploaded and a <bc-attachment> HTML tag is returned along with the
attachable_sgid. Use these in rich text content fields to embed files.

No project is needed — attachment upload is account-scoped.`,
		Example: `  basecamp attach ./screenshot.png
  basecamp attach ./a.png ./b.pdf --json`,
		Annotations: map[string]string{"agent_notes": "Returns attachable_sgid + <bc-attachment> HTML for embedding in rich text\nAccount-scoped — no project needed\nUse --json for structured output in pipelines"},
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return cmd.Help()
			}

			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			refs, err := uploadAttachments(cmd, app, args)
			if err != nil {
				return err
			}

			// Build results
			results := make([]attachResult, len(refs))
			for i, ref := range refs {
				results[i] = attachResult{
					AttachableSGID: ref.SGID,
					Filename:       ref.Filename,
					ContentType:    ref.ContentType,
					HTML:           richtext.AttachmentToHTML(ref.SGID, ref.Filename, ref.ContentType),
				}
			}

			// Styled output for TTY
			if app.Output.EffectiveFormat() == output.FormatStyled {
				w := cmd.OutOrStdout()
				for _, r := range results {
					fmt.Fprintf(w, "Attached %s (%s)\n%s\n", r.Filename, r.ContentType, r.HTML)
				}
				return nil
			}

			return app.OK(results,
				output.WithSummary(fmt.Sprintf("Uploaded %d file(s)", len(results))),
			)
		},
	}

	return cmd
}

// uploadAttachments uploads each path and returns attachment references.
// Sequential, fails on first error.
func uploadAttachments(cmd *cobra.Command, app *appctx.App, paths []string) ([]richtext.AttachmentRef, error) {
	refs := make([]richtext.AttachmentRef, 0, len(paths))

	for _, path := range paths {
		normalized := richtext.NormalizeDragPath(path)

		if err := richtext.ValidateFile(normalized); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}

		contentType := richtext.DetectMIME(normalized)
		filename := filepath.Base(normalized)

		f, err := os.Open(normalized)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}

		resp, err := app.Account().Attachments().Create(cmd.Context(), filename, contentType, f)
		f.Close()
		if err != nil {
			return nil, convertSDKError(err)
		}

		refs = append(refs, richtext.AttachmentRef{
			SGID:        resp.AttachableSGID,
			Filename:    filename,
			ContentType: contentType,
		})
	}

	return refs, nil
}

// resolveLocalImages scans HTML (output of MarkdownToHTML) for <img> tags with
// local file paths and replaces them with <bc-attachment> tags after uploading.
//
// Behavior per src type:
//   - Remote URLs (http://, https://): skip
//   - Non-file URIs (data:, cid:, blob:, etc.): skip
//   - Local path exists: upload, replace <img> with <bc-attachment>
//   - Local path missing: error
//   - Placeholder (? or empty): error
func resolveLocalImages(cmd *cobra.Command, app *appctx.App, htmlStr string) (string, error) {
	// Quick bail: no images
	if !hasImgTag(htmlStr) {
		return htmlStr, nil
	}

	// Find all <img> tags
	matches := imgTagPattern.FindAllStringSubmatchIndex(htmlStr, -1)
	if len(matches) == 0 {
		return htmlStr, nil
	}

	// Process in reverse order so replacements don't shift indices
	result := htmlStr
	for i := len(matches) - 1; i >= 0; i-- {
		m := matches[i]
		fullStart, fullEnd := m[0], m[1]
		srcStart, srcEnd := m[2], m[3]
		altStart, altEnd := m[4], m[5]

		src := html.UnescapeString(htmlStr[srcStart:srcEnd])
		alt := ""
		if altStart >= 0 {
			alt = htmlStr[altStart:altEnd]
		}

		action, classErr := classifyImageSrc(src, alt)
		if classErr != nil {
			return "", classErr
		}
		if action == imgSkip {
			continue
		}

		// Normalize file:// URLs to filesystem paths
		src = richtext.NormalizeDragPath(src)

		// Local path — must exist
		if err := richtext.ValidateFile(src); err != nil {
			return "", fmt.Errorf("%s: %w", src, err)
		}

		// Upload
		contentType := richtext.DetectMIME(src)
		filename := filepath.Base(src)

		f, err := os.Open(src)
		if err != nil {
			return "", fmt.Errorf("%s: %w", src, err)
		}

		resp, err := app.Account().Attachments().Create(cmd.Context(), filename, contentType, f)
		f.Close()
		if err != nil {
			return "", convertSDKError(err)
		}

		// Replace <img> with <bc-attachment>
		bcTag := richtext.AttachmentToHTML(resp.AttachableSGID, filename, contentType)
		result = result[:fullStart] + bcTag + result[fullEnd:]
	}

	return result, nil
}
