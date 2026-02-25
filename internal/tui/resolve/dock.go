package resolve

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/basecamp/basecamp-cli/internal/output"
	"github.com/basecamp/basecamp-cli/internal/tui"
)

// DockTool represents a tool in a project's dock.
type DockTool struct {
	Name    string `json:"name"`
	Title   string `json:"title"`
	ID      int64  `json:"id"`
	Enabled bool   `json:"enabled"`
}

// DockToolResult holds the result of dock tool resolution.
type DockToolResult struct {
	ToolID string
	Tool   *DockTool
	Source ResolvedSource
}

// DockTool resolves a dock tool ID from a project using the following precedence:
// 1. Explicit ID provided (--campfire, --board, etc.)
// 2. Single tool of type exists - use it automatically
// 3. Interactive prompt (if terminal is interactive)
// 4. Error listing available tools (if not interactive)
func (r *Resolver) DockTool(ctx context.Context, projectID, dockName, explicitID, friendlyName string) (*DockToolResult, error) {
	// 1. If explicit ID provided, use it directly
	if explicitID != "" {
		return &DockToolResult{
			ToolID: explicitID,
			Source: SourceFlag,
		}, nil
	}

	// Fetch project to get dock tools
	tools, err := r.fetchDockTools(ctx, projectID, dockName)
	if err != nil {
		return nil, err
	}

	// Handle cases based on number of matching tools
	switch len(tools) {
	case 0:
		return nil, output.ErrNotFoundHint(friendlyName, projectID, fmt.Sprintf("Project has no %s", friendlyName))

	case 1:
		// Single tool - use it automatically
		return &DockToolResult{
			ToolID: fmt.Sprintf("%d", tools[0].ID),
			Tool:   &tools[0],
			Source: SourceDefault,
		}, nil

	default:
		// Multiple tools - try interactive prompt
		if !r.IsInteractive() {
			return nil, r.multiToolError(tools, friendlyName)
		}

		return r.promptForDockTool(tools, friendlyName)
	}
}

// fetchDockTools retrieves enabled dock tools of a specific type from a project.
func (r *Resolver) fetchDockTools(ctx context.Context, projectID, dockName string) ([]DockTool, error) {
	// Ensure account is configured before making API calls
	if r.config.AccountID == "" {
		return nil, output.ErrUsage("Account must be resolved before fetching dock tools")
	}

	// Fetch project data
	path := fmt.Sprintf("/projects/%s.json", projectID)
	resp, err := r.sdk.ForAccount(r.config.AccountID).Get(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch project: %w", err)
	}

	var project struct {
		Dock []DockTool `json:"dock"`
	}
	if err := json.Unmarshal(resp.Data, &project); err != nil {
		return nil, fmt.Errorf("failed to parse project: %w", err)
	}

	// Filter to matching enabled tools only
	var matches []DockTool
	for _, tool := range project.Dock {
		if tool.Name == dockName && tool.Enabled {
			matches = append(matches, tool)
		}
	}

	return matches, nil
}

// promptForDockTool shows an interactive picker for dock tool selection.
func (r *Resolver) promptForDockTool(tools []DockTool, friendlyName string) (*DockToolResult, error) {
	items := make([]tui.PickerItem, len(tools))
	for i, tool := range tools {
		title := tool.Title
		if title == "" {
			title = friendlyName
		}

		description := fmt.Sprintf("ID: %d", tool.ID)
		if !tool.Enabled {
			description += " (disabled)"
		}

		items[i] = tui.PickerItem{
			ID:          fmt.Sprintf("%d", tool.ID),
			Title:       title,
			Description: description,
		}
	}

	selected, err := tui.Pick(fmt.Sprintf("Select a %s", friendlyName), items)
	if err != nil {
		return nil, fmt.Errorf("%s selection failed: %w", friendlyName, err)
	}
	if selected == nil {
		return nil, output.ErrUsage(fmt.Sprintf("%s selection canceled", friendlyName))
	}

	// Find the original tool for the result
	var selectedTool *DockTool
	for i := range tools {
		if fmt.Sprintf("%d", tools[i].ID) == selected.ID {
			selectedTool = &tools[i]
			break
		}
	}

	return &DockToolResult{
		ToolID: selected.ID,
		Tool:   selectedTool,
		Source: SourcePrompt,
	}, nil
}

// multiToolError creates an error message listing available tools.
func (r *Resolver) multiToolError(tools []DockTool, friendlyName string) error {
	var toolList strings.Builder
	for _, tool := range tools {
		title := tool.Title
		if title == "" {
			title = friendlyName
		}
		fmt.Fprintf(&toolList, "\n  - %s (ID: %d)", title, tool.ID)
	}

	return &output.Error{
		Code:    output.CodeAmbiguous,
		Message: fmt.Sprintf("Project has %d %ss", len(tools), friendlyName),
		Hint:    fmt.Sprintf("Specify ID directly. Available:%s", toolList.String()),
	}
}

// Convenience methods for specific dock tool types

// Campfire resolves a campfire ID from a project.
func (r *Resolver) Campfire(ctx context.Context, projectID, explicitID string) (*DockToolResult, error) {
	return r.DockTool(ctx, projectID, "chat", explicitID, "campfire")
}

// MessageBoard resolves a message board ID from a project.
func (r *Resolver) MessageBoard(ctx context.Context, projectID, explicitID string) (*DockToolResult, error) {
	return r.DockTool(ctx, projectID, "message_board", explicitID, "message board")
}

// Todoset resolves a todoset ID from a project.
func (r *Resolver) Todoset(ctx context.Context, projectID, explicitID string) (*DockToolResult, error) {
	return r.DockTool(ctx, projectID, "todoset", explicitID, "todoset")
}

// Schedule resolves a schedule ID from a project.
func (r *Resolver) Schedule(ctx context.Context, projectID, explicitID string) (*DockToolResult, error) {
	return r.DockTool(ctx, projectID, "schedule", explicitID, "schedule")
}

// Vault resolves a vault (Docs & Files) ID from a project.
func (r *Resolver) Vault(ctx context.Context, projectID, explicitID string) (*DockToolResult, error) {
	return r.DockTool(ctx, projectID, "vault", explicitID, "vault")
}

// Inbox resolves an inbox ID from a project.
func (r *Resolver) Inbox(ctx context.Context, projectID, explicitID string) (*DockToolResult, error) {
	return r.DockTool(ctx, projectID, "inbox", explicitID, "inbox")
}

// Questionnaire resolves a questionnaire (Automatic Check-ins) ID from a project.
func (r *Resolver) Questionnaire(ctx context.Context, projectID, explicitID string) (*DockToolResult, error) {
	return r.DockTool(ctx, projectID, "questionnaire", explicitID, "questionnaire")
}
