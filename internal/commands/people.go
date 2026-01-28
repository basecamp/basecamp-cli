package commands

import (
	"fmt"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/bcq/internal/appctx"
	"github.com/basecamp/bcq/internal/output"
)

func NewMeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "me",
		Short: "Show current user profile",
		Long:  "Display information about the currently authenticated user.",
		RunE:  runMe,
	}
	return cmd
}

func runMe(cmd *cobra.Command, args []string) error {
	app := appctx.FromContext(cmd.Context())

	if !app.Auth.IsAuthenticated() {
		return output.ErrAuth("Not authenticated")
	}

	person, err := app.SDK.People().Me(cmd.Context())
	if err != nil {
		return convertSDKError(err)
	}

	// Store user ID for "me" resolution in future commands (non-fatal if fails)
	_ = app.Auth.SetUserID(fmt.Sprintf("%d", person.ID))

	summary := fmt.Sprintf("%s <%s>", person.Name, person.EmailAddress)
	breadcrumbs := []output.Breadcrumb{
		{Action: "projects", Cmd: "bcq projects", Description: "List your projects"},
		{Action: "todos", Cmd: "bcq todos --assignee me", Description: "Your assigned todos"},
		{Action: "auth", Cmd: "bcq auth status", Description: "Auth status"},
	}

	return app.OK(person,
		output.WithSummary(summary),
		output.WithBreadcrumbs(breadcrumbs...),
	)
}

func NewPeopleCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "people [action]",
		Short: "Manage people",
		Long:  "List, show, and manage people in your Basecamp account.",
	}

	cmd.AddCommand(newPeopleListCmd())
	cmd.AddCommand(newPeopleShowCmd())
	cmd.AddCommand(newPeoplePingableCmd())
	cmd.AddCommand(newPeopleAddCmd())
	cmd.AddCommand(newPeopleRemoveCmd())

	return cmd
}

func newPeopleListCmd() *cobra.Command {
	var projectID string

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List people",
		Long:  "List all people in your Basecamp account, or in a specific project.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPeopleList(cmd, projectID)
		},
	}

	cmd.Flags().StringVarP(&projectID, "project", "p", "", "List people in a specific project")

	return cmd
}

func runPeopleList(cmd *cobra.Command, projectID string) error {
	app := appctx.FromContext(cmd.Context())

	var people []basecamp.Person
	var err error

	if projectID != "" {
		// Resolve project name to ID if needed
		resolvedID, _, resolveErr := app.Names.ResolveProject(cmd.Context(), projectID)
		if resolveErr != nil {
			return resolveErr
		}
		bucketID, parseErr := strconv.ParseInt(resolvedID, 10, 64)
		if parseErr != nil {
			return output.ErrUsage("Invalid project ID")
		}
		people, err = app.SDK.People().ListProjectPeople(cmd.Context(), bucketID)
	} else {
		people, err = app.SDK.People().List(cmd.Context())
	}

	if err != nil {
		return convertSDKError(err)
	}

	summary := fmt.Sprintf("%d people", len(people))
	breadcrumbs := []output.Breadcrumb{
		{Action: "show", Cmd: "bcq people show <id>", Description: "Show person details"},
	}

	return app.OK(people,
		output.WithSummary(summary),
		output.WithBreadcrumbs(breadcrumbs...),
	)
}

func newPeopleShowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <id|name>",
		Short: "Show person details",
		Long:  "Display detailed information about a specific person.",
		Args:  cobra.ExactArgs(1),
		RunE:  runPeopleShow,
	}
	return cmd
}

func runPeopleShow(cmd *cobra.Command, args []string) error {
	app := appctx.FromContext(cmd.Context())

	// Resolve person name/ID
	personIDStr, _, err := app.Names.ResolvePerson(cmd.Context(), args[0])
	if err != nil {
		return err
	}

	personID, err := strconv.ParseInt(personIDStr, 10, 64)
	if err != nil {
		return output.ErrUsage("Invalid person ID")
	}

	person, err := app.SDK.People().Get(cmd.Context(), personID)
	if err != nil {
		return convertSDKError(err)
	}

	return app.OK(person, output.WithSummary(person.Name))
}

func newPeoplePingableCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pingable",
		Short: "List pingable people",
		Long:  "List people who can be @mentioned (pinged) in comments and messages.",
		RunE:  runPeoplePingable,
	}
	return cmd
}

