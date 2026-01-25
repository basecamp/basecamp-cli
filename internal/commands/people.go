package commands

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/basecamp/bcq/internal/appctx"
	"github.com/basecamp/bcq/internal/output"
)

// PersonDetail represents a Basecamp person with full details from the API.
type PersonDetail struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	EmailAddress string `json:"email_address"`
	Title        string `json:"title,omitempty"`
	Admin        bool   `json:"admin"`
	Owner        bool   `json:"owner"`
	TimeZone     string `json:"time_zone,omitempty"`
	AvatarURL    string `json:"avatar_url,omitempty"`
	Company      *struct {
		Name string `json:"name"`
	} `json:"company,omitempty"`
}

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

	resp, err := app.API.Get(cmd.Context(), "/my/profile.json")
	if err != nil {
		return err
	}

	var person PersonDetail
	if err := json.Unmarshal(resp.Data, &person); err != nil {
		return err
	}

	// Store user ID for "me" resolution in future commands (non-fatal if fails)
	_ = app.Auth.SetUserID(fmt.Sprintf("%d", person.ID))

	summary := fmt.Sprintf("%s <%s>", person.Name, person.EmailAddress)
	breadcrumbs := []output.Breadcrumb{
		{Action: "projects", Cmd: "bcq projects", Description: "List your projects"},
		{Action: "todos", Cmd: "bcq todos --assignee me", Description: "Your assigned todos"},
		{Action: "auth", Cmd: "bcq auth status", Description: "Auth status"},
	}

	return app.Output.OK(resp.Data,
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

	var path string
	if projectID != "" {
		// Resolve project name to ID if needed
		resolvedID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
		if err != nil {
			return err
		}
		path = fmt.Sprintf("/projects/%s/people.json", resolvedID)
	} else {
		path = "/people.json"
	}

	resp, err := app.API.Get(cmd.Context(), path)
	if err != nil {
		return err
	}

	var people []PersonDetail
	if err := json.Unmarshal(resp.Data, &people); err != nil {
		return err
	}

	summary := fmt.Sprintf("%d people", len(people))
	breadcrumbs := []output.Breadcrumb{
		{Action: "show", Cmd: "bcq people show <id>", Description: "Show person details"},
	}

	return app.Output.OK(resp.Data,
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
	personID, _, err := app.Names.ResolvePerson(cmd.Context(), args[0])
	if err != nil {
		return err
	}

	resp, err := app.API.Get(cmd.Context(), fmt.Sprintf("/people/%s.json", personID))
	if err != nil {
		return err
	}

	var person PersonDetail
	if err := json.Unmarshal(resp.Data, &person); err != nil {
		return err
	}

	return app.Output.OK(resp.Data, output.WithSummary(person.Name))
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

	resp, err := app.API.Get(cmd.Context(), "/circles/people.json")
	if err != nil {
		return err
	}

	var people []PersonDetail
	if err := json.Unmarshal(resp.Data, &people); err != nil {
		return err
	}

	summary := fmt.Sprintf("%d pingable people", len(people))

	return app.Output.OK(resp.Data, output.WithSummary(summary))
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

	// Resolve all person IDs
	var ids []int64
	for _, pid := range personIDs {
		resolvedID, _, err := app.Names.ResolvePerson(cmd.Context(), pid)
		if err != nil {
			return err
		}
		var id int64
		_, _ = fmt.Sscanf(resolvedID, "%d", &id) //nolint:gosec // G104: ID validated
		ids = append(ids, id)
	}

	// Build request body
	body := map[string][]int64{"grant": ids}

	resp, err := app.API.Put(cmd.Context(), fmt.Sprintf("/projects/%s/people/users.json", resolvedProjectID), body)
	if err != nil {
		return err
	}

	summary := fmt.Sprintf("Added %d person(s) to project #%s", len(ids), resolvedProjectID)
	breadcrumbs := []output.Breadcrumb{
		{Action: "list", Cmd: fmt.Sprintf("bcq people list --project %s", resolvedProjectID), Description: "List project members"},
	}

	data := resp.Data
	if len(data) == 0 {
		data = []byte("{}")
	}

	return app.Output.OK(data,
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

	// Resolve all person IDs
	var ids []int64
	for _, pid := range personIDs {
		resolvedID, _, err := app.Names.ResolvePerson(cmd.Context(), pid)
		if err != nil {
			return err
		}
		var id int64
		_, _ = fmt.Sscanf(resolvedID, "%d", &id) //nolint:gosec // G104: ID validated
		ids = append(ids, id)
	}

	// Build request body
	body := map[string][]int64{"revoke": ids}

	resp, err := app.API.Put(cmd.Context(), fmt.Sprintf("/projects/%s/people/users.json", resolvedProjectID), body)
	if err != nil {
		return err
	}

	summary := fmt.Sprintf("Removed %d person(s) from project #%s", len(ids), resolvedProjectID)
	breadcrumbs := []output.Breadcrumb{
		{Action: "list", Cmd: fmt.Sprintf("bcq people list --project %s", resolvedProjectID), Description: "List project members"},
	}

	data := resp.Data
	if len(data) == 0 {
		data = []byte("{}")
	}

	return app.Output.OK(data,
		output.WithSummary(summary),
		output.WithBreadcrumbs(breadcrumbs...),
	)
}
