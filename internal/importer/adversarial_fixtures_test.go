package importer

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestAdversarialSyntheticFixturesRoundTripWithoutBasecamp(t *testing.T) {
	tests := []struct {
		name           string
		fixture        string
		wantDelimiter  string
		mapping        *MappingConfig
		destination    *DestinationConfig
		wantCounts     PlanCounts
		wantCandidates map[string]int
		assertRows     func(t *testing.T, manifest *ImportArtifactManifest, rows []artifactTodoRow, plan *Plan)
	}{
		{
			name:          "wide duplicate headers multiline todos",
			fixture:       "wide-duplicate-multiline.csv",
			wantDelimiter: ",",
			mapping: &MappingConfig{
				SchemaVersion:  planSchemaVersion,
				RecordID:       &ColumnRef{ColumnIndex: 0, ColumnName: "ID"},
				Title:          &ColumnRef{ColumnIndex: 1, ColumnName: "Title"},
				Description:    &ColumnRef{ColumnIndex: 2, ColumnName: "Description"},
				Todolist:       &ColumnRef{ColumnIndex: 3, ColumnName: "List"},
				Status:         &ColumnRef{ColumnIndex: 4, ColumnName: "Status"},
				Assignees:      &ColumnRef{ColumnIndex: 5, ColumnName: "Assignee Emails"},
				DueOn:          &ColumnRef{ColumnIndex: 6, ColumnName: "Due Date"},
				AttachmentURLs: []ColumnRef{{ColumnIndex: 7, ColumnName: "Attachment URL"}},
				Comments:       []ColumnRef{{ColumnIndex: 8, ColumnName: "Comment"}},
				CustomFields:   "all_unmapped_columns",
			},
			destination: &DestinationConfig{SchemaVersion: planSchemaVersion, Mode: "existing_project", ProjectID: "123", TodolistStrategy: "create_from_column"},
			wantCounts:  PlanCounts{Todolists: 2, Todos: 3},
			wantCandidates: map[string]int{
				"record_id":       0,
				"title":           1,
				"description":     2,
				"todolist":        3,
				"status":          4,
				"assignees":       5,
				"due_on":          6,
				"attachment_urls": 7,
				"comments":        8,
			},
			assertRows: func(t *testing.T, manifest *ImportArtifactManifest, rows []artifactTodoRow, plan *Plan) {
				t.Helper()
				if manifest.Files.Todos != artifactTodosFileName || manifest.Files.Cards != "" {
					t.Fatalf("artifact files = %+v", manifest.Files)
				}
				if len(rows[0].AssigneeEmails) != 2 || rows[0].AssigneeEmails[1] != "jamie@example.com" {
					t.Fatalf("assignee split = %+v", rows[0].AssigneeEmails)
				}
				if !strings.Contains(rows[0].Description, "wet paint") || !strings.Contains(rows[0].Comments[0], "second comment line") {
					t.Fatalf("multiline text was not preserved: %+v", rows[0])
				}
				if rows[1].DueOn != "2026-06-02" || rows[2].DueOn != "2026-06-04" {
					t.Fatalf("normalized due dates = %q, %q", rows[1].DueOn, rows[2].DueOn)
				}
				if rows[0].CustomFields["Label [9]"] != "renovation" || rows[0].CustomFields["Label [10]"] != "Q2" || rows[1].CustomFields["Parent ID"] != "W-001" {
					t.Fatalf("custom fields = %+v", rows[0].CustomFields)
				}
			},
		},
		{
			name:          "semicolon ragged rows generated columns",
			fixture:       "semicolon-ragged-generated-columns.csv",
			wantDelimiter: ";",
			mapping: &MappingConfig{
				SchemaVersion: planSchemaVersion,
				RecordID:      &ColumnRef{ColumnIndex: 0, ColumnName: "key"},
				Title:         &ColumnRef{ColumnIndex: 1, ColumnName: "summary"},
				Todolist:      &ColumnRef{ColumnIndex: 2, ColumnName: "bucket"},
				DueOn:         &ColumnRef{ColumnIndex: 3, ColumnName: "due", DateOrder: "dmy"},
				Assignees:     &ColumnRef{ColumnIndex: 4, ColumnName: "owner"},
				Description:   &ColumnRef{ColumnIndex: 5, ColumnName: "detail"},
				CustomFields:  "all_unmapped_columns",
			},
			destination:    &DestinationConfig{SchemaVersion: planSchemaVersion, Mode: "existing_project", ProjectID: "123", TodolistStrategy: "create_from_column"},
			wantCounts:     PlanCounts{Todolists: 3, Todos: 4},
			wantCandidates: map[string]int{"record_id": 0, "title": 1, "todolist": 2, "due_on": 3, "assignees": 4, "description": 5},
			assertRows: func(t *testing.T, manifest *ImportArtifactManifest, rows []artifactTodoRow, plan *Plan) {
				t.Helper()
				if rows[2].TodolistName != "Imported todos" {
					t.Fatalf("blank bucket fallback = %q", rows[2].TodolistName)
				}
				if rows[0].CustomFields["Column 7"] != "orphan custom value" || rows[2].CustomFields["Column 8"] != "generated two" {
					t.Fatalf("generated-column custom fields: row1=%+v row3=%+v", rows[0].CustomFields, rows[2].CustomFields)
				}
				if rows[0].DueOn != "2026-06-18" || rows[3].DueOn != "2026-06-21" {
					t.Fatalf("dmy due dates = %q, %q", rows[0].DueOn, rows[3].DueOn)
				}
			},
		},
		{
			name:          "tab card artifact duplicate custom fields",
			fixture:       "tab-cards-duplicate-columns.csv",
			wantDelimiter: "\t",
			mapping: &MappingConfig{
				SchemaVersion:  planSchemaVersion,
				RecordID:       &ColumnRef{ColumnIndex: 0, ColumnName: "card_id"},
				Title:          &ColumnRef{ColumnIndex: 1, ColumnName: "title"},
				Description:    &ColumnRef{ColumnIndex: 2, ColumnName: "description"},
				Column:         &ColumnRef{ColumnIndex: 3, ColumnName: "column"},
				Status:         &ColumnRef{ColumnIndex: 4, ColumnName: "status"},
				Assignees:      &ColumnRef{ColumnIndex: 5, ColumnName: "owner"},
				DueOn:          &ColumnRef{ColumnIndex: 6, ColumnName: "due"},
				AttachmentURLs: []ColumnRef{{ColumnIndex: 7, ColumnName: "link"}},
				Comments:       []ColumnRef{{ColumnIndex: 8, ColumnName: "comment"}},
				CustomFields:   "all_unmapped_columns",
			},
			destination:    &DestinationConfig{SchemaVersion: planSchemaVersion, ResourceType: resourceTypeCards, Mode: "existing_project", ProjectID: "123", CardTableID: "888", ColumnStrategy: "create_from_column"},
			wantCounts:     PlanCounts{CardColumns: 2, Cards: 3},
			wantCandidates: map[string]int{"record_id": 0, "title": 1, "description": 2, "todolist": 3, "status": 4, "assignees": 5, "due_on": 6, "attachment_urls": 7, "comments": 8},
			assertRows: func(t *testing.T, manifest *ImportArtifactManifest, rows []artifactTodoRow, plan *Plan) {
				t.Helper()
				if manifest.Files.Cards != artifactCardsFileName || manifest.Files.Todos != "" {
					t.Fatalf("artifact files = %+v", manifest.Files)
				}
				if rows[0].CardTableID != 888 || rows[0].TodolistName != "Backlog" {
					t.Fatalf("card grouping = %+v", rows[0])
				}
				if rows[0].CustomFields["rank [9]"] != "P1" || rows[0].CustomFields["rank [10]"] != "customer" {
					t.Fatalf("duplicate custom fields = %+v", rows[0].CustomFields)
				}
				if plan.Operations[0].Op != "create_card_column" || plan.Operations[2].Op != "create_card" {
					t.Fatalf("card operations = %+v", plan.Operations[:3])
				}
			},
		},
		{
			name:          "pipe existing todolist many urls comments",
			fixture:       "pipe-existing-todolist-many-urls-comments.csv",
			wantDelimiter: "|",
			mapping: &MappingConfig{
				SchemaVersion:  planSchemaVersion,
				RecordID:       &ColumnRef{ColumnIndex: 0, ColumnName: "external_id"},
				Title:          &ColumnRef{ColumnIndex: 1, ColumnName: "todo title"},
				Description:    &ColumnRef{ColumnIndex: 2, ColumnName: "body"},
				DueOn:          &ColumnRef{ColumnIndex: 3, ColumnName: "due"},
				Assignees:      &ColumnRef{ColumnIndex: 4, ColumnName: "owner"},
				AttachmentURLs: []ColumnRef{{ColumnIndex: 5, ColumnName: "file url"}, {ColumnIndex: 6, ColumnName: "source url"}},
				Comments:       []ColumnRef{{ColumnIndex: 7, ColumnName: "comment one"}, {ColumnIndex: 8, ColumnName: "comment two"}},
				CustomFields:   "all_unmapped_columns",
			},
			destination:    &DestinationConfig{SchemaVersion: planSchemaVersion, Mode: "existing_project", ProjectID: "123", TodolistStrategy: "existing_todolist", TodolistID: "456", TodolistName: "Security Tasks"},
			wantCounts:     PlanCounts{Todos: 3},
			wantCandidates: map[string]int{"record_id": 0, "title": 1, "description": 2, "due_on": 3, "assignees": 4, "attachment_urls": 5, "comments": 7},
			assertRows: func(t *testing.T, manifest *ImportArtifactManifest, rows []artifactTodoRow, plan *Plan) {
				t.Helper()
				if len(rows[0].AttachmentURLs) != 2 || len(rows[0].Comments) != 2 {
					t.Fatalf("urls/comments row1 = %+v / %+v", rows[0].AttachmentURLs, rows[0].Comments)
				}
				if rows[1].DueOn != "" || rows[2].DueOn != "2026-08-03" {
					t.Fatalf("blank/rfc3339 due dates = %q, %q", rows[1].DueOn, rows[2].DueOn)
				}
				if rows[2].CustomFields["severity"] != "critical" || plan.Counts.Todolists != 0 {
					t.Fatalf("custom/counts = %+v / %+v", rows[2].CustomFields, plan.Counts)
				}
			},
		},
		{
			name:          "new project fallback groups",
			fixture:       "new-project-fallback-groups.csv",
			wantDelimiter: ",",
			mapping: &MappingConfig{
				SchemaVersion: planSchemaVersion,
				RecordID:      &ColumnRef{ColumnIndex: 0, ColumnName: "ref"},
				Title:         &ColumnRef{ColumnIndex: 1, ColumnName: "title"},
				Description:   &ColumnRef{ColumnIndex: 2, ColumnName: "notes"},
				Todolist:      &ColumnRef{ColumnIndex: 3, ColumnName: "list"},
				DueOn:         &ColumnRef{ColumnIndex: 4, ColumnName: "due"},
				Assignees:     &ColumnRef{ColumnIndex: 5, ColumnName: "email"},
				CustomFields:  "all_unmapped_columns",
			},
			destination:    &DestinationConfig{SchemaVersion: planSchemaVersion, Mode: "new_project", ProjectName: "Imported Adversarial Project", TodolistStrategy: "create_from_column"},
			wantCounts:     PlanCounts{Projects: 1, Todolists: 3, Todos: 3},
			wantCandidates: map[string]int{"record_id": 0, "title": 1, "description": 2, "todolist": 3, "due_on": 4, "assignees": 5},
			assertRows: func(t *testing.T, manifest *ImportArtifactManifest, rows []artifactTodoRow, plan *Plan) {
				t.Helper()
				if plan.Operations[0].Op != "create_project" || plan.Operations[0].ProjectName != "Imported Adversarial Project" {
					t.Fatalf("first operation = %+v", plan.Operations[0])
				}
				if rows[1].TodolistName != "Imported todos" || rows[1].CustomFields["custom_b"] != "two" {
					t.Fatalf("fallback/custom row2 = %+v", rows[1])
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inspection, err := InspectCSV(adversarialFixturePath(tt.fixture), InspectOptions{SampleSize: 3})
			if err != nil {
				t.Fatalf("InspectCSV() error = %v", err)
			}
			if inspection.Dialect.Delimiter != tt.wantDelimiter {
				t.Fatalf("delimiter = %q, want %q", inspection.Dialect.Delimiter, tt.wantDelimiter)
			}
			for role, columnIndex := range tt.wantCandidates {
				assertRoleCandidate(t, inspection, role, columnIndex)
			}

			outDir := filepath.Join(t.TempDir(), "artifact")
			compiled, err := CompileArtifact(inspection, tt.mapping, tt.destination, outDir)
			if err != nil {
				t.Fatalf("CompileArtifact() error = %v", err)
			}
			assertPlanCounts(t, compiled.Manifest.Counts, tt.wantCounts)

			plan, err := PlanFromArtifact(outDir)
			if err != nil {
				t.Fatalf("PlanFromArtifact() error = %v", err)
			}
			if plan.RequiresUserInput || plan.Status != "ready_for_approval" {
				t.Fatalf("plan = %+v", plan)
			}
			assertPlanCounts(t, plan.Counts, tt.wantCounts)
			if !strings.Contains(plan.DryRunMarkdown, "Import dry run") {
				t.Fatalf("dry run markdown missing header: %s", plan.DryRunMarkdown)
			}

			manifest, rows, err := readArtifact(outDir)
			if err != nil {
				t.Fatalf("readArtifact() error = %v", err)
			}
			wantRows := tt.wantCounts.Todos
			if tt.wantCounts.Cards > 0 {
				wantRows = tt.wantCounts.Cards
			}
			if len(rows) != wantRows {
				t.Fatalf("artifact rows = %d, want %d", len(rows), wantRows)
			}
			tt.assertRows(t, manifest, rows, plan)
		})
	}
}

func TestLargeSyntheticCSVArtifactRoundTripWithoutBasecamp(t *testing.T) {
	var buf bytes.Buffer
	writer := csv.NewWriter(&buf)
	header := []string{"id", "title", "list", "due", "owner", "notes", "flag"}
	for j := 0; j < 40; j++ {
		header = append(header, fmt.Sprintf("custom_%02d", j))
	}
	if err := writer.Write(header); err != nil {
		t.Fatalf("write header: %v", err)
	}
	for i := 1; i <= 250; i++ {
		row := []string{
			fmt.Sprintf("L-%03d", i),
			fmt.Sprintf("Import row %03d", i),
			fmt.Sprintf("Group %02d", (i-1)%17),
			fmt.Sprintf("2026-10-%02d", (i-1)%28+1),
			fmt.Sprintf("person%03d@example.com", i),
			fmt.Sprintf("Deterministic note with comma, quote \"%03d\", and enough text to exercise artifact CSV escaping.", i),
			fmt.Sprintf("flag-%02d", i%5),
		}
		for j := 0; j < 40; j++ {
			row = append(row, fmt.Sprintf("row-%03d-custom-%02d", i, j))
		}
		if err := writer.Write(row); err != nil {
			t.Fatalf("write row %d: %v", i, err)
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		t.Fatalf("flush CSV: %v", err)
	}

	inspection := inspectTempCSV(t, buf.String())
	mapping := &MappingConfig{
		SchemaVersion: planSchemaVersion,
		RecordID:      &ColumnRef{ColumnIndex: 0, ColumnName: "id"},
		Title:         &ColumnRef{ColumnIndex: 1, ColumnName: "title"},
		Todolist:      &ColumnRef{ColumnIndex: 2, ColumnName: "list"},
		DueOn:         &ColumnRef{ColumnIndex: 3, ColumnName: "due"},
		Assignees:     &ColumnRef{ColumnIndex: 4, ColumnName: "owner"},
		Description:   &ColumnRef{ColumnIndex: 5, ColumnName: "notes"},
		CustomFields:  "all_unmapped_columns",
	}
	destination := &DestinationConfig{SchemaVersion: planSchemaVersion, Mode: "existing_project", ProjectID: "123", TodolistStrategy: "create_from_column"}
	outDir := filepath.Join(t.TempDir(), "large-artifact")

	compiled, err := CompileArtifact(inspection, mapping, destination, outDir)
	if err != nil {
		t.Fatalf("CompileArtifact() error = %v", err)
	}
	assertPlanCounts(t, compiled.Manifest.Counts, PlanCounts{Todolists: 17, Todos: 250})

	plan, err := PlanFromArtifact(outDir)
	if err != nil {
		t.Fatalf("PlanFromArtifact() error = %v", err)
	}
	assertPlanCounts(t, plan.Counts, PlanCounts{Todolists: 17, Todos: 250})
	if len(plan.Operations) != 267 {
		t.Fatalf("operation count = %d, want 267", len(plan.Operations))
	}
	if plan.Operations[17].Title != "Import row 001" || plan.Operations[266].Title != "Import row 250" {
		t.Fatalf("first/last todo operations = %+v / %+v", plan.Operations[17], plan.Operations[266])
	}
	_, rows, err := readArtifact(outDir)
	if err != nil {
		t.Fatalf("readArtifact() error = %v", err)
	}
	if len(rows) != 250 {
		t.Fatalf("artifact rows = %d, want 250", len(rows))
	}
	if len(rows[0].CustomFields) != 41 || rows[0].CustomFields["custom_39"] != "row-001-custom-39" {
		t.Fatalf("wide custom fields = %+v", rows[0].CustomFields)
	}
}

func adversarialFixturePath(name string) string {
	return filepath.Join("../../testdata/import/csv/synthetic/adversarial", name)
}

func assertRoleCandidate(t *testing.T, inspection *Inspection, role string, wantColumn int) {
	t.Helper()
	for _, candidate := range inspection.RoleCandidates[role] {
		if candidate.ColumnIndex == wantColumn {
			return
		}
	}
	t.Fatalf("role %s does not include column %d: %+v", role, wantColumn, inspection.RoleCandidates[role])
}

func assertPlanCounts(t *testing.T, got, want PlanCounts) {
	t.Helper()
	if got.Projects != want.Projects || got.Todolists != want.Todolists || got.Todos != want.Todos || got.CardColumns != want.CardColumns || got.Cards != want.Cards {
		t.Fatalf("counts = %+v, want %+v", got, want)
	}
}
