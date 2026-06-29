package importer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPlanImportCreatesDeterministicDryRun(t *testing.T) {
	inspection := inspectTempCSV(t, `id,title,notes,list,status,owner,due,link,priority
T-1,Buy paint,"Get blue, low VOC",Home,todo,alex@example.com,2026-06-01,https://example.com/a,High
T-2,Fix gate,Latch sticks,Home,doing,jamie@example.com,2026-06-02,,Low
T-3,Book venue,Call two places,Events,todo,,2026-06-03,https://example.com/b,
`)

	mapping := &MappingConfig{
		SchemaVersion:  planSchemaVersion,
		RecordID:       &ColumnRef{ColumnIndex: 0, ColumnName: "id"},
		Title:          &ColumnRef{ColumnIndex: 1, ColumnName: "title"},
		Description:    &ColumnRef{ColumnIndex: 2, ColumnName: "notes"},
		Todolist:       &ColumnRef{ColumnIndex: 3, ColumnName: "list"},
		Status:         &ColumnRef{ColumnIndex: 4, ColumnName: "status"},
		Assignees:      &ColumnRef{ColumnIndex: 5, ColumnName: "owner"},
		DueOn:          &ColumnRef{ColumnIndex: 6, ColumnName: "due"},
		AttachmentURLs: []ColumnRef{{ColumnIndex: 7, ColumnName: "link"}},
		CustomFields:   "all_unmapped_columns",
	}
	destination := &DestinationConfig{SchemaVersion: planSchemaVersion, Mode: "existing_project", ProjectID: "123", TodolistStrategy: "create_from_column"}

	plan, err := PlanImport(inspection, mapping, destination)
	if err != nil {
		t.Fatalf("PlanImport() error = %v", err)
	}
	if plan.Status != "ready_for_approval" || plan.RequiresUserInput {
		t.Fatalf("plan status = %s requires_user_input=%v", plan.Status, plan.RequiresUserInput)
	}
	if plan.Counts.Projects != 0 || plan.Counts.Todolists != 2 || plan.Counts.Todos != 3 {
		t.Fatalf("counts = %+v", plan.Counts)
	}
	if len(plan.Operations) != 5 {
		t.Fatalf("operation count = %d, want 5", len(plan.Operations))
	}
	if plan.Operations[0].Op != "create_todolist" || plan.Operations[0].TodolistName != "Home" {
		t.Fatalf("first op = %+v", plan.Operations[0])
	}
	if plan.Operations[2].Op != "create_todo" || plan.Operations[2].Title != "Buy paint" || plan.Operations[2].TodolistName != "Home" {
		t.Fatalf("first todo op = %+v", plan.Operations[2])
	}
	if got := plan.Operations[2].CustomFields["priority"]; got != "High" {
		t.Fatalf("custom priority = %q, want High", got)
	}
	if !strings.Contains(plan.DryRunMarkdown, "- Todolists: 2") || !strings.Contains(plan.DryRunMarkdown, "Row 1: create todo \"Buy paint\"") {
		t.Fatalf("dry run markdown missing expected content:\n%s", plan.DryRunMarkdown)
	}
}

func TestPlanImportRequiresTitleMapping(t *testing.T) {
	inspection := inspectTempCSV(t, "id,task\n1,Do the thing\n")
	mapping := &MappingConfig{SchemaVersion: planSchemaVersion, RecordID: &ColumnRef{ColumnIndex: 0}}
	destination := &DestinationConfig{SchemaVersion: planSchemaVersion, Mode: "existing_project", ProjectID: "123", TodolistStrategy: "single_todolist", TodolistName: "Imported"}

	plan, err := PlanImport(inspection, mapping, destination)
	if err != nil {
		t.Fatalf("PlanImport() error = %v", err)
	}
	if plan.Status != "requires_user_input" || !plan.RequiresUserInput {
		t.Fatalf("plan status = %s requires_user_input=%v", plan.Status, plan.RequiresUserInput)
	}
	if !planHasQuestion(plan, "confirm_title_column") {
		t.Fatalf("expected confirm_title_column question, got %+v", plan.Questions)
	}
	if len(plan.Operations) != 0 {
		t.Fatalf("expected no operations while user input is required, got %+v", plan.Operations)
	}
}

