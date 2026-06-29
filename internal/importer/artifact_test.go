package importer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCompileArtifactWritesValidatedBasecampImportCSV(t *testing.T) {
	inspection := inspectTempCSV(t, `id,title,notes,list,status,owner,due,link,priority
T-1,Buy paint,"Get blue, low VOC",Home,todo,alex@example.com,2026-06-01,https://example.com/a,High
T-2,Book venue,Call two places,Events,todo,jamie@example.com,2026-06-03,https://example.com/b,
`)
	mapping := &MappingConfig{
		SchemaVersion:  planSchemaVersion,
		RecordID:       &ColumnRef{ColumnIndex: 0},
		Title:          &ColumnRef{ColumnIndex: 1},
		Description:    &ColumnRef{ColumnIndex: 2},
		Todolist:       &ColumnRef{ColumnIndex: 3},
		Status:         &ColumnRef{ColumnIndex: 4},
		Assignees:      &ColumnRef{ColumnIndex: 5},
		DueOn:          &ColumnRef{ColumnIndex: 6},
		AttachmentURLs: []ColumnRef{{ColumnIndex: 7}},
		CustomFields:   "all_unmapped_columns",
	}
	destination := &DestinationConfig{SchemaVersion: planSchemaVersion, Mode: "existing_project", ProjectID: "123", TodolistStrategy: "create_from_column"}
	outDir := filepath.Join(t.TempDir(), "basecamp-import")

	result, err := CompileArtifact(inspection, mapping, destination, outDir)
	if err != nil {
		t.Fatalf("CompileArtifact() error = %v", err)
	}
	if result.Status != "compiled" || result.Manifest.ArtifactFormat != artifactFormat {
		t.Fatalf("unexpected result: %+v", result)
	}
	if _, err := os.Stat(filepath.Join(outDir, artifactManifestName)); err != nil {
		t.Fatalf("manifest missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outDir, artifactTodosFileName)); err != nil {
		t.Fatalf("todos CSV missing: %v", err)
	}

	manifest, rows, err := readArtifact(outDir)
	if err != nil {
		t.Fatalf("readArtifact() error = %v", err)
	}
	if manifest.Counts.Todos != 2 || manifest.Counts.Todolists != 2 {
		t.Fatalf("manifest counts = %+v", manifest.Counts)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	if rows[0].Title != "Buy paint" || rows[0].AssigneeEmails[0] != "alex@example.com" {
		t.Fatalf("first artifact row = %+v", rows[0])
	}
	if rows[0].CustomFields["priority"] != "High" {
		t.Fatalf("custom fields = %+v", rows[0].CustomFields)
	}
}

func TestCompileArtifactEnforcesPrivateArtifactPermissions(t *testing.T) {
	inspection := inspectTempCSV(t, "id,title\n1,Do the thing\n")
	mapping := &MappingConfig{SchemaVersion: planSchemaVersion, Title: &ColumnRef{ColumnIndex: 1}}
	destination := &DestinationConfig{SchemaVersion: planSchemaVersion, Mode: "existing_project", ProjectID: "123"}
	outDir := filepath.Join(t.TempDir(), "artifact")
	if err := os.MkdirAll(outDir, 0o777); err != nil {
		t.Fatalf("mkdir artifact: %v", err)
	}
	if err := os.Chmod(outDir, 0o777); err != nil {
		t.Fatalf("chmod artifact dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outDir, artifactManifestName), []byte("{}"), 0o666); err != nil {
		t.Fatalf("seed manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outDir, artifactTodosFileName), []byte("old"), 0o666); err != nil {
		t.Fatalf("seed todos: %v", err)
	}

	if _, err := CompileArtifact(inspection, mapping, destination, outDir); err != nil {
		t.Fatalf("CompileArtifact() error = %v", err)
	}
	assertFileMode(t, outDir, 0o750)
	assertFileMode(t, filepath.Join(outDir, artifactManifestName), 0o600)
	assertFileMode(t, filepath.Join(outDir, artifactTodosFileName), 0o600)
}

func assertFileMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("mode %s = %o, want %o", path, got, want)
	}
}

func TestPlanFromArtifactMatchesCompiledOperations(t *testing.T) {
	inspection := inspectTempCSV(t, "id,title,list\n1,First,Backlog\n2,Second,Doing\n")
	mapping := &MappingConfig{SchemaVersion: planSchemaVersion, RecordID: &ColumnRef{ColumnIndex: 0}, Title: &ColumnRef{ColumnIndex: 1}, Todolist: &ColumnRef{ColumnIndex: 2}}
	destination := &DestinationConfig{SchemaVersion: planSchemaVersion, Mode: "existing_project", ProjectID: "123", TodolistStrategy: "create_from_column"}
	outDir := filepath.Join(t.TempDir(), "artifact")
	if _, err := CompileArtifact(inspection, mapping, destination, outDir); err != nil {
		t.Fatalf("CompileArtifact() error = %v", err)
	}

	plan, err := PlanFromArtifact(outDir)
	if err != nil {
		t.Fatalf("PlanFromArtifact() error = %v", err)
	}
	if plan.Status != "ready_for_approval" || plan.RequiresUserInput {
		t.Fatalf("plan = %+v", plan)
	}
	if plan.Counts.Todolists != 2 || plan.Counts.Todos != 2 {
		t.Fatalf("counts = %+v", plan.Counts)
	}
	if len(plan.Operations) != 4 {
		t.Fatalf("operation count = %d, want 4", len(plan.Operations))
	}
	if plan.Operations[2].Title != "First" || plan.Operations[2].TodolistName != "Backlog" {
		t.Fatalf("first todo op = %+v", plan.Operations[2])
	}
	if !strings.Contains(plan.DryRunMarkdown, "Row 1: create todo \"First\"") {
		t.Fatalf("dry run markdown missing todo: %s", plan.DryRunMarkdown)
	}
}

func TestCompileArtifactWritesCardsArtifact(t *testing.T) {
	inspection := inspectTempCSV(t, "id,title,column\n1,First,Backlog\n2,Second,Doing\n")
	mapping := &MappingConfig{SchemaVersion: planSchemaVersion, RecordID: &ColumnRef{ColumnIndex: 0}, Title: &ColumnRef{ColumnIndex: 1}, Column: &ColumnRef{ColumnIndex: 2}}
	destination := &DestinationConfig{SchemaVersion: planSchemaVersion, ResourceType: resourceTypeCards, Mode: "existing_project", ProjectID: "123", CardTableID: "888", ColumnStrategy: "create_from_column"}
	outDir := filepath.Join(t.TempDir(), "artifact")

	result, err := CompileArtifact(inspection, mapping, destination, outDir)
	if err != nil {
		t.Fatalf("CompileArtifact() error = %v", err)
	}
	if result.Manifest.Counts.Cards != 2 || result.Manifest.Counts.CardColumns != 2 || result.Manifest.Files.Cards != artifactCardsFileName {
		t.Fatalf("manifest = %+v", result.Manifest)
	}
	if _, err := os.Stat(filepath.Join(outDir, artifactCardsFileName)); err != nil {
		t.Fatalf("cards.csv not written: %v", err)
	}
	plan, err := PlanFromArtifact(outDir)
	if err != nil {
		t.Fatalf("PlanFromArtifact() error = %v", err)
	}
	if plan.Operations[0].Op != "create_card_column" || plan.Operations[2].Op != "create_card" {
		t.Fatalf("operations = %+v", plan.Operations)
	}
}

func TestCompileArtifactRejectsUnconfirmedInputs(t *testing.T) {
	inspection := inspectTempCSV(t, "id,title\n1,Do the thing\n")
	mapping := &MappingConfig{SchemaVersion: planSchemaVersion}
	destination := &DestinationConfig{SchemaVersion: planSchemaVersion, Mode: "existing_project", ProjectID: "123"}

	_, err := CompileArtifact(inspection, mapping, destination, filepath.Join(t.TempDir(), "artifact"))
	if err == nil || !strings.Contains(err.Error(), "requires confirmed") {
		t.Fatalf("expected confirmed input error, got %v", err)
	}
}

func TestCompileArtifactRejectsBlankTitle(t *testing.T) {
	inspection := inspectTempCSV(t, "id,title\n1,\n")
	mapping := &MappingConfig{SchemaVersion: planSchemaVersion, Title: &ColumnRef{ColumnIndex: 1}}
	destination := &DestinationConfig{SchemaVersion: planSchemaVersion, Mode: "existing_project", ProjectID: "123"}

	_, err := CompileArtifact(inspection, mapping, destination, filepath.Join(t.TempDir(), "artifact"))
	if err == nil || !strings.Contains(err.Error(), "blank title") {
		t.Fatalf("expected blank title error, got %v", err)
	}
}

func TestCompileArtifactNormalizesDueDates(t *testing.T) {
	inspection := inspectTempCSV(t, "id,title,due\n1,Do the thing,06/18/2026\n")
	mapping := &MappingConfig{SchemaVersion: planSchemaVersion, Title: &ColumnRef{ColumnIndex: 1}, DueOn: &ColumnRef{ColumnIndex: 2}}
	destination := &DestinationConfig{SchemaVersion: planSchemaVersion, Mode: "existing_project", ProjectID: "123", TodolistStrategy: "existing_todolist", TodolistID: "456"}
	outDir := filepath.Join(t.TempDir(), "artifact")

	_, err := CompileArtifact(inspection, mapping, destination, outDir)
	if err != nil {
		t.Fatalf("CompileArtifact() error = %v", err)
	}
	_, rows, err := readArtifact(outDir)
	if err != nil {
		t.Fatalf("readArtifact() error = %v", err)
	}
	if rows[0].DueOn != "2026-06-18" {
		t.Fatalf("artifact due_on = %q, want 2026-06-18", rows[0].DueOn)
	}
}

func TestCompileArtifactRejectsAmbiguousDueDates(t *testing.T) {
	inspection := inspectTempCSV(t, "id,title,due\n1,Do the thing,06/01/2026\n")
	mapping := &MappingConfig{SchemaVersion: planSchemaVersion, Title: &ColumnRef{ColumnIndex: 1}, DueOn: &ColumnRef{ColumnIndex: 2}}
	destination := &DestinationConfig{SchemaVersion: planSchemaVersion, Mode: "existing_project", ProjectID: "123", TodolistStrategy: "existing_todolist", TodolistID: "456"}

	_, err := CompileArtifact(inspection, mapping, destination, filepath.Join(t.TempDir(), "artifact"))
	if err == nil || !strings.Contains(err.Error(), "date_order") {
		t.Fatalf("expected date_order error, got %v", err)
	}
}

func TestReadArtifactRejectsTamperedTodoCount(t *testing.T) {
	inspection := inspectTempCSV(t, "id,title\n1,Do the thing\n")
	mapping := &MappingConfig{SchemaVersion: planSchemaVersion, Title: &ColumnRef{ColumnIndex: 1}}
	destination := &DestinationConfig{SchemaVersion: planSchemaVersion, Mode: "existing_project", ProjectID: "123"}
	outDir := filepath.Join(t.TempDir(), "artifact")
	if _, err := CompileArtifact(inspection, mapping, destination, outDir); err != nil {
		t.Fatalf("CompileArtifact() error = %v", err)
	}
	manifestPath := filepath.Join(outDir, artifactManifestName)
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	data = []byte(strings.Replace(string(data), `"todos": 1`, `"todos": 2`, 1))
	if err := os.WriteFile(manifestPath, data, 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	_, _, err = readArtifact(outDir)
	if err == nil || !strings.Contains(err.Error(), "does not match manifest") {
		t.Fatalf("expected count mismatch error, got %v", err)
	}
}
