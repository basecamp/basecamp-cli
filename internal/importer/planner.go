package importer

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

const planSchemaVersion = 1

const (
	resourceTypeTodos = "todos"
	resourceTypeCards = "cards"
)

// MappingConfig records user-confirmed CSV-to-Basecamp mapping choices.
type MappingConfig struct {
	SchemaVersion  int         `json:"schema_version"`
	RecordID       *ColumnRef  `json:"record_id,omitempty"`
	Title          *ColumnRef  `json:"title,omitempty"`
	Description    *ColumnRef  `json:"description,omitempty"`
	Todolist       *ColumnRef  `json:"todolist,omitempty"`
	Column         *ColumnRef  `json:"column,omitempty"`
	Status         *ColumnRef  `json:"status,omitempty"`
	Assignees      *ColumnRef  `json:"assignees,omitempty"`
	DueOn          *ColumnRef  `json:"due_on,omitempty"`
	AttachmentURLs []ColumnRef `json:"attachment_urls,omitempty"`
	Comments       []ColumnRef `json:"comments,omitempty"`
	CustomFields   string      `json:"custom_fields,omitempty"`
}

// ColumnRef identifies a CSV column by stable index and optional display name.
type ColumnRef struct {
	ColumnIndex   int    `json:"column_index"`
	ColumnName    string `json:"column_name,omitempty"`
	MappingPolicy string `json:"mapping_policy,omitempty"`
	DateOrder     string `json:"date_order,omitempty"`
}

// DestinationConfig records the Basecamp destination choices for a plan.
type DestinationConfig struct {
	SchemaVersion    int    `json:"schema_version"`
	ResourceType     string `json:"resource_type,omitempty"`
	Mode             string `json:"mode"`
	ProjectID        string `json:"project_id,omitempty"`
	ProjectName      string `json:"project_name,omitempty"`
	TodolistStrategy string `json:"todolist_strategy,omitempty"`
	TodolistID       string `json:"todolist_id,omitempty"`
	TodolistName     string `json:"todolist_name,omitempty"`
	CardTableID      string `json:"card_table_id,omitempty"`
	ColumnStrategy   string `json:"column_strategy,omitempty"`
	ColumnID         string `json:"column_id,omitempty"`
	ColumnName       string `json:"column_name,omitempty"`
}

// Plan describes the deterministic dry-run generated from confirmed mappings.
type Plan struct {
	SchemaVersion     int                `json:"schema_version"`
	Status            string             `json:"status"`
	RequiresUserInput bool               `json:"requires_user_input"`
	SourceFingerprint Fingerprint        `json:"source_fingerprint"`
	Destination       DestinationConfig  `json:"destination"`
	Counts            PlanCounts         `json:"counts"`
	DryRunMarkdown    string             `json:"dry_run_markdown"`
	Operations        []PlannedOperation `json:"operations"`
	Questions         []MappingQuestion  `json:"questions"`
	Warnings          []ImportWarning    `json:"warnings"`
}

// PlanCounts summarizes planned write operations.
type PlanCounts struct {
	Projects    int `json:"projects"`
	Todolists   int `json:"todolists"`
	Todos       int `json:"todos"`
	CardColumns int `json:"card_columns,omitempty"`
	Cards       int `json:"cards,omitempty"`
}

// PlannedOperation is one Basecamp write that can be executed after approval.
type PlannedOperation struct {
	Op             string            `json:"op"`
	SourceRow      int               `json:"source_row,omitempty"`
	SourceRecordID string            `json:"source_record_id,omitempty"`
	ProjectID      string            `json:"project_id,omitempty"`
	ProjectName    string            `json:"project_name,omitempty"`
	TodolistID     string            `json:"todolist_id,omitempty"`
	TodolistName   string            `json:"todolist_name,omitempty"`
	CardTableID    string            `json:"card_table_id,omitempty"`
	ColumnID       string            `json:"column_id,omitempty"`
	ColumnName     string            `json:"column_name,omitempty"`
	Title          string            `json:"title,omitempty"`
	Description    string            `json:"description,omitempty"`
	Status         string            `json:"status,omitempty"`
	DueOn          string            `json:"due_on,omitempty"`
	Assignees      []string          `json:"assignees,omitempty"`
	AttachmentURLs []string          `json:"attachment_urls,omitempty"`
	Comments       []string          `json:"comments,omitempty"`
	CustomFields   map[string]string `json:"custom_fields,omitempty"`
}

