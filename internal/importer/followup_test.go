package importer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCreateFollowupArtifactRequiresReviewed(t *testing.T) {
	outDir := failedExecutionArtifact(t)
	_, err := CreateFollowupArtifact(outDir, filepath.Join(t.TempDir(), "followup"), FollowupOptions{})
	if err == nil || !strings.Contains(err.Error(), "--reviewed required") {
		t.Fatalf("expected reviewed error, got %v", err)
	}
}

func TestCreateFollowupArtifactRejectsNonEmptyOutputDirectory(t *testing.T) {
	artifactDir := failedExecutionArtifact(t)
	followupDir := filepath.Join(t.TempDir(), "followup")
	if err := os.MkdirAll(followupDir, 0o755); err != nil {
		t.Fatalf("mkdir followup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(followupDir, "existing.txt"), []byte("keep"), 0o600); err != nil {
		t.Fatalf("seed followup: %v", err)
	}

	_, err := CreateFollowupArtifact(artifactDir, followupDir, FollowupOptions{Reviewed: true})
	if err == nil || !strings.Contains(err.Error(), "empty or not exist") {
		t.Fatalf("expected non-empty output error, got %v", err)
	}
}

func TestCreateFollowupArtifactFromFailedExistingTodolistExecution(t *testing.T) {
	artifactDir := compileSimpleExecutionArtifact(t, &DestinationConfig{SchemaVersion: planSchemaVersion, Mode: "existing_project", ProjectID: "123", TodolistStrategy: "existing_todolist", TodolistID: "456", TodolistName: "Imported"})
	client := &fakeWriteClient{failTodoRows: map[string]error{"Second": assertError("boom")}}
	_, _ = ExecuteArtifact(context.Background(), artifactDir, client, ExecuteOptions{Approved: true})

	followupDir := filepath.Join(t.TempDir(), "followup")
	result, err := CreateFollowupArtifact(artifactDir, followupDir, FollowupOptions{Reviewed: true})
	if err != nil {
		t.Fatalf("CreateFollowupArtifact() error = %v", err)
	}
	if result.Status != "compiled" || result.Manifest.Counts.Todos != 1 || len(result.PendingTodos) != 1 {
		t.Fatalf("result = %+v", result)
	}
	manifest, rows, err := readArtifact(followupDir)
	if err != nil {
		t.Fatalf("read followup: %v", err)
	}
	if manifest.Destination.TodolistStrategy != "existing_todolist" || manifest.Destination.TodolistID != "456" {
		t.Fatalf("destination = %+v", manifest.Destination)
	}
	if len(rows) != 1 || rows[0].SourceRow != 2 || rows[0].Title != "Second" || rows[0].TodolistID != 456 {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestCreateFollowupArtifactUsesCreatedTodolistIDs(t *testing.T) {
	artifactDir := failedExecutionArtifact(t)
	followupDir := filepath.Join(t.TempDir(), "followup")

	_, err := CreateFollowupArtifact(artifactDir, followupDir, FollowupOptions{Reviewed: true})
	if err != nil {
		t.Fatalf("CreateFollowupArtifact() error = %v", err)
	}
	manifest, rows, err := readArtifact(followupDir)
	if err != nil {
		t.Fatalf("read followup: %v", err)
	}
	if manifest.Counts.Todos != 1 || manifest.Counts.Todolists != 0 || manifest.Destination.TodolistStrategy != "existing_todolist" {
		t.Fatalf("manifest = %+v", manifest)
	}
	if rows[0].SourceRow != 2 || rows[0].TodolistID == 0 {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestCreateFollowupArtifactRejectsCompletedExecution(t *testing.T) {
	artifactDir := compileSimpleExecutionArtifact(t, &DestinationConfig{SchemaVersion: planSchemaVersion, Mode: "existing_project", ProjectID: "123", TodolistStrategy: "existing_todolist", TodolistID: "456", TodolistName: "Imported"})
	if _, err := ExecuteArtifact(context.Background(), artifactDir, &fakeWriteClient{}, ExecuteOptions{Approved: true}); err != nil {
		t.Fatalf("ExecuteArtifact() error = %v", err)
	}

	_, err := CreateFollowupArtifact(artifactDir, filepath.Join(t.TempDir(), "followup"), FollowupOptions{Reviewed: true})
	if err == nil || !strings.Contains(err.Error(), "review_required") {
		t.Fatalf("expected review_required error, got %v", err)
	}
}

func failedExecutionArtifact(t *testing.T) string {
	t.Helper()
	artifactDir := compileSimpleExecutionArtifact(t, &DestinationConfig{SchemaVersion: planSchemaVersion, Mode: "existing_project", ProjectID: "123", TodolistStrategy: "create_from_column"})
	client := &fakeWriteClient{failTodoRows: map[string]error{"Second": assertError("boom")}}
	_, _ = ExecuteArtifact(context.Background(), artifactDir, client, ExecuteOptions{Approved: true})
	return artifactDir
}
