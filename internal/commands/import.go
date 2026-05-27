package commands

import (
	"context"
	"fmt"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/importer"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// NewImportCmd creates the import command group.
func NewImportCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "import",
		Short: "Inspect and import external CSV data",
		Long:  "Inspect external CSV data and produce deterministic import artifacts for Basecamp.",
	}

	cmd.AddCommand(
		newImportInspectCmd(),
		newImportCompileCmd(),
		newImportPlanCmd(),
		newImportStatusCmd(),
		newImportRepairCmd(),
		newImportFollowupCmd(),
		newImportPreflightCmd(),
		newImportExecuteCmd(),
	)
	return cmd
}

type importWriteClient struct {
	cmd       *cobra.Command
	app       *appctx.App
	todosetID string
}

type importPreflightClient struct {
	cmd *cobra.Command
	app *appctx.App
}

func (c *importPreflightClient) ExistingTodos(ctx context.Context, todolistID int64) ([]importer.ExistingTodo, error) {
	result, err := c.app.Account().Todos().List(ctx, todolistID, &basecamp.TodoListOptions{Limit: -1})
	if err != nil {
		return nil, convertSDKError(err)
	}
	out := make([]importer.ExistingTodo, 0, len(result.Todos))
	for _, todo := range result.Todos {
		title := todo.Title
		if title == "" {
			title = todo.Content
		}
		out = append(out, importer.ExistingTodo{ID: todo.ID, Title: title})
	}
	return out, nil
}

func (c *importPreflightClient) ExistingTodolists(ctx context.Context, projectID int64) ([]importer.ExistingTodolist, error) {
	resolved, err := c.app.Resolve().Todoset(ctx, strconv.FormatInt(projectID, 10), "")
	if err != nil {
		return nil, err
	}
	todosetID, err := strconv.ParseInt(resolved.ToolID, 10, 64)
	if err != nil {
		return nil, output.ErrUsage("Invalid todoset ID")
	}
	result, err := c.app.Account().Todolists().List(ctx, todosetID, &basecamp.TodolistListOptions{})
	if err != nil {
		return nil, convertSDKError(err)
	}
	out := make([]importer.ExistingTodolist, 0, len(result.Todolists))
	for _, list := range result.Todolists {
		name := list.Title
		if name == "" {
			name = list.Name
		}
		out = append(out, importer.ExistingTodolist{ID: list.ID, Name: name})
	}
	return out, nil
}

func (c *importWriteClient) CreateProject(ctx context.Context, name string) (int64, error) {
	project, err := c.app.Account().Projects().Create(ctx, &basecamp.CreateProjectRequest{Name: name})
	if err != nil {
		return 0, convertSDKError(err)
	}
	return project.ID, nil
}

func (c *importWriteClient) CreateTodolist(ctx context.Context, projectID int64, name string) (int64, error) {
	if c.todosetID == "" {
		resolved, err := c.app.Resolve().Todoset(ctx, strconv.FormatInt(projectID, 10), "")
		if err != nil {
			return 0, err
		}
		c.todosetID = resolved.ToolID
	}
	todosetID, err := strconv.ParseInt(c.todosetID, 10, 64)
	if err != nil {
		return 0, output.ErrUsage("Invalid todoset ID")
	}
	todolist, err := c.app.Account().Todolists().Create(ctx, todosetID, &basecamp.CreateTodolistRequest{Name: name})
	if err != nil {
		return 0, convertSDKError(err)
	}
	return todolist.ID, nil
}

func (c *importWriteClient) CreateTodo(ctx context.Context, todolistID int64, todo importer.ExecutableTodo) (int64, error) {
	created, err := c.app.Account().Todos().Create(ctx, todolistID, &basecamp.CreateTodoRequest{Content: todo.Title, Description: todo.Description, DueOn: todo.DueOn})
	if err != nil {
		return 0, convertSDKError(err)
	}
	return created.ID, nil
}

func newImportStatusCmd() *cobra.Command {
	var artifactPath string

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show local Basecamp import artifact status",
		Long:  "Show local Basecamp import artifact status and execution ledger details without reading or writing Basecamp.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if artifactPath == "" {
				return output.ErrUsage("--artifact required")
			}
			result, err := importer.StatusArtifact(artifactPath)
			if err != nil {
				return err
			}
			app := appctx.FromContext(cmd.Context())
			if app == nil {
				fmt.Fprintln(cmd.OutOrStdout(), result.Status)
				return nil
			}
			return app.OK(result, output.WithSummary(fmt.Sprintf("Import artifact status: %s", result.Status)))
		},
	}

	cmd.Flags().StringVar(&artifactPath, "artifact", "", "Validated Basecamp import artifact directory")
	return cmd
}

