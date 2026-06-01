package importer

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const artifactExecutionFileName = "execution.json"

// ExecuteOptions controls approved artifact execution.
type ExecuteOptions struct {
	Approved bool
}

// ArtifactWriteClient performs Basecamp writes for a validated import artifact.
type ArtifactWriteClient interface {
	CreateProject(ctx context.Context, name string) (int64, error)
	CreateTodolist(ctx context.Context, projectID int64, name string) (int64, error)
	CreateTodo(ctx context.Context, todolistID int64, todo ExecutableTodo) (int64, error)
	CardTableID(ctx context.Context, projectID int64) (int64, error)
	CreateCardColumn(ctx context.Context, cardTableID int64, name string) (int64, error)
	CreateCard(ctx context.Context, columnID int64, card ExecutableCard) (int64, error)
}

// ExecutableTodo is the normalized todo payload sent to Basecamp.
type ExecutableTodo struct {
	Title       string
	Description string
	DueOn       string
}

// ExecutableCard is the normalized card payload sent to Basecamp.
type ExecutableCard struct {
	Title   string
	Content string
}

// ExecuteResult reports records created from a validated import artifact.
type ExecuteResult struct {
	SchemaVersion int              `json:"schema_version"`
	Status        string           `json:"status"`
	Created       ExecuteCounts    `json:"created"`
	Skipped       []ExecuteSkipped `json:"skipped,omitempty"`
	LedgerPath    string           `json:"ledger_path,omitempty"`
}

// ExecuteCounts counts records created by artifact execution.
type ExecuteCounts struct {
	Projects    int `json:"projects"`
	Todolists   int `json:"todolists"`
	Todos       int `json:"todos"`
	CardColumns int `json:"card_columns,omitempty"`
	Cards       int `json:"cards,omitempty"`
}

// ExecuteSkipped reports artifact data that was preserved but not written as a native Basecamp field.
type ExecuteSkipped struct {
	SourceRow int    `json:"source_row,omitempty"`
	Field     string `json:"field"`
	Reason    string `json:"reason"`
}

// ExecutionLedger records an artifact execution attempt.
type ExecutionLedger struct {
	SchemaVersion     int                        `json:"schema_version"`
	ArtifactFormat    string                     `json:"artifact_format"`
	Status            string                     `json:"status"`
	SourceFingerprint Fingerprint                `json:"source_fingerprint"`
	StartedAt         string                     `json:"started_at"`
	CompletedAt       string                     `json:"completed_at,omitempty"`
	FailedAt          string                     `json:"failed_at,omitempty"`
	Created           ExecuteCounts              `json:"created,omitempty"`
	Operations        []ExecutionLedgerOperation `json:"operations,omitempty"`
	Error             string                     `json:"error,omitempty"`
}

// ExecutionLedgerOperation records one completed or failed artifact operation.
type ExecutionLedgerOperation struct {
	Op             string `json:"op"`
	Status         string `json:"status"`
	SourceRow      int    `json:"source_row,omitempty"`
	SourceRecordID string `json:"source_record_id,omitempty"`
	ProjectID      int64  `json:"project_id,omitempty"`
	ProjectName    string `json:"project_name,omitempty"`
	TodolistID     int64  `json:"todolist_id,omitempty"`
	TodolistName   string `json:"todolist_name,omitempty"`
	CardTableID    int64  `json:"card_table_id,omitempty"`
	ColumnID       int64  `json:"column_id,omitempty"`
	ColumnName     string `json:"column_name,omitempty"`
	Title          string `json:"title,omitempty"`
	CreatedID      int64  `json:"created_id,omitempty"`
	At             string `json:"at"`
	Error          string `json:"error,omitempty"`
}

