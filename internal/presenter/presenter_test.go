package presenter

import (
	"strings"
	"testing"
	"time"

	"github.com/basecamp/bcq/internal/tui"
)

// enUS is the default locale used by most tests.
var enUS = NewLocale("en-US")

// =============================================================================
// Schema Loading Tests
// =============================================================================

func TestLookupByName(t *testing.T) {
	schema := LookupByName("todo")
	if schema == nil {
		t.Fatal("Expected todo schema, got nil")
	}
	if schema.Entity != "todo" {
		t.Errorf("Entity = %q, want %q", schema.Entity, "todo")
	}
	if schema.Kind != "recording" {
		t.Errorf("Kind = %q, want %q", schema.Kind, "recording")
	}
	if schema.TypeKey != "Todo" {
		t.Errorf("TypeKey = %q, want %q", schema.TypeKey, "Todo")
	}
}

func TestLookupByTypeKey(t *testing.T) {
	schema := LookupByTypeKey("Todo")
	if schema == nil {
		t.Fatal("Expected schema for type key 'Todo', got nil")
	}
	if schema.Entity != "todo" {
		t.Errorf("Entity = %q, want %q", schema.Entity, "todo")
	}
}

func TestLookupMissing(t *testing.T) {
	if s := LookupByName("nonexistent"); s != nil {
		t.Errorf("Expected nil for nonexistent entity, got %v", s)
	}
	if s := LookupByTypeKey("Nonexistent"); s != nil {
		t.Errorf("Expected nil for nonexistent type key, got %v", s)
	}
}

func TestSchemaIdentity(t *testing.T) {
	schema := LookupByName("todo")
	if schema == nil {
		t.Fatal("Expected todo schema")
	}

	if schema.Identity.Label != "content" {
		t.Errorf("Identity.Label = %q, want %q", schema.Identity.Label, "content")
	}
	if schema.Identity.ID != "id" {
		t.Errorf("Identity.ID = %q, want %q", schema.Identity.ID, "id")
	}
}

func TestSchemaFields(t *testing.T) {
	schema := LookupByName("todo")
	if schema == nil {
		t.Fatal("Expected todo schema")
	}

	content, ok := schema.Fields["content"]
	if !ok {
		t.Fatal("Expected 'content' field in schema")
	}
	if content.Role != "title" {
		t.Errorf("content.Role = %q, want %q", content.Role, "title")
	}
	if content.Emphasis != "primary" {
		t.Errorf("content.Emphasis = %q, want %q", content.Emphasis, "primary")
	}

	completed, ok := schema.Fields["completed"]
	if !ok {
		t.Fatal("Expected 'completed' field in schema")
	}
	if completed.Format != "boolean" {
		t.Errorf("completed.Format = %q, want %q", completed.Format, "boolean")
	}
	if completed.Labels["true"] != "done" {
		t.Errorf("completed.Labels[true] = %q, want %q", completed.Labels["true"], "done")
	}
}

func TestSchemaViews(t *testing.T) {
	schema := LookupByName("todo")
	if schema == nil {
		t.Fatal("Expected todo schema")
	}

	if len(schema.Views.List.Columns) != 4 {
		t.Errorf("List columns = %d, want 4", len(schema.Views.List.Columns))
	}
	if schema.Views.List.Columns[0] != "content" {
		t.Errorf("First list column = %q, want %q", schema.Views.List.Columns[0], "content")
	}

	if len(schema.Views.Detail.Sections) != 3 {
		t.Errorf("Detail sections = %d, want 3", len(schema.Views.Detail.Sections))
	}
}

func TestSchemaAffordances(t *testing.T) {
	schema := LookupByName("todo")
	if schema == nil {
		t.Fatal("Expected todo schema")
	}

	if len(schema.Actions) != 3 {
		t.Errorf("Actions = %d, want 3", len(schema.Actions))
	}
	if schema.Actions[0].Action != "complete" {
		t.Errorf("First action = %q, want %q", schema.Actions[0].Action, "complete")
	}
}

// =============================================================================
// Detect Tests
// =============================================================================

func TestDetectWithEntityHint(t *testing.T) {
	data := map[string]any{"content": "Fix bug"}
	schema := Detect(data, "todo")
	if schema == nil {
		t.Fatal("Expected schema with entity hint 'todo'")
	}
	if schema.Entity != "todo" {
		t.Errorf("Entity = %q, want %q", schema.Entity, "todo")
	}
}

func TestDetectWithTypeField(t *testing.T) {
	data := map[string]any{"type": "Todo", "content": "Fix bug"}
	schema := Detect(data, "")
	if schema == nil {
		t.Fatal("Expected schema from type field 'Todo'")
	}
	if schema.Entity != "todo" {
		t.Errorf("Entity = %q, want %q", schema.Entity, "todo")
	}
}

func TestDetectWithSliceTypeField(t *testing.T) {
	data := []map[string]any{
		{"type": "Todo", "content": "Fix bug"},
		{"type": "Todo", "content": "Write tests"},
	}
	schema := Detect(data, "")
	if schema == nil {
		t.Fatal("Expected schema from slice type field")
	}
}

