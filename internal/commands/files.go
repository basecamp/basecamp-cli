package commands

import (
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/output"
	"github.com/basecamp/basecamp-cli/internal/richtext"
)

// NewFilesCmd creates the files command group.
func NewFilesCmd() *cobra.Command {
	var project string
	var vaultID string

	cmd := &cobra.Command{
		Use:     "files",
		Aliases: []string{"file"},
		Short:   "Manage Docs & Files",
		Long: `Manage Docs & Files.

Each project has a root folder containing documents, uploads, and subfolders.`,
		Annotations: map[string]string{"agent_notes": "files is the unified view — use uploads, docs, folders for type-specific listing\n--vault <id> filters to contents of a specific folder\nDocuments support Markdown content\nCross-project: basecamp recordings documents --json or basecamp recordings uploads --json"},
	}

	cmd.PersistentFlags().StringVarP(&project, "project", "p", "", "Project ID or name")
	cmd.PersistentFlags().StringVar(&project, "in", "", "Project ID (alias for --project)")
	cmd.PersistentFlags().StringVar(&vaultID, "vault", "", "Folder ID (default: root)")
	cmd.PersistentFlags().StringVar(&vaultID, "folder", "", "Folder ID (alias for --vault)")

	cmd.AddCommand(
		newFilesListCmd(&project, &vaultID),
		newFoldersCmd(&project, &vaultID),
		newUploadsCmd(&project, &vaultID),
		newDocsCmd(&project, &vaultID),
		newFilesShowCmd(&project),
		newFilesUpdateCmd(&project),
		newFilesDownloadCmd(&project),
		newRecordableTrashCmd("file"),
		newRecordableArchiveCmd("file"),
		newRecordableRestoreCmd("file"),
	)

	return cmd
}

// NewVaultsCmd creates the vaults/folders command alias.
func NewVaultsCmd() *cobra.Command {
	cmd := NewFilesCmd()
	cmd.Use = "vaults"
	cmd.Aliases = []string{"vault", "folders"}
	cmd.Short = "Manage folders (alias for files)"
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

// NewUploadsCmd creates the top-level uploads command.
func NewUploadsCmd() *cobra.Command {
	var project string
	var vaultID string

	cmd := &cobra.Command{
		Use:   "uploads",
		Short: "Manage uploaded files",
		Long:  "List, show, and upload files in a project's Docs & Files area.",
	}

	cmd.PersistentFlags().StringVarP(&project, "project", "p", "", "Project ID or name")
	cmd.PersistentFlags().StringVar(&project, "in", "", "Project ID (alias for --project)")
	cmd.PersistentFlags().StringVar(&vaultID, "vault", "", "Folder ID (default: root)")
	cmd.PersistentFlags().StringVar(&vaultID, "folder", "", "Folder ID (alias for --vault)")

	cmd.AddCommand(
		newUploadsListCmd(&project, &vaultID),
		newUploadsCreateCmd(&project, &vaultID),
		newFilesShowCmd(&project),
	)

	return cmd
}

func newFilesListCmd(project, vaultID *string) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all items in a folder",
		Long:  "List all folders, documents, and uploads in a folder.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runFilesList(cmd, *project, *vaultID)
		},
	}
}

func runFilesList(cmd *cobra.Command, project, vaultID string) error {
	app := appctx.FromContext(cmd.Context())

	// Resolve account (enables interactive prompt if needed)
	if err := ensureAccount(cmd, app); err != nil {
		return err
	}

	// Resolve project from CLI flags and config, with interactive fallback
	projectID := project
	if projectID == "" {
		projectID = app.Flags.Project
	}
	if projectID == "" {
		projectID = app.Config.ProjectID
	}

	// If no project specified, try interactive resolution
	if projectID == "" {
		if err := ensureProject(cmd, app); err != nil {
			return err
		}
		projectID = app.Config.ProjectID
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

	vaultIDNum, err := strconv.ParseInt(resolvedVaultID, 10, 64)
	if err != nil {
		return output.ErrUsage("Invalid folder ID")
	}

	// Validate vault exists
	if _, err := app.Account().Vaults().Get(cmd.Context(), vaultIDNum); err != nil {
		return convertSDKError(err)
	}

	// Get folders (subvaults) using SDK
	var folders []basecamp.Vault
	foldersResult, err := app.Account().Vaults().List(cmd.Context(), vaultIDNum, nil)
	if err != nil {
		folders = []basecamp.Vault{} // Best-effort
	} else {
		folders = foldersResult.Vaults
	}

	// Get uploads using SDK
	var uploads []basecamp.Upload
	uploadsResult, err := app.Account().Uploads().List(cmd.Context(), vaultIDNum, nil)
	if err != nil {
		uploads = []basecamp.Upload{} // Best-effort
	} else {
		uploads = uploadsResult.Uploads
	}

	// Get documents using SDK
	var documents []basecamp.Document
	documentsResult, err := app.Account().Documents().List(cmd.Context(), vaultIDNum, nil)
	if err != nil {
		documents = []basecamp.Document{} // Best-effort
	} else {
		documents = documentsResult.Documents
	}

	// Slim output to id, name, type, size
	type fileListItem struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
		Type string `json:"type"`
		Size string `json:"size,omitempty"`
	}
	var items []fileListItem
	for _, f := range folders {
		items = append(items, fileListItem{ID: f.ID, Name: f.Title, Type: "Folder"})
	}
	for _, u := range uploads {
		items = append(items, fileListItem{ID: u.ID, Name: u.Title, Type: "Upload", Size: humanSize(u.ByteSize)})
	}
	for _, d := range documents {
		items = append(items, fileListItem{ID: d.ID, Name: d.Title, Type: "Document"})
	}

	summary := fmt.Sprintf("%d folders, %d files, %d documents", len(folders), len(uploads), len(documents))

	respOpts := []output.ResponseOption{
		output.WithSummary(summary),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "show",
				Cmd:         fmt.Sprintf("basecamp files show <id> --in %s", resolvedProjectID),
				Description: "Show item details",
			},
			output.Breadcrumb{
				Action:      "folder",
				Cmd:         fmt.Sprintf("basecamp files folder create <name> --in %s", resolvedProjectID),
				Description: "Create folder",
			},
			output.Breadcrumb{
				Action:      "doc",
				Cmd:         fmt.Sprintf("basecamp files doc create <title> --in %s", resolvedProjectID),
				Description: "Create document",
			},
		),
	}

	// Add notice for large result sets pointing to subcommands with pagination
	total := len(folders) + len(uploads) + len(documents)
	if total > 50 {
		respOpts = append(respOpts, output.WithNotice(
			"For pagination control, use: basecamp files folders, basecamp files uploads, or basecamp files documents",
		))
	}

	return app.OK(items, respOpts...)
}