// ExecuteArtifact creates Basecamp records from a validated import artifact after explicit approval.
func ExecuteArtifact(ctx context.Context, artifactDir string, client ArtifactWriteClient, opts ExecuteOptions) (result *ExecuteResult, err error) {
	if !opts.Approved {
		return nil, fmt.Errorf("import execution requires explicit approval")
	}
	if client == nil {
		return nil, fmt.Errorf("import execution requires a write client")
	}
	manifest, rows, err := readArtifact(artifactDir)
	if err != nil {
		return nil, err
	}

	ledger, err := beginArtifactExecution(artifactDir, manifest)
	if err != nil {
		return nil, err
	}
	ledgerFinalized := false
	defer func() {
		if err != nil && !ledgerFinalized {
			_ = finishArtifactExecution(artifactDir, ledger, "failed", result, err)
		}
	}()

	result = &ExecuteResult{SchemaVersion: planSchemaVersion, Status: "completed", LedgerPath: filepath.Join(artifactDir, artifactExecutionFileName)}
	projectID, err := executeArtifactProject(ctx, artifactDir, client, manifest, ledger, result)
	if err != nil {
		return nil, err
	}

	resourceType, err := destinationResourceType(&manifest.Destination)
	if err != nil {
		return nil, err
	}
	if resourceType == resourceTypeCards {
		if err := executeArtifactCards(ctx, artifactDir, client, projectID, manifest, rows, ledger, result); err != nil {
			return nil, err
		}
	} else {
		if err := executeArtifactTodos(ctx, artifactDir, client, projectID, manifest, rows, ledger, result); err != nil {
			return nil, err
		}
	}
	if err := finishArtifactExecution(artifactDir, ledger, "completed", result, nil); err != nil {
		ledgerFinalized = true
		return nil, err
	}
	ledgerFinalized = true
	return result, nil
}

func executeArtifactTodos(ctx context.Context, artifactDir string, client ArtifactWriteClient, projectID int64, manifest *ImportArtifactManifest, rows []artifactTodoRow, ledger *ExecutionLedger, result *ExecuteResult) error {
	listIDs, err := executeArtifactTodolists(ctx, artifactDir, client, projectID, manifest, rows, ledger, result)
	if err != nil {
		return err
	}
	for _, row := range rows {
		listName := row.TodolistName
		if strings.TrimSpace(listName) == "" {
			listName = "Imported todos"
		}
		todolistID := row.TodolistID
		if todolistID == 0 {
			todolistID = listIDs[listName]
		}
		if todolistID == 0 {
			return fmt.Errorf("source row %d has no executable todolist", row.SourceRow)
		}
		if len(row.AssigneeEmails) > 0 || len(row.AssigneeNames) > 0 {
			result.Skipped = append(result.Skipped, ExecuteSkipped{SourceRow: row.SourceRow, Field: "assignees", Reason: "artifact does not contain Basecamp person IDs"})
		}
		todo := ExecutableTodo{Title: row.Title, Description: executionDescription(row), DueOn: row.DueOn}
		createdID, createErr := client.CreateTodo(ctx, todolistID, todo)
		if createErr != nil {
			err := fmt.Errorf("create todo from source row %d: %w", row.SourceRow, createErr)
			appendExecutionLedgerOperation(ledger, ExecutionLedgerOperation{Op: "create_todo", Status: "failed", SourceRow: row.SourceRow, SourceRecordID: row.SourceRecordID, ProjectID: projectID, TodolistID: todolistID, TodolistName: listName, Title: row.Title, Error: err.Error()})
			_ = writeExecutionLedger(filepath.Join(artifactDir, artifactExecutionFileName), ledger)
			return err
		}
		result.Created.Todos++
		ledger.Created.Todos = result.Created.Todos
		appendExecutionLedgerOperation(ledger, ExecutionLedgerOperation{Op: "create_todo", Status: "completed", SourceRow: row.SourceRow, SourceRecordID: row.SourceRecordID, ProjectID: projectID, TodolistID: todolistID, TodolistName: listName, Title: row.Title, CreatedID: createdID})
		if err := writeExecutionLedger(filepath.Join(artifactDir, artifactExecutionFileName), ledger); err != nil {
			return err
		}
	}
	return nil
}