// PlanImport builds a deterministic dry-run from an inspection, mapping, and destination.
func PlanImport(inspection *Inspection, mapping *MappingConfig, destination *DestinationConfig) (*Plan, error) {
	if inspection == nil {
		return nil, fmt.Errorf("inspection is required")
	}
	if mapping == nil {
		return nil, fmt.Errorf("mapping is required")
	}
	if destination == nil {
		return nil, fmt.Errorf("destination is required")
	}
	if inspection.SchemaVersion != inspectionSchemaVersion {
		return nil, fmt.Errorf("unsupported inspection schema_version %d", inspection.SchemaVersion)
	}
	if mapping.SchemaVersion != planSchemaVersion {
		return nil, fmt.Errorf("unsupported mapping schema_version %d", mapping.SchemaVersion)
	}
	if destination.SchemaVersion != planSchemaVersion {
		return nil, fmt.Errorf("unsupported destination schema_version %d", destination.SchemaVersion)
	}

	records, err := recordsForInspection(inspection)
	if err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return nil, fmt.Errorf("inspection source contains no rows")
	}
	rows := records[1:]
	if len(rows) != inspection.RowCount {
		return nil, fmt.Errorf("inspection row count %d does not match source row count %d", inspection.RowCount, len(rows))
	}

	if err := validateMappingIndexes(inspection, mapping); err != nil {
		return nil, err
	}

	plan := &Plan{
		SchemaVersion:     planSchemaVersion,
		Status:            "ready_for_approval",
		SourceFingerprint: inspection.Fingerprint,
		Destination:       *destination,
		Warnings:          make([]ImportWarning, 0),
		Questions:         make([]MappingQuestion, 0),
	}

	resourceType, err := destinationResourceType(destination)
	if err != nil {
		return nil, err
	}
	plan.Destination.ResourceType = resourceType

	if mapping.Title == nil {
		plan.RequiresUserInput = true
		plan.Questions = append(plan.Questions, MappingQuestion{ID: "confirm_title_column", Prompt: fmt.Sprintf("Which column should become the Basecamp %s title?", resourceSingular(resourceType)), Choices: candidateIndexes(inspection.RoleCandidates["title"])})
	}
	if destination.Mode == "" {
		plan.RequiresUserInput = true
		plan.Questions = append(plan.Questions, MappingQuestion{ID: "confirm_destination", Prompt: fmt.Sprintf("Which Basecamp project should receive the imported %s?", resourceType)})
	}
	if resourceType == resourceTypeTodos && destination.TodolistStrategy == "create_from_column" && mapping.Todolist == nil {
		plan.RequiresUserInput = true
		plan.Questions = append(plan.Questions, MappingQuestion{ID: "confirm_todolist_column", Prompt: "Which column should group todos into Basecamp todolists?", Choices: candidateIndexes(inspection.RoleCandidates["todolist"])})
	}
	if resourceType == resourceTypeCards && destination.ColumnStrategy == "create_from_column" && cardColumnMapping(mapping) == nil {
		plan.RequiresUserInput = true
		plan.Questions = append(plan.Questions, MappingQuestion{ID: "confirm_card_column", Prompt: "Which column should group cards into Basecamp card table columns?", Choices: candidateIndexes(inspection.RoleCandidates["todolist"])})
	}
	if assigneeNeedsPolicy(rows, mapping) {
		plan.RequiresUserInput = true
		plan.Warnings = append(plan.Warnings, ImportWarning{Code: "ambiguous_assignee_values", Columns: []int{mapping.Assignees.ColumnIndex}, Message: "Assignee values include display names. Choose an assignee mapping policy before planning assignments."})
		plan.Questions = append(plan.Questions, MappingQuestion{ID: "confirm_assignee_policy", Prompt: "How should assignee display names be handled?", Choices: []int{mapping.Assignees.ColumnIndex}})
	}
	if plan.RequiresUserInput {
		plan.Status = "requires_user_input"
		plan.DryRunMarkdown = renderDryRunMarkdown(plan)
		return plan, nil
	}

	operations := make([]PlannedOperation, 0, len(rows)+4)
	switch destination.Mode {
	case "new_project":
		if strings.TrimSpace(destination.ProjectName) == "" {
			return nil, fmt.Errorf("destination project_name is required for new_project mode")
		}
		operations = append(operations, PlannedOperation{Op: "create_project", ProjectName: strings.TrimSpace(destination.ProjectName)})
		plan.Counts.Projects = 1
	case "existing_project":
		if strings.TrimSpace(destination.ProjectID) == "" && strings.TrimSpace(destination.ProjectName) == "" {
			return nil, fmt.Errorf("destination project_id or project_name is required for existing_project mode")
		}
	default:
		return nil, fmt.Errorf("unsupported destination mode %q", destination.Mode)
	}

	if resourceType == resourceTypeTodos {
		listNames := plannedTodolistNames(rows, mapping, destination)
		if shouldCreateTodolists(destination) {
			for _, name := range listNames {
				operations = append(operations, PlannedOperation{Op: "create_todolist", ProjectID: destination.ProjectID, ProjectName: destination.ProjectName, TodolistName: name})
			}
			plan.Counts.Todolists = len(listNames)
		}
	} else {
		columnNames := plannedCardColumnNames(rows, mapping, destination)
		if shouldCreateCardColumns(destination) {
			for _, name := range columnNames {
				operations = append(operations, PlannedOperation{Op: "create_card_column", ProjectID: destination.ProjectID, ProjectName: destination.ProjectName, CardTableID: destination.CardTableID, ColumnName: name})
			}
			plan.Counts.CardColumns = len(columnNames)
		}
	}

	dueOnValues, err := normalizedDueOnValues(rows, mapping)
	if err != nil {
		return nil, err
	}

	mapped := mappedColumnIndexes(mapping)
	duplicateColumns := duplicateColumnIndexes(inspection.Columns)
	for rowIndex, row := range rows {
		title := valueAt(row, mapping.Title.ColumnIndex)
		if strings.TrimSpace(title) == "" {
			plan.Warnings = append(plan.Warnings, ImportWarning{Code: "blank_title", Columns: []int{mapping.Title.ColumnIndex}, Message: fmt.Sprintf("Source row %d has a blank title and will be skipped by execution.", rowIndex+1)})
		}

		opName := "create_todo"
		if resourceType == resourceTypeCards {
			opName = "create_card"
		}
		op := PlannedOperation{
			Op:             opName,
			SourceRow:      rowIndex + 1,
			SourceRecordID: mappedValue(row, mapping.RecordID),
			ProjectID:      destination.ProjectID,
			ProjectName:    destination.ProjectName,
			TodolistID:     destination.TodolistID,
			TodolistName:   todolistNameForRow(row, mapping, destination),
			CardTableID:    destination.CardTableID,
			ColumnID:       destination.ColumnID,
			ColumnName:     cardColumnNameForRow(row, mapping, destination),
			Title:          title,
			Description:    mappedValue(row, mapping.Description),
			Status:         mappedValue(row, mapping.Status),
			DueOn:          dueOnValues[rowIndex],
			Assignees:      assigneesForRow(row, mapping),
			AttachmentURLs: mappedValues(row, mapping.AttachmentURLs),
			Comments:       mappedValues(row, mapping.Comments),
			CustomFields:   customFieldsForRow(row, inspection.Columns, mapped, duplicateColumns, mapping.CustomFields),
		}
		operations = append(operations, op)
		if resourceType == resourceTypeCards {
			plan.Counts.Cards++
		} else {
			plan.Counts.Todos++
		}
	}

	plan.Operations = operations
	plan.DryRunMarkdown = renderDryRunMarkdown(plan)
	return plan, nil
}