func newFoldersCmd(project, vaultID *string) *cobra.Command {
	var limit int
	var page int
	var all bool

	cmd := &cobra.Command{
		Use:     "folders",
		Aliases: []string{"folder", "vaults", "vault"},
		Short:   "Manage folders",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runFoldersList(cmd, *project, *vaultID, limit, page, all)
		},
	}

	cmd.Flags().IntVarP(&limit, "limit", "n", 0, "Maximum number of folders to fetch (0 = all)")
	cmd.Flags().BoolVar(&all, "all", false, "Fetch all folders (no limit)")
	cmd.Flags().IntVar(&page, "page", 0, "Fetch a single page (use --all for everything)")

	cmd.AddCommand(
		newFoldersListCmd(project, vaultID),
		newFoldersCreateCmd(project, vaultID),
	)

	return cmd
}

func newFoldersListCmd(project, vaultID *string) *cobra.Command {
	var limit int
	var page int
	var all bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List folders in a folder",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runFoldersList(cmd, *project, *vaultID, limit, page, all)
		},
	}

	cmd.Flags().IntVarP(&limit, "limit", "n", 0, "Maximum number of folders to fetch (0 = all)")
	cmd.Flags().BoolVar(&all, "all", false, "Fetch all folders (no limit)")
	cmd.Flags().IntVar(&page, "page", 0, "Fetch a single page (use --all for everything)")

	return cmd
}

func runFoldersList(cmd *cobra.Command, project, vaultID string, limit, page int, all bool) error {
	app := appctx.FromContext(cmd.Context())

	// Validate flag combinations
	if all && limit > 0 {
		return output.ErrUsage("--all and --limit are mutually exclusive")
	}
	if page > 0 && (all || limit > 0) {
		return output.ErrUsage("--page cannot be combined with --all or --limit")
	}
	if page > 1 {
		return output.ErrUsage("only --page 1 is supported; use --all to fetch everything")
	}

	if err := ensureAccount(cmd, app); err != nil {
		return err
	}

	// Resolve project, with interactive fallback
	projectID := project
	if projectID == "" {
		projectID = app.Flags.Project
	}
	if projectID == "" {
		projectID = app.Config.ProjectID
	}
	if projectID == "" {
		if err := ensureProject(cmd, app); err != nil {
			return err
		}
		projectID = app.Config.ProjectID
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

	vaultIDNum, err := strconv.ParseInt(resolvedVaultID, 10, 64)
	if err != nil {
		return output.ErrUsage("Invalid folder ID")
	}

	// Build pagination options
	opts := &basecamp.VaultListOptions{}
	if all {
		opts.Limit = -1 // SDK treats -1 as "fetch all"
	} else if limit > 0 {
		opts.Limit = limit
	}
	if page > 0 {
		opts.Page = page
	}

	// Get folders using SDK
	foldersResult, err := app.Account().Vaults().List(cmd.Context(), vaultIDNum, opts)
	if err != nil {
		return convertSDKError(err)
	}
	folders := foldersResult.Vaults

	return app.OK(folders,
		output.WithSummary(fmt.Sprintf("%d folders", len(folders))),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "create",
				Cmd:         fmt.Sprintf("basecamp files folder create <name> --in %s", resolvedProjectID),
				Description: "Create folder",
			},
			output.Breadcrumb{
				Action:      "list",
				Cmd:         fmt.Sprintf("basecamp files --vault <id> --in %s", resolvedProjectID),
				Description: "List folder contents",
			},
		),
	)
}

