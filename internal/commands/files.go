package commands

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/basecamp/bcq/internal/api"
	"github.com/basecamp/bcq/internal/appctx"
	"github.com/basecamp/bcq/internal/output"
)

// FilesListResult represents the combined contents of a vault.
type FilesListResult struct {
	VaultID    int64  `json:"vault_id"`
	VaultTitle string `json:"vault_title"`
	Folders    []any  `json:"folders"`
	Files      []any  `json:"files"`
	Documents  []any  `json:"documents"`
}

// NewFilesCmd creates the files command group.
func NewFilesCmd() *cobra.Command {
	var project string
	var vaultID string

	cmd := &cobra.Command{
		Use:     "files",
		Aliases: []string{"file"},
		Short:   "Manage Docs & Files",
		Long: `Manage Docs & Files (vaults, uploads, documents).

A vault is a container for documents, uploads (files), and subvaults (folders).
Each project has one root vault in its dock.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Default to list when called without subcommand
			return runFilesList(cmd, project, vaultID)
		},
	}

	cmd.PersistentFlags().StringVarP(&project, "project", "p", "", "Project ID or name")
	cmd.PersistentFlags().StringVar(&project, "in", "", "Project ID (alias for --project)")
	cmd.PersistentFlags().StringVar(&vaultID, "vault", "", "Vault/folder ID (default: root)")
	cmd.PersistentFlags().StringVar(&vaultID, "folder", "", "Folder ID (alias for --vault)")

	cmd.AddCommand(
		newFilesListCmd(&project, &vaultID),
		newFoldersCmd(&project, &vaultID),
		newUploadsCmd(&project, &vaultID),
		newDocsCmd(&project, &vaultID),
		newFilesShowCmd(&project),
		newFilesUpdateCmd(&project),
	)

	return cmd
}

// NewVaultsCmd creates the vaults/folders command alias.
func NewVaultsCmd() *cobra.Command {
	cmd := NewFilesCmd()
	cmd.Use = "vaults"
	cmd.Aliases = []string{"vault", "folders"}
	cmd.Short = "Manage vaults/folders (alias for files)"
	return cmd
}

// NewDocsCmd creates the docs command alias.
func NewDocsCmd() *cobra.Command {
	cmd := NewFilesCmd()
	cmd.Use = "docs"
	cmd.Aliases = []string{"documents"}
	cmd.Short = "Manage documents (alias for files)"
	return cmd
}

// NewUploadsCmd creates the uploads command alias.
func NewUploadsCmd() *cobra.Command {
	cmd := NewFilesCmd()
	cmd.Use = "uploads"
	cmd.Aliases = []string{"upload"}
	cmd.Short = "Manage file uploads (alias for files)"
	return cmd
}

func newFilesListCmd(project, vaultID *string) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all items in a vault",
		Long:  "List all folders, documents, and uploads in a vault.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runFilesList(cmd, *project, *vaultID)
		},
	}
}

func runFilesList(cmd *cobra.Command, project, vaultID string) error {
	app := appctx.FromContext(cmd.Context())
	if err := app.API.RequireAccount(); err != nil {
		return err
	}

	// Resolve project
	projectID := project
	if projectID == "" {
		projectID = app.Flags.Project
	}
	if projectID == "" {
		projectID = app.Config.ProjectID
	}
	if projectID == "" {
		return output.ErrUsage("--project is required")
	}

	resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
	if err != nil {
		return err
	}

	// Get vault ID
	resolvedVaultID := vaultID
	if resolvedVaultID == "" {
		resolvedVaultID, err = getVaultID(cmd, app, resolvedProjectID)
		if err != nil {
			return err
		}
	}

	// Get vault details
	vaultPath := fmt.Sprintf("/buckets/%s/vaults/%s.json", resolvedProjectID, resolvedVaultID)
	vaultResp, err := app.API.Get(cmd.Context(), vaultPath)
	if err != nil {
		return err
	}

	var vaultData struct {
		Title string `json:"title"`
	}
	if err := json.Unmarshal(vaultResp.Data, &vaultData); err != nil {
		return err
	}
	vaultTitle := vaultData.Title
	if vaultTitle == "" {
		vaultTitle = "Docs & Files"
	}

	// Get folders (subvaults)
	foldersPath := fmt.Sprintf("/buckets/%s/vaults/%s/vaults.json", resolvedProjectID, resolvedVaultID)
	foldersResp, err := app.API.Get(cmd.Context(), foldersPath)
	var folders []any
	if err == nil {
		json.Unmarshal(foldersResp.Data, &folders)
	}

	// Get uploads
	uploadsPath := fmt.Sprintf("/buckets/%s/vaults/%s/uploads.json", resolvedProjectID, resolvedVaultID)
	uploadsResp, err := app.API.Get(cmd.Context(), uploadsPath)
	var uploads []any
	if err == nil {
		json.Unmarshal(uploadsResp.Data, &uploads)
	}

	// Get documents
	docsPath := fmt.Sprintf("/buckets/%s/vaults/%s/documents.json", resolvedProjectID, resolvedVaultID)
	docsResp, err := app.API.Get(cmd.Context(), docsPath)
	var documents []any
	if err == nil {
		json.Unmarshal(docsResp.Data, &documents)
	}

	// Build result
	vaultIDNum := int64(0)
	fmt.Sscanf(resolvedVaultID, "%d", &vaultIDNum)

	result := FilesListResult{
		VaultID:    vaultIDNum,
		VaultTitle: vaultTitle,
		Folders:    folders,
		Files:      uploads,
		Documents:  documents,
	}

	summary := fmt.Sprintf("%d folders, %d files, %d documents", len(folders), len(uploads), len(documents))

	return app.Output.OK(result,
		output.WithSummary(summary),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "show",
				Cmd:         fmt.Sprintf("bcq files show <id> --in %s", resolvedProjectID),
				Description: "Show item details",
			},
			output.Breadcrumb{
				Action:      "folder",
				Cmd:         fmt.Sprintf("bcq files folder create --name <name> --in %s", resolvedProjectID),
				Description: "Create folder",
			},
			output.Breadcrumb{
				Action:      "doc",
				Cmd:         fmt.Sprintf("bcq files doc create --title <title> --in %s", resolvedProjectID),
				Description: "Create document",
			},
		),
	)
}

func newFoldersCmd(project, vaultID *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "folders",
		Aliases: []string{"folder", "vaults", "vault"},
		Short:   "Manage folders/vaults",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runFoldersList(cmd, *project, *vaultID)
		},
	}

	cmd.AddCommand(
		newFoldersListCmd(project, vaultID),
		newFoldersCreateCmd(project, vaultID),
	)

	return cmd
}

func newFoldersListCmd(project, vaultID *string) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List folders in a vault",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runFoldersList(cmd, *project, *vaultID)
		},
	}
}

func runFoldersList(cmd *cobra.Command, project, vaultID string) error {
	app := appctx.FromContext(cmd.Context())
	if err := app.API.RequireAccount(); err != nil {
		return err
	}

	// Resolve project
	projectID := project
	if projectID == "" {
		projectID = app.Flags.Project
	}
	if projectID == "" {
		projectID = app.Config.ProjectID
	}
	if projectID == "" {
		return output.ErrUsage("--project is required")
	}

	resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
	if err != nil {
		return err
	}

	// Get vault ID
	resolvedVaultID := vaultID
	if resolvedVaultID == "" {
		resolvedVaultID, err = getVaultID(cmd, app, resolvedProjectID)
		if err != nil {
			return err
		}
	}

	path := fmt.Sprintf("/buckets/%s/vaults/%s/vaults.json", resolvedProjectID, resolvedVaultID)
	resp, err := app.API.Get(cmd.Context(), path)
	if err != nil {
		return err
	}

	var folders []any
	if err := resp.UnmarshalData(&folders); err != nil {
		return fmt.Errorf("failed to parse folders: %w", err)
	}

	return app.Output.OK(folders,
		output.WithSummary(fmt.Sprintf("%d folders", len(folders))),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "create",
				Cmd:         fmt.Sprintf("bcq files folder create --name <name> --in %s", resolvedProjectID),
				Description: "Create folder",
			},
			output.Breadcrumb{
				Action:      "list",
				Cmd:         fmt.Sprintf("bcq files --vault <id> --in %s", resolvedProjectID),
				Description: "List folder contents",
			},
		),
	)
}

func newFoldersCreateCmd(project, vaultID *string) *cobra.Command {
	var name string

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new folder",
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := app.API.RequireAccount(); err != nil {
				return err
			}

			if name == "" {
				return output.ErrUsage("--name is required")
			}

			// Resolve project
			projectID := *project
			if projectID == "" {
				projectID = app.Flags.Project
			}
			if projectID == "" {
				projectID = app.Config.ProjectID
			}
			if projectID == "" {
				return output.ErrUsage("--project is required")
			}

			resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
			if err != nil {
				return err
			}

			// Get vault ID
			resolvedVaultID := *vaultID
			if resolvedVaultID == "" {
				resolvedVaultID, err = getVaultID(cmd, app, resolvedProjectID)
				if err != nil {
					return err
				}
			}

			body := map[string]string{
				"title": name,
			}

			path := fmt.Sprintf("/buckets/%s/vaults/%s/vaults.json", resolvedProjectID, resolvedVaultID)
			resp, err := app.API.Post(cmd.Context(), path, body)
			if err != nil {
				return err
			}

			var folder struct {
				ID int64 `json:"id"`
			}
			json.Unmarshal(resp.Data, &folder)

			return app.Output.OK(json.RawMessage(resp.Data),
				output.WithSummary(fmt.Sprintf("Created folder #%d: %s", folder.ID, name)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "list",
						Cmd:         fmt.Sprintf("bcq files --vault %d --in %s", folder.ID, resolvedProjectID),
						Description: "List folder contents",
					},
				),
			)
		},
	}

	cmd.Flags().StringVarP(&name, "name", "n", "", "Folder name (required)")
	cmd.MarkFlagRequired("name")

	return cmd
}

func newUploadsCmd(project, vaultID *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "uploads",
		Aliases: []string{"upload"},
		Short:   "Manage uploaded files",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runUploadsList(cmd, *project, *vaultID)
		},
	}

	cmd.AddCommand(
		newUploadsListCmd(project, vaultID),
	)

	return cmd
}

func newUploadsListCmd(project, vaultID *string) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List uploaded files in a vault",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runUploadsList(cmd, *project, *vaultID)
		},
	}
}

func runUploadsList(cmd *cobra.Command, project, vaultID string) error {
	app := appctx.FromContext(cmd.Context())
	if err := app.API.RequireAccount(); err != nil {
		return err
	}

	// Resolve project
	projectID := project
	if projectID == "" {
		projectID = app.Flags.Project
	}
	if projectID == "" {
		projectID = app.Config.ProjectID
	}
	if projectID == "" {
		return output.ErrUsage("--project is required")
	}

	resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
	if err != nil {
		return err
	}

	// Get vault ID
	resolvedVaultID := vaultID
	if resolvedVaultID == "" {
		resolvedVaultID, err = getVaultID(cmd, app, resolvedProjectID)
		if err != nil {
			return err
		}
	}

	path := fmt.Sprintf("/buckets/%s/vaults/%s/uploads.json", resolvedProjectID, resolvedVaultID)
	resp, err := app.API.Get(cmd.Context(), path)
	if err != nil {
		return err
	}

	var uploads []any
	if err := resp.UnmarshalData(&uploads); err != nil {
		return fmt.Errorf("failed to parse uploads: %w", err)
	}

	return app.Output.OK(uploads,
		output.WithSummary(fmt.Sprintf("%d files", len(uploads))),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "show",
				Cmd:         fmt.Sprintf("bcq files show <id> --in %s", resolvedProjectID),
				Description: "Show file details",
			},
		),
	)
}

func newDocsCmd(project, vaultID *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "documents",
		Aliases: []string{"document", "doc"},
		Short:   "Manage documents",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDocsList(cmd, *project, *vaultID)
		},
	}

	cmd.AddCommand(
		newDocsListCmd(project, vaultID),
		newDocsCreateCmd(project, vaultID),
	)

	return cmd
}

func newDocsListCmd(project, vaultID *string) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List documents in a vault",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDocsList(cmd, *project, *vaultID)
		},
	}
}

func runDocsList(cmd *cobra.Command, project, vaultID string) error {
	app := appctx.FromContext(cmd.Context())
	if err := app.API.RequireAccount(); err != nil {
		return err
	}

	// Resolve project
	projectID := project
	if projectID == "" {
		projectID = app.Flags.Project
	}
	if projectID == "" {
		projectID = app.Config.ProjectID
	}
	if projectID == "" {
		return output.ErrUsage("--project is required")
	}

	resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
	if err != nil {
		return err
	}

	// Get vault ID
	resolvedVaultID := vaultID
	if resolvedVaultID == "" {
		resolvedVaultID, err = getVaultID(cmd, app, resolvedProjectID)
		if err != nil {
			return err
		}
	}

	path := fmt.Sprintf("/buckets/%s/vaults/%s/documents.json", resolvedProjectID, resolvedVaultID)
	resp, err := app.API.Get(cmd.Context(), path)
	if err != nil {
		return err
	}

	var documents []any
	if err := resp.UnmarshalData(&documents); err != nil {
		return fmt.Errorf("failed to parse documents: %w", err)
	}

	return app.Output.OK(documents,
		output.WithSummary(fmt.Sprintf("%d documents", len(documents))),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "create",
				Cmd:         fmt.Sprintf("bcq files doc create --title <title> --in %s", resolvedProjectID),
				Description: "Create document",
			},
			output.Breadcrumb{
				Action:      "show",
				Cmd:         fmt.Sprintf("bcq files show <id> --in %s", resolvedProjectID),
				Description: "Show document",
			},
		),
	)
}

func newDocsCreateCmd(project, vaultID *string) *cobra.Command {
	var title string
	var content string
	var draft bool

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new document",
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := app.API.RequireAccount(); err != nil {
				return err
			}

			if title == "" {
				return output.ErrUsage("--title is required")
			}

			// Resolve project
			projectID := *project
			if projectID == "" {
				projectID = app.Flags.Project
			}
			if projectID == "" {
				projectID = app.Config.ProjectID
			}
			if projectID == "" {
				return output.ErrUsage("--project is required")
			}

			resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
			if err != nil {
				return err
			}

			// Get vault ID
			resolvedVaultID := *vaultID
			if resolvedVaultID == "" {
				resolvedVaultID, err = getVaultID(cmd, app, resolvedProjectID)
				if err != nil {
					return err
				}
			}

			body := map[string]string{
				"title": title,
			}
			if content != "" {
				body["content"] = content
			}
			if draft {
				body["status"] = "drafted"
			} else {
				body["status"] = "active"
			}

			path := fmt.Sprintf("/buckets/%s/vaults/%s/documents.json", resolvedProjectID, resolvedVaultID)
			resp, err := app.API.Post(cmd.Context(), path, body)
			if err != nil {
				return err
			}

			var doc struct {
				ID int64 `json:"id"`
			}
			json.Unmarshal(resp.Data, &doc)

			return app.Output.OK(json.RawMessage(resp.Data),
				output.WithSummary(fmt.Sprintf("Created document #%d: %s", doc.ID, title)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "show",
						Cmd:         fmt.Sprintf("bcq files show %d --in %s", doc.ID, resolvedProjectID),
						Description: "View document",
					},
					output.Breadcrumb{
						Action:      "update",
						Cmd:         fmt.Sprintf("bcq files update %d --content <text> --in %s", doc.ID, resolvedProjectID),
						Description: "Update document",
					},
				),
			)
		},
	}

	cmd.Flags().StringVarP(&title, "title", "t", "", "Document title (required)")
	cmd.Flags().StringVarP(&content, "content", "c", "", "Document content")
	cmd.Flags().BoolVar(&draft, "draft", false, "Create as draft (default: published)")
	cmd.MarkFlagRequired("title")

	return cmd
}

func newFilesShowCmd(project *string) *cobra.Command {
	var itemType string

	cmd := &cobra.Command{
		Use:   "show <id>",
		Short: "Show item details",
		Long:  "Show details for a vault, document, or upload.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := app.API.RequireAccount(); err != nil {
				return err
			}

			itemID := args[0]

			// Resolve project
			projectID := *project
			if projectID == "" {
				projectID = app.Flags.Project
			}
			if projectID == "" {
				projectID = app.Config.ProjectID
			}
			if projectID == "" {
				return output.ErrUsage("--project is required")
			}

			resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
			if err != nil {
				return err
			}

			// Try to detect type if not specified
			var resp *api.Response
			var detectedType string

			if itemType == "" {
				// Try vault first
				path := fmt.Sprintf("/buckets/%s/vaults/%s.json", resolvedProjectID, itemID)
				resp, err = app.API.Get(cmd.Context(), path)
				if err == nil && len(resp.Data) > 0 {
					detectedType = "vault"
				} else {
					// Try upload
					path = fmt.Sprintf("/buckets/%s/uploads/%s.json", resolvedProjectID, itemID)
					resp, err = app.API.Get(cmd.Context(), path)
					if err == nil && len(resp.Data) > 0 {
						detectedType = "upload"
					} else {
						// Try document
						path = fmt.Sprintf("/buckets/%s/documents/%s.json", resolvedProjectID, itemID)
						resp, err = app.API.Get(cmd.Context(), path)
						if err == nil && len(resp.Data) > 0 {
							detectedType = "document"
						}
					}
				}
			} else {
				var path string
				switch itemType {
				case "vault", "folder":
					path = fmt.Sprintf("/buckets/%s/vaults/%s.json", resolvedProjectID, itemID)
					detectedType = "vault"
				case "upload", "file":
					path = fmt.Sprintf("/buckets/%s/uploads/%s.json", resolvedProjectID, itemID)
					detectedType = "upload"
				case "document", "doc":
					path = fmt.Sprintf("/buckets/%s/documents/%s.json", resolvedProjectID, itemID)
					detectedType = "document"
				default:
					return output.ErrUsageHint(
						fmt.Sprintf("Invalid type: %s", itemType),
						"Use: vault, upload, or document",
					)
				}
				resp, err = app.API.Get(cmd.Context(), path)
			}

			if err != nil {
				return err
			}
			if resp == nil || len(resp.Data) == 0 {
				return output.ErrNotFound("item", itemID)
			}

			// Parse for summary
			var data map[string]any
			json.Unmarshal(resp.Data, &data)

			title := ""
			if t, ok := data["title"].(string); ok {
				title = t
			} else if f, ok := data["filename"].(string); ok {
				title = f
			}

			itemTypeDisplay := detectedType
			if t, ok := data["type"].(string); ok {
				itemTypeDisplay = t
			}

			summary := fmt.Sprintf("%s: %s", itemTypeDisplay, title)

			breadcrumbs := []output.Breadcrumb{
				{
					Action:      "update",
					Cmd:         fmt.Sprintf("bcq files update %s --in %s", itemID, resolvedProjectID),
					Description: "Update item",
				},
			}

			if detectedType == "vault" {
				breadcrumbs = append(breadcrumbs, output.Breadcrumb{
					Action:      "contents",
					Cmd:         fmt.Sprintf("bcq files --vault %s --in %s", itemID, resolvedProjectID),
					Description: "List contents",
				})
			}

			return app.Output.OK(json.RawMessage(resp.Data),
				output.WithSummary(summary),
				output.WithBreadcrumbs(breadcrumbs...),
			)
		},
	}

	cmd.Flags().StringVarP(&itemType, "type", "t", "", "Item type (vault, upload, document)")

	return cmd
}

func newFilesUpdateCmd(project *string) *cobra.Command {
	var title string
	var content string
	var itemType string

	cmd := &cobra.Command{
		Use:   "update <id>",
		Short: "Update a document, vault, or upload",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := app.API.RequireAccount(); err != nil {
				return err
			}

			itemID := args[0]

			if title == "" && content == "" {
				return output.ErrUsage("at least one of --title or --content is required")
			}

			// Resolve project
			projectID := *project
			if projectID == "" {
				projectID = app.Flags.Project
			}
			if projectID == "" {
				projectID = app.Config.ProjectID
			}
			if projectID == "" {
				return output.ErrUsage("--project is required")
			}

			resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
			if err != nil {
				return err
			}

			// Auto-detect type if not specified
			var endpoint string
			var detectedType string

			if itemType != "" {
				switch itemType {
				case "vault", "folder":
					endpoint = fmt.Sprintf("/buckets/%s/vaults/%s.json", resolvedProjectID, itemID)
					detectedType = "vault"
				case "document", "doc":
					endpoint = fmt.Sprintf("/buckets/%s/documents/%s.json", resolvedProjectID, itemID)
					detectedType = "document"
				case "upload", "file":
					endpoint = fmt.Sprintf("/buckets/%s/uploads/%s.json", resolvedProjectID, itemID)
					detectedType = "upload"
				default:
					return output.ErrUsageHint(
						fmt.Sprintf("Invalid type: %s", itemType),
						"Use: vault, document, or upload",
					)
				}
			} else {
				// Try document first (most common update case)
				path := fmt.Sprintf("/buckets/%s/documents/%s.json", resolvedProjectID, itemID)
				_, err := app.API.Get(cmd.Context(), path)
				if err == nil {
					endpoint = path
					detectedType = "document"
				} else {
					// Try vault
					path = fmt.Sprintf("/buckets/%s/vaults/%s.json", resolvedProjectID, itemID)
					_, err = app.API.Get(cmd.Context(), path)
					if err == nil {
						endpoint = path
						detectedType = "vault"
					} else {
						// Try upload
						path = fmt.Sprintf("/buckets/%s/uploads/%s.json", resolvedProjectID, itemID)
						_, err = app.API.Get(cmd.Context(), path)
						if err == nil {
							endpoint = path
							detectedType = "upload"
						} else {
							return output.ErrUsageHint(
								fmt.Sprintf("Item %s not found", itemID),
								"Specify --type if needed",
							)
						}
					}
				}
			}

			body := make(map[string]string)
			if title != "" {
				body["title"] = title
			}
			if content != "" {
				body["content"] = content
			}

			resp, err := app.API.Put(cmd.Context(), endpoint, body)
			if err != nil {
				return err
			}

			return app.Output.OK(json.RawMessage(resp.Data),
				output.WithSummary(fmt.Sprintf("Updated %s #%s", detectedType, itemID)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "show",
						Cmd:         fmt.Sprintf("bcq files show %s --in %s", itemID, resolvedProjectID),
						Description: "View item",
					},
				),
			)
		},
	}

	cmd.Flags().StringVarP(&title, "title", "t", "", "New title")
	cmd.Flags().StringVarP(&content, "content", "c", "", "New content")
	cmd.Flags().StringVar(&itemType, "type", "", "Item type (vault, document, upload)")

	return cmd
}

// getVaultID retrieves the root vault ID from a project's dock, handling multi-dock projects.
func getVaultID(cmd *cobra.Command, app *appctx.App, projectID string) (string, error) {
	return getDockToolID(cmd.Context(), app, projectID, "vault", "", "vault")
}
