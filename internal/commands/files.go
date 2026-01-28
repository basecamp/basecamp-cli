package commands

import (
	"fmt"
	"strconv"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/spf13/cobra"

	"github.com/basecamp/bcq/internal/appctx"
	"github.com/basecamp/bcq/internal/output"
)

// FilesListResult represents the combined contents of a vault.
type FilesListResult struct {
	VaultID    int64               `json:"vault_id"`
	VaultTitle string              `json:"vault_title"`
	Folders    []basecamp.Vault    `json:"folders"`
	Files      []basecamp.Upload   `json:"files"`
	Documents  []basecamp.Document `json:"documents"`
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

	bucketID, err := strconv.ParseInt(resolvedProjectID, 10, 64)
	if err != nil {
		return output.ErrUsage("Invalid project ID")
	}

	// Get vault ID
	resolvedVaultID := vaultID
	if resolvedVaultID == "" {
		resolvedVaultID, err = getVaultID(cmd, app, resolvedProjectID)
		if err != nil {
			return err
		}
	}

	vaultIDNum, err := strconv.ParseInt(resolvedVaultID, 10, 64)
	if err != nil {
		return output.ErrUsage("Invalid vault ID")
	}

	// Get vault details using SDK
	vault, err := app.SDK.Vaults().Get(cmd.Context(), bucketID, vaultIDNum)
	if err != nil {
		return convertSDKError(err)
	}

	vaultTitle := vault.Title
	if vaultTitle == "" {
		vaultTitle = "Docs & Files"
	}

	// Get folders (subvaults) using SDK
	folders, err := app.SDK.Vaults().List(cmd.Context(), bucketID, vaultIDNum)
	if err != nil {
		folders = []basecamp.Vault{} // Best-effort
	}

	// Get uploads using SDK
	uploads, err := app.SDK.Uploads().List(cmd.Context(), bucketID, vaultIDNum)
	if err != nil {
		uploads = []basecamp.Upload{} // Best-effort
	}

	// Get documents using SDK
	documents, err := app.SDK.Documents().List(cmd.Context(), bucketID, vaultIDNum)
	if err != nil {
		documents = []basecamp.Document{} // Best-effort
	}

	// Build result
	result := FilesListResult{
		VaultID:    vaultIDNum,
		VaultTitle: vaultTitle,
		Folders:    folders,
		Files:      uploads,
		Documents:  documents,
	}

	summary := fmt.Sprintf("%d folders, %d files, %d documents", len(folders), len(uploads), len(documents))

	return app.OK(result,
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

	bucketID, err := strconv.ParseInt(resolvedProjectID, 10, 64)
	if err != nil {
		return output.ErrUsage("Invalid project ID")
	}

	// Get vault ID
	resolvedVaultID := vaultID
	if resolvedVaultID == "" {
		resolvedVaultID, err = getVaultID(cmd, app, resolvedProjectID)
		if err != nil {
			return err
		}
	}

	vaultIDNum, err := strconv.ParseInt(resolvedVaultID, 10, 64)
	if err != nil {
		return output.ErrUsage("Invalid vault ID")
	}

	// Get folders using SDK
	folders, err := app.SDK.Vaults().List(cmd.Context(), bucketID, vaultIDNum)
	if err != nil {
		return convertSDKError(err)
	}

	return app.OK(folders,
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

			bucketID, err := strconv.ParseInt(resolvedProjectID, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid project ID")
			}

			// Get vault ID
			resolvedVaultID := *vaultID
			if resolvedVaultID == "" {
				resolvedVaultID, err = getVaultID(cmd, app, resolvedProjectID)
				if err != nil {
					return err
				}
			}

			vaultIDNum, err := strconv.ParseInt(resolvedVaultID, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid vault ID")
			}

			// Create folder using SDK
			req := &basecamp.CreateVaultRequest{
				Title: name,
			}

			folder, err := app.SDK.Vaults().Create(cmd.Context(), bucketID, vaultIDNum, req)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(folder,
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
	_ = cmd.MarkFlagRequired("name")

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

	bucketID, err := strconv.ParseInt(resolvedProjectID, 10, 64)
	if err != nil {
		return output.ErrUsage("Invalid project ID")
	}

	// Get vault ID
	resolvedVaultID := vaultID
	if resolvedVaultID == "" {
		resolvedVaultID, err = getVaultID(cmd, app, resolvedProjectID)
		if err != nil {
			return err
		}
	}

	vaultIDNum, err := strconv.ParseInt(resolvedVaultID, 10, 64)
	if err != nil {
		return output.ErrUsage("Invalid vault ID")
	}

	// Get uploads using SDK
	uploads, err := app.SDK.Uploads().List(cmd.Context(), bucketID, vaultIDNum)
	if err != nil {
		return convertSDKError(err)
	}

	return app.OK(uploads,
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

	bucketID, err := strconv.ParseInt(resolvedProjectID, 10, 64)
	if err != nil {
		return output.ErrUsage("Invalid project ID")
	}

	// Get vault ID
	resolvedVaultID := vaultID
	if resolvedVaultID == "" {
		resolvedVaultID, err = getVaultID(cmd, app, resolvedProjectID)
		if err != nil {
			return err
		}
	}

	vaultIDNum, err := strconv.ParseInt(resolvedVaultID, 10, 64)
	if err != nil {
		return output.ErrUsage("Invalid vault ID")
	}

	// Get documents using SDK
	documents, err := app.SDK.Documents().List(cmd.Context(), bucketID, vaultIDNum)
	if err != nil {
		return convertSDKError(err)
	}

	return app.OK(documents,
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

			bucketID, err := strconv.ParseInt(resolvedProjectID, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid project ID")
			}

			// Get vault ID
			resolvedVaultID := *vaultID
			if resolvedVaultID == "" {
				resolvedVaultID, err = getVaultID(cmd, app, resolvedProjectID)
				if err != nil {
					return err
				}
			}

			vaultIDNum, err := strconv.ParseInt(resolvedVaultID, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid vault ID")
			}

			// Create document using SDK
			req := &basecamp.CreateDocumentRequest{
				Title:   title,
				Content: content,
			}
			if draft {
				req.Status = "drafted"
			} else {
				req.Status = "active"
			}

			doc, err := app.SDK.Documents().Create(cmd.Context(), bucketID, vaultIDNum, req)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(doc,
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
	_ = cmd.MarkFlagRequired("title")

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

			itemIDStr := args[0]
			itemID, err := strconv.ParseInt(itemIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid item ID")
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

			bucketID, err := strconv.ParseInt(resolvedProjectID, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid project ID")
			}

			// Try to detect type if not specified
			var result any
			var detectedType string
			var title string

			if itemType == "" {
				// Auto-detect type by trying each in order
				// Track first error to return if all fail (may be auth error, not 404)
				var firstErr error

				// Try vault first
				vault, err := app.SDK.Vaults().Get(cmd.Context(), bucketID, itemID)
				if err == nil {
					result = vault
					detectedType = "vault"
					title = vault.Title
				} else {
					firstErr = err
					// Try upload
					upload, err := app.SDK.Uploads().Get(cmd.Context(), bucketID, itemID)
					if err == nil {
						result = upload
						detectedType = "upload"
						title = upload.Filename
						if title == "" {
							title = upload.Title
						}
					} else {
						// Try document
						doc, err := app.SDK.Documents().Get(cmd.Context(), bucketID, itemID)
						if err == nil {
							result = doc
							detectedType = "document"
							title = doc.Title
						}
					}
				}

				// If all probes failed, check if first error was 404 or something else
				if result == nil && firstErr != nil {
					sdkErr := basecamp.AsError(firstErr)
					if sdkErr.Code != basecamp.CodeNotFound {
						// Return actual error (auth, permission, network, etc.)
						return convertSDKError(firstErr)
					}
				}
			} else {
				switch itemType {
				case "vault", "folder":
					vault, err := app.SDK.Vaults().Get(cmd.Context(), bucketID, itemID)
					if err != nil {
						return convertSDKError(err)
					}
					result = vault
					detectedType = "vault"
					title = vault.Title
				case "upload", "file":
					upload, err := app.SDK.Uploads().Get(cmd.Context(), bucketID, itemID)
					if err != nil {
						return convertSDKError(err)
					}
					result = upload
					detectedType = "upload"
					title = upload.Filename
					if title == "" {
						title = upload.Title
					}
				case "document", "doc":
					doc, err := app.SDK.Documents().Get(cmd.Context(), bucketID, itemID)
					if err != nil {
						return convertSDKError(err)
					}
					result = doc
					detectedType = "document"
					title = doc.Title
				default:
					return output.ErrUsageHint(
						fmt.Sprintf("Invalid type: %s", itemType),
						"Use: vault, upload, or document",
					)
				}
			}

			if result == nil {
				return output.ErrNotFound("item", itemIDStr)
			}

			summary := fmt.Sprintf("%s: %s", detectedType, title)

			breadcrumbs := []output.Breadcrumb{
				{
					Action:      "update",
					Cmd:         fmt.Sprintf("bcq files update %s --in %s", itemIDStr, resolvedProjectID),
					Description: "Update item",
				},
			}

			if detectedType == "vault" {
				breadcrumbs = append(breadcrumbs, output.Breadcrumb{
					Action:      "contents",
					Cmd:         fmt.Sprintf("bcq files --vault %s --in %s", itemIDStr, resolvedProjectID),
					Description: "List contents",
				})
			}

			return app.OK(result,
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

			itemIDStr := args[0]
			itemID, err := strconv.ParseInt(itemIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid item ID")
			}

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

			bucketID, err := strconv.ParseInt(resolvedProjectID, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid project ID")
			}

			// Auto-detect type if not specified
			var result any
			var detectedType string

			if itemType != "" {
				switch itemType {
				case "vault", "folder":
					req := &basecamp.UpdateVaultRequest{Title: title}
					vault, err := app.SDK.Vaults().Update(cmd.Context(), bucketID, itemID, req)
					if err != nil {
						return convertSDKError(err)
					}
					result = vault
					detectedType = "vault"
				case "document", "doc":
					req := &basecamp.UpdateDocumentRequest{Title: title, Content: content}
					doc, err := app.SDK.Documents().Update(cmd.Context(), bucketID, itemID, req)
					if err != nil {
						return convertSDKError(err)
					}
					result = doc
					detectedType = "document"
				case "upload", "file":
					req := &basecamp.UpdateUploadRequest{Description: content}
					if title != "" {
						req.BaseName = title
					}
					upload, err := app.SDK.Uploads().Update(cmd.Context(), bucketID, itemID, req)
					if err != nil {
						return convertSDKError(err)
					}
					result = upload
					detectedType = "upload"
				default:
					return output.ErrUsageHint(
						fmt.Sprintf("Invalid type: %s", itemType),
						"Use: vault, document, or upload",
					)
				}
			} else {
				// Auto-detect type by trying each in order
				// Track first error to check if it was 404 or something else
				var firstErr error

				// Try document first (most common update case)
				_, err := app.SDK.Documents().Get(cmd.Context(), bucketID, itemID)
				if err == nil {
					req := &basecamp.UpdateDocumentRequest{Title: title, Content: content}
					doc, err := app.SDK.Documents().Update(cmd.Context(), bucketID, itemID, req)
					if err != nil {
						return convertSDKError(err)
					}
					result = doc
					detectedType = "document"
				} else {
					firstErr = err
					// Try vault
					_, err = app.SDK.Vaults().Get(cmd.Context(), bucketID, itemID)
					if err == nil {
						req := &basecamp.UpdateVaultRequest{Title: title}
						vault, err := app.SDK.Vaults().Update(cmd.Context(), bucketID, itemID, req)
						if err != nil {
							return convertSDKError(err)
						}
						result = vault
						detectedType = "vault"
					} else {
						// Try upload
						_, err = app.SDK.Uploads().Get(cmd.Context(), bucketID, itemID)
						if err == nil {
							req := &basecamp.UpdateUploadRequest{Description: content}
							if title != "" {
								req.BaseName = title
							}
							upload, err := app.SDK.Uploads().Update(cmd.Context(), bucketID, itemID, req)
							if err != nil {
								return convertSDKError(err)
							}
							result = upload
							detectedType = "upload"
						} else {
							// All probes failed - check if first error was 404 or something else
							sdkErr := basecamp.AsError(firstErr)
							if sdkErr.Code != basecamp.CodeNotFound {
								// Return actual error (auth, permission, network, etc.)
								return convertSDKError(firstErr)
							}
							return output.ErrUsageHint(
								fmt.Sprintf("Item %s not found", itemIDStr),
								"Specify --type if needed",
							)
						}
					}
				}
			}

			return app.OK(result,
				output.WithSummary(fmt.Sprintf("Updated %s #%s", detectedType, itemIDStr)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "show",
						Cmd:         fmt.Sprintf("bcq files show %s --in %s", itemIDStr, resolvedProjectID),
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