func newFoldersCreateCmd(project, vaultID *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a new folder",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Show help when invoked with no arguments
			if len(args) == 0 {
				return missingArg(cmd, "<name>")
			}

			name := args[0]

			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// Resolve project, with interactive fallback
			projectID := *project
			if projectID == "" {
				projectID = app.Flags.Project
			}
			if projectID == "" {
				projectID = app.Config.ProjectID
			}
			if projectID == "" {
				if err := ensureProject(cmd, app); err != nil {
					return err
				}
				projectID = app.Config.ProjectID
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

			vaultIDNum, err := strconv.ParseInt(resolvedVaultID, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid folder ID")
			}

			// Create folder using SDK
			req := &basecamp.CreateVaultRequest{
				Title: name,
			}

			folder, err := app.Account().Vaults().Create(cmd.Context(), vaultIDNum, req)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(folder,
				output.WithSummary(fmt.Sprintf("Created folder #%d: %s", folder.ID, name)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "list",
						Cmd:         fmt.Sprintf("basecamp files --vault %d --in %s", folder.ID, resolvedProjectID),
						Description: "List folder contents",
					},
				),
			)
		},
	}

	return cmd
}

func newUploadsCmd(project, vaultID *string) *cobra.Command {
	var limit int
	var page int
	var all bool

	cmd := &cobra.Command{
		Use:     "uploads",
		Aliases: []string{"upload"},
		Short:   "Manage uploaded files",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runUploadsList(cmd, *project, *vaultID, limit, page, all)
		},
	}

	cmd.Flags().IntVarP(&limit, "limit", "n", 0, "Maximum number of files to fetch (0 = all)")
	cmd.Flags().BoolVar(&all, "all", false, "Fetch all files (no limit)")
	cmd.Flags().IntVar(&page, "page", 0, "Fetch a single page (use --all for everything)")

	cmd.AddCommand(
		newUploadsListCmd(project, vaultID),
		newUploadsCreateCmd(project, vaultID),
	)

	return cmd
}

func newUploadsListCmd(project, vaultID *string) *cobra.Command {
	var limit int
	var page int
	var all bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List uploaded files in a folder",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runUploadsList(cmd, *project, *vaultID, limit, page, all)
		},
	}

	cmd.Flags().IntVarP(&limit, "limit", "n", 0, "Maximum number of files to fetch (0 = all)")
	cmd.Flags().BoolVar(&all, "all", false, "Fetch all files (no limit)")
	cmd.Flags().IntVar(&page, "page", 0, "Fetch a single page (use --all for everything)")

	return cmd
}

func runUploadsList(cmd *cobra.Command, project, vaultID string, limit, page int, all bool) error {
	app := appctx.FromContext(cmd.Context())

	// Validate flag combinations
	if all && limit > 0 {
		return output.ErrUsage("--all and --limit are mutually exclusive")
	}
	if page > 0 && (all || limit > 0) {
		return output.ErrUsage("--page cannot be combined with --all or --limit")
	}
	if page > 1 {
		return output.ErrUsage("only --page 1 is supported; use --all to fetch everything")
	}

	if err := ensureAccount(cmd, app); err != nil {
		return err
	}

	// Resolve project, with interactive fallback
	projectID := project
	if projectID == "" {
		projectID = app.Flags.Project
	}
	if projectID == "" {
		projectID = app.Config.ProjectID
	}
	if projectID == "" {
		if err := ensureProject(cmd, app); err != nil {
			return err
		}
		projectID = app.Config.ProjectID
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

	vaultIDNum, err := strconv.ParseInt(resolvedVaultID, 10, 64)
	if err != nil {
		return output.ErrUsage("Invalid folder ID")
	}

	// Build pagination options
	opts := &basecamp.UploadListOptions{}
	if all {
		opts.Limit = -1 // SDK treats -1 as "fetch all"
	} else if limit > 0 {
		opts.Limit = limit
	}
	if page > 0 {
		opts.Page = page
	}

	// Get uploads using SDK
	uploadsResult, err := app.Account().Uploads().List(cmd.Context(), vaultIDNum, opts)
	if err != nil {
		return convertSDKError(err)
	}
	uploads := uploadsResult.Uploads

	return app.OK(uploads,
		output.WithSummary(fmt.Sprintf("%d files", len(uploads))),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "show",
				Cmd:         fmt.Sprintf("basecamp files show <id> --in %s", resolvedProjectID),
				Description: "Show file details",
			},
		),
	)
}

func newUploadsCreateCmd(project, vaultID *string) *cobra.Command {
	var description string

	cmd := &cobra.Command{
		Use:   "create <file>",
		Short: "Upload a file to Docs & Files",
		Long: `Upload a file to a project's Docs & Files area.

Two-step process: the file is first uploaded as an attachment, then created
as an upload in the target folder (vault).`,
		Example: `  basecamp uploads create ./report.pdf --in my-project
  basecamp uploads create ./photo.png --folder 123 --description "Site photo"`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runUploadFile(cmd, *project, *vaultID, args[0], description)
		},
	}

	cmd.Flags().StringVar(&description, "description", "", "Upload description (Markdown)")

	return cmd
}