func TestPlanImportRequiresAssigneePolicyForDisplayNames(t *testing.T) {
	inspection := inspectTempCSV(t, "id,title,owner\n1,Do the thing,Alex Rivera\n")
	mapping := &MappingConfig{
		SchemaVersion: planSchemaVersion,
		Title:         &ColumnRef{ColumnIndex: 1},
		Assignees:     &ColumnRef{ColumnIndex: 2},
	}
	destination := &DestinationConfig{SchemaVersion: planSchemaVersion, Mode: "existing_project", ProjectID: "123", TodolistStrategy: "single_todolist", TodolistName: "Imported"}

	plan, err := PlanImport(inspection, mapping, destination)
	if err != nil {
		t.Fatalf("PlanImport() error = %v", err)
	}
	if plan.Status != "requires_user_input" || !planHasQuestion(plan, "confirm_assignee_policy") || !planHasWarning(plan, "ambiguous_assignee_values") {
		t.Fatalf("expected assignee policy gate, plan = %+v", plan)
	}

	mapping.Assignees.MappingPolicy = "leave_unassigned_when_ambiguous"
	plan, err = PlanImport(inspection, mapping, destination)
	if err != nil {
		t.Fatalf("PlanImport() with policy error = %v", err)
	}
	if plan.RequiresUserInput {
		t.Fatalf("expected ready plan with assignee policy, got %+v", plan)
	}
	if len(plan.Operations) != 2 { // create_todolist + create_todo
		t.Fatalf("operation count = %d, want 2", len(plan.Operations))
	}
	if len(plan.Operations[1].Assignees) != 0 {
		t.Fatalf("display-name assignee should be left unassigned, got %+v", plan.Operations[1].Assignees)
	}
}

func TestPlanImportRejectsChangedFingerprint(t *testing.T) {
	path := writeTempCSV(t, "id,title\n1,Do the thing\n")
	inspection, err := InspectCSV(path, InspectOptions{SampleSize: 1})
	if err != nil {
		t.Fatalf("InspectCSV() error = %v", err)
	}
	if err := os.WriteFile(path, []byte("id,title\n1,Changed\n"), 0o644); err != nil {
		t.Fatalf("change CSV: %v", err)
	}

	_, err = PlanImport(inspection, &MappingConfig{SchemaVersion: planSchemaVersion, Title: &ColumnRef{ColumnIndex: 1}}, &DestinationConfig{SchemaVersion: planSchemaVersion, Mode: "existing_project", ProjectID: "123"})
	if err == nil || !strings.Contains(err.Error(), "fingerprint changed") {
		t.Fatalf("expected fingerprint changed error, got %v", err)
	}
}

func TestReadInspectionFileAcceptsCLIEnvelope(t *testing.T) {
	inspection := inspectTempCSV(t, "id,title\n1,Do the thing\n")
	path := filepath.Join(t.TempDir(), "inspection.json")
	data := `{"ok":true,"data":{"schema_version":1,"status":"profiled","format":"csv","export_path":"` + inspection.ExportPath + `","fingerprint":{"algorithm":"sha256-file-v1","value":"` + inspection.Fingerprint.Value + `"},"dialect":{"delimiter":",","has_header":true,"encoding":"utf-8"},"row_count":1,"columns":[],"role_candidates":{},"sample_rows":[],"warnings":[],"questions":[]}}`
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatalf("write inspection envelope: %v", err)
	}

	read, err := ReadInspectionFile(path)
	if err != nil {
		t.Fatalf("ReadInspectionFile() error = %v", err)
	}
	if read.SchemaVersion != 1 || read.ExportPath != inspection.ExportPath {
		t.Fatalf("unexpected inspection: %+v", read)
	}
}

