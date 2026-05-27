package importer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ArtifactPreflightClient performs Basecamp reads for artifact readiness checks.
type ArtifactPreflightClient interface {
	ExistingTodolists(ctx context.Context, projectID int64) ([]ExistingTodolist, error)
	ExistingTodos(ctx context.Context, todolistID int64) ([]ExistingTodo, error)
}

// ExistingTodolist describes a Basecamp todolist considered during preflight.
type ExistingTodolist struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// ExistingTodo describes a Basecamp todo considered during preflight.
type ExistingTodo struct {
	ID    int64  `json:"id"`
	Title string `json:"title"`
}

// PreflightResult reports readiness checks for an import artifact.
type PreflightResult struct {
	SchemaVersion  int                 `json:"schema_version"`
	Status         string              `json:"status"`
	Checks         []PreflightCheck    `json:"checks"`
	Collisions     []TodolistCollision `json:"collisions,omitempty"`
	TodoCollisions []TodoCollision     `json:"todo_collisions,omitempty"`
}

// PreflightCheck reports one artifact readiness check.
type PreflightCheck struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

// TodolistCollision reports an existing Basecamp todolist with the same name as a planned created todolist.
type TodolistCollision struct {
	Name       string `json:"name"`
	ExistingID int64  `json:"existing_id"`
}

// TodoCollision reports an existing Basecamp todo with the same title as a planned imported todo.
type TodoCollision struct {
	SourceRow  int    `json:"source_row"`
	Title      string `json:"title"`
	TodolistID int64  `json:"todolist_id"`
	ExistingID int64  `json:"existing_id"`
}

// PreflightArtifact checks an artifact for readiness before execution and performs no writes.
func PreflightArtifact(ctx context.Context, artifactDir string, client ArtifactPreflightClient) (*PreflightResult, error) {
	manifest, rows, err := readArtifact(artifactDir)
	if err != nil {
		return nil, err
	}

	result := &PreflightResult{SchemaVersion: planSchemaVersion, Status: "passed", Checks: []PreflightCheck{}}
	ledgerCheck := preflightExecutionLedgerCheck(artifactDir)
	if ledgerCheck.Status == "blocked" {
		result.Checks = append(result.Checks, ledgerCheck)
		result.Status = "blocked"
		return result, nil
	}
	result.Checks = append(result.Checks, ledgerCheck)

	if client == nil && manifest.Destination.Mode == "existing_project" {
		return nil, fmt.Errorf("import preflight requires a read client")
	}

	if err := checkPreflightTodolistCollisions(ctx, result, manifest, rows, client); err != nil {
		return nil, err
	}
	if result.Status == "blocked" {
		return result, nil
	}
	if err := checkPreflightTodoCollisions(ctx, result, manifest, rows, client); err != nil {
		return nil, err
	}
	return result, nil
}

func checkPreflightTodolistCollisions(ctx context.Context, result *PreflightResult, manifest *ImportArtifactManifest, rows []artifactTodoRow, client ArtifactPreflightClient) error {
	if manifest.Destination.Mode != "existing_project" || !shouldCreateTodolists(&manifest.Destination) {
		result.Checks = append(result.Checks, PreflightCheck{Name: "todolist_name_collisions", Status: "passed", Message: "Artifact execution does not create todolists in an existing project."})
		return nil
	}

	plannedNames := artifactTodolistNames(rows)
	if len(plannedNames) == 0 {
		result.Checks = append(result.Checks, PreflightCheck{Name: "todolist_name_collisions", Status: "passed", Message: "Artifact execution does not create todolists."})
		return nil
	}

	projectID, err := parseOptionalInt64(manifest.Destination.ProjectID)
	if err != nil {
		result.Status = "blocked"
		result.Checks = append(result.Checks, PreflightCheck{Name: "todolist_name_collisions", Status: "blocked", Message: fmt.Sprintf("Invalid artifact destination project_id: %v", err)})
		return nil
	}
	if projectID == 0 {
		result.Status = "blocked"
		result.Checks = append(result.Checks, PreflightCheck{Name: "todolist_name_collisions", Status: "blocked", Message: "Artifact destination project_id is required to check todolist collisions."})
		return nil
	}

	existing, err := client.ExistingTodolists(ctx, projectID)
	if err != nil {
		return err
	}
	collisions := todolistCollisions(plannedNames, existing)
	if len(collisions) > 0 {
		result.Status = "blocked"
		result.Collisions = collisions
		result.Checks = append(result.Checks, PreflightCheck{Name: "todolist_name_collisions", Status: "blocked", Message: fmt.Sprintf("%d planned todolist name(s) already exist in the destination project.", len(collisions))})
		return nil
	}

	result.Checks = append(result.Checks, PreflightCheck{Name: "todolist_name_collisions", Status: "passed", Message: fmt.Sprintf("Checked %d planned todolist name(s) against existing Basecamp todolists.", len(plannedNames))})
	return nil
}