// NewUploadCmd creates the top-level 'upload' shortcut command.
func NewUploadCmd() *cobra.Command {
	var project string
	var vaultID string
	var description string

	cmd := &cobra.Command{
		Use:   "upload <file>",
		Short: "Upload a file to Docs & Files",
		Long: `Upload a file to a project's Docs & Files area.

Shortcut for 'basecamp uploads create'. The file is uploaded as an
attachment and then created as an upload in the target folder.`,
		Example: `  basecamp upload ./report.pdf --in my-project
  basecamp upload ./photo.png --folder 123 --description "Site photo"`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runUploadFile(cmd, project, vaultID, args[0], description)
		},
	}

	cmd.Flags().StringVarP(&project, "project", "p", "", "Project ID or name")
	cmd.Flags().StringVar(&project, "in", "", "Project ID (alias for --project)")
	cmd.Flags().StringVar(&vaultID, "vault", "", "Folder ID (default: root)")
	cmd.Flags().StringVar(&vaultID, "folder", "", "Folder ID (alias for --vault)")
	cmd.Flags().StringVar(&description, "description", "", "Upload description (Markdown)")

	return cmd
}

func runUploadFile(cmd *cobra.Command, project, vaultID, filePath, description string) error {
	app := appctx.FromContext(cmd.Context())

	if err := ensureAccount(cmd, app); err != nil {
		return err
	}

	// Normalize drag/paste paths and validate
	filePath = richtext.NormalizeDragPath(filePath)
	if err := richtext.ValidateFile(filePath); err != nil {
		return fmt.Errorf("%s: %w", filePath, err)
	}

	// Resolve project, with interactive fallback
	projectID := project
	if projectID == "" {
		projectID = app.Flags.Project
	}
	if projectID == "" {
		projectID = app.Config.ProjectID
	}
	if projectID == "" {
		if err := ensureProject(cmd, app); err != nil {
			return err
		}
		projectID = app.Config.ProjectID
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

	vaultIDNum, err := strconv.ParseInt(resolvedVaultID, 10, 64)
	if err != nil {
		return output.ErrUsage("Invalid folder ID")
	}

	// Step 1: Upload attachment
	contentType := richtext.DetectMIME(filePath)
	filename := filepath.Base(filePath)

	f, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("%s: %w", filePath, err)
	}
	defer f.Close()

	resp, err := app.Account().Attachments().Create(cmd.Context(), filename, contentType, f)
	if err != nil {
		return convertSDKError(err)
	}

	// Step 2: Create upload in vault
	req := &basecamp.CreateUploadRequest{
		AttachableSGID: resp.AttachableSGID,
		BaseName:       strings.TrimSuffix(filename, filepath.Ext(filename)),
	}
	if description != "" {
		descHTML := richtext.MarkdownToHTML(description)
		descHTML, resolveErr := resolveLocalImages(cmd, app, descHTML)
		if resolveErr != nil {
			return resolveErr
		}
		req.Description = descHTML
	}

	upload, err := app.Account().Uploads().Create(cmd.Context(), vaultIDNum, req)
	if err != nil {
		return convertSDKError(err)
	}

	// Derive breadcrumb prefix from the command path so it matches the
	// invocation (e.g. "basecamp files uploads" vs "basecamp uploads").
	uploadsPath := cmd.Parent().CommandPath()
	if cmd.Parent().Parent() == nil {
		// Shortcut command (e.g. "basecamp upload") sits directly under root;
		// point breadcrumbs at the canonical uploads command group.
		uploadsPath = "basecamp uploads"
	}

	return app.OK(upload,
		output.WithSummary(fmt.Sprintf("Uploaded %s (#%d)", filename, upload.ID)),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "show",
				Cmd:         fmt.Sprintf("%s show %d --in %s", uploadsPath, upload.ID, resolvedProjectID),
				Description: "View upload",
			},
			output.Breadcrumb{
				Action:      "list",
				Cmd:         fmt.Sprintf("%s --in %s", uploadsPath, resolvedProjectID),
				Description: "List uploads",
			},
		),
	)
}

func newDocsCmd(project, vaultID *string) *cobra.Command {
	var limit int
	var page int
	var all bool

	cmd := &cobra.Command{
		Use:     "documents",
		Aliases: []string{"document", "doc"},
		Short:   "Manage documents",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDocsList(cmd, *project, *vaultID, limit, page, all)
		},
	}

	cmd.Flags().IntVarP(&limit, "limit", "n", 0, "Maximum number of documents to fetch (0 = all)")
	cmd.Flags().BoolVar(&all, "all", false, "Fetch all documents (no limit)")
	cmd.Flags().IntVar(&page, "page", 0, "Fetch a single page (use --all for everything)")

	cmd.AddCommand(
		newDocsListCmd(project, vaultID),
		newDocsCreateCmd(project, vaultID),
	)

	return cmd
}

func newDocsListCmd(project, vaultID *string) *cobra.Command {
	var limit int
	var page int
	var all bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List documents in a folder",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDocsList(cmd, *project, *vaultID, limit, page, all)
		},
	}

	cmd.Flags().IntVarP(&limit, "limit", "n", 0, "Maximum number of documents to fetch (0 = all)")
	cmd.Flags().BoolVar(&all, "all", false, "Fetch all documents (no limit)")
	cmd.Flags().IntVar(&page, "page", 0, "Fetch a single page (use --all for everything)")

	return cmd
}

