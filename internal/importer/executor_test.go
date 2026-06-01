package importer

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

type fakeWriteClient struct {
	nextID       int64
	projects     []string
	todolists    []fakeCreatedTodolist
	todos        []fakeCreatedTodo
	cardColumns  []fakeCreatedCardColumn
	cards        []fakeCreatedCard
	failTodoRows map[string]error
}

type fakeCreatedTodolist struct {
	ProjectID int64
	Name      string
	ID        int64
}

type fakeCreatedTodo struct {
	TodolistID int64
	Todo       ExecutableTodo
	ID         int64
}

type fakeCreatedCardColumn struct {
	CardTableID int64
	Name        string
	ID          int64
}

type fakeCreatedCard struct {
	ColumnID int64
	Card     ExecutableCard
	ID       int64
}

func (f *fakeWriteClient) CreateProject(ctx context.Context, name string) (int64, error) {
	f.projects = append(f.projects, name)
	return f.next(), nil
}

func (f *fakeWriteClient) CreateTodolist(ctx context.Context, projectID int64, name string) (int64, error) {
	id := f.next()
	f.todolists = append(f.todolists, fakeCreatedTodolist{ProjectID: projectID, Name: name, ID: id})
	return id, nil
}

func (f *fakeWriteClient) CreateTodo(ctx context.Context, todolistID int64, todo ExecutableTodo) (int64, error) {
	if f.failTodoRows != nil {
		if err := f.failTodoRows[todo.Title]; err != nil {
			return 0, err
		}
	}
	id := f.next()
	f.todos = append(f.todos, fakeCreatedTodo{TodolistID: todolistID, Todo: todo, ID: id})
	return id, nil
}

func (f *fakeWriteClient) CardTableID(ctx context.Context, projectID int64) (int64, error) {
	return 888, nil
}

func (f *fakeWriteClient) CreateCardColumn(ctx context.Context, cardTableID int64, name string) (int64, error) {
	id := f.next()
	f.cardColumns = append(f.cardColumns, fakeCreatedCardColumn{CardTableID: cardTableID, Name: name, ID: id})
	return id, nil
}

func (f *fakeWriteClient) CreateCard(ctx context.Context, columnID int64, card ExecutableCard) (int64, error) {
	id := f.next()
	f.cards = append(f.cards, fakeCreatedCard{ColumnID: columnID, Card: card, ID: id})
	return id, nil
}

func (f *fakeWriteClient) next() int64 {
	if f.nextID == 0 {
		f.nextID = 100
	}
	id := f.nextID
	f.nextID++
	return id
}

func TestExecuteArtifactRequiresApproval(t *testing.T) {
	outDir := compileSimpleExecutionArtifact(t, &DestinationConfig{SchemaVersion: planSchemaVersion, Mode: "existing_project", ProjectID: "123", TodolistStrategy: "existing_todolist", TodolistID: "456", TodolistName: "Imported"})
	_, err := ExecuteArtifact(context.Background(), outDir, &fakeWriteClient{}, ExecuteOptions{})
	if err == nil || !strings.Contains(err.Error(), "explicit approval") {
		t.Fatalf("expected approval error, got %v", err)
	}
}

func TestExecuteArtifactCreatesTodolistsAndTodos(t *testing.T) {
	outDir := compileSimpleExecutionArtifact(t, &DestinationConfig{SchemaVersion: planSchemaVersion, Mode: "existing_project", ProjectID: "123", TodolistStrategy: "create_from_column"})
	client := &fakeWriteClient{}

	result, err := ExecuteArtifact(context.Background(), outDir, client, ExecuteOptions{Approved: true})
	if err != nil {
		t.Fatalf("ExecuteArtifact() error = %v", err)
	}
	if result.Status != "completed" {
		t.Fatalf("status = %q", result.Status)
	}
	if result.Created.Projects != 0 || result.Created.Todolists != 2 || result.Created.Todos != 2 {
		t.Fatalf("created = %+v", result.Created)
	}
	if len(client.todolists) != 2 || client.todolists[0].Name != "Backlog" || client.todolists[1].Name != "Doing" {
		t.Fatalf("todolists = %+v", client.todolists)
	}
	if len(client.todos) != 2 || client.todos[0].Todo.Title != "First" || client.todos[1].Todo.Title != "Second" {
		t.Fatalf("todos = %+v", client.todos)
	}
	if client.todos[0].TodolistID != client.todolists[0].ID {
		t.Fatalf("first todo todolist ID = %d, want %d", client.todos[0].TodolistID, client.todolists[0].ID)
	}
}