func checkPreflightTodoCollisions(ctx context.Context, result *PreflightResult, manifest *ImportArtifactManifest, rows []artifactTodoRow, client ArtifactPreflightClient) error {
	if manifest.Destination.Mode != "existing_project" || manifest.Destination.TodolistStrategy != "existing_todolist" {
		result.Checks = append(result.Checks, PreflightCheck{Name: "todo_title_collisions", Status: "passed", Message: "Artifact execution does not add todos to an existing todolist."})
		return nil
	}
	targets, targetIssue := todoCollisionTargets(manifest, rows)
	if targetIssue != "" {
		result.Status = "blocked"
		result.Checks = append(result.Checks, PreflightCheck{Name: "todo_title_collisions", Status: "blocked", Message: targetIssue})
		return nil
	}

	allCollisions := make([]TodoCollision, 0)
	checked := 0
	for todolistID, targetRows := range targets {
		existing, err := client.ExistingTodos(ctx, todolistID)
		if err != nil {
			return err
		}
		allCollisions = append(allCollisions, todoCollisions(targetRows, todolistID, existing)...)
		checked += len(targetRows)
	}
	if len(allCollisions) > 0 {
		result.Status = "blocked"
		result.TodoCollisions = allCollisions
		result.Checks = append(result.Checks, PreflightCheck{Name: "todo_title_collisions", Status: "blocked", Message: fmt.Sprintf("%d planned todo title(s) already exist in destination todolists.", len(allCollisions))})
		return nil
	}
	result.Checks = append(result.Checks, PreflightCheck{Name: "todo_title_collisions", Status: "passed", Message: fmt.Sprintf("Checked %d planned todo title(s) against existing Basecamp todos.", checked)})
	return nil
}

func todoCollisionTargets(manifest *ImportArtifactManifest, rows []artifactTodoRow) (map[int64][]artifactTodoRow, string) {
	manifestID, err := parseOptionalInt64(manifest.Destination.TodolistID)
	if err != nil {
		return nil, fmt.Sprintf("invalid destination todolist_id: %v", err)
	}
	targets := make(map[int64][]artifactTodoRow)
	for _, row := range rows {
		id := row.TodolistID
		if id == 0 {
			id = manifestID
		}
		if id == 0 {
			return nil, "artifact destination todolist_id is required to check todo title collisions"
		}
		targets[id] = append(targets[id], row)
	}
	return targets, ""
}

func preflightExecutionLedgerCheck(artifactDir string) PreflightCheck {
	path := filepath.Join(artifactDir, artifactExecutionFileName)
	if _, err := os.Stat(path); err == nil {
		return PreflightCheck{Name: "execution_ledger", Status: "blocked", Message: "Artifact already has execution.json; execution refuses to run again."}
	} else if !os.IsNotExist(err) {
		return PreflightCheck{Name: "execution_ledger", Status: "blocked", Message: fmt.Sprintf("Execution ledger cannot be checked: %v", err)}
	}
	return PreflightCheck{Name: "execution_ledger", Status: "passed", Message: "No execution ledger exists for this artifact."}
}

func todoCollisions(rows []artifactTodoRow, todolistID int64, existing []ExistingTodo) []TodoCollision {
	byTitle := make(map[string]ExistingTodo)
	for _, todo := range existing {
		title := strings.TrimSpace(todo.Title)
		if title == "" {
			continue
		}
		byTitle[strings.ToLower(title)] = ExistingTodo{ID: todo.ID, Title: title}
	}
	collisions := make([]TodoCollision, 0)
	for _, row := range rows {
		title := strings.TrimSpace(row.Title)
		if title == "" {
			continue
		}
		if existing, ok := byTitle[strings.ToLower(title)]; ok {
			collisions = append(collisions, TodoCollision{SourceRow: row.SourceRow, Title: title, TodolistID: todolistID, ExistingID: existing.ID})
		}
	}
	return collisions
}

func todolistCollisions(plannedNames []string, existing []ExistingTodolist) []TodolistCollision {
	byName := make(map[string]ExistingTodolist)
	for _, list := range existing {
		name := strings.TrimSpace(list.Name)
		if name == "" {
			continue
		}
		byName[strings.ToLower(name)] = ExistingTodolist{ID: list.ID, Name: name}
	}
	collisions := make([]TodolistCollision, 0)
	for _, planned := range plannedNames {
		name := strings.TrimSpace(planned)
		if name == "" {
			name = "Imported todos"
		}
		if existing, ok := byName[strings.ToLower(name)]; ok {
			collisions = append(collisions, TodolistCollision{Name: name, ExistingID: existing.ID})
		}
	}
	return collisions
}

// BlockedMessage summarizes blockers for command errors.
func (r *PreflightResult) BlockedMessage() string {
	if r == nil || r.Status != "blocked" {
		return ""
	}
	messages := make([]string, 0, len(r.Checks)+len(r.Collisions)+len(r.TodoCollisions))
	for _, check := range r.Checks {
		if check.Status == "blocked" {
			messages = append(messages, check.Message)
		}
	}
	for _, collision := range r.Collisions {
		messages = append(messages, fmt.Sprintf("Todolist %q already exists with ID %d.", collision.Name, collision.ExistingID))
	}
	for _, collision := range r.TodoCollisions {
		messages = append(messages, fmt.Sprintf("Todo %q from source row %d already exists with ID %d.", collision.Title, collision.SourceRow, collision.ExistingID))
	}
	return strings.Join(messages, " ")
}