func runDocsList(cmd *cobra.Command, project, vaultID string, limit, page int, all bool) error {
	app := appctx.FromContext(cmd.Context())

	// Validate flag combinations
	if all && limit > 0 {
		return output.ErrUsage("--all and --limit are mutually exclusive")
	}
	if page > 0 && (all || limit > 0) {
		return output.ErrUsage("--page cannot be combined with --all or --limit")
	}
	if page > 1 {
		return output.ErrUsage("only --page 1 is supported; use --all to fetch everything")
	}

	if err := ensureAccount(cmd, app); err != nil {
		return err
	}

	// Resolve project, with interactive fallback
	projectID := project
	if projectID == "" {
		projectID = app.Flags.Project
	}
	if projectID == "" {
		projectID = app.Config.ProjectID
	}
	if projectID == "" {
		if err := ensureProject(cmd, app); err != nil {
			return err
		}
		projectID = app.Config.ProjectID
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

	vaultIDNum, err := strconv.ParseInt(resolvedVaultID, 10, 64)
	if err != nil {
		return output.ErrUsage("Invalid folder ID")
	}

	// Build pagination options
	opts := &basecamp.DocumentListOptions{}
	if all {
		opts.Limit = -1 // SDK treats -1 as "fetch all"
	} else if limit > 0 {
		opts.Limit = limit
	}
	if page > 0 {
		opts.Page = page
	}

	// Get documents using SDK
	documentsResult, err := app.Account().Documents().List(cmd.Context(), vaultIDNum, opts)
	if err != nil {
		return convertSDKError(err)
	}
	documents := documentsResult.Documents

	return app.OK(documents,
		output.WithSummary(fmt.Sprintf("%d documents", len(documents))),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "create",
				Cmd:         fmt.Sprintf("basecamp files doc create <title> --in %s", resolvedProjectID),
				Description: "Create document",
			},
			output.Breadcrumb{
				Action:      "show",
				Cmd:         fmt.Sprintf("basecamp files show <id> --in %s", resolvedProjectID),
				Description: "Show document",
			},
		),
	)
}

func newDocsCreateCmd(project, vaultID *string) *cobra.Command {
	var draft bool
	var subscribe string
	var noSubscribe bool
	var attachFiles []string

	cmd := &cobra.Command{
		Use:   "create <title> [content]",
		Short: "Create a new document",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Show help when invoked with no arguments
			if len(args) == 0 {
				return missingArg(cmd, "<title>")
			}

			title := args[0]

			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}
			content := ""
			if len(args) > 1 {
				content = args[1]
			}

			// Resolve subscription flags before project (fail fast on bad input)
			subs, err := applySubscribeFlags(cmd.Context(), app.Names, subscribe, cmd.Flags().Changed("subscribe"), noSubscribe)
			if err != nil {
				return err
			}

			// Resolve project, with interactive fallback
			projectID := *project
			if projectID == "" {
				projectID = app.Flags.Project
			}
			if projectID == "" {
				projectID = app.Config.ProjectID
			}
			if projectID == "" {
				if err := ensureProject(cmd, app); err != nil {
					return err
				}
				projectID = app.Config.ProjectID
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

			vaultIDNum, err := strconv.ParseInt(resolvedVaultID, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid folder ID")
			}

			// Create document using SDK
			// Convert Markdown content to HTML
			html := richtext.MarkdownToHTML(content)

			// Resolve inline images
			html, imgErr := resolveLocalImages(cmd, app, html)
			if imgErr != nil {
				return imgErr
			}

			// Upload explicit --attach files and embed
			if len(attachFiles) > 0 {
				refs, attachErr := uploadAttachments(cmd, app, attachFiles)
				if attachErr != nil {
					return attachErr
				}
				html = richtext.EmbedAttachments(html, refs)
			}

			req := &basecamp.CreateDocumentRequest{
				Title:         title,
				Content:       html,
				Subscriptions: subs,
			}
			if draft {
				req.Status = "drafted"
			} else {
				req.Status = "active"
			}

			doc, err := app.Account().Documents().Create(cmd.Context(), vaultIDNum, req)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(doc,
				output.WithSummary(fmt.Sprintf("Created document #%d: %s", doc.ID, title)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "show",
						Cmd:         fmt.Sprintf("basecamp files show %d --in %s", doc.ID, resolvedProjectID),
						Description: "View document",
					},
					output.Breadcrumb{
						Action:      "update",
						Cmd:         fmt.Sprintf("basecamp files update %d <title> --in %s", doc.ID, resolvedProjectID),
						Description: "Update document",
					},
				),
			)
		},
	}

	cmd.Flags().BoolVar(&draft, "draft", false, "Create as draft (default: published)")
	cmd.Flags().StringVar(&subscribe, "subscribe", "", "Subscribe specific people (comma-separated names, emails, IDs, or \"me\")")
	cmd.Flags().BoolVar(&noSubscribe, "no-subscribe", false, "Don't subscribe anyone else (silent, no notifications)")
	cmd.Flags().StringArrayVar(&attachFiles, "attach", nil, "Attach file (repeatable)")

	return cmd
}