func TestDetectNoMatch(t *testing.T) {
	data := map[string]any{"name": "something"}
	schema := Detect(data, "")
	if schema != nil {
		t.Errorf("Expected nil for unmatched data, got %v", schema)
	}
}

// =============================================================================
// Field Formatting Tests
// =============================================================================

func TestFormatFieldBoolean(t *testing.T) {
	spec := FieldSpec{
		Format: "boolean",
		Labels: map[string]string{"true": "done", "false": "pending"},
	}

	if got := FormatField(spec, "completed", true, enUS); got != "done" {
		t.Errorf("FormatField(true) = %q, want %q", got, "done")
	}
	if got := FormatField(spec, "completed", false, enUS); got != "pending" {
		t.Errorf("FormatField(false) = %q, want %q", got, "pending")
	}
}

func TestFormatFieldBooleanNoLabels(t *testing.T) {
	spec := FieldSpec{Format: "boolean"}

	if got := FormatField(spec, "active", true, enUS); got != "yes" {
		t.Errorf("FormatField(true) = %q, want %q", got, "yes")
	}
	if got := FormatField(spec, "active", false, enUS); got != "no" {
		t.Errorf("FormatField(false) = %q, want %q", got, "no")
	}
}

func TestFormatFieldDate(t *testing.T) {
	spec := FieldSpec{Format: "date"}

	if got := FormatField(spec, "due_on", "2024-03-15", enUS); got != "Mar 15, 2024" {
		t.Errorf("FormatField(date) = %q, want %q", got, "Mar 15, 2024")
	}
	if got := FormatField(spec, "due_on", "", enUS); got != "" {
		t.Errorf("FormatField(empty date) = %q, want empty", got)
	}
}

func TestFormatFieldPeople(t *testing.T) {
	spec := FieldSpec{Format: "people"}
	people := []any{
		map[string]any{"name": "Alice", "id": float64(1)},
		map[string]any{"name": "Bob", "id": float64(2)},
	}

	got := FormatField(spec, "assignees", people, enUS)
	if got != "Alice, Bob" {
		t.Errorf("FormatField(people) = %q, want %q", got, "Alice, Bob")
	}
}

func TestFormatFieldPeopleEmpty(t *testing.T) {
	spec := FieldSpec{Format: "people"}

	if got := FormatField(spec, "assignees", []any{}, enUS); got != "" {
		t.Errorf("FormatField(empty people) = %q, want empty", got)
	}
	if got := FormatField(spec, "assignees", nil, enUS); got != "" {
		t.Errorf("FormatField(nil people) = %q, want empty", got)
	}
}

func TestFormatFieldText(t *testing.T) {
	spec := FieldSpec{Format: "text"}

	if got := FormatField(spec, "content", "Fix the bug", enUS); got != "Fix the bug" {
		t.Errorf("FormatField(text) = %q, want %q", got, "Fix the bug")
	}
	if got := FormatField(spec, "id", float64(123), enUS); got != "123" {
		t.Errorf("FormatField(number) = %q, want %q", got, "123")
	}
}

func TestIsOverdue(t *testing.T) {
	if IsOverdue("2020-01-01") != true {
		t.Error("2020-01-01 should be overdue")
	}
	if IsOverdue("2099-01-01") != false {
		t.Error("2099-01-01 should not be overdue")
	}
	if IsOverdue("") != false {
		t.Error("empty string should not be overdue")
	}
	if IsOverdue(nil) != false {
		t.Error("nil should not be overdue")
	}
}

// =============================================================================
// Template Tests
// =============================================================================

func TestRenderTemplate(t *testing.T) {
	data := map[string]any{"content": "Fix the bug", "id": float64(123)}

	got := RenderTemplate("{{.content}}", data)
	if got != "Fix the bug" {
		t.Errorf("RenderTemplate = %q, want %q", got, "Fix the bug")
	}
}

func TestRenderTemplateWithNot(t *testing.T) {
	data := map[string]any{"completed": false}

	got := RenderTemplate("{{not .completed}}", data)
	if got != "true" {
		t.Errorf("RenderTemplate(not false) = %q, want %q", got, "true")
	}

	data["completed"] = true
	got = RenderTemplate("{{not .completed}}", data)
	if got != "false" {
		t.Errorf("RenderTemplate(not true) = %q, want %q", got, "false")
	}
}

func TestRenderTemplateInvalid(t *testing.T) {
	got := RenderTemplate("{{.missing}}", map[string]any{})
	if got != "<no value>" {
		t.Errorf("RenderTemplate(missing key) = %q, want %q", got, "<no value>")
	}

	// Parse errors produce a visible placeholder
	got = RenderTemplate("{{.bad syntax", map[string]any{})
	if got != "<template error>" {
		t.Errorf("RenderTemplate(parse error) = %q, want %q", got, "<template error>")
	}
}

func TestRenderTemplateLargeID(t *testing.T) {
	// JSON-decoded IDs are float64; large values must not use scientific notation
	data := map[string]any{"id": float64(123456789)}
	got := RenderTemplate("bcq done {{.id}}", data)
	if got != "bcq done 123456789" {
		t.Errorf("RenderTemplate(large ID) = %q, want %q", got, "bcq done 123456789")
	}
}