func TestPlanImportRejectsMissingColumnIndex(t *testing.T) {
	inspection := inspectTempCSV(t, "id,title\n1,Do the thing\n")
	_, err := PlanImport(inspection, &MappingConfig{SchemaVersion: planSchemaVersion, Title: &ColumnRef{ColumnIndex: 99}}, &DestinationConfig{SchemaVersion: planSchemaVersion, Mode: "existing_project", ProjectID: "123"})
	if err == nil || !strings.Contains(err.Error(), "outside available columns") {
		t.Fatalf("expected missing column error, got %v", err)
	}
}

func TestPlanImportValidatesMappingColumnName(t *testing.T) {
	inspection := inspectTempCSV(t, "id,title,due,link,comment\n1,Do the thing,2026-06-01,https://example.com,Ready\n")
	destination := &DestinationConfig{SchemaVersion: planSchemaVersion, Mode: "existing_project", ProjectID: "123", TodolistStrategy: "existing_todolist", TodolistID: "456"}

	mapping := &MappingConfig{
		SchemaVersion:  planSchemaVersion,
		Title:          &ColumnRef{ColumnIndex: 1, ColumnName: "title"},
		DueOn:          &ColumnRef{ColumnIndex: 2, ColumnName: "due"},
		AttachmentURLs: []ColumnRef{{ColumnIndex: 3, ColumnName: "link"}},
		Comments:       []ColumnRef{{ColumnIndex: 4, ColumnName: "comment"}},
	}
	if _, err := PlanImport(inspection, mapping, destination); err != nil {
		t.Fatalf("PlanImport() with matching column_name error = %v", err)
	}

	mapping.Title.ColumnName = "due"
	_, err := PlanImport(inspection, mapping, destination)
	if err == nil || !strings.Contains(err.Error(), "mapping title column_index 1") || !strings.Contains(err.Error(), "\"title\"") || !strings.Contains(err.Error(), "\"due\"") {
		t.Fatalf("expected title column_name mismatch error, got %v", err)
	}
}

func TestPlanImportAllowsEmptyMappingColumnName(t *testing.T) {
	inspection := inspectTempCSV(t, "id,title\n1,Do the thing\n")
	mapping := &MappingConfig{SchemaVersion: planSchemaVersion, Title: &ColumnRef{ColumnIndex: 1, ColumnName: ""}}
	destination := &DestinationConfig{SchemaVersion: planSchemaVersion, Mode: "existing_project", ProjectID: "123", TodolistStrategy: "existing_todolist", TodolistID: "456"}

	if _, err := PlanImport(inspection, mapping, destination); err != nil {
		t.Fatalf("PlanImport() with empty column_name error = %v", err)
	}
}

func TestPlanImportValidatesArrayMappingColumnName(t *testing.T) {
	inspection := inspectTempCSV(t, "id,title,link\n1,Do the thing,https://example.com\n")
	mapping := &MappingConfig{SchemaVersion: planSchemaVersion, Title: &ColumnRef{ColumnIndex: 1}, AttachmentURLs: []ColumnRef{{ColumnIndex: 2, ColumnName: "wrong"}}}
	destination := &DestinationConfig{SchemaVersion: planSchemaVersion, Mode: "existing_project", ProjectID: "123", TodolistStrategy: "existing_todolist", TodolistID: "456"}

	_, err := PlanImport(inspection, mapping, destination)
	if err == nil || !strings.Contains(err.Error(), "mapping attachment_urls[0] column_index 2") || !strings.Contains(err.Error(), "\"link\"") || !strings.Contains(err.Error(), "\"wrong\"") {
		t.Fatalf("expected attachment_urls column_name mismatch error, got %v", err)
	}
}