func newFilesShowCmd(project *string) *cobra.Command {
	var itemType string
	var dlDir *string
	var cf *commentFlags

	cmd := &cobra.Command{
		Use:   "show <id|url>",
		Short: "Show item details",
		Long: `Show details for a vault, document, or upload.

You can pass either an item ID or a Basecamp URL:
  basecamp files show 789 --in my-project
  basecamp files show https://3.basecamp.com/123/buckets/456/documents/789`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// Extract ID and project from URL if provided
			itemIDStr, urlProjectID := extractWithProject(args[0])

			itemID, err := strconv.ParseInt(itemIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid item ID")
			}

			// Resolve project - use URL > flag > config, with interactive fallback
			projectID := *project
			if projectID == "" && urlProjectID != "" {
				projectID = urlProjectID
			}
			if projectID == "" {
				projectID = app.Flags.Project
			}
			if projectID == "" {
				projectID = app.Config.ProjectID
			}
			if projectID == "" {
				if err := ensureProject(cmd, app); err != nil {
					return err
				}
				projectID = app.Config.ProjectID
			}

			resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
			if err != nil {
				return err
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
				vault, err := app.Account().Vaults().Get(cmd.Context(), itemID)
				if err == nil {
					result = vault
					detectedType = "vault"
					title = vault.Title
				} else {
					firstErr = err
					// Try upload
					upload, err := app.Account().Uploads().Get(cmd.Context(), itemID)
					if err == nil {
						result = upload
						detectedType = "upload"
						title = upload.Filename
						if title == "" {
							title = upload.Title
						}
					} else {
						// Try document
						doc, err := app.Account().Documents().Get(cmd.Context(), itemID)
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
					vault, err := app.Account().Vaults().Get(cmd.Context(), itemID)
					if err != nil {
						return convertSDKError(err)
					}
					result = vault
					detectedType = "vault"
					title = vault.Title
				case "upload", "file":
					upload, err := app.Account().Uploads().Get(cmd.Context(), itemID)
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
					doc, err := app.Account().Documents().Get(cmd.Context(), itemID)
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
					Cmd:         fmt.Sprintf("basecamp files update %s --in %s", itemIDStr, resolvedProjectID),
					Description: "Update item",
				},
			}

			if detectedType == "vault" {
				breadcrumbs = append(breadcrumbs, output.Breadcrumb{
					Action:      "contents",
					Cmd:         fmt.Sprintf("basecamp files --vault %s --in %s", itemIDStr, resolvedProjectID),
					Description: "List contents",
				})
			}

			enrichment := fetchCommentsForRecording(cmd.Context(), app, itemIDStr, cf)

			opts := []output.ResponseOption{
				output.WithSummary(summary),
				output.WithBreadcrumbs(breadcrumbs...),
			}

			data := result
			attachmentNotice := ""
			if detectedType == "document" {
				doc, ok := result.(*basecamp.Document)
				if ok {
					attachments := downloadableAttachments(richtext.ParseAttachments(doc.Content))
					if len(attachments) > 0 {
						dl := runDownloadAttachments(cmd, app, attachments, dlDir)
						var dlResults []attachmentResult
						if dl != nil {
							dlResults = dl.Results
						}
						data = withAttachmentMeta(doc, "content", attachments, dlResults)
						attachmentNotice = fmt.Sprintf("%d attachment(s) — download: basecamp attachments download %s",
							len(attachments), itemIDStr)
						if dl != nil && dl.Notice != "" {
							attachmentNotice += "; " + dl.Notice
						}
						opts = append(opts,
							output.WithBreadcrumbs(attachmentBreadcrumb(itemIDStr, len(attachments))),
						)
					}
				}
			}

			data, extraOpts := enrichment.apply(data, attachmentNotice)
			opts = append(opts, extraOpts...)

			return app.OK(data, opts...)
		},
	}

	cmd.Flags().StringVarP(&itemType, "type", "t", "", "Item type (vault, upload, document)")
	dlDir = addDownloadAttachmentsFlag(cmd)
	cf = addCommentFlags(cmd, false)

	return cmd
}

