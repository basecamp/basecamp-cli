package importer

// RepairResult summarizes a local artifact execution ledger for recovery review.
type RepairResult struct {
	SchemaVersion       int                        `json:"schema_version"`
	Status              string                     `json:"status"`
	ArtifactPath        string                     `json:"artifact_path"`
	ExecutionStatus     string                     `json:"execution_status,omitempty"`
	Created             ExecuteCounts              `json:"created,omitempty"`
	CompletedOperations []ExecutionLedgerOperation `json:"completed_operations,omitempty"`
	FailedOperations    []ExecutionLedgerOperation `json:"failed_operations,omitempty"`
	PendingTodos        []RepairPendingTodo        `json:"pending_todos,omitempty"`
	PendingCards        []RepairPendingTodo        `json:"pending_cards,omitempty"`
	Guidance            []string                   `json:"guidance"`
}

// RepairPendingTodo identifies an artifact todo row that has no completed ledger operation.
type RepairPendingTodo struct {
	SourceRow      int    `json:"source_row"`
	SourceRecordID string `json:"source_record_id,omitempty"`
	Title          string `json:"title"`
	TodolistName   string `json:"todolist_name,omitempty"`
}

// RepairArtifact reads local artifact and execution files and summarizes recovery state.
func RepairArtifact(artifactDir string) (*RepairResult, error) {
	manifest, rows, err := readArtifact(artifactDir)
	if err != nil {
		return nil, err
	}
	status, err := StatusArtifact(artifactDir)
	if err != nil {
		return nil, err
	}

	result := &RepairResult{
		SchemaVersion: planSchemaVersion,
		Status:        "not_executed",
		ArtifactPath:  artifactDir,
		Guidance:      []string{"This artifact has no execution ledger. Run preflight before approved execution."},
	}
	if status.Execution == nil {
		if status.Status == "ledger_unreadable" {
			result.Status = "ledger_unreadable"
			result.Guidance = []string{"The execution ledger cannot be read. Inspect execution.json before using this artifact."}
		}
		return result, nil
	}

	ledger := status.Execution
	result.ExecutionStatus = ledger.Status
	result.Created = ledger.Created
	result.CompletedOperations, result.FailedOperations = splitLedgerOperations(ledger.Operations)
	resourceType, err := destinationResourceType(&manifest.Destination)
	if err != nil {
		return nil, err
	}
	if resourceType == resourceTypeCards {
		result.PendingCards = pendingRecordsForRepair(rows, ledger.Operations, "create_card")
	} else {
		result.PendingTodos = pendingRecordsForRepair(rows, ledger.Operations, "create_todo")
	}

	switch ledger.Status {
	case "completed":
		result.Status = "completed"
		result.Guidance = []string{"Execution completed. This artifact is closed and cannot be executed again."}
	case "failed", "started":
		result.Status = "review_required"
		result.Guidance = []string{
			"Review completed_operations against Basecamp before taking further action.",
			"Review failed_operations and pending_todos before creating a fresh follow-up artifact.",
			"Do not remove execution.json to rerun this artifact.",
		}
	default:
		result.Status = "review_required"
		result.Guidance = []string{"The execution ledger has an unrecognized status. Review execution.json before using this artifact."}
	}
	return result, nil
}

func splitLedgerOperations(operations []ExecutionLedgerOperation) ([]ExecutionLedgerOperation, []ExecutionLedgerOperation) {
	completed := make([]ExecutionLedgerOperation, 0)
	failed := make([]ExecutionLedgerOperation, 0)
	for _, op := range operations {
		switch op.Status {
		case "completed":
			completed = append(completed, op)
		case "failed":
			failed = append(failed, op)
		}
	}
	return completed, failed
}

func pendingRecordsForRepair(rows []artifactTodoRow, operations []ExecutionLedgerOperation, operationName string) []RepairPendingTodo {
	completedRows := make(map[int]struct{})
	for _, op := range operations {
		if op.Op == operationName && op.Status == "completed" && op.SourceRow != 0 {
			completedRows[op.SourceRow] = struct{}{}
		}
	}
	pending := make([]RepairPendingTodo, 0)
	for _, row := range rows {
		if _, ok := completedRows[row.SourceRow]; ok {
			continue
		}
		pending = append(pending, RepairPendingTodo{SourceRow: row.SourceRow, SourceRecordID: row.SourceRecordID, Title: row.Title, TodolistName: row.TodolistName})
	}
	return pending
}