func newImportRepairCmd() *cobra.Command {
	var artifactPath string

	cmd := &cobra.Command{
		Use:   "repair",
		Short: "Review a Basecamp import artifact for recovery",
		Long:  "Review local Basecamp import artifact execution records and summarize safe recovery state without reading or writing Basecamp.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if artifactPath == "" {
				return output.ErrUsage("--artifact required")
			}
			result, err := importer.RepairArtifact(artifactPath)
			if err != nil {
				return err
			}
			app := appctx.FromContext(cmd.Context())
			if app == nil {
				fmt.Fprintln(cmd.OutOrStdout(), result.Status)
				return nil
			}
			return app.OK(result, output.WithSummary(fmt.Sprintf("Import repair status: %s", result.Status)))
		},
	}

	cmd.Flags().StringVar(&artifactPath, "artifact", "", "Validated Basecamp import artifact directory")
	return cmd
}

func newImportFollowupCmd() *cobra.Command {
	var artifactPath string
	var outDir string
	var reviewed bool

	cmd := &cobra.Command{
		Use:   "followup",
		Short: "Create a reviewed follow-up import artifact",
		Long:  "Create a fresh local Basecamp import artifact from pending rows after reviewing a failed execution ledger.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if artifactPath == "" {
				return output.ErrUsage("--artifact required")
			}
			if outDir == "" {
				return output.ErrUsage("--out required")
			}
			if !reviewed {
				return output.ErrUsage("--reviewed required")
			}
			result, err := importer.CreateFollowupArtifact(artifactPath, outDir, importer.FollowupOptions{Reviewed: reviewed})
			if err != nil {
				return err
			}
			app := appctx.FromContext(cmd.Context())
			if app == nil {
				fmt.Fprintln(cmd.OutOrStdout(), result.Status)
				return nil
			}
			return app.OK(result, output.WithSummary(fmt.Sprintf("Compiled follow-up artifact with %d pending todos", result.Manifest.Counts.Todos)))
		},
	}

	cmd.Flags().StringVar(&artifactPath, "artifact", "", "Source Basecamp import artifact directory")
	cmd.Flags().StringVar(&outDir, "out", "", "Output directory for the follow-up import artifact")
	cmd.Flags().BoolVar(&reviewed, "reviewed", false, "Confirm Basecamp state and the repair summary have been reviewed")
	return cmd
}

func newImportPreflightCmd() *cobra.Command {
	var artifactPath string

	cmd := &cobra.Command{
		Use:   "preflight",
		Short: "Check a Basecamp import artifact before execution",
		Long:  "Check a validated Basecamp import artifact for execution readiness without creating Basecamp records.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if artifactPath == "" {
				return output.ErrUsage("--artifact required")
			}
			app := appctx.FromContext(cmd.Context())
			if app == nil {
				return fmt.Errorf("app not initialized")
			}
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}
			result, err := importer.PreflightArtifact(cmd.Context(), artifactPath, &importPreflightClient{cmd: cmd, app: app})
			if err != nil {
				return err
			}
			return app.OK(result, output.WithSummary(fmt.Sprintf("Import preflight %s", result.Status)))
		},
	}

	cmd.Flags().StringVar(&artifactPath, "artifact", "", "Validated Basecamp import artifact directory")
	return cmd
}

func newImportExecuteCmd() *cobra.Command {
	var artifactPath string
	var approved bool

	cmd := &cobra.Command{
		Use:   "execute",
		Short: "Execute a validated Basecamp import artifact",
		Long:  "Execute a validated Basecamp import artifact after explicit approval.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if artifactPath == "" {
				return output.ErrUsage("--artifact required")
			}
			if !approved {
				return output.ErrUsage("--approved required")
			}
			app := appctx.FromContext(cmd.Context())
			if app == nil {
				return fmt.Errorf("app not initialized")
			}
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}
			preflight, err := importer.PreflightArtifact(cmd.Context(), artifactPath, &importPreflightClient{cmd: cmd, app: app})
			if err != nil {
				return err
			}
			if preflight.Status == "blocked" {
				return output.ErrUsage(preflight.BlockedMessage())
			}
			result, err := importer.ExecuteArtifact(cmd.Context(), artifactPath, &importWriteClient{cmd: cmd, app: app}, importer.ExecuteOptions{Approved: approved})
			if err != nil {
				return err
			}
			return app.OK(result, output.WithSummary(fmt.Sprintf("Imported %d todos", result.Created.Todos)))
		},
	}

	cmd.Flags().StringVar(&artifactPath, "artifact", "", "Validated Basecamp import artifact directory")
	cmd.Flags().BoolVar(&approved, "approved", false, "Confirm that the planned import is approved for execution")
	return cmd
}