func TestPlanImportNormalizesSafeDueDates(t *testing.T) {
	inspection := inspectTempCSV(t, "id,title,due\n1,ISO,2026-06-01\n2,RFC3339,2026-06-02T14:30:00Z\n3,YMD Slash,2026/06/03\n4,Month name,\"June 4, 2026\"\n")
	mapping := &MappingConfig{SchemaVersion: planSchemaVersion, Title: &ColumnRef{ColumnIndex: 1}, DueOn: &ColumnRef{ColumnIndex: 2}}
	destination := &DestinationConfig{SchemaVersion: planSchemaVersion, Mode: "existing_project", ProjectID: "123", TodolistStrategy: "existing_todolist", TodolistID: "456"}

	plan, err := PlanImport(inspection, mapping, destination)
	if err != nil {
		t.Fatalf("PlanImport() error = %v", err)
	}
	got := []string{plan.Operations[0].DueOn, plan.Operations[1].DueOn, plan.Operations[2].DueOn, plan.Operations[3].DueOn}
	want := []string{"2026-06-01", "2026-06-02", "2026-06-03", "2026-06-04"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("due_on[%d] = %q, want %q (all due_on values: %+v)", i, got[i], want[i], got)
		}
	}
}

func TestPlanImportInfersMDYSlashDueDates(t *testing.T) {
	inspection := inspectTempCSV(t, "id,title,due\n1,First,06/18/2026\n2,Second,06/19/2026\n")
	mapping := &MappingConfig{SchemaVersion: planSchemaVersion, Title: &ColumnRef{ColumnIndex: 1}, DueOn: &ColumnRef{ColumnIndex: 2}}
	destination := &DestinationConfig{SchemaVersion: planSchemaVersion, Mode: "existing_project", ProjectID: "123", TodolistStrategy: "existing_todolist", TodolistID: "456"}

	plan, err := PlanImport(inspection, mapping, destination)
	if err != nil {
		t.Fatalf("PlanImport() error = %v", err)
	}
	if plan.Operations[0].DueOn != "2026-06-18" || plan.Operations[1].DueOn != "2026-06-19" {
		t.Fatalf("due dates = %q, %q", plan.Operations[0].DueOn, plan.Operations[1].DueOn)
	}
}

func TestPlanImportInfersDMYSlashDueDates(t *testing.T) {
	inspection := inspectTempCSV(t, "id,title,due\n1,First,18/06/2026\n2,Second,19/06/2026\n")
	mapping := &MappingConfig{SchemaVersion: planSchemaVersion, Title: &ColumnRef{ColumnIndex: 1}, DueOn: &ColumnRef{ColumnIndex: 2}}
	destination := &DestinationConfig{SchemaVersion: planSchemaVersion, Mode: "existing_project", ProjectID: "123", TodolistStrategy: "existing_todolist", TodolistID: "456"}

	plan, err := PlanImport(inspection, mapping, destination)
	if err != nil {
		t.Fatalf("PlanImport() error = %v", err)
	}
	if plan.Operations[0].DueOn != "2026-06-18" || plan.Operations[1].DueOn != "2026-06-19" {
		t.Fatalf("due dates = %q, %q", plan.Operations[0].DueOn, plan.Operations[1].DueOn)
	}
}

func TestPlanImportUsesExplicitSlashDateOrder(t *testing.T) {
	inspection := inspectTempCSV(t, "id,title,due\n1,First,06/01/2026\n")
	mapping := &MappingConfig{SchemaVersion: planSchemaVersion, Title: &ColumnRef{ColumnIndex: 1}, DueOn: &ColumnRef{ColumnIndex: 2, DateOrder: "dmy"}}
	destination := &DestinationConfig{SchemaVersion: planSchemaVersion, Mode: "existing_project", ProjectID: "123", TodolistStrategy: "existing_todolist", TodolistID: "456"}

	plan, err := PlanImport(inspection, mapping, destination)
	if err != nil {
		t.Fatalf("PlanImport() error = %v", err)
	}
	if plan.Operations[0].DueOn != "2026-01-06" {
		t.Fatalf("due_on = %q, want 2026-01-06", plan.Operations[0].DueOn)
	}
}