func executeArtifactCards(ctx context.Context, artifactDir string, client ArtifactWriteClient, projectID int64, manifest *ImportArtifactManifest, rows []artifactTodoRow, ledger *ExecutionLedger, result *ExecuteResult) error {
	columnIDs, cardTableID, err := executeArtifactCardColumns(ctx, artifactDir, client, projectID, manifest, rows, ledger, result)
	if err != nil {
		return err
	}
	for _, row := range rows {
		columnName := row.TodolistName
		if strings.TrimSpace(columnName) == "" {
			columnName = "Imported cards"
		}
		columnID := row.TodolistID
		if columnID == 0 {
			columnID = columnIDs[columnName]
		}
		if columnID == 0 {
			return fmt.Errorf("source row %d has no executable card column", row.SourceRow)
		}
		if len(row.AssigneeEmails) > 0 || len(row.AssigneeNames) > 0 {
			result.Skipped = append(result.Skipped, ExecuteSkipped{SourceRow: row.SourceRow, Field: "assignees", Reason: "artifact does not contain Basecamp person IDs"})
		}
		card := ExecutableCard{Title: row.Title, Content: executionDescription(row)}
		createdID, createErr := client.CreateCard(ctx, columnID, card)
		if createErr != nil {
			err := fmt.Errorf("create card from source row %d: %w", row.SourceRow, createErr)
			appendExecutionLedgerOperation(ledger, ExecutionLedgerOperation{Op: "create_card", Status: "failed", SourceRow: row.SourceRow, SourceRecordID: row.SourceRecordID, ProjectID: projectID, CardTableID: cardTableID, ColumnID: columnID, ColumnName: columnName, Title: row.Title, Error: err.Error()})
			_ = writeExecutionLedger(filepath.Join(artifactDir, artifactExecutionFileName), ledger)
			return err
		}
		result.Created.Cards++
		ledger.Created.Cards = result.Created.Cards
		appendExecutionLedgerOperation(ledger, ExecutionLedgerOperation{Op: "create_card", Status: "completed", SourceRow: row.SourceRow, SourceRecordID: row.SourceRecordID, ProjectID: projectID, CardTableID: cardTableID, ColumnID: columnID, ColumnName: columnName, Title: row.Title, CreatedID: createdID})
		if err := writeExecutionLedger(filepath.Join(artifactDir, artifactExecutionFileName), ledger); err != nil {
			return err
		}
	}
	return nil
}

func executeArtifactProject(ctx context.Context, artifactDir string, client ArtifactWriteClient, manifest *ImportArtifactManifest, ledger *ExecutionLedger, result *ExecuteResult) (int64, error) {
	if manifest.Destination.Mode != "new_project" {
		return executionProjectID(ctx, client, manifest)
	}
	name := strings.TrimSpace(manifest.Destination.ProjectName)
	if name == "" {
		return 0, fmt.Errorf("artifact destination project_name is required")
	}
	projectID, err := client.CreateProject(ctx, name)
	if err != nil {
		wrapped := fmt.Errorf("create project %q: %w", name, err)
		appendExecutionLedgerOperation(ledger, ExecutionLedgerOperation{Op: "create_project", Status: "failed", ProjectName: name, Error: wrapped.Error()})
		_ = writeExecutionLedger(filepath.Join(artifactDir, artifactExecutionFileName), ledger)
		return 0, wrapped
	}
	result.Created.Projects = 1
	ledger.Created.Projects = result.Created.Projects
	appendExecutionLedgerOperation(ledger, ExecutionLedgerOperation{Op: "create_project", Status: "completed", ProjectName: name, ProjectID: projectID, CreatedID: projectID})
	if err := writeExecutionLedger(filepath.Join(artifactDir, artifactExecutionFileName), ledger); err != nil {
		return 0, err
	}
	return projectID, nil
}