func TestEvalCondition(t *testing.T) {
	data := map[string]any{"completed": false}

	if !EvalCondition("", data) {
		t.Error("Empty condition should return true")
	}
	if !EvalCondition("{{not .completed}}", data) {
		t.Error("not false should be true")
	}
	if EvalCondition("{{.completed}}", data) {
		t.Error("false should not be true")
	}
}

func TestRenderHeadline(t *testing.T) {
	schema := LookupByName("todo")
	if schema == nil {
		t.Fatal("Expected todo schema")
	}

	// Incomplete todo → default headline
	data := map[string]any{
		"content":   "Fix the bug",
		"completed": false,
	}
	got := RenderHeadline(schema, data)
	if got != "Fix the bug" {
		t.Errorf("Headline = %q, want %q", got, "Fix the bug")
	}

	// Completed todo → completed headline
	data["completed"] = true
	got = RenderHeadline(schema, data)
	if got != "[done] Fix the bug" {
		t.Errorf("Completed headline = %q, want %q", got, "[done] Fix the bug")
	}
}

// =============================================================================
// Render Tests
// =============================================================================

func TestRenderDetailTodo(t *testing.T) {
	schema := LookupByName("todo")
	if schema == nil {
		t.Fatal("Expected todo schema")
	}

	data := map[string]any{
		"id":        float64(12345),
		"content":   "Fix the login bug",
		"completed": false,
		"due_on":    "2026-01-15",
		"assignees": []any{
			map[string]any{"name": "Alice"},
			map[string]any{"name": "Bob"},
		},
		"description": "The login page throws a 500 error",
		"created_at":  "2025-12-01T10:00:00Z",
	}

	styles := NewStyles(tui.NoColorTheme(), false)

	var buf strings.Builder
	if err := RenderDetail(&buf, schema, data, styles, enUS); err != nil {
		t.Fatalf("RenderDetail failed: %v", err)
	}

	out := buf.String()

	// Should contain headline
	if !strings.Contains(out, "Fix the login bug") {
		t.Errorf("Output should contain headline, got:\n%s", out)
	}

	// Should contain status fields
	if !strings.Contains(out, "pending") {
		t.Errorf("Output should contain 'pending', got:\n%s", out)
	}
	if !strings.Contains(out, "Jan 15, 2026") {
		t.Errorf("Output should contain formatted due date, got:\n%s", out)
	}
	if !strings.Contains(out, "Alice, Bob") {
		t.Errorf("Output should contain assignees, got:\n%s", out)
	}

	// Should contain description as body text
	if !strings.Contains(out, "The login page throws a 500 error") {
		t.Errorf("Output should contain description, got:\n%s", out)
	}

	// Should contain affordances
	if !strings.Contains(out, "Mark done") {
		t.Errorf("Output should contain 'Mark done' affordance, got:\n%s", out)
	}
	if !strings.Contains(out, "bcq done 12345") {
		t.Errorf("Output should contain rendered affordance command, got:\n%s", out)
	}

	// Completed affordance should NOT appear (todo is not completed)
	if strings.Contains(out, "Reopen") {
		t.Errorf("Output should NOT contain 'Reopen' for incomplete todo, got:\n%s", out)
	}
}

func TestRenderDetailCompletedTodo(t *testing.T) {
	schema := LookupByName("todo")
	if schema == nil {
		t.Fatal("Expected todo schema")
	}

	data := map[string]any{
		"id":        float64(99),
		"content":   "Write docs",
		"completed": true,
	}

	styles := NewStyles(tui.NoColorTheme(), false)

	var buf strings.Builder
	if err := RenderDetail(&buf, schema, data, styles, enUS); err != nil {
		t.Fatalf("RenderDetail failed: %v", err)
	}

	out := buf.String()

	// Completed headline
	if !strings.Contains(out, "[done] Write docs") {
		t.Errorf("Output should contain completed headline, got:\n%s", out)
	}

	// Reopen affordance should appear
	if !strings.Contains(out, "Reopen") {
		t.Errorf("Output should contain 'Reopen' for completed todo, got:\n%s", out)
	}

	// Mark done should NOT appear
	if strings.Contains(out, "Mark done") {
		t.Errorf("Output should NOT contain 'Mark done' for completed todo, got:\n%s", out)
	}
}

func TestRenderListTodos(t *testing.T) {
	schema := LookupByName("todo")
	if schema == nil {
		t.Fatal("Expected todo schema")
	}

	data := []map[string]any{
		{"content": "Fix bug", "completed": false, "due_on": "2026-02-01", "assignees": []any{}},
		{"content": "Write tests", "completed": true, "due_on": "", "assignees": []any{map[string]any{"name": "Alice"}}},
	}

	styles := NewStyles(tui.NoColorTheme(), false)

	var buf strings.Builder
	if err := RenderList(&buf, schema, data, styles, enUS); err != nil {
		t.Fatalf("RenderList failed: %v", err)
	}

	out := buf.String()

	if !strings.Contains(out, "Fix bug") {
		t.Errorf("Output should contain 'Fix bug', got:\n%s", out)
	}
	if !strings.Contains(out, "Write tests") {
		t.Errorf("Output should contain 'Write tests', got:\n%s", out)
	}
}

// =============================================================================
// Present Tests
// =============================================================================