func TestExecuteArtifactCreatesCardColumnsAndCards(t *testing.T) {
	outDir := compileSimpleExecutionArtifact(t, &DestinationConfig{SchemaVersion: planSchemaVersion, ResourceType: resourceTypeCards, Mode: "existing_project", ProjectID: "123", CardTableID: "888", ColumnStrategy: "create_from_column"})
	client := &fakeWriteClient{}

	result, err := ExecuteArtifact(context.Background(), outDir, client, ExecuteOptions{Approved: true})
	if err != nil {
		t.Fatalf("ExecuteArtifact() error = %v", err)
	}
	if result.Created.CardColumns != 2 || result.Created.Cards != 2 {
		t.Fatalf("created = %+v", result.Created)
	}
	if len(client.cardColumns) != 2 || client.cardColumns[0].Name != "Backlog" || len(client.cards) != 2 || client.cards[0].Card.Title != "First" {
		t.Fatalf("client = %+v", client)
	}
	var ledger ExecutionLedger
	if err := readJSONData(filepath.Join(outDir, artifactExecutionFileName), &ledger); err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	if ledger.Operations[0].Op != "create_card_column" || ledger.Operations[2].Op != "create_card" {
		t.Fatalf("ledger operations = %+v", ledger.Operations)
	}
}

func TestExecuteArtifactUsesExistingTodolist(t *testing.T) {
	outDir := compileSimpleExecutionArtifact(t, &DestinationConfig{SchemaVersion: planSchemaVersion, Mode: "existing_project", ProjectID: "123", TodolistStrategy: "existing_todolist", TodolistID: "456", TodolistName: "Imported"})
	client := &fakeWriteClient{}

	result, err := ExecuteArtifact(context.Background(), outDir, client, ExecuteOptions{Approved: true})
	if err != nil {
		t.Fatalf("ExecuteArtifact() error = %v", err)
	}
	if result.Created.Todolists != 0 || len(client.todolists) != 0 {
		t.Fatalf("expected no created todolists, result=%+v client=%+v", result, client.todolists)
	}
	if len(client.todos) != 2 || client.todos[0].TodolistID != 456 || client.todos[1].TodolistID != 456 {
		t.Fatalf("todos = %+v", client.todos)
	}
}

func TestExecuteArtifactWritesCompletedLedgerAndRefusesReplay(t *testing.T) {
	outDir := compileSimpleExecutionArtifact(t, &DestinationConfig{SchemaVersion: planSchemaVersion, Mode: "existing_project", ProjectID: "123", TodolistStrategy: "existing_todolist", TodolistID: "456", TodolistName: "Imported"})
	result, err := ExecuteArtifact(context.Background(), outDir, &fakeWriteClient{}, ExecuteOptions{Approved: true})
	if err != nil {
		t.Fatalf("ExecuteArtifact() error = %v", err)
	}
	if result.LedgerPath != filepath.Join(outDir, artifactExecutionFileName) {
		t.Fatalf("ledger path = %q", result.LedgerPath)
	}

	var ledger ExecutionLedger
	if err := readJSONData(filepath.Join(outDir, artifactExecutionFileName), &ledger); err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	if ledger.Status != "completed" || ledger.Created.Todos != 2 || ledger.CompletedAt == "" {
		t.Fatalf("ledger = %+v", ledger)
	}
	if len(ledger.Operations) != 2 || ledger.Operations[0].Op != "create_todo" || ledger.Operations[0].SourceRow != 1 || ledger.Operations[0].CreatedID == 0 {
		t.Fatalf("ledger operations = %+v", ledger.Operations)
	}

	client := &fakeWriteClient{}
	_, err = ExecuteArtifact(context.Background(), outDir, client, ExecuteOptions{Approved: true})
	if err == nil || !strings.Contains(err.Error(), "refusing to execute again") {
		t.Fatalf("expected replay refusal, got %v", err)
	}
	if len(client.projects) != 0 || len(client.todolists) != 0 || len(client.todos) != 0 {
		t.Fatalf("second execution wrote records: %+v", client)
	}
}

func TestExecuteArtifactCreatesProject(t *testing.T) {
	outDir := compileSimpleExecutionArtifact(t, &DestinationConfig{SchemaVersion: planSchemaVersion, Mode: "new_project", ProjectName: "Imported Project", TodolistStrategy: "create_from_column"})
	client := &fakeWriteClient{}

	result, err := ExecuteArtifact(context.Background(), outDir, client, ExecuteOptions{Approved: true})
	if err != nil {
		t.Fatalf("ExecuteArtifact() error = %v", err)
	}
	if result.Created.Projects != 1 || len(client.projects) != 1 || client.projects[0] != "Imported Project" {
		t.Fatalf("projects result=%+v client=%+v", result.Created, client.projects)
	}
	if len(client.todolists) != 2 || client.todolists[0].ProjectID != 100 {
		t.Fatalf("todolists = %+v", client.todolists)
	}
	var ledger ExecutionLedger
	if err := readJSONData(filepath.Join(outDir, artifactExecutionFileName), &ledger); err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	if len(ledger.Operations) != 5 || ledger.Operations[0].Op != "create_project" || ledger.Operations[0].CreatedID != 100 {
		t.Fatalf("ledger operations = %+v", ledger.Operations)
	}
}