func runPeoplePingable(cmd *cobra.Command, args []string) error {
	app := appctx.FromContext(cmd.Context())

	people, err := app.SDK.People().Pingable(cmd.Context())
	if err != nil {
		return convertSDKError(err)
	}

	summary := fmt.Sprintf("%d pingable people", len(people))

	return app.OK(people, output.WithSummary(summary))
}

func newPeopleAddCmd() *cobra.Command {
	var projectID string

	cmd := &cobra.Command{
		Use:   "add <person-id> [person-id...]",
		Short: "Add people to a project",
		Long:  "Grant people access to a project.",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPeopleAdd(cmd, args, projectID)
		},
	}

	cmd.Flags().StringVarP(&projectID, "project", "p", "", "Project to add people to (required)")
	_ = cmd.MarkFlagRequired("project")

	return cmd
}

func runPeopleAdd(cmd *cobra.Command, personIDs []string, projectID string) error {
	app := appctx.FromContext(cmd.Context())

	// Resolve project
	resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
	if err != nil {
		return err
	}

	bucketID, err := strconv.ParseInt(resolvedProjectID, 10, 64)
	if err != nil {
		return output.ErrUsage("Invalid project ID")
	}

	// Resolve all person IDs
	var ids []int64
	for _, pid := range personIDs {
		resolvedID, _, resolveErr := app.Names.ResolvePerson(cmd.Context(), pid)
		if resolveErr != nil {
			return resolveErr
		}
		id, parseErr := strconv.ParseInt(resolvedID, 10, 64)
		if parseErr != nil {
			return output.ErrUsage("Invalid person ID")
		}
		ids = append(ids, id)
	}

	// Build SDK request
	req := &basecamp.UpdateProjectAccessRequest{
		Grant: ids,
	}

	result, err := app.SDK.People().UpdateProjectAccess(cmd.Context(), bucketID, req)
	if err != nil {
		return convertSDKError(err)
	}

	summary := fmt.Sprintf("Added %d person(s) to project #%s", len(ids), resolvedProjectID)
	breadcrumbs := []output.Breadcrumb{
		{Action: "list", Cmd: fmt.Sprintf("bcq people list --project %s", resolvedProjectID), Description: "List project members"},
	}

	return app.OK(result,
		output.WithSummary(summary),
		output.WithBreadcrumbs(breadcrumbs...),
	)
}

func newPeopleRemoveCmd() *cobra.Command {
	var projectID string

	cmd := &cobra.Command{
		Use:   "remove <person-id> [person-id...]",
		Short: "Remove people from a project",
		Long:  "Revoke people's access to a project.",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPeopleRemove(cmd, args, projectID)
		},
	}

	cmd.Flags().StringVarP(&projectID, "project", "p", "", "Project to remove people from (required)")
	_ = cmd.MarkFlagRequired("project")

	return cmd
}

func runPeopleRemove(cmd *cobra.Command, personIDs []string, projectID string) error {
	app := appctx.FromContext(cmd.Context())

	// Resolve project
	resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
	if err != nil {
		return err
	}

	bucketID, err := strconv.ParseInt(resolvedProjectID, 10, 64)
	if err != nil {
		return output.ErrUsage("Invalid project ID")
	}

	// Resolve all person IDs
	var ids []int64
	for _, pid := range personIDs {
		resolvedID, _, resolveErr := app.Names.ResolvePerson(cmd.Context(), pid)
		if resolveErr != nil {
			return resolveErr
		}
		id, parseErr := strconv.ParseInt(resolvedID, 10, 64)
		if parseErr != nil {
			return output.ErrUsage("Invalid person ID")
		}
		ids = append(ids, id)
	}

	// Build SDK request
	req := &basecamp.UpdateProjectAccessRequest{
		Revoke: ids,
	}

	result, err := app.SDK.People().UpdateProjectAccess(cmd.Context(), bucketID, req)
	if err != nil {
		return convertSDKError(err)
	}

	summary := fmt.Sprintf("Removed %d person(s) from project #%s", len(ids), resolvedProjectID)
	breadcrumbs := []output.Breadcrumb{
		{Action: "list", Cmd: fmt.Sprintf("bcq people list --project %s", resolvedProjectID), Description: "List project members"},
	}

	return app.OK(result,
		output.WithSummary(summary),
		output.WithBreadcrumbs(breadcrumbs...),
	)
}