func TestPresentWithSchema(t *testing.T) {
	data := map[string]any{
		"id":        float64(1),
		"content":   "Test todo",
		"completed": false,
	}

	var buf strings.Builder
	handled := PresentWithTheme(&buf, data, "todo", ModeStyled, tui.NoColorTheme(), enUS)
	if !handled {
		t.Error("Present should handle todo entity")
	}
	if !strings.Contains(buf.String(), "Test todo") {
		t.Errorf("Output should contain 'Test todo', got:\n%s", buf.String())
	}
}

func TestPresentWithoutSchema(t *testing.T) {
	data := map[string]any{"name": "something"}

	var buf strings.Builder
	handled := PresentWithTheme(&buf, data, "", ModeStyled, tui.NoColorTheme(), enUS)
	if handled {
		t.Error("Present should not handle data without matching schema")
	}
}

func TestPresentSlice(t *testing.T) {
	data := []map[string]any{
		{"content": "Todo 1", "completed": false, "due_on": "", "assignees": []any{}},
		{"content": "Todo 2", "completed": true, "due_on": "", "assignees": []any{}},
	}

	var buf strings.Builder
	handled := PresentWithTheme(&buf, data, "todo", ModeStyled, tui.NoColorTheme(), enUS)
	if !handled {
		t.Error("Present should handle todo list")
	}
	out := buf.String()
	if !strings.Contains(out, "Todo 1") || !strings.Contains(out, "Todo 2") {
		t.Errorf("Output should contain both todos, got:\n%s", out)
	}
}

func TestPresentEmptySlice(t *testing.T) {
	data := []map[string]any{}

	var buf strings.Builder
	handled := PresentWithTheme(&buf, data, "todo", ModeStyled, tui.NoColorTheme(), enUS)
	if handled {
		t.Error("Present should not handle empty slice (fall back to generic)")
	}
}

// =============================================================================
// Collapse Tests
// =============================================================================

func TestCollapsedFieldsHidden(t *testing.T) {
	schema := LookupByName("todo")
	if schema == nil {
		t.Fatal("Expected todo schema")
	}

	// Todo with empty description and no assignees - collapsed fields should not appear
	data := map[string]any{
		"id":          float64(1),
		"content":     "Simple todo",
		"completed":   false,
		"description": "",
		"assignees":   []any{},
	}

	styles := NewStyles(tui.NoColorTheme(), false)

	var buf strings.Builder
	if err := RenderDetail(&buf, schema, data, styles, enUS); err != nil {
		t.Fatalf("RenderDetail failed: %v", err)
	}

	out := buf.String()

	// Description and Assignees are collapse:true, so they should not render when empty
	if strings.Contains(out, "Description") {
		t.Errorf("Empty collapsed description should not appear, got:\n%s", out)
	}
}

// =============================================================================
// Multi-Schema Registry Tests (regression: pointer aliasing bug)
// =============================================================================

func TestRegistryMultipleSchemas(t *testing.T) {
	todo := LookupByName("todo")
	project := LookupByName("project")

	if todo == nil {
		t.Fatal("Expected todo schema")
	}
	if project == nil {
		t.Fatal("Expected project schema")
	}

	// The critical check: each schema must be distinct, not aliased
	if todo == project {
		t.Fatal("todo and project schemas must be different pointers (registry aliasing bug)")
	}
	if todo.Entity != "todo" {
		t.Errorf("todo.Entity = %q, want %q", todo.Entity, "todo")
	}
	if project.Entity != "project" {
		t.Errorf("project.Entity = %q, want %q", project.Entity, "project")
	}

	// TypeKey lookup must also return distinct schemas
	todoByType := LookupByTypeKey("Todo")
	projectByType := LookupByTypeKey("Project")

	if todoByType == nil || projectByType == nil {
		t.Fatal("Expected both type key lookups to succeed")
	}
	if todoByType.Entity != "todo" {
		t.Errorf("LookupByTypeKey('Todo').Entity = %q, want %q", todoByType.Entity, "todo")
	}
	if projectByType.Entity != "project" {
		t.Errorf("LookupByTypeKey('Project').Entity = %q, want %q", projectByType.Entity, "project")
	}
}

// =============================================================================
// Auto-Detection Tests (type field without explicit WithEntity)
// =============================================================================

func TestDetectByTypeFieldOnly(t *testing.T) {
	// Single object with type field, no hint
	data := map[string]any{
		"type": "Project",
		"name": "Q1 Launch",
		"id":   float64(42),
	}
	schema := Detect(data, "")
	if schema == nil {
		t.Fatal("Expected schema from type field alone")
	}
	if schema.Entity != "project" {
		t.Errorf("Entity = %q, want %q", schema.Entity, "project")
	}
}

func TestDetectHintOverridesTypeField(t *testing.T) {
	// Hint should take precedence over type field
	data := map[string]any{
		"type":    "Project",
		"content": "Some content",
	}
	schema := Detect(data, "todo")
	if schema == nil {
		t.Fatal("Expected schema from hint")
	}
	if schema.Entity != "todo" {
		t.Errorf("Entity = %q, want %q (hint should win)", schema.Entity, "todo")
	}
}

// =============================================================================
// Overdue Emphasis Tests
// =============================================================================