func newFilesUpdateCmd(project *string) *cobra.Command {
	var title string
	var content string
	var itemType string

	cmd := &cobra.Command{
		Use:   "update <id|url>",
		Short: "Update a document, vault, or upload",
		Long: `Update a document, vault, or upload.

You can pass either an item ID or a Basecamp URL:
  basecamp files update 789 --title "new title" --in my-project
  basecamp files update 789 --content "new content" --in my-project`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(title) == "" && strings.TrimSpace(content) == "" && itemType == "" {
				return noChanges(cmd)
			}

			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// Extract ID and project from URL if provided
			itemIDStr, urlProjectID := extractWithProject(args[0])

			itemID, err := strconv.ParseInt(itemIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid item ID")
			}

			// Resolve project - use URL > flag > config, with interactive fallback
			projectID := *project
			if projectID == "" && urlProjectID != "" {
				projectID = urlProjectID
			}
			if projectID == "" {
				projectID = app.Flags.Project
			}
			if projectID == "" {
				projectID = app.Config.ProjectID
			}
			if projectID == "" {
				if err := ensureProject(cmd, app); err != nil {
					return err
				}
				projectID = app.Config.ProjectID
			}

			resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
			if err != nil {
				return err
			}

			// Auto-detect type if not specified
			var result any
			var detectedType string

			if itemType != "" {
				switch itemType {
				case "vault", "folder":
					req := &basecamp.UpdateVaultRequest{Title: title}
					vault, err := app.Account().Vaults().Update(cmd.Context(), itemID, req)
					if err != nil {
						return convertSDKError(err)
					}
					result = vault
					detectedType = "vault"
				case "document", "doc":
					docHTML := richtext.MarkdownToHTML(content)
					docHTML, err = resolveLocalImages(cmd, app, docHTML)
					if err != nil {
						return err
					}
					req := &basecamp.UpdateDocumentRequest{Title: title, Content: docHTML}
					doc, err := app.Account().Documents().Update(cmd.Context(), itemID, req)
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
					upload, err := app.Account().Uploads().Update(cmd.Context(), itemID, req)
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
				_, err := app.Account().Documents().Get(cmd.Context(), itemID)
				if err == nil {
					docHTML := richtext.MarkdownToHTML(content)
					docHTML, resolveErr := resolveLocalImages(cmd, app, docHTML)
					if resolveErr != nil {
						return resolveErr
					}
					req := &basecamp.UpdateDocumentRequest{Title: title, Content: docHTML}
					doc, err := app.Account().Documents().Update(cmd.Context(), itemID, req)
					if err != nil {
						return convertSDKError(err)
					}
					result = doc
					detectedType = "document"
				} else {
					firstErr = err
					// Try vault
					_, err = app.Account().Vaults().Get(cmd.Context(), itemID)
					if err == nil {
						req := &basecamp.UpdateVaultRequest{Title: title}
						vault, err := app.Account().Vaults().Update(cmd.Context(), itemID, req)
						if err != nil {
							return convertSDKError(err)
						}
						result = vault
						detectedType = "vault"
					} else {
						// Try upload
						_, err = app.Account().Uploads().Get(cmd.Context(), itemID)
						if err == nil {
							req := &basecamp.UpdateUploadRequest{Description: content}
							if title != "" {
								req.BaseName = title
							}
							upload, err := app.Account().Uploads().Update(cmd.Context(), itemID, req)
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
						Cmd:         fmt.Sprintf("basecamp files show %s --in %s", itemIDStr, resolvedProjectID),
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

func newFilesDownloadCmd(project *string) *cobra.Command {
	var outDir string

	cmd := &cobra.Command{
		Use:   "download <upload-id|url>",
		Short: "Download an uploaded file",
		Long: `Download an uploaded file to the local filesystem.

You can pass either an upload ID, a Basecamp URL, or a storage URL:
  basecamp files download 789 --in my-project
  basecamp files download https://3.basecamp.com/123/buckets/456/uploads/789
  basecamp files download "https://storage.3.basecamp.com/123/blobs/abc/download/report.pdf"
  basecamp files download 789 --out ./downloads --in my-project
  basecamp files download 789 --out - --in my-project  # stream to stdout

Storage URLs (from attachments in rich text) are downloaded directly
via the API. No --in flag is needed for storage URLs.

Use --out - to stream the file to stdout (for piping to other commands).`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// Stdout streaming: --out -
			if outDir == "-" {
				if isStorageURL(args[0]) {
					result, err := app.Account().DownloadURL(cmd.Context(), args[0])
					if err != nil {
						return convertSDKError(err)
					}
					defer result.Body.Close()
					_, err = io.Copy(cmd.OutOrStdout(), result.Body)
					return err
				}
				// Upload ID path — resolve project, then stream
				uploadIDStr, urlProjectID := extractWithProject(args[0])
				uploadID, err := strconv.ParseInt(uploadIDStr, 10, 64)
				if err != nil {
					return output.ErrUsage("Invalid upload ID")
				}
				if _, err := resolveDownloadProject(cmd, app, urlProjectID, *project); err != nil {
					return err
				}
				result, err := app.Account().Uploads().Download(cmd.Context(), uploadID)
				if err != nil {
					return convertSDKError(err)
				}
				defer result.Body.Close()
				_, err = io.Copy(cmd.OutOrStdout(), result.Body)
				return err
			}

			// Storage URL path: download via SDK (handles URL rewriting, auth, redirects)
			if isStorageURL(args[0]) {
				result, err := app.Account().DownloadURL(cmd.Context(), args[0])
				if err != nil {
					return convertSDKError(err)
				}
				defer result.Body.Close()

				filename, outputPath, bytesWritten, err := writeDownloadToFile(result, outDir, result.Filename)
				if err != nil {
					return err
				}

				downloadResult := struct {
					URL      string `json:"url"`
					Filename string `json:"filename"`
					Path     string `json:"path"`
					ByteSize int64  `json:"byte_size"`
				}{
					URL:      args[0],
					Filename: filename,
					Path:     outputPath,
					ByteSize: bytesWritten,
				}

				return app.OK(downloadResult,
					output.WithSummary(fmt.Sprintf("Downloaded %s (%d bytes)", filename, bytesWritten)),
				)
			}

			// Upload ID path: two-step download via SDK
			uploadIDStr, urlProjectID := extractWithProject(args[0])

			uploadID, err := strconv.ParseInt(uploadIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid upload ID")
			}

			resolvedProjectID, err := resolveDownloadProject(cmd, app, urlProjectID, *project)
			if err != nil {
				return err
			}

			// Download the file
			result, err := app.Account().Uploads().Download(cmd.Context(), uploadID)
			if err != nil {
				return convertSDKError(err)
			}
			defer result.Body.Close()

			fallback := fmt.Sprintf("upload-%d", uploadID)
			filename, outputPath, bytesWritten, err := writeDownloadToFile(result, outDir, fallback)
			if err != nil {
				return err
			}

			// Build result for output
			downloadResult := struct {
				UploadID  int64  `json:"upload_id"`
				Filename  string `json:"filename"`
				Path      string `json:"path"`
				ByteSize  int64  `json:"byte_size"`
				ProjectID string `json:"project_id"`
			}{
				UploadID:  uploadID,
				Filename:  filename,
				Path:      outputPath,
				ByteSize:  bytesWritten,
				ProjectID: resolvedProjectID,
			}

			return app.OK(downloadResult,
				output.WithSummary(fmt.Sprintf("Downloaded %s (%d bytes)", filename, bytesWritten)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "show",
						Cmd:         fmt.Sprintf("basecamp files show %d --in %s", uploadID, resolvedProjectID),
						Description: "View upload details",
					},
				),
			)
		},
	}

	cmd.Flags().StringVarP(&outDir, "out", "o", "", "Output directory (default: current directory)")

	return cmd
}

// createFile creates a file for writing, creating parent directories if needed.
func createFile(path string) (*os.File, error) {
	// Create parent directories if they don't exist
	dir := filepath.Dir(path)
	if dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return nil, err
		}
	}
	return os.Create(path)
}

// copyFileContent copies from reader to writer and returns bytes written.
func copyFileContent(dst *os.File, src io.Reader) (int64, error) {
	return io.Copy(dst, src)
}

// writeDownloadToFile writes a DownloadResult to a file in outDir, using
// result.Filename if available, otherwise fallbackName. Returns the sanitized
// filename, output path, and bytes written.
func writeDownloadToFile(result *basecamp.DownloadResult, outDir, fallbackName string) (filename, outputPath string, written int64, err error) {
	dir := outDir
	if dir == "" {
		dir = "."
	}

	filename = result.Filename
	if filename == "" {
		filename = fallbackName
	}
	filename = filepath.Base(filename)
	if filename == "." || filename == "" {
		filename = "download"
	}
	outputPath = filepath.Join(dir, filename)

	// Verify the resolved path is within dir to prevent traversal attacks
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return "", "", 0, fmt.Errorf("failed to resolve output directory: %w", err)
	}
	absPath, err := filepath.Abs(outputPath)
	if err != nil {
		return "", "", 0, fmt.Errorf("failed to resolve output path: %w", err)
	}
	if !strings.HasPrefix(absPath, absDir+string(filepath.Separator)) {
		return "", "", 0, output.ErrUsage("Invalid filename: path traversal detected")
	}

	outFile, err := createFile(outputPath)
	if err != nil {
		return "", "", 0, fmt.Errorf("failed to create output file: %w", err)
	}
	defer outFile.Close()

	written, err = copyFileContent(outFile, result.Body)
	if err != nil {
		return "", "", 0, fmt.Errorf("failed to write file: %w", err)
	}
	return filename, outputPath, written, nil
}

// resolveDownloadProject resolves a project ID for file download commands
// using the standard precedence: URL-extracted > --project flag > global flag
// > config > interactive fallback. Returns the resolved project ID.
func resolveDownloadProject(cmd *cobra.Command, app *appctx.App, urlProjectID, flagProject string) (string, error) {
	projectID := urlProjectID
	if projectID == "" {
		projectID = flagProject
	}
	if projectID == "" {
		projectID = app.Flags.Project
	}
	if projectID == "" {
		projectID = app.Config.ProjectID
	}
	if projectID == "" {
		if err := ensureProject(cmd, app); err != nil {
			return "", err
		}
		projectID = app.Config.ProjectID
	}
	resolved, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
	if err != nil {
		return "", err
	}
	return resolved, nil
}

// isStorageURL returns true if the argument looks like a Basecamp storage blob URL.
func isStorageURL(arg string) bool {
	u, err := url.Parse(arg)
	if err != nil {
		return false
	}
	return u.Scheme == "https" &&
		strings.HasSuffix(u.Hostname(), ".basecamp.com") &&
		strings.HasPrefix(u.Hostname(), "storage.") &&
		strings.Contains(u.Path, "/blobs/") &&
		strings.Contains(u.Path, "/download/")
}

// humanSize formats bytes as a human-readable string (e.g., "9.1mb").
func humanSize(bytes int64) string {
	switch {
	case bytes >= 1_000_000_000:
		return fmt.Sprintf("%.1fgb", float64(bytes)/1_000_000_000)
	case bytes >= 1_000_000:
		return fmt.Sprintf("%.1fmb", float64(bytes)/1_000_000)
	case bytes >= 1_000:
		return fmt.Sprintf("%.1fkb", float64(bytes)/1_000)
	default:
		return fmt.Sprintf("%db", bytes)
	}
}

// getVaultID retrieves the root vault ID from a project's dock, handling multi-dock projects.
func getVaultID(cmd *cobra.Command, app *appctx.App, projectID string) (string, error) {
	return getDockToolID(cmd.Context(), app, projectID, "vault", "", "vault", "vault")
}