func executeArtifactTodolists(ctx context.Context, artifactDir string, client ArtifactWriteClient, projectID int64, manifest *ImportArtifactManifest, rows []artifactTodoRow, ledger *ExecutionLedger, result *ExecuteResult) (map[string]int64, error) {
	listIDs := make(map[string]int64)
	if manifest.Destination.TodolistStrategy == "existing_todolist" {
		id, err := parseOptionalInt64(manifest.Destination.TodolistID)
		if err != nil {
			return nil, fmt.Errorf("invalid destination todolist_id: %w", err)
		}
		if id == 0 {
			return nil, fmt.Errorf("artifact destination todolist_id is required for execution")
		}
		listIDs[manifest.Destination.TodolistName] = id
		listIDs["Imported todos"] = id
		return listIDs, nil
	}

	for _, name := range artifactTodolistNames(rows) {
		id, createErr := client.CreateTodolist(ctx, projectID, name)
		if createErr != nil {
			err := fmt.Errorf("create todolist %q: %w", name, createErr)
			appendExecutionLedgerOperation(ledger, ExecutionLedgerOperation{Op: "create_todolist", Status: "failed", ProjectID: projectID, TodolistName: name, Error: err.Error()})
			_ = writeExecutionLedger(filepath.Join(artifactDir, artifactExecutionFileName), ledger)
			return nil, err
		}
		listIDs[name] = id
		result.Created.Todolists++
		ledger.Created.Todolists = result.Created.Todolists
		appendExecutionLedgerOperation(ledger, ExecutionLedgerOperation{Op: "create_todolist", Status: "completed", ProjectID: projectID, TodolistID: id, TodolistName: name, CreatedID: id})
		if err := writeExecutionLedger(filepath.Join(artifactDir, artifactExecutionFileName), ledger); err != nil {
			return nil, err
		}
	}
	return listIDs, nil
}

func executeArtifactCardColumns(ctx context.Context, artifactDir string, client ArtifactWriteClient, projectID int64, manifest *ImportArtifactManifest, rows []artifactTodoRow, ledger *ExecutionLedger, result *ExecuteResult) (map[string]int64, int64, error) {
	cardTableID, err := parseOptionalInt64(manifest.Destination.CardTableID)
	if err != nil {
		return nil, 0, fmt.Errorf("invalid destination card_table_id: %w", err)
	}
	if cardTableID == 0 {
		cardTableID, err = client.CardTableID(ctx, projectID)
		if err != nil {
			return nil, 0, err
		}
	}
	columnIDs := make(map[string]int64)
	if manifest.Destination.ColumnStrategy == "existing_column" {
		id, err := parseOptionalInt64(manifest.Destination.ColumnID)
		if err != nil {
			return nil, 0, fmt.Errorf("invalid destination column_id: %w", err)
		}
		if id == 0 {
			return nil, 0, fmt.Errorf("artifact destination column_id is required for card execution")
		}
		columnIDs[manifest.Destination.ColumnName] = id
		columnIDs["Imported cards"] = id
		return columnIDs, cardTableID, nil
	}
	for _, name := range artifactCardColumnNames(rows) {
		id, createErr := client.CreateCardColumn(ctx, cardTableID, name)
		if createErr != nil {
			err := fmt.Errorf("create card column %q: %w", name, createErr)
			appendExecutionLedgerOperation(ledger, ExecutionLedgerOperation{Op: "create_card_column", Status: "failed", ProjectID: projectID, CardTableID: cardTableID, ColumnName: name, Error: err.Error()})
			_ = writeExecutionLedger(filepath.Join(artifactDir, artifactExecutionFileName), ledger)
			return nil, 0, err
		}
		columnIDs[name] = id
		result.Created.CardColumns++
		ledger.Created.CardColumns = result.Created.CardColumns
		appendExecutionLedgerOperation(ledger, ExecutionLedgerOperation{Op: "create_card_column", Status: "completed", ProjectID: projectID, CardTableID: cardTableID, ColumnID: id, ColumnName: name, CreatedID: id})
		if err := writeExecutionLedger(filepath.Join(artifactDir, artifactExecutionFileName), ledger); err != nil {
			return nil, 0, err
		}
	}
	return columnIDs, cardTableID, nil
}

func appendExecutionLedgerOperation(ledger *ExecutionLedger, op ExecutionLedgerOperation) {
	op.At = time.Now().UTC().Format(time.RFC3339)
	ledger.Operations = append(ledger.Operations, op)
}