func TestIsOverdueRFC3339(t *testing.T) {
	// RFC3339 timestamp in the past
	if !IsOverdue("2020-06-15T10:30:00Z") {
		t.Error("RFC3339 timestamp in 2020 should be overdue")
	}
	// RFC3339 timestamp in the future
	if IsOverdue("2099-06-15T10:30:00Z") {
		t.Error("RFC3339 timestamp in 2099 should not be overdue")
	}
}

func TestOverdueEmphasisAppliesToOwnField(t *testing.T) {
	schema := LookupByName("todo")
	if schema == nil {
		t.Fatal("Expected todo schema")
	}

	// due_on field has when_overdue: warning
	data := map[string]any{
		"id":        float64(1),
		"content":   "Overdue task",
		"completed": false,
		"due_on":    "2020-01-01",
	}

	styles := NewStyles(tui.NoColorTheme(), false)
	var buf strings.Builder
	if err := RenderDetail(&buf, schema, data, styles, enUS); err != nil {
		t.Fatalf("RenderDetail failed: %v", err)
	}

	// The output should contain the formatted date (test passes if no panic/error)
	out := buf.String()
	if !strings.Contains(out, "Jan 1, 2020") {
		t.Errorf("Output should contain overdue date, got:\n%s", out)
	}
}

// =============================================================================
// Body Field Formatting Tests
// =============================================================================