func newImportCompileCmd() *cobra.Command {
	var inspectionPath string
	var mappingPath string
	var destinationPath string
	var outDir string

	cmd := &cobra.Command{
		Use:   "compile",
		Short: "Compile a validated Basecamp import artifact",
		Long:  "Compile inspection, mapping, and destination JSON files into a validated Basecamp import CSV artifact.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if inspectionPath == "" {
				return output.ErrUsage("--inspection required")
			}
			if mappingPath == "" {
				return output.ErrUsage("--mapping required")
			}
			if destinationPath == "" {
				return output.ErrUsage("--destination required")
			}
			if outDir == "" {
				return output.ErrUsage("--out required")
			}

			inspection, err := importer.ReadInspectionFile(inspectionPath)
			if err != nil {
				return err
			}
			mapping, err := importer.ReadMappingFile(mappingPath)
			if err != nil {
				return err
			}
			destination, err := importer.ReadDestinationFile(destinationPath)
			if err != nil {
				return err
			}
			result, err := importer.CompileArtifact(inspection, mapping, destination, outDir)
			if err != nil {
				return err
			}

			app := appctx.FromContext(cmd.Context())
			if app == nil {
				fmt.Fprintln(cmd.OutOrStdout(), result.Status)
				return nil
			}
			return app.OK(result, output.WithSummary(fmt.Sprintf("Compiled import artifact with %d todos", result.Manifest.Counts.Todos)))
		},
	}

	cmd.Flags().StringVar(&inspectionPath, "inspection", "", "Inspection JSON file")
	cmd.Flags().StringVar(&mappingPath, "mapping", "", "Confirmed mapping JSON file")
	cmd.Flags().StringVar(&destinationPath, "destination", "", "Destination JSON file")
	cmd.Flags().StringVar(&outDir, "out", "", "Output directory for the Basecamp import artifact")
	return cmd
}

func newImportPlanCmd() *cobra.Command {
	var artifactPath string
	var inspectionPath string
	var mappingPath string
	var destinationPath string

	cmd := &cobra.Command{
		Use:   "plan",
		Short: "Plan a CSV import",
		Long:  "Plan a CSV import from a validated artifact or from inspection, mapping, and destination JSON files without creating Basecamp records.",
		RunE: func(cmd *cobra.Command, args []string) error {
			var plan *importer.Plan
			var err error
			if artifactPath != "" {
				if inspectionPath != "" || mappingPath != "" || destinationPath != "" {
					return output.ErrUsage("--artifact cannot be combined with --inspection, --mapping, or --destination")
				}
				plan, err = importer.PlanFromArtifact(artifactPath)
			} else {
				if inspectionPath == "" {
					return output.ErrUsage("--inspection required")
				}
				if mappingPath == "" {
					return output.ErrUsage("--mapping required")
				}
				if destinationPath == "" {
					return output.ErrUsage("--destination required")
				}
				var inspection *importer.Inspection
				inspection, err = importer.ReadInspectionFile(inspectionPath)
				if err != nil {
					return err
				}
				var mapping *importer.MappingConfig
				mapping, err = importer.ReadMappingFile(mappingPath)
				if err != nil {
					return err
				}
				var destination *importer.DestinationConfig
				destination, err = importer.ReadDestinationFile(destinationPath)
				if err != nil {
					return err
				}
				plan, err = importer.PlanImport(inspection, mapping, destination)
			}
			if err != nil {
				return err
			}

			app := appctx.FromContext(cmd.Context())
			if app == nil {
				fmt.Fprintln(cmd.OutOrStdout(), plan.Status)
				return nil
			}
			return app.OK(plan, output.WithSummary(fmt.Sprintf("Planned %d todos", plan.Counts.Todos)))
		},
	}

	cmd.Flags().StringVar(&artifactPath, "artifact", "", "Validated Basecamp import artifact directory")
	cmd.Flags().StringVar(&inspectionPath, "inspection", "", "Inspection JSON file")
	cmd.Flags().StringVar(&mappingPath, "mapping", "", "Confirmed mapping JSON file")
	cmd.Flags().StringVar(&destinationPath, "destination", "", "Destination JSON file")
	return cmd
}

func newImportInspectCmd() *cobra.Command {
	var sampleSize int

	cmd := &cobra.Command{
		Use:   "inspect <csv-path>",
		Short: "Inspect a CSV export",
		Long:  "Inspect a CSV export and report columns, value shapes, mapping candidates, warnings, and mapping questions.",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) < 1 {
				return missingArg(cmd, "csv-path")
			}
			if len(args) > 1 {
				return output.ErrUsage("accepts exactly one CSV path")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			inspection, err := importer.InspectCSV(args[0], importer.InspectOptions{SampleSize: sampleSize})
			if err != nil {
				return err
			}

			app := appctx.FromContext(cmd.Context())
			if app == nil {
				fmt.Fprintln(cmd.OutOrStdout(), inspection.Status)
				return nil
			}
			return app.OK(inspection, output.WithSummary(fmt.Sprintf("Profiled %d CSV rows and %d columns", inspection.RowCount, len(inspection.Columns))))
		},
	}

	cmd.Flags().IntVar(&sampleSize, "sample-size", 5, "Number of sample rows to include")
	return cmd
}