func beginArtifactExecution(artifactDir string, manifest *ImportArtifactManifest) (*ExecutionLedger, error) {
	ledgerPath := filepath.Join(artifactDir, artifactExecutionFileName)
	if data, err := os.ReadFile(ledgerPath); err == nil {
		var existing ExecutionLedger
		if jsonErr := json.Unmarshal(data, &existing); jsonErr != nil {
			return nil, fmt.Errorf("artifact execution ledger exists at %s and cannot be read; refusing to execute again", ledgerPath)
		}
		return nil, fmt.Errorf("artifact execution ledger exists at %s with status %q; refusing to execute again to avoid duplicate Basecamp records", ledgerPath, existing.Status)
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("checking artifact execution ledger: %w", err)
	}

	ledger := &ExecutionLedger{
		SchemaVersion:     planSchemaVersion,
		ArtifactFormat:    manifest.ArtifactFormat,
		Status:            "started",
		SourceFingerprint: manifest.SourceFingerprint,
		StartedAt:         time.Now().UTC().Format(time.RFC3339),
	}
	if err := writeExecutionLedger(ledgerPath, ledger); err != nil {
		return nil, err
	}
	return ledger, nil
}

func finishArtifactExecution(artifactDir string, ledger *ExecutionLedger, status string, result *ExecuteResult, executionErr error) error {
	ledger.Status = status
	if result != nil {
		ledger.Created = result.Created
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if status == "completed" {
		ledger.CompletedAt = now
	} else {
		ledger.FailedAt = now
		if executionErr != nil {
			ledger.Error = executionErr.Error()
		}
	}
	return writeExecutionLedger(filepath.Join(artifactDir, artifactExecutionFileName), ledger)
}

func writeExecutionLedger(path string, ledger *ExecutionLedger) error {
	data, err := json.MarshalIndent(ledger, "", "  ")
	if err != nil {
		return fmt.Errorf("encode artifact execution ledger: %w", err)
	}
	data = append(data, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil { //nolint:gosec // G306: Execution ledgers are user-readable recovery files
		return fmt.Errorf("write artifact execution ledger: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("write artifact execution ledger: %w", err)
	}
	return nil
}

func executionProjectID(ctx context.Context, client ArtifactWriteClient, manifest *ImportArtifactManifest) (int64, error) {
	if manifest.Destination.Mode == "new_project" {
		name := strings.TrimSpace(manifest.Destination.ProjectName)
		if name == "" {
			return 0, fmt.Errorf("artifact destination project_name is required")
		}
		return client.CreateProject(ctx, name)
	}
	id, err := parseOptionalInt64(manifest.Destination.ProjectID)
	if err != nil {
		return 0, fmt.Errorf("invalid destination project_id: %w", err)
	}
	if id == 0 {
		return 0, fmt.Errorf("artifact destination project_id is required for execution")
	}
	return id, nil
}

func executionDescription(row artifactTodoRow) string {
	parts := make([]string, 0)
	if strings.TrimSpace(row.Description) != "" {
		parts = append(parts, row.Description)
	}
	metadata := make([]string, 0)
	if row.SourceRecordID != "" {
		metadata = append(metadata, "Source record ID: "+row.SourceRecordID)
	}
	if row.Status != "" {
		metadata = append(metadata, "Source status: "+row.Status)
	}
	if len(row.AssigneeEmails) > 0 {
		metadata = append(metadata, "Source assignee emails: "+strings.Join(row.AssigneeEmails, ", "))
	}
	if len(row.AssigneeNames) > 0 {
		metadata = append(metadata, "Source assignee names: "+strings.Join(row.AssigneeNames, ", "))
	}
	if len(row.AttachmentURLs) > 0 {
		metadata = append(metadata, "Source attachment URLs: "+strings.Join(row.AttachmentURLs, ", "))
	}
	if len(row.Comments) > 0 {
		metadata = append(metadata, "Source comments: "+strings.Join(row.Comments, " | "))
	}
	if len(row.CustomFields) > 0 {
		metadata = append(metadata, "Source custom fields:")
		for _, key := range sortedMapKeys(row.CustomFields) {
			metadata = append(metadata, fmt.Sprintf("- %s: %s", key, row.CustomFields[key]))
		}
	}
	if len(metadata) > 0 {
		parts = append(parts, strings.Join(metadata, "\n"))
	}
	return strings.Join(parts, "\n\n")
}

func formatOptionalInt64(value int64) string {
	if value == 0 {
		return ""
	}
	return strconv.FormatInt(value, 10)
}

func parseOptionalInt64(value string) (int64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, nil
	}
	return strconv.ParseInt(value, 10, 64)
}

func sortedMapKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}