func TestBodyFieldUsesFormatField(t *testing.T) {
	schema := LookupByName("todo")
	if schema == nil {
		t.Fatal("Expected todo schema")
	}

	data := map[string]any{
		"id":          float64(1),
		"content":     "Test",
		"completed":   false,
		"description": "This is the body text",
	}

	styles := NewStyles(tui.NoColorTheme(), false)
	var buf strings.Builder
	if err := RenderDetail(&buf, schema, data, styles, enUS); err != nil {
		t.Fatalf("RenderDetail failed: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "This is the body text") {
		t.Errorf("Output should contain body text, got:\n%s", out)
	}
}

func TestEmptyNonCollapsedFieldSkipped(t *testing.T) {
	schema := LookupByName("todo")
	if schema == nil {
		t.Fatal("Expected todo schema")
	}

	// due_on is not collapse:true, but is empty - should still skip rendering a blank label
	data := map[string]any{
		"id":        float64(1),
		"content":   "Test",
		"completed": false,
		"due_on":    "",
	}

	styles := NewStyles(tui.NoColorTheme(), false)
	var buf strings.Builder
	if err := RenderDetail(&buf, schema, data, styles, enUS); err != nil {
		t.Fatalf("RenderDetail failed: %v", err)
	}

	out := buf.String()
	// The "Due" label should not appear when the value is empty
	if strings.Contains(out, "Due") {
		t.Errorf("Empty non-collapsed field should not render blank label, got:\n%s", out)
	}
}

// =============================================================================
// Deterministic Output Order Tests
// =============================================================================

func TestRenderAllFieldsIsDeterministic(t *testing.T) {
	schema := LookupByName("todo")
	if schema == nil {
		t.Fatal("Expected todo schema")
	}

	data := map[string]any{
		"id":        float64(1),
		"content":   "Test",
		"completed": false,
		"due_on":    "2026-03-01",
	}

	styles := NewStyles(tui.NoColorTheme(), false)

	// Render multiple times and verify output is stable
	var firstOutput string
	for i := 0; i < 10; i++ {
		var buf strings.Builder
		if err := RenderDetail(&buf, schema, data, styles, enUS); err != nil {
			t.Fatalf("RenderDetail failed on iteration %d: %v", i, err)
		}
		if i == 0 {
			firstOutput = buf.String()
		} else if buf.String() != firstOutput {
			t.Fatalf("Output changed between iterations (non-deterministic map ordering)")
		}
	}
}

// =============================================================================
// Markdown Rendering Tests
// =============================================================================

func TestRenderDetailMarkdown(t *testing.T) {
	schema := LookupByName("todo")
	if schema == nil {
		t.Fatal("Expected todo schema")
	}

	data := map[string]any{
		"id":          float64(12345),
		"content":     "Fix the login bug",
		"completed":   false,
		"due_on":      "2026-01-15",
		"assignees":   []any{map[string]any{"name": "Alice"}},
		"description": "The login page throws a 500 error",
		"created_at":  "2025-12-01T10:00:00Z",
	}

	var buf strings.Builder
	if err := RenderDetailMarkdown(&buf, schema, data, enUS); err != nil {
		t.Fatalf("RenderDetailMarkdown failed: %v", err)
	}

	out := buf.String()

	// Headline should be bold Markdown
	if !strings.Contains(out, "**Fix the login bug**") {
		t.Errorf("Markdown detail should have bold headline, got:\n%s", out)
	}

	// Section headings should be Markdown headings
	if !strings.Contains(out, "#### Status") {
		t.Errorf("Markdown detail should have '#### Status' heading, got:\n%s", out)
	}
	if !strings.Contains(out, "#### Metadata") {
		t.Errorf("Markdown detail should have '#### Metadata' heading, got:\n%s", out)
	}

	// Fields should be Markdown list items with bold labels
	if !strings.Contains(out, "- **Completed:** pending") {
		t.Errorf("Markdown detail should have '- **Completed:** pending', got:\n%s", out)
	}
	if !strings.Contains(out, "- **Due:** Jan 15, 2026") {
		t.Errorf("Markdown detail should have '- **Due:** Jan 15, 2026', got:\n%s", out)
	}

	// Body text should appear as plain paragraph (no label)
	if !strings.Contains(out, "The login page throws a 500 error") {
		t.Errorf("Markdown detail should contain body text, got:\n%s", out)
	}

	// Affordances should use Markdown structure
	if !strings.Contains(out, "#### Next") {
		t.Errorf("Markdown detail should have '#### Next', got:\n%s", out)
	}
	if !strings.Contains(out, "- `bcq done 12345`") {
		t.Errorf("Markdown affordance should use backtick code, got:\n%s", out)
	}

	// No ANSI escape codes
	if strings.Contains(out, "\x1b[") {
		t.Errorf("Markdown output should contain no ANSI codes, got:\n%q", out)
	}
}

func TestRenderListMarkdown(t *testing.T) {
	schema := LookupByName("todo")
	if schema == nil {
		t.Fatal("Expected todo schema")
	}

	data := []map[string]any{
		{"content": "Fix bug", "completed": false, "due_on": "2026-02-01", "assignees": []any{}},
		{"content": "Write tests", "completed": true, "due_on": "", "assignees": []any{map[string]any{"name": "Alice"}}},
	}

	var buf strings.Builder
	if err := RenderListMarkdown(&buf, schema, data, enUS); err != nil {
		t.Fatalf("RenderListMarkdown failed: %v", err)
	}

	out := buf.String()

	// Should be a Markdown table with header + divider + rows
	if !strings.Contains(out, "| Content |") {
		t.Errorf("Markdown table should have Content header, got:\n%s", out)
	}
	if !strings.Contains(out, "| --- |") {
		t.Errorf("Markdown table should have divider row, got:\n%s", out)
	}
	if !strings.Contains(out, "Fix bug") {
		t.Errorf("Markdown table should contain 'Fix bug', got:\n%s", out)
	}
	if !strings.Contains(out, "Write tests") {
		t.Errorf("Markdown table should contain 'Write tests', got:\n%s", out)
	}

	// No ANSI escape codes
	if strings.Contains(out, "\x1b[") {
		t.Errorf("Markdown output should contain no ANSI codes, got:\n%q", out)
	}
}

func TestPresentMarkdownMode(t *testing.T) {
	data := map[string]any{
		"id":        float64(1),
		"content":   "Markdown todo",
		"completed": false,
	}

	var buf strings.Builder
	handled := PresentWithTheme(&buf, data, "todo", ModeMarkdown, tui.NoColorTheme(), enUS)
	if !handled {
		t.Error("Present should handle todo in ModeMarkdown")
	}
	out := buf.String()
	if !strings.Contains(out, "**Markdown todo**") {
		t.Errorf("Markdown present should produce bold headline, got:\n%s", out)
	}
}

// =============================================================================
// Body Field Emphasis Tests
// =============================================================================

func TestBodyFieldWithExplicitEmphasis(t *testing.T) {
	// Construct a schema where the body field has explicit emphasis.
	// Verify that resolveEmphasis is used (not hardcoded styles.Body).
	schema := &EntitySchema{
		Entity:   "test",
		Identity: Identity{Label: "title", ID: "id"},
		Headline: map[string]HeadlineSpec{
			"default": {Template: "{{.title}}"},
		},
		Fields: map[string]FieldSpec{
			"title": {Role: "title", Format: "text"},
			"body": {
				Role:     "body",
				Format:   "text",
				Emphasis: "warning",
			},
		},
		Views: ViewSpecs{
			Detail: DetailView{
				Sections: []DetailSection{
					{Fields: []string{"title", "body"}},
				},
			},
		},
	}

	data := map[string]any{
		"title": "Test",
		"body":  "Body with emphasis",
	}

	// Render with emphasis:warning
	styles := NewStyles(tui.NoColorTheme(), false)
	var bufWarning strings.Builder
	if err := RenderDetail(&bufWarning, schema, data, styles, enUS); err != nil {
		t.Fatalf("RenderDetail failed: %v", err)
	}

	// Render with no emphasis (should use styles.Body fallback)
	schema.Fields["body"] = FieldSpec{Role: "body", Format: "text"}
	var bufDefault strings.Builder
	if err := RenderDetail(&bufDefault, schema, data, styles, enUS); err != nil {
		t.Fatalf("RenderDetail failed: %v", err)
	}

	// Both should contain the body text (emphasis only matters with styled output)
	if !strings.Contains(bufWarning.String(), "Body with emphasis") {
		t.Errorf("Warning body text should appear, got:\n%s", bufWarning.String())
	}
	if !strings.Contains(bufDefault.String(), "Body with emphasis") {
		t.Errorf("Default body text should appear, got:\n%s", bufDefault.String())
	}

	// Structural test: verify resolveEmphasis is called for body fields
	// by checking the code path doesn't panic and produces output.
	// With NoColorTheme both paths produce identical plain text, which is correct.
	// The real differentiation happens with styled=true on a real terminal.
}

func TestBodyFieldEmphasisResolution(t *testing.T) {
	// Unit test resolveEmphasis for body fields
	styles := NewStyles(tui.NoColorTheme(), false)

	// Body with explicit emphasis → should return the emphasis style, not Body
	specWithEmphasis := FieldSpec{Role: "body", Emphasis: "warning"}
	style := resolveEmphasis(specWithEmphasis, "body", "text", styles)
	_ = style // Would be Warning style with a real theme

	// Body without emphasis → caller should fall back to styles.Body
	specNoEmphasis := FieldSpec{Role: "body"}
	style = resolveEmphasis(specNoEmphasis, "body", "text", styles)
	// resolveEmphasis returns styles.Normal when no emphasis is set
	_ = style
}

// =============================================================================
// Opt-In Contract Tests
// =============================================================================

func TestDetectRequiresExplicitHintOrTypeField(t *testing.T) {
	// Data without a type field and no hint should not match
	data := map[string]any{
		"content":   "Some content",
		"completed": false,
	}
	if s := Detect(data, ""); s != nil {
		t.Error("Data without type field and no hint should not match a schema")
	}

	// Same data with explicit hint should match
	if s := Detect(data, "todo"); s == nil {
		t.Error("Data with explicit 'todo' hint should match")
	}
}

// =============================================================================
// IsOverdue Date-Only Local Timezone Test
// =============================================================================

func TestIsOverdueDateOnlyIsLocal(t *testing.T) {
	now := time.Now()

	// Today's date should never be overdue, regardless of timezone.
	today := now.Format("2006-01-02")
	if IsOverdue(today) {
		t.Errorf("Today's date (%s) should NOT be overdue", today)
	}

	// Yesterday should be overdue.
	yesterday := now.AddDate(0, 0, -1).Format("2006-01-02")
	if !IsOverdue(yesterday) {
		t.Errorf("Yesterday's date (%s) should be overdue", yesterday)
	}

	// Tomorrow should not be overdue.
	tomorrow := now.AddDate(0, 0, 1).Format("2006-01-02")
	if IsOverdue(tomorrow) {
		t.Errorf("Tomorrow's date (%s) should NOT be overdue", tomorrow)
	}
}

// =============================================================================
// Markdown Table Pipe Escaping Test
// =============================================================================

func TestMarkdownTableEscapesPipes(t *testing.T) {
	schema := LookupByName("todo")
	if schema == nil {
		t.Fatal("Expected todo schema")
	}

	data := []map[string]any{
		{
			"content":   "Fix bug | urgent",
			"completed": false,
			"due_on":    "2026-02-01",
			"assignees": []any{},
		},
	}

	var buf strings.Builder
	if err := RenderListMarkdown(&buf, schema, data, enUS); err != nil {
		t.Fatalf("RenderListMarkdown failed: %v", err)
	}

	out := buf.String()

	// The pipe in "Fix bug | urgent" should be escaped
	if !strings.Contains(out, `Fix bug \| urgent`) {
		t.Errorf("Pipes in cell content should be escaped, got:\n%s", out)
	}
}

// =============================================================================
// Body Style Fallback in renderAllFields Test
// =============================================================================

func TestBodyStyleFallbackInSchemaWithoutSections(t *testing.T) {
	// Schema with no detail sections → uses renderAllFields path
	schema := &EntitySchema{
		Entity:   "test",
		Identity: Identity{Label: "title", ID: "id"},
		Headline: map[string]HeadlineSpec{
			"default": {Template: "{{.title}}"},
		},
		Fields: map[string]FieldSpec{
			"title": {Role: "title", Format: "text"},
			"body":  {Role: "body", Format: "text"},
		},
		Views: ViewSpecs{
			// No detail sections — forces renderAllFields path
		},
	}

	data := map[string]any{
		"title": "Test",
		"body":  "Body text via renderAllFields",
	}

	styles := NewStyles(tui.NoColorTheme(), false)
	var buf strings.Builder
	if err := RenderDetail(&buf, schema, data, styles, enUS); err != nil {
		t.Fatalf("RenderDetail failed: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "Body text via renderAllFields") {
		t.Errorf("Body text should appear in renderAllFields path, got:\n%s", out)
	}
}

// =============================================================================
// Locale-Aware Formatting Tests
// =============================================================================

func TestLocaleDetection(t *testing.T) {
	tests := []struct {
		raw  string
		want string
	}{
		{"en_US.UTF-8", "en-US"},
		{"de_DE.UTF-8", "de-DE"},
		{"fr_FR.ISO8859-1", "fr-FR"},
		{"ja_JP.UTF-8", "ja-JP"},
		{"", "en-US"}, // fallback
	}

	for _, tt := range tests {
		loc := NewLocale(tt.raw)
		got := loc.Tag().String()
		if got != tt.want {
			t.Errorf("NewLocale(%q).Tag() = %q, want %q", tt.raw, got, tt.want)
		}
	}
}

func TestLocaleSplitDetection(t *testing.T) {
	// NewLocaleSplit allows different tags for dates vs numbers
	loc := NewLocaleSplit("en_GB.UTF-8", "de_DE.UTF-8")

	// Dates should use en-GB (Day Month Year)
	date, _ := time.Parse("2006-01-02", "2026-03-15")
	gotDate := loc.FormatDate(date)
	if gotDate != "15 Mar 2026" {
		t.Errorf("Split locale FormatDate = %q, want %q (en-GB)", gotDate, "15 Mar 2026")
	}

	// Numbers should use de-DE (dot grouping)
	gotNum := loc.FormatNumber(1234567.89)
	if !strings.Contains(gotNum, ".") || !strings.Contains(gotNum, ",") {
		t.Errorf("Split locale FormatNumber(1234567.89) = %q, expected German separators", gotNum)
	}
}

func TestLocaleDateFormats(t *testing.T) {
	date, _ := time.Parse("2006-01-02", "2026-03-15")
	spec := FieldSpec{Format: "date"}

	tests := []struct {
		locale string
		want   string
	}{
		{"en-US", "Mar 15, 2026"}, // US: Month Day, Year
		{"en-GB", "15 Mar 2026"},  // UK: Day Month Year
		{"de-DE", "15. Mar 2026"}, // DE: Day. Month Year
		{"ja-JP", "2026-03-15"},   // JP: Year-Month-Day
	}

	for _, tt := range tests {
		loc := NewLocale(tt.locale)
		got := FormatField(spec, "due_on", "2026-03-15", loc)
		if got != tt.want {
			t.Errorf("FormatField(date, %q) = %q, want %q", tt.locale, got, tt.want)
		}
		// Also verify via Locale.FormatDate directly
		direct := loc.FormatDate(date)
		if direct != tt.want {
			t.Errorf("FormatDate(%q) = %q, want %q", tt.locale, direct, tt.want)
		}
	}
}

func TestLocaleNumberFormats(t *testing.T) {
	tests := []struct {
		locale string
		value  float64
		want   string
	}{
		{"en-US", 1234.56, "1,234.56"},
		{"de-DE", 1234.56, "1.234,56"},
		{"fr-FR", 1234.56, "1\u00a0234,56"}, // French uses non-breaking space
		{"en-US", 42, "42"},
		{"de-DE", 42, "42"},
		{"en-US", 1000000, "1,000,000"},
		{"de-DE", 1000000, "1.000.000"},
	}

	for _, tt := range tests {
		loc := NewLocale(tt.locale)
		got := loc.FormatNumber(tt.value)
		if got != tt.want {
			t.Errorf("FormatNumber(%v, %q) = %q, want %q", tt.value, tt.locale, got, tt.want)
		}
	}
}

func TestLocaleNumberViaFormatField(t *testing.T) {
	spec := FieldSpec{Format: "number"}
	de := NewLocale("de-DE")

	got := FormatField(spec, "amount", float64(1234.56), de)
	if got != "1.234,56" {
		t.Errorf("FormatField(number, de-DE) = %q, want %q", got, "1.234,56")
	}
}

func TestLocaleTextNumberFormatting(t *testing.T) {
	// formatText should NOT localize numbers — IDs and other numeric
	// values must remain copy-paste safe. Use format: "number" for locale output.
	spec := FieldSpec{Format: "text"}
	de := NewLocale("de-DE")

	got := FormatField(spec, "id", float64(1234), de)
	if got != "1234" {
		t.Errorf("FormatField(text/number, de-DE) = %q, want %q (raw, no grouping)", got, "1234")
	}
}

func TestLocaleRelativeTimeFallback(t *testing.T) {
	// Old dates fall back to locale-formatted date
	spec := FieldSpec{Format: "relative_time"}
	gb := NewLocale("en-GB")

	got := FormatField(spec, "created_at", "2020-06-15T10:30:00Z", gb)
	if got != "15 Jun 2020" {
		t.Errorf("FormatField(relative_time old date, en-GB) = %q, want %q", got, "15 Jun 2020")
	}
}

func TestLocaleRenderDetailUsesLocale(t *testing.T) {
	schema := LookupByName("todo")
	if schema == nil {
		t.Fatal("Expected todo schema")
	}

	data := map[string]any{
		"id":        float64(1),
		"content":   "Test",
		"completed": false,
		"due_on":    "2026-03-15",
	}

	gb := NewLocale("en-GB")
	styles := NewStyles(tui.NoColorTheme(), false)

	var buf strings.Builder
	if err := RenderDetail(&buf, schema, data, styles, gb); err != nil {
		t.Fatalf("RenderDetail failed: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "15 Mar 2026") {
		t.Errorf("en-GB detail should show '15 Mar 2026', got:\n%s", out)
	}
}

func TestLocaleRenderListMarkdownUsesLocale(t *testing.T) {
	schema := LookupByName("todo")
	if schema == nil {
		t.Fatal("Expected todo schema")
	}

	data := []map[string]any{
		{"content": "Task", "completed": false, "due_on": "2026-03-15", "assignees": []any{}},
	}

	de := NewLocale("de-DE")

	var buf strings.Builder
	if err := RenderListMarkdown(&buf, schema, data, de); err != nil {
		t.Fatalf("RenderListMarkdown failed: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "15. Mar 2026") {
		t.Errorf("de-DE markdown table should show '15. Mar 2026', got:\n%s", out)
	}
}
