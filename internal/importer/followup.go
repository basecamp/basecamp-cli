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
	PendingTodos  []RepairPendingRecord  `json:"pending_todos,omitempty"`
	PendingCards  []RepairPendingRecord  `json:"pending_cards,omitempty"`
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
	resourceType, err := destinationResourceType(&manifest.Destination)
	if err != nil {
		return nil, err
	}
	pendingRecords := repair.PendingTodos
	if resourceType == resourceTypeCards {
		pendingRecords = repair.PendingCards
	}
	if len(pendingRecords) == 0 {
		return nil, fmt.Errorf("follow-up artifact has no pending %s", resourceType)
	}

	projectID, projectName, err := followupProject(manifest, repair.CompletedOperations)
	if err != nil {
		return nil, err
	}
	groupIDs := followupGroupIDs(resourceType, repair.CompletedOperations)
	completedRows := completedSourceRows(resourceType, repair.CompletedOperations)

	pendingRows := make([]artifactTodoRow, 0, len(pendingRecords))
	pendingSummaries := make([]RepairPendingRecord, 0, len(pendingRecords))
	for _, row := range rows {
		if _, completed := completedRows[row.SourceRow]; completed {
			continue
		}
		resolvedGroupID := row.TodolistID
		groupName := strings.TrimSpace(row.TodolistName)
		if groupName == "" {
			groupName = "Imported todos"
			if resourceType == resourceTypeCards {
				groupName = "Imported cards"
			}
		}
		if resolvedGroupID == 0 {
			resolvedGroupID = groupIDs[groupName]
		}
		if resolvedGroupID == 0 {
			return nil, fmt.Errorf("source row %d cannot be added to follow-up artifact because %q has no created ID in execution.json", row.SourceRow, groupName)
		}
		row.ProjectID = projectID
		row.ProjectName = projectName
		row.TodolistID = resolvedGroupID
		row.TodolistName = groupName
		pendingRows = append(pendingRows, row)
		pendingSummaries = append(pendingSummaries, RepairPendingRecord{SourceRow: row.SourceRow, SourceRecordID: row.SourceRecordID, Title: row.Title, GroupName: groupName})
	}
	if len(pendingRows) == 0 {
		return nil, fmt.Errorf("follow-up artifact has no pending rows")
	}

	firstGroupID := formatOptionalInt64(pendingRows[0].TodolistID)
	followupDestination := manifest.Destination
	followupDestination.Mode = "existing_project"
	followupDestination.ProjectID = projectID
	followupDestination.ProjectName = projectName
	if resourceType == resourceTypeCards {
		followupDestination.ColumnStrategy = "existing_column"
		followupDestination.ColumnID = firstGroupID
		followupDestination.ColumnName = pendingRows[0].TodolistName
	} else {
		followupDestination.TodolistStrategy = "existing_todolist"
		followupDestination.TodolistID = firstGroupID
		followupDestination.TodolistName = pendingRows[0].TodolistName
	}

	followupCounts := PlanCounts{Todos: len(pendingRows)}
	followupFiles := ArtifactFiles{Todos: artifactTodosFileName}
	if resourceType == resourceTypeCards {
		followupCounts = PlanCounts{Cards: len(pendingRows)}
		followupFiles = ArtifactFiles{Cards: artifactCardsFileName}
	}
	followupManifest := *manifest
	followupManifest.Status = "compiled"
	followupManifest.Destination = followupDestination
	followupManifest.Counts = followupCounts
	followupManifest.Files = followupFiles

	if err := writeArtifact(outDir, followupManifest, pendingRows); err != nil {
		return nil, err
	}
	return &FollowupArtifactResult{
		SchemaVersion: planSchemaVersion,
		Status:        "compiled",
		ArtifactPath:  outDir,
		Manifest:      followupManifest,
		PendingTodos:  pendingSummariesFor(resourceTypeTodos, resourceType, pendingSummaries),
		PendingCards:  pendingSummariesFor(resourceTypeCards, resourceType, pendingSummaries),
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

func pendingSummariesFor(want, got string, summaries []RepairPendingRecord) []RepairPendingRecord {
	if want != got {
		return nil
	}
	return summaries
}

func followupGroupIDs(resourceType string, operations []ExecutionLedgerOperation) map[string]int64 {
	if resourceType == resourceTypeCards {
		out := make(map[string]int64)
		for _, op := range operations {
			if op.Op != "create_card_column" || op.Status != "completed" {
				continue
			}
			name := strings.TrimSpace(op.ColumnName)
			if name == "" {
				name = "Imported cards"
			}
			id := op.CreatedID
			if id == 0 {
				id = op.ColumnID
			}
			if id != 0 {
				out[name] = id
			}
		}
		return out
	}
	return followupTodolistIDs(operations)
}

func completedSourceRows(resourceType string, operations []ExecutionLedgerOperation) map[int]struct{} {
	if resourceType == resourceTypeCards {
		return completedRowsForOperation(operations, "create_card")
	}
	return completedTodoSourceRows(operations)
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
	return completedRowsForOperation(operations, "create_todo")
}

func completedRowsForOperation(operations []ExecutionLedgerOperation, operationName string) map[int]struct{} {
	out := make(map[int]struct{})
	for _, op := range operations {
		if op.Op == operationName && op.Status == "completed" && op.SourceRow != 0 {
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