func TestPlanImportRejectsAmbiguousSlashDueDates(t *testing.T) {
	inspection := inspectTempCSV(t, "id,title,due\n1,First,06/01/2026\n2,Second,07/02/2026\n")
	mapping := &MappingConfig{SchemaVersion: planSchemaVersion, Title: &ColumnRef{ColumnIndex: 1}, DueOn: &ColumnRef{ColumnIndex: 2}}
	destination := &DestinationConfig{SchemaVersion: planSchemaVersion, Mode: "existing_project", ProjectID: "123", TodolistStrategy: "existing_todolist", TodolistID: "456"}

	_, err := PlanImport(inspection, mapping, destination)
	if err == nil || !strings.Contains(err.Error(), "date_order") || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("expected ambiguous date_order error, got %v", err)
	}
}

func TestPlanImportRejectsConflictingSlashDueDates(t *testing.T) {
	inspection := inspectTempCSV(t, "id,title,due\n1,First,06/18/2026\n2,Second,18/06/2026\n")
	mapping := &MappingConfig{SchemaVersion: planSchemaVersion, Title: &ColumnRef{ColumnIndex: 1}, DueOn: &ColumnRef{ColumnIndex: 2}}
	destination := &DestinationConfig{SchemaVersion: planSchemaVersion, Mode: "existing_project", ProjectID: "123", TodolistStrategy: "existing_todolist", TodolistID: "456"}

	_, err := PlanImport(inspection, mapping, destination)
	if err == nil || !strings.Contains(err.Error(), "conflicting date orders") {
		t.Fatalf("expected conflicting date order error, got %v", err)
	}
}

func TestPlanImportRejectsUnparseableDueDate(t *testing.T) {
	inspection := inspectTempCSV(t, "id,title,due\n1,First,not soon\n")
	mapping := &MappingConfig{SchemaVersion: planSchemaVersion, Title: &ColumnRef{ColumnIndex: 1}, DueOn: &ColumnRef{ColumnIndex: 2}}
	destination := &DestinationConfig{SchemaVersion: planSchemaVersion, Mode: "existing_project", ProjectID: "123", TodolistStrategy: "existing_todolist", TodolistID: "456"}

	_, err := PlanImport(inspection, mapping, destination)
	if err == nil || !strings.Contains(err.Error(), "source row 1") || !strings.Contains(err.Error(), "unsupported date format") {
		t.Fatalf("expected source-row date parse error, got %v", err)
	}
}

func TestPlanImportRejectsTwoDigitYearDueDate(t *testing.T) {
	inspection := inspectTempCSV(t, "id,title,due\n1,First,06/01/26\n")
	mapping := &MappingConfig{SchemaVersion: planSchemaVersion, Title: &ColumnRef{ColumnIndex: 1}, DueOn: &ColumnRef{ColumnIndex: 2, DateOrder: "mdy"}}
	destination := &DestinationConfig{SchemaVersion: planSchemaVersion, Mode: "existing_project", ProjectID: "123", TodolistStrategy: "existing_todolist", TodolistID: "456"}

	_, err := PlanImport(inspection, mapping, destination)
	if err == nil || !strings.Contains(err.Error(), "two-digit years") {
		t.Fatalf("expected two-digit year error, got %v", err)
	}
}

func inspectTempCSV(t *testing.T, content string) *Inspection {
	t.Helper()
	path := writeTempCSV(t, content)
	inspection, err := InspectCSV(path, InspectOptions{SampleSize: 2})
	if err != nil {
		t.Fatalf("InspectCSV() error = %v", err)
	}
	return inspection
}

func writeTempCSV(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tasks.csv")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write CSV: %v", err)
	}
	return path
}

func planHasQuestion(plan *Plan, id string) bool {
	for _, question := range plan.Questions {
		if question.ID == id {
			return true
		}
	}
	return false
}

func planHasWarning(plan *Plan, code string) bool {
	for _, warning := range plan.Warnings {
		if warning.Code == code {
			return true
		}
	}
	return false
}
