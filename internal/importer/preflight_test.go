package importer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakePreflightClient struct {
	todolists     []ExistingTodolist
	todos         []ExistingTodo
	todosByList   map[int64][]ExistingTodo
	cardColumns   []ExistingCardColumn
	cards         []ExistingCard
	cardsByColumn map[int64][]ExistingCard
}

func (f fakePreflightClient) CardTableID(ctx context.Context, projectID int64) (int64, error) {
	return 888, nil
}

func (f fakePreflightClient) ExistingCardColumns(ctx context.Context, cardTableID int64) ([]ExistingCardColumn, error) {
	return f.cardColumns, nil
}

func (f fakePreflightClient) ExistingCards(ctx context.Context, columnID int64) ([]ExistingCard, error) {
	if f.cardsByColumn != nil {
		return f.cardsByColumn[columnID], nil
	}
	return f.cards, nil
}

func (f fakePreflightClient) ExistingTodolists(ctx context.Context, projectID int64) ([]ExistingTodolist, error) {
	return f.todolists, nil
}

func (f fakePreflightClient) ExistingTodos(ctx context.Context, todolistID int64) ([]ExistingTodo, error) {
	if f.todosByList != nil {
		return f.todosByList[todolistID], nil
	}
	return f.todos, nil
}

func TestPreflightArtifactPassesWithoutCollisions(t *testing.T) {
	outDir := compileSimpleExecutionArtifact(t, &DestinationConfig{SchemaVersion: planSchemaVersion, Mode: "existing_project", ProjectID: "123", TodolistStrategy: "create_from_column"})
	client := fakePreflightClient{todolists: []ExistingTodolist{{ID: 1, Name: "Existing"}}}

	result, err := PreflightArtifact(context.Background(), outDir, client)
	if err != nil {
		t.Fatalf("PreflightArtifact() error = %v", err)
	}
	if result.Status != "passed" || len(result.Collisions) != 0 {
		t.Fatalf("result = %+v", result)
	}
	if !preflightHasCheck(result, "execution_ledger", "passed") || !preflightHasCheck(result, "todolist_name_collisions", "passed") {
		t.Fatalf("checks = %+v", result.Checks)
	}
}

func TestPreflightArtifactBlocksTodolistNameCollisions(t *testing.T) {
	outDir := compileSimpleExecutionArtifact(t, &DestinationConfig{SchemaVersion: planSchemaVersion, Mode: "existing_project", ProjectID: "123", TodolistStrategy: "create_from_column"})
	client := fakePreflightClient{todolists: []ExistingTodolist{{ID: 10, Name: "backlog"}}}

	result, err := PreflightArtifact(context.Background(), outDir, client)
	if err != nil {
		t.Fatalf("PreflightArtifact() error = %v", err)
	}
	if result.Status != "blocked" || len(result.Collisions) != 1 {
		t.Fatalf("result = %+v", result)
	}
	if result.Collisions[0].Name != "Backlog" || result.Collisions[0].ExistingID != 10 {
		t.Fatalf("collisions = %+v", result.Collisions)
	}
	if !strings.Contains(result.BlockedMessage(), "Backlog") {
		t.Fatalf("blocked message = %q", result.BlockedMessage())
	}
}

func TestPreflightArtifactBlocksTodoTitleCollisions(t *testing.T) {
	outDir := compileSimpleExecutionArtifact(t, &DestinationConfig{SchemaVersion: planSchemaVersion, Mode: "existing_project", ProjectID: "123", TodolistStrategy: "existing_todolist", TodolistID: "456", TodolistName: "Imported"})
	client := fakePreflightClient{todos: []ExistingTodo{{ID: 99, Title: "first"}}}

	result, err := PreflightArtifact(context.Background(), outDir, client)
	if err != nil {
		t.Fatalf("PreflightArtifact() error = %v", err)
	}
	if result.Status != "blocked" || len(result.TodoCollisions) != 1 {
		t.Fatalf("result = %+v", result)
	}
	collision := result.TodoCollisions[0]
	if collision.SourceRow != 1 || collision.Title != "First" || collision.TodolistID != 456 || collision.ExistingID != 99 {
		t.Fatalf("todo collision = %+v", collision)
	}
	if !strings.Contains(result.BlockedMessage(), "source row 1") {
		t.Fatalf("blocked message = %q", result.BlockedMessage())
	}
}

func TestPreflightArtifactChecksExistingTodolistTodos(t *testing.T) {
	outDir := compileSimpleExecutionArtifact(t, &DestinationConfig{SchemaVersion: planSchemaVersion, Mode: "existing_project", ProjectID: "123", TodolistStrategy: "existing_todolist", TodolistID: "456", TodolistName: "Imported"})
	client := fakePreflightClient{todos: []ExistingTodo{{ID: 99, Title: "Already there"}}}

	result, err := PreflightArtifact(context.Background(), outDir, client)
	if err != nil {
		t.Fatalf("PreflightArtifact() error = %v", err)
	}
	if result.Status != "passed" || !preflightHasCheck(result, "todo_title_collisions", "passed") {
		t.Fatalf("result = %+v", result)
	}
}

func TestPreflightArtifactBlocksCardColumnNameCollisions(t *testing.T) {
	outDir := compileSimpleExecutionArtifact(t, &DestinationConfig{SchemaVersion: planSchemaVersion, ResourceType: resourceTypeCards, Mode: "existing_project", ProjectID: "123", CardTableID: "888", ColumnStrategy: "create_from_column"})
	client := fakePreflightClient{cardColumns: []ExistingCardColumn{{ID: 42, Name: "Backlog"}}}

	result, err := PreflightArtifact(context.Background(), outDir, client)
	if err != nil {
		t.Fatalf("PreflightArtifact() error = %v", err)
	}
	if result.Status != "blocked" || len(result.ColumnCollisions) != 1 || result.ColumnCollisions[0].Name != "Backlog" {
		t.Fatalf("result = %+v", result)
	}
}

func TestPreflightArtifactBlocksExistingExecutionLedger(t *testing.T) {
	outDir := compileSimpleExecutionArtifact(t, &DestinationConfig{SchemaVersion: planSchemaVersion, Mode: "existing_project", ProjectID: "123", TodolistStrategy: "create_from_column"})
	if err := os.WriteFile(filepath.Join(outDir, artifactExecutionFileName), []byte(`{"status":"completed"}`), 0o644); err != nil {
		t.Fatalf("write ledger: %v", err)
	}

	result, err := PreflightArtifact(context.Background(), outDir, fakePreflightClient{})
	if err != nil {
		t.Fatalf("PreflightArtifact() error = %v", err)
	}
	if result.Status != "blocked" || !preflightHasCheck(result, "execution_ledger", "blocked") {
		t.Fatalf("result = %+v", result)
	}
}

func TestPreflightArtifactSkipsCollisionCheckForNewProject(t *testing.T) {
	outDir := compileSimpleExecutionArtifact(t, &DestinationConfig{SchemaVersion: planSchemaVersion, Mode: "new_project", ProjectName: "Imported", TodolistStrategy: "create_from_column"})

	result, err := PreflightArtifact(context.Background(), outDir, nil)
	if err != nil {
		t.Fatalf("PreflightArtifact() error = %v", err)
	}
	if result.Status != "passed" || !preflightHasCheck(result, "todolist_name_collisions", "passed") {
		t.Fatalf("result = %+v", result)
	}
}

func preflightHasCheck(result *PreflightResult, name, status string) bool {
	for _, check := range result.Checks {
		if check.Name == name && check.Status == status {
			return true
		}
	}
	return false
}
