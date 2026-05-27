package importer

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestRepairArtifactReportsNotExecuted(t *testing.T) {
	outDir := compileSimpleExecutionArtifact(t, &DestinationConfig{SchemaVersion: planSchemaVersion, Mode: "existing_project", ProjectID: "123", TodolistStrategy: "existing_todolist", TodolistID: "456", TodolistName: "Imported"})

	result, err := RepairArtifact(outDir)
	if err != nil {
		t.Fatalf("RepairArtifact() error = %v", err)
	}
	if result.Status != "not_executed" || result.ExecutionStatus != "" {
		t.Fatalf("result = %+v", result)
	}
}

func TestRepairArtifactReportsCompleted(t *testing.T) {
	outDir := compileSimpleExecutionArtifact(t, &DestinationConfig{SchemaVersion: planSchemaVersion, Mode: "existing_project", ProjectID: "123", TodolistStrategy: "existing_todolist", TodolistID: "456", TodolistName: "Imported"})
	if _, err := ExecuteArtifact(context.Background(), outDir, &fakeWriteClient{}, ExecuteOptions{Approved: true}); err != nil {
		t.Fatalf("ExecuteArtifact() error = %v", err)
	}

	result, err := RepairArtifact(outDir)
	if err != nil {
		t.Fatalf("RepairArtifact() error = %v", err)
	}
	if result.Status != "completed" || result.ExecutionStatus != "completed" || result.Created.Todos != 2 {
		t.Fatalf("result = %+v", result)
	}
	if len(result.PendingTodos) != 0 || len(result.CompletedOperations) != 2 {
		t.Fatalf("result = %+v", result)
	}
}

func TestRepairArtifactReportsFailedPartialExecution(t *testing.T) {
	outDir := compileSimpleExecutionArtifact(t, &DestinationConfig{SchemaVersion: planSchemaVersion, Mode: "existing_project", ProjectID: "123", TodolistStrategy: "create_from_column"})
	client := &fakeWriteClient{failTodoRows: map[string]error{"Second": assertError("boom")}}
	_, _ = ExecuteArtifact(context.Background(), outDir, client, ExecuteOptions{Approved: true})

	result, err := RepairArtifact(outDir)
	if err != nil {
		t.Fatalf("RepairArtifact() error = %v", err)
	}
	if result.Status != "review_required" || result.ExecutionStatus != "failed" {
		t.Fatalf("result = %+v", result)
	}
	if len(result.FailedOperations) != 1 || result.FailedOperations[0].SourceRow != 2 {
		t.Fatalf("failed operations = %+v", result.FailedOperations)
	}
	if len(result.PendingTodos) != 1 || result.PendingTodos[0].SourceRow != 2 || result.PendingTodos[0].Title != "Second" {
		t.Fatalf("pending todos = %+v", result.PendingTodos)
	}
}

func TestRepairArtifactReportsLedgerUnreadable(t *testing.T) {
	outDir := compileSimpleExecutionArtifact(t, &DestinationConfig{SchemaVersion: planSchemaVersion, Mode: "existing_project", ProjectID: "123", TodolistStrategy: "existing_todolist", TodolistID: "456", TodolistName: "Imported"})
	if err := writeExecutionLedger(filepath.Join(outDir, artifactExecutionFileName), &ExecutionLedger{Status: "started"}); err != nil {
		t.Fatalf("write ledger: %v", err)
	}
	// Replace with malformed JSON after creating the file portably.
	if err := os.WriteFile(filepath.Join(outDir, artifactExecutionFileName), []byte("{"), 0o644); err != nil {
		t.Fatalf("write malformed ledger: %v", err)
	}

	result, err := RepairArtifact(outDir)
	if err != nil {
		t.Fatalf("RepairArtifact() error = %v", err)
	}
	if result.Status != "ledger_unreadable" {
		t.Fatalf("result = %+v", result)
	}
}
