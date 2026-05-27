package importer

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// FollowupOptions controls follow-up artifact generation.
type FollowupOptions struct {
	Reviewed bool
}

// FollowupArtifactResult reports a generated follow-up artifact.
type FollowupArtifactResult struct {
	SchemaVersion int                    `json:"schema_version"`
	Status        string                 `json:"status"`
	ArtifactPath  string                 `json:"artifact_path"`
	Manifest      ImportArtifactManifest `json:"manifest"`
	PendingTodos  []RepairPendingTodo    `json:"pending_todos"`
	Guidance      []string               `json:"guidance"`
}

// CreateFollowupArtifact writes a fresh artifact containing pending rows from a reviewed failed execution.
func CreateFollowupArtifact(artifactDir, outDir string, opts FollowupOptions) (*FollowupArtifactResult, error) {
	if strings.TrimSpace(outDir) == "" {
		return nil, fmt.Errorf("follow-up artifact output directory is required")
	}
	if !opts.Reviewed {
		return nil, fmt.Errorf("--reviewed required after reviewing Basecamp state and the repair summary")
	}
	if samePath(artifactDir, outDir) {
		return nil, fmt.Errorf("follow-up artifact output must be different from the source artifact")
	}
	if _, err := os.Stat(filepath.Join(outDir, artifactExecutionFileName)); err == nil {
		return nil, fmt.Errorf("follow-up artifact output already contains execution.json")
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("checking follow-up output: %w", err)
	}

	manifest, rows, err := readArtifact(artifactDir)
	if err != nil {
		return nil, err
	}
	repair, err := RepairArtifact(artifactDir)
	if err != nil {
		return nil, err
	}
	if repair.Status != "review_required" {
		return nil, fmt.Errorf("follow-up artifact requires repair status review_required, got %q", repair.Status)
	}
	if len(repair.PendingTodos) == 0 {
		return nil, fmt.Errorf("follow-up artifact has no pending todos")
	}

	projectID, projectName, err := followupProject(manifest, repair.CompletedOperations)
	if err != nil {
		return nil, err
	}
	listIDs := followupTodolistIDs(repair.CompletedOperations)
	completedTodoRows := completedTodoSourceRows(repair.CompletedOperations)

	pendingRows := make([]artifactTodoRow, 0, len(repair.PendingTodos))
	pendingSummaries := make([]RepairPendingTodo, 0, len(repair.PendingTodos))
	for _, row := range rows {
		if _, completed := completedTodoRows[row.SourceRow]; completed {
			continue
		}
		resolvedListID := row.TodolistID
		listName := strings.TrimSpace(row.TodolistName)
		if listName == "" {
			listName = "Imported todos"
		}
		if resolvedListID == 0 {
			resolvedListID = listIDs[listName]
		}
		if resolvedListID == 0 {
			return nil, fmt.Errorf("source row %d cannot be added to follow-up artifact because todolist %q has no created ID in execution.json", row.SourceRow, listName)
		}
		row.ProjectID = projectID
		row.ProjectName = projectName
		row.TodolistID = resolvedListID
		row.TodolistName = listName
		pendingRows = append(pendingRows, row)
		pendingSummaries = append(pendingSummaries, RepairPendingTodo{SourceRow: row.SourceRow, SourceRecordID: row.SourceRecordID, Title: row.Title, TodolistName: listName})
	}
	if len(pendingRows) == 0 {
		return nil, fmt.Errorf("follow-up artifact has no pending rows")
	}

	firstTodolistID := formatOptionalInt64(pendingRows[0].TodolistID)
	followupDestination := manifest.Destination
	followupDestination.Mode = "existing_project"
	followupDestination.ProjectID = projectID
	followupDestination.ProjectName = projectName
	followupDestination.TodolistStrategy = "existing_todolist"
	followupDestination.TodolistID = firstTodolistID
	followupDestination.TodolistName = pendingRows[0].TodolistName

	followupManifest := *manifest
	followupManifest.Status = "compiled"
	followupManifest.Destination = followupDestination
	followupManifest.Counts = PlanCounts{Todos: len(pendingRows)}
	followupManifest.Files = ArtifactFiles{Todos: artifactTodosFileName}

	if err := writeArtifact(outDir, followupManifest, pendingRows); err != nil {
		return nil, err
	}
	return &FollowupArtifactResult{
		SchemaVersion: planSchemaVersion,
		Status:        "compiled",
		ArtifactPath:  outDir,
		Manifest:      followupManifest,
		PendingTodos:  pendingSummaries,
		Guidance: []string{
			"Review the follow-up artifact plan before preflight and execution.",
			"The source artifact remains closed and must not be rerun.",
		},
	}, nil
}

func followupProject(manifest *ImportArtifactManifest, operations []ExecutionLedgerOperation) (string, string, error) {
	if manifest.Destination.Mode == "existing_project" {
		if strings.TrimSpace(manifest.Destination.ProjectID) == "" {
			return "", "", fmt.Errorf("source artifact destination project_id is required")
		}
		return manifest.Destination.ProjectID, manifest.Destination.ProjectName, nil
	}
	for _, op := range operations {
		if op.Op == "create_project" && op.Status == "completed" && op.CreatedID != 0 {
			return strconv.FormatInt(op.CreatedID, 10), op.ProjectName, nil
		}
	}
	return "", "", fmt.Errorf("new_project follow-up requires a completed create_project operation in execution.json")
}

func followupTodolistIDs(operations []ExecutionLedgerOperation) map[string]int64 {
	out := make(map[string]int64)
	for _, op := range operations {
		if op.Op != "create_todolist" || op.Status != "completed" {
			continue
		}
		name := strings.TrimSpace(op.TodolistName)
		if name == "" {
			name = "Imported todos"
		}
		id := op.CreatedID
		if id == 0 {
			id = op.TodolistID
		}
		if id != 0 {
			out[name] = id
		}
	}
	return out
}

func completedTodoSourceRows(operations []ExecutionLedgerOperation) map[int]struct{} {
	out := make(map[int]struct{})
	for _, op := range operations {
		if op.Op == "create_todo" && op.Status == "completed" && op.SourceRow != 0 {
			out[op.SourceRow] = struct{}{}
		}
	}
	return out
}

func samePath(a, b string) bool {
	absA, errA := filepath.Abs(a)
	absB, errB := filepath.Abs(b)
	if errA != nil || errB != nil {
		return a == b
	}
	return absA == absB
}