// ReadInspectionFile reads an inspection JSON file. Files may contain either raw data or a CLI JSON envelope.
func ReadInspectionFile(path string) (*Inspection, error) {
	var inspection Inspection
	if err := readJSONData(path, &inspection); err != nil {
		return nil, err
	}
	return &inspection, nil
}

// ReadMappingFile reads a mapping JSON file.
func ReadMappingFile(path string) (*MappingConfig, error) {
	var mapping MappingConfig
	if err := readJSONData(path, &mapping); err != nil {
		return nil, err
	}
	return &mapping, nil
}

// ReadDestinationFile reads a destination JSON file.
func ReadDestinationFile(path string) (*DestinationConfig, error) {
	var destination DestinationConfig
	if err := readJSONData(path, &destination); err != nil {
		return nil, err
	}
	return &destination, nil
}

func recordsForInspection(inspection *Inspection) ([][]string, error) {
	data, err := os.ReadFile(inspection.ExportPath)
	if err != nil {
		return nil, fmt.Errorf("read inspected CSV: %w", err)
	}
	if inspection.Fingerprint.Algorithm != "sha256-file-v1" {
		return nil, fmt.Errorf("unsupported fingerprint algorithm %q", inspection.Fingerprint.Algorithm)
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != inspection.Fingerprint.Value {
		return nil, fmt.Errorf("inspected CSV fingerprint changed")
	}
	delimiter, err := delimiterRune(inspection.Dialect.Delimiter)
	if err != nil {
		return nil, err
	}
	return readCSV(data, delimiter)
}

func delimiterRune(value string) (rune, error) {
	if value == "\\t" {
		return '\t', nil
	}
	runes := []rune(value)
	if len(runes) != 1 {
		return 0, fmt.Errorf("invalid CSV delimiter %q", value)
	}
	return runes[0], nil
}

func validateMappingIndexes(inspection *Inspection, mapping *MappingConfig) error {
	refs := []struct {
		role string
		ref  *ColumnRef
	}{
		{"record_id", mapping.RecordID},
		{"title", mapping.Title},
		{"description", mapping.Description},
		{"todolist", mapping.Todolist},
		{"column", mapping.Column},
		{"status", mapping.Status},
		{"assignees", mapping.Assignees},
		{"due_on", mapping.DueOn},
	}
	for _, item := range refs {
		if err := validateMappingColumnRef(inspection, item.role, item.ref); err != nil {
			return err
		}
	}
	for i := range mapping.AttachmentURLs {
		if err := validateMappingColumnRef(inspection, fmt.Sprintf("attachment_urls[%d]", i), &mapping.AttachmentURLs[i]); err != nil {
			return err
		}
	}
	for i := range mapping.Comments {
		if err := validateMappingColumnRef(inspection, fmt.Sprintf("comments[%d]", i), &mapping.Comments[i]); err != nil {
			return err
		}
	}
	return nil
}

func validateMappingColumnRef(inspection *Inspection, role string, ref *ColumnRef) error {
	if ref == nil {
		return nil
	}
	maxIndex := len(inspection.Columns) - 1
	if ref.ColumnIndex < 0 || ref.ColumnIndex > maxIndex {
		return fmt.Errorf("mapping %s column_index %d is outside available columns 0..%d", role, ref.ColumnIndex, maxIndex)
	}
	providedName := strings.TrimSpace(ref.ColumnName)
	if providedName == "" {
		return nil
	}
	expectedName := inspection.Columns[ref.ColumnIndex].Name
	if providedName != expectedName {
		return fmt.Errorf("mapping %s column_index %d points to column_name %q, but mapping provides %q", role, ref.ColumnIndex, expectedName, providedName)
	}
	return nil
}

func assigneeNeedsPolicy(rows [][]string, mapping *MappingConfig) bool {
	if mapping.Assignees == nil || mapping.Assignees.MappingPolicy != "" {
		return false
	}
	for _, row := range rows {
		for _, value := range splitPeople(valueAt(row, mapping.Assignees.ColumnIndex)) {
			if value != "" && !emailRE.MatchString(value) {
				return true
			}
		}
	}
	return false
}

func destinationResourceType(destination *DestinationConfig) (string, error) {
	resourceType := strings.TrimSpace(destination.ResourceType)
	if resourceType == "" {
		return resourceTypeTodos, nil
	}
	switch resourceType {
	case resourceTypeTodos, resourceTypeCards:
		return resourceType, nil
	default:
		return "", fmt.Errorf("unsupported destination resource_type %q", resourceType)
	}
}

func resourceSingular(resourceType string) string {
	if resourceType == resourceTypeCards {
		return "card"
	}
	return "todo"
}

func plannedTodolistNames(rows [][]string, mapping *MappingConfig, destination *DestinationConfig) []string {
	if destination.TodolistStrategy == "existing_todolist" {
		return nil
	}
	if destination.TodolistStrategy == "single_todolist" || mapping.Todolist == nil {
		name := strings.TrimSpace(destination.TodolistName)
		if name == "" {
			name = "Imported todos"
		}
		return []string{name}
	}
	seen := make(map[string]struct{})
	out := make([]string, 0)
	for _, row := range rows {
		name := strings.TrimSpace(valueAt(row, mapping.Todolist.ColumnIndex))
		if name == "" {
			name = "Imported todos"
		}
		if _, ok := seen[name]; !ok {
			seen[name] = struct{}{}
			out = append(out, name)
		}
	}
	return out
}

func shouldCreateTodolists(destination *DestinationConfig) bool {
	return destination.TodolistStrategy == "" || destination.TodolistStrategy == "single_todolist" || destination.TodolistStrategy == "create_from_column"
}

func plannedCardColumnNames(rows [][]string, mapping *MappingConfig, destination *DestinationConfig) []string {
	if destination.ColumnStrategy == "existing_column" {
		return nil
	}
	columnRef := cardColumnMapping(mapping)
	if destination.ColumnStrategy == "single_column" || columnRef == nil {
		name := strings.TrimSpace(destination.ColumnName)
		if name == "" {
			name = "Imported cards"
		}
		return []string{name}
	}
	seen := make(map[string]struct{})
	out := make([]string, 0)
	for _, row := range rows {
		name := strings.TrimSpace(valueAt(row, columnRef.ColumnIndex))
		if name == "" {
			name = "Imported cards"
		}
		if _, ok := seen[name]; !ok {
			seen[name] = struct{}{}
			out = append(out, name)
		}
	}
	return out
}

func shouldCreateCardColumns(destination *DestinationConfig) bool {
	return destination.ColumnStrategy == "" || destination.ColumnStrategy == "single_column" || destination.ColumnStrategy == "create_from_column"
}

func cardColumnMapping(mapping *MappingConfig) *ColumnRef {
	if mapping.Column != nil {
		return mapping.Column
	}
	return mapping.Todolist
}

func cardColumnNameForRow(row []string, mapping *MappingConfig, destination *DestinationConfig) string {
	if destination.ColumnStrategy == "existing_column" {
		return destination.ColumnName
	}
	columnRef := cardColumnMapping(mapping)
	if destination.ColumnStrategy == "single_column" || columnRef == nil {
		if strings.TrimSpace(destination.ColumnName) != "" {
			return strings.TrimSpace(destination.ColumnName)
		}
		return "Imported cards"
	}
	value := strings.TrimSpace(valueAt(row, columnRef.ColumnIndex))
	if value == "" {
		return "Imported cards"
	}
	return value
}

func todolistNameForRow(row []string, mapping *MappingConfig, destination *DestinationConfig) string {
	if destination.TodolistStrategy == "existing_todolist" {
		return destination.TodolistName
	}
	if destination.TodolistStrategy == "single_todolist" || mapping.Todolist == nil {
		if strings.TrimSpace(destination.TodolistName) != "" {
			return strings.TrimSpace(destination.TodolistName)
		}
		return "Imported todos"
	}
	value := strings.TrimSpace(valueAt(row, mapping.Todolist.ColumnIndex))
	if value == "" {
		return "Imported todos"
	}
	return value
}

func mappedColumnIndexes(mapping *MappingConfig) map[int]struct{} {
	mapped := make(map[int]struct{})
	add := func(ref *ColumnRef) {
		if ref != nil {
			mapped[ref.ColumnIndex] = struct{}{}
		}
	}
	add(mapping.RecordID)
	add(mapping.Title)
	add(mapping.Description)
	add(mapping.Todolist)
	add(mapping.Column)
	add(mapping.Status)
	add(mapping.Assignees)
	add(mapping.DueOn)
	for i := range mapping.AttachmentURLs {
		mapped[mapping.AttachmentURLs[i].ColumnIndex] = struct{}{}
	}
	for i := range mapping.Comments {
		mapped[mapping.Comments[i].ColumnIndex] = struct{}{}
	}
	return mapped
}

func duplicateColumnIndexes(columns []ColumnProfile) map[int]struct{} {
	out := make(map[int]struct{})
	for _, column := range columns {
		if column.DuplicateName {
			out[column.Index] = struct{}{}
		}
	}
	return out
}

func customFieldsForRow(row []string, columns []ColumnProfile, mapped, duplicateColumns map[int]struct{}, policy string) map[string]string {
	if policy != "all_unmapped_columns" {
		return nil
	}
	out := make(map[string]string)
	for _, column := range columns {
		if _, ok := mapped[column.Index]; ok {
			continue
		}
		value := strings.TrimSpace(valueAt(row, column.Index))
		if value == "" {
			continue
		}
		name := column.Name
		if _, duplicated := duplicateColumns[column.Index]; duplicated {
			name = fmt.Sprintf("%s [%d]", name, column.Index)
		}
		out[name] = value
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func mappedValue(row []string, ref *ColumnRef) string {
	if ref == nil {
		return ""
	}
	return valueAt(row, ref.ColumnIndex)
}

func mappedValues(row []string, refs []ColumnRef) []string {
	out := make([]string, 0, len(refs))
	for _, ref := range refs {
		value := strings.TrimSpace(valueAt(row, ref.ColumnIndex))
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

func assigneesForRow(row []string, mapping *MappingConfig) []string {
	if mapping.Assignees == nil {
		return nil
	}
	values := splitPeople(valueAt(row, mapping.Assignees.ColumnIndex))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if emailRE.MatchString(value) {
			out = append(out, value)
			continue
		}
		if mapping.Assignees.MappingPolicy == "include_display_names" {
			out = append(out, value)
		}
	}
	return out
}

func splitPeople(value string) []string {
	fields := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '\r'
	})
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if field != "" {
			out = append(out, field)
		}
	}
	return out
}

func valueAt(row []string, index int) string {
	if index < 0 || index >= len(row) {
		return ""
	}
	return strings.TrimSpace(row[index])
}

func renderDryRunMarkdown(plan *Plan) string {
	var b strings.Builder
	b.WriteString("# Import dry run\n\n")
	if plan.RequiresUserInput {
		b.WriteString("Status: requires user input\n\n")
	} else {
		b.WriteString("Status: ready for approval\n\n")
	}
	b.WriteString("## Planned writes\n\n")
	fmt.Fprintf(&b, "- Projects: %d\n", plan.Counts.Projects)
	fmt.Fprintf(&b, "- Todolists: %d\n", plan.Counts.Todolists)
	fmt.Fprintf(&b, "- Todos: %d\n", plan.Counts.Todos)
	fmt.Fprintf(&b, "- Card columns: %d\n", plan.Counts.CardColumns)
	fmt.Fprintf(&b, "- Cards: %d\n", plan.Counts.Cards)

	if len(plan.Operations) > 0 {
		b.WriteString("\n## Operations\n\n")
		for _, op := range plan.Operations {
			switch op.Op {
			case "create_project":
				fmt.Fprintf(&b, "- Create project: %s\n", op.ProjectName)
			case "create_todolist":
				fmt.Fprintf(&b, "- Create todolist: %s\n", op.TodolistName)
			case "create_card_column":
				fmt.Fprintf(&b, "- Create card column: %s\n", op.ColumnName)
			case "create_todo":
				fmt.Fprintf(&b, "- Row %d: create todo %q", op.SourceRow, op.Title)
				if op.TodolistName != "" {
					fmt.Fprintf(&b, " in %q", op.TodolistName)
				}
				b.WriteString("\n")
			case "create_card":
				fmt.Fprintf(&b, "- Row %d: create card %q", op.SourceRow, op.Title)
				if op.ColumnName != "" {
					fmt.Fprintf(&b, " in %q", op.ColumnName)
				}
				b.WriteString("\n")
			}
		}
	}

	if len(plan.Warnings) > 0 {
		b.WriteString("\n## Warnings\n\n")
		for _, warning := range plan.Warnings {
			fmt.Fprintf(&b, "- %s: %s\n", warning.Code, warning.Message)
		}
	}
	if len(plan.Questions) > 0 {
		b.WriteString("\n## Questions\n\n")
		for _, question := range plan.Questions {
			fmt.Fprintf(&b, "- %s: %s\n", question.ID, question.Prompt)
		}
	}
	return b.String()
}

func readJSONData(path string, target any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read JSON: %w", err)
	}
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return fmt.Errorf("parse JSON: %w", err)
	}
	if len(envelope.Data) > 0 {
		if err := json.Unmarshal(envelope.Data, target); err != nil {
			return fmt.Errorf("parse JSON data: %w", err)
		}
		return nil
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("parse JSON: %w", err)
	}
	return nil
}
