package importer

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestStatusArtifactReportsNotExecuted(t *testing.T) {
	outDir := compileSimpleExecutionArtifact(t, &DestinationConfig{SchemaVersion: planSchemaVersion, Mode: "existing_project", ProjectID: "123", TodolistStrategy: "existing_todolist", TodolistID: "456", TodolistName: "Imported"})

	status, err := StatusArtifact(outDir)
	if err != nil {
		t.Fatalf("StatusArtifact() error = %v", err)
	}
	if status.Status != "not_executed" || status.Execution != nil {
		t.Fatalf("status = %+v", status)
	}
	if status.Counts.Todos != 2 || status.ArtifactFormat != artifactFormat {
		t.Fatalf("status summary = %+v", status)
	}
}

func TestStatusArtifactReportsCompletedExecution(t *testing.T) {
	outDir := compileSimpleExecutionArtifact(t, &DestinationConfig{SchemaVersion: planSchemaVersion, Mode: "existing_project", ProjectID: "123", TodolistStrategy: "existing_todolist", TodolistID: "456", TodolistName: "Imported"})
	if _, err := ExecuteArtifact(context.Background(), outDir, &fakeWriteClient{}, ExecuteOptions{Approved: true}); err != nil {
		t.Fatalf("ExecuteArtifact() error = %v", err)
	}

	status, err := StatusArtifact(outDir)
	if err != nil {
		t.Fatalf("StatusArtifact() error = %v", err)
	}
	if status.Status != "completed" || status.Execution == nil || status.Execution.Created.Todos != 2 {
		t.Fatalf("status = %+v", status)
	}
}

func TestStatusArtifactReportsFailedExecution(t *testing.T) {
	outDir := compileSimpleExecutionArtifact(t, &DestinationConfig{SchemaVersion: planSchemaVersion, Mode: "existing_project", ProjectID: "123", TodolistStrategy: "create_from_column"})
	client := &fakeWriteClient{failTodoRows: map[string]error{"Second": assertError("boom")}}
	_, _ = ExecuteArtifact(context.Background(), outDir, client, ExecuteOptions{Approved: true})

	status, err := StatusArtifact(outDir)
	if err != nil {
		t.Fatalf("StatusArtifact() error = %v", err)
	}
	if status.Status != "failed" || status.Execution == nil || status.Execution.Error == "" {
		t.Fatalf("status = %+v", status)
	}
}

func TestStatusArtifactReportsUnreadableLedger(t *testing.T) {
	outDir := compileSimpleExecutionArtifact(t, &DestinationConfig{SchemaVersion: planSchemaVersion, Mode: "existing_project", ProjectID: "123", TodolistStrategy: "existing_todolist", TodolistID: "456", TodolistName: "Imported"})
	if err := os.WriteFile(filepath.Join(outDir, artifactExecutionFileName), []byte("{"), 0o644); err != nil {
		t.Fatalf("write ledger: %v", err)
	}

	status, err := StatusArtifact(outDir)
	if err != nil {
		t.Fatalf("StatusArtifact() error = %v", err)
	}
	if status.Status != "ledger_unreadable" || status.Execution != nil {
		t.Fatalf("status = %+v", status)
	}
}

type assertError string

func (e assertError) Error() string { return string(e) }