func TestExecuteArtifactReportsSkippedAssignees(t *testing.T) {
	inspection := inspectTempCSV(t, "id,title,list,owner\n1,First,Backlog,alex@example.com\n")
	mapping := &MappingConfig{SchemaVersion: planSchemaVersion, Title: &ColumnRef{ColumnIndex: 1}, Todolist: &ColumnRef{ColumnIndex: 2}, Assignees: &ColumnRef{ColumnIndex: 3}}
	destination := &DestinationConfig{SchemaVersion: planSchemaVersion, Mode: "existing_project", ProjectID: "123", TodolistStrategy: "create_from_column"}
	outDir := filepath.Join(t.TempDir(), "artifact")
	if _, err := CompileArtifact(inspection, mapping, destination, outDir); err != nil {
		t.Fatalf("CompileArtifact() error = %v", err)
	}

	result, err := ExecuteArtifact(context.Background(), outDir, &fakeWriteClient{}, ExecuteOptions{Approved: true})
	if err != nil {
		t.Fatalf("ExecuteArtifact() error = %v", err)
	}
	if len(result.Skipped) != 1 || result.Skipped[0].Field != "assignees" {
		t.Fatalf("skipped = %+v", result.Skipped)
	}
}

func TestExecuteArtifactStopsOnTodoError(t *testing.T) {
	outDir := compileSimpleExecutionArtifact(t, &DestinationConfig{SchemaVersion: planSchemaVersion, Mode: "existing_project", ProjectID: "123", TodolistStrategy: "create_from_column"})
	client := &fakeWriteClient{failTodoRows: map[string]error{"Second": fmt.Errorf("boom")}}

	_, err := ExecuteArtifact(context.Background(), outDir, client, ExecuteOptions{Approved: true})
	if err == nil || !strings.Contains(err.Error(), "source row 2") {
		t.Fatalf("expected source row error, got %v", err)
	}
}

func TestExecuteArtifactWritesFailedLedgerAndRefusesReplay(t *testing.T) {
	outDir := compileSimpleExecutionArtifact(t, &DestinationConfig{SchemaVersion: planSchemaVersion, Mode: "existing_project", ProjectID: "123", TodolistStrategy: "create_from_column"})
	client := &fakeWriteClient{failTodoRows: map[string]error{"Second": fmt.Errorf("boom")}}

	_, err := ExecuteArtifact(context.Background(), outDir, client, ExecuteOptions{Approved: true})
	if err == nil || !strings.Contains(err.Error(), "source row 2") {
		t.Fatalf("expected source row error, got %v", err)
	}
	var ledger ExecutionLedger
	if err := readJSONData(filepath.Join(outDir, artifactExecutionFileName), &ledger); err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	if ledger.Status != "failed" || ledger.FailedAt == "" || !strings.Contains(ledger.Error, "source row 2") {
		t.Fatalf("ledger = %+v", ledger)
	}
	last := ledger.Operations[len(ledger.Operations)-1]
	if last.Op != "create_todo" || last.Status != "failed" || last.SourceRow != 2 || !strings.Contains(last.Error, "source row 2") {
		t.Fatalf("ledger operations = %+v", ledger.Operations)
	}

	secondClient := &fakeWriteClient{}
	_, err = ExecuteArtifact(context.Background(), outDir, secondClient, ExecuteOptions{Approved: true})
	if err == nil || !strings.Contains(err.Error(), "refusing to execute again") {
		t.Fatalf("expected replay refusal, got %v", err)
	}
	if len(secondClient.projects) != 0 || len(secondClient.todolists) != 0 || len(secondClient.todos) != 0 {
		t.Fatalf("second execution wrote records: %+v", secondClient)
	}
}

func compileSimpleExecutionArtifact(t *testing.T, destination *DestinationConfig) string {
	t.Helper()
	inspection := inspectTempCSV(t, "id,title,list\n1,First,Backlog\n2,Second,Doing\n")
	mapping := &MappingConfig{SchemaVersion: planSchemaVersion, RecordID: &ColumnRef{ColumnIndex: 0}, Title: &ColumnRef{ColumnIndex: 1}, Todolist: &ColumnRef{ColumnIndex: 2}}
	outDir := filepath.Join(t.TempDir(), "artifact")
	if _, err := CompileArtifact(inspection, mapping, destination, outDir); err != nil {
		t.Fatalf("CompileArtifact() error = %v", err)
	}
	return outDir
}
