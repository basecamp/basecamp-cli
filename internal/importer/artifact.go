package importer

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	artifactFormat        = "basecamp-import-csv-v1"
	artifactManifestName  = "import.json"
	artifactTodosFileName = "todos.csv"
	artifactCardsFileName = "cards.csv"
)

var artifactTodoHeader = []string{
	"source_path",
	"source_row",
	"source_record_id",
	"project_id",
	"project_name",
	"todolist_id",
	"todolist_name",
	"title",
	"description",
	"due_on",
	"assignee_emails",
	"assignee_names",
	"status",
	"attachment_urls_json",
	"comments_json",
	"custom_fields_json",
}

var artifactCardHeader = []string{
	"source_path",
	"source_row",
	"source_record_id",
	"project_id",
	"project_name",
	"card_table_id",
	"column_id",
	"column_name",
	"title",
	"content",
	"due_on",
	"assignee_emails",
	"assignee_names",
	"status",
	"attachment_urls_json",
	"comments_json",
	"custom_fields_json",
}

// ImportArtifactManifest describes a validated Basecamp import CSV artifact.
type ImportArtifactManifest struct {
	SchemaVersion     int               `json:"schema_version"`
	Status            string            `json:"status"`
	ArtifactFormat    string            `json:"artifact_format"`
	SourcePath        string            `json:"source_path"`
	SourceFingerprint Fingerprint       `json:"source_fingerprint"`
	Destination       DestinationConfig `json:"destination"`
	Counts            PlanCounts        `json:"counts"`
	Files             ArtifactFiles     `json:"files"`
}

// ArtifactFiles names the files that belong to an import artifact.
type ArtifactFiles struct {
	Todos string `json:"todos,omitempty"`
	Cards string `json:"cards,omitempty"`
}

// CompileArtifactResult reports the artifact written by CompileArtifact.
type CompileArtifactResult struct {
	SchemaVersion int                    `json:"schema_version"`
	Status        string                 `json:"status"`
	ArtifactPath  string                 `json:"artifact_path"`
	Manifest      ImportArtifactManifest `json:"manifest"`
}

type artifactTodoRow struct {
	SourcePath     string            `json:"source_path"`
	SourceRow      int               `json:"source_row"`
	SourceRecordID string            `json:"source_record_id"`
	ProjectID      string            `json:"project_id"`
	ProjectName    string            `json:"project_name"`
	CardTableID    int64             `json:"card_table_id,omitempty"`
	TodolistID     int64             `json:"todolist_id"`
	TodolistName   string            `json:"todolist_name"`
	Title          string            `json:"title"`
	Description    string            `json:"description"`
	DueOn          string            `json:"due_on"`
	AssigneeEmails []string          `json:"assignee_emails"`
	AssigneeNames  []string          `json:"assignee_names"`
	Status         string            `json:"status"`
	AttachmentURLs []string          `json:"attachment_urls"`
	Comments       []string          `json:"comments"`
	CustomFields   map[string]string `json:"custom_fields"`
}

// CompileArtifact writes a validated Basecamp import CSV artifact from confirmed import inputs.
func CompileArtifact(inspection *Inspection, mapping *MappingConfig, destination *DestinationConfig, outDir string) (*CompileArtifactResult, error) {
	if strings.TrimSpace(outDir) == "" {
		return nil, fmt.Errorf("artifact output directory is required")
	}
	plan, err := PlanImport(inspection, mapping, destination)
	if err != nil {
		return nil, err
	}
	if plan.RequiresUserInput {
		return nil, fmt.Errorf("import artifact requires confirmed mapping and destination choices")
	}

	resourceType, err := destinationResourceType(&plan.Destination)
	if err != nil {
		return nil, err
	}
	rowCap := plan.Counts.Todos
	if resourceType == resourceTypeCards {
		rowCap = plan.Counts.Cards
	}
	rows := make([]artifactTodoRow, 0, rowCap)
	for _, op := range plan.Operations {
		if resourceType == resourceTypeTodos && op.Op != "create_todo" {
			continue
		}
		if resourceType == resourceTypeCards && op.Op != "create_card" {
			continue
		}
		if strings.TrimSpace(op.Title) == "" {
			return nil, fmt.Errorf("source row %d has a blank title", op.SourceRow)
		}
		emails, names := splitAssignees(op.Assignees)
		groupID := op.TodolistID
		if resourceType == resourceTypeCards {
			groupID = op.ColumnID
		}
		parsedGroupID, err := parseOptionalInt64(groupID)
		if err != nil {
			return nil, fmt.Errorf("source row %d has invalid destination group id: %w", op.SourceRow, err)
		}
		cardTableID, err := parseOptionalInt64(op.CardTableID)
		if err != nil {
			return nil, fmt.Errorf("source row %d has invalid card_table_id: %w", op.SourceRow, err)
		}
		groupName := op.TodolistName
		if resourceType == resourceTypeCards {
			groupName = op.ColumnName
		}
		rows = append(rows, artifactTodoRow{
			SourcePath:     inspection.ExportPath,
			SourceRow:      op.SourceRow,
			SourceRecordID: op.SourceRecordID,
			ProjectID:      op.ProjectID,
			ProjectName:    op.ProjectName,
			CardTableID:    cardTableID,
			TodolistID:     parsedGroupID,
			TodolistName:   groupName,
			Title:          op.Title,
			Description:    op.Description,
			DueOn:          op.DueOn,
			AssigneeEmails: emails,
			AssigneeNames:  names,
			Status:         op.Status,
			AttachmentURLs: op.AttachmentURLs,
			Comments:       op.Comments,
			CustomFields:   op.CustomFields,
		})
	}

	files := ArtifactFiles{Todos: artifactTodosFileName}
	if resourceType == resourceTypeCards {
		files = ArtifactFiles{Cards: artifactCardsFileName}
	}
	manifest := ImportArtifactManifest{
		SchemaVersion:     planSchemaVersion,
		Status:            "compiled",
		ArtifactFormat:    artifactFormat,
		SourcePath:        inspection.ExportPath,
		SourceFingerprint: inspection.Fingerprint,
		Destination:       *destination,
		Counts:            plan.Counts,
		Files:             files,
	}
	if err := writeArtifact(outDir, manifest, rows); err != nil {
		return nil, err
	}
	return &CompileArtifactResult{SchemaVersion: planSchemaVersion, Status: "compiled", ArtifactPath: outDir, Manifest: manifest}, nil
}

// PlanFromArtifact builds a deterministic dry-run from a validated Basecamp import CSV artifact.
func PlanFromArtifact(artifactDir string) (*Plan, error) {
	manifest, rows, err := readArtifact(artifactDir)
	if err != nil {
		return nil, err
	}
	plan := &Plan{
		SchemaVersion:     planSchemaVersion,
		Status:            "ready_for_approval",
		RequiresUserInput: false,
		SourceFingerprint: manifest.SourceFingerprint,
		Destination:       manifest.Destination,
		Counts:            manifest.Counts,
		Warnings:          []ImportWarning{},
		Questions:         []MappingQuestion{},
	}

	resourceType, err := destinationResourceType(&manifest.Destination)
	if err != nil {
		return nil, err
	}
	operations := make([]PlannedOperation, 0, len(rows)+manifest.Counts.Todolists+manifest.Counts.CardColumns+manifest.Counts.Projects)
	if manifest.Destination.Mode == "new_project" {
		operations = append(operations, PlannedOperation{Op: "create_project", ProjectName: manifest.Destination.ProjectName})
	}
	if resourceType == resourceTypeTodos && shouldCreateTodolists(&manifest.Destination) {
		for _, name := range artifactTodolistNames(rows) {
			operations = append(operations, PlannedOperation{Op: "create_todolist", ProjectID: manifest.Destination.ProjectID, ProjectName: manifest.Destination.ProjectName, TodolistName: name})
		}
	}
	if resourceType == resourceTypeCards && shouldCreateCardColumns(&manifest.Destination) {
		for _, name := range artifactCardColumnNames(rows) {
			operations = append(operations, PlannedOperation{Op: "create_card_column", ProjectID: manifest.Destination.ProjectID, ProjectName: manifest.Destination.ProjectName, CardTableID: manifest.Destination.CardTableID, ColumnName: name})
		}
	}
	for _, row := range rows {
		op := PlannedOperation{
			Op:             "create_todo",
			SourceRow:      row.SourceRow,
			SourceRecordID: row.SourceRecordID,
			ProjectID:      row.ProjectID,
			ProjectName:    row.ProjectName,
			TodolistID:     formatOptionalInt64(row.TodolistID),
			TodolistName:   row.TodolistName,
			Title:          row.Title,
			Description:    row.Description,
			Status:         row.Status,
			DueOn:          row.DueOn,
			Assignees:      append(append([]string{}, row.AssigneeEmails...), row.AssigneeNames...),
			AttachmentURLs: row.AttachmentURLs,
			Comments:       row.Comments,
			CustomFields:   row.CustomFields,
		}
		if resourceType == resourceTypeCards {
			op.Op = "create_card"
			op.CardTableID = formatOptionalInt64(row.CardTableID)
			op.ColumnID = formatOptionalInt64(row.TodolistID)
			op.ColumnName = row.TodolistName
			op.TodolistID = ""
			op.TodolistName = ""
		}
		operations = append(operations, op)
	}
	plan.Operations = operations
	plan.DryRunMarkdown = renderDryRunMarkdown(plan)
	return plan, nil
}

func readArtifact(artifactDir string) (*ImportArtifactManifest, []artifactTodoRow, error) {
	manifestPath := filepath.Join(artifactDir, artifactManifestName)
	var manifest ImportArtifactManifest
	if err := readJSONData(manifestPath, &manifest); err != nil {
		return nil, nil, err
	}
	if manifest.SchemaVersion != planSchemaVersion {
		return nil, nil, fmt.Errorf("unsupported artifact schema_version %d", manifest.SchemaVersion)
	}
	if manifest.ArtifactFormat != artifactFormat {
		return nil, nil, fmt.Errorf("unsupported artifact format %q", manifest.ArtifactFormat)
	}
	resourceType, err := destinationResourceType(&manifest.Destination)
	if err != nil {
		return nil, nil, err
	}
	if resourceType == resourceTypeCards {
		cardsPath, err := artifactMemberPath(artifactDir, manifest.Files.Cards, artifactCardsFileName)
		if err != nil {
			return nil, nil, err
		}
		rows, err := readArtifactCards(cardsPath)
		if err != nil {
			return nil, nil, err
		}
		if len(rows) != manifest.Counts.Cards {
			return nil, nil, fmt.Errorf("artifact card count %d does not match manifest count %d", len(rows), manifest.Counts.Cards)
		}
		return &manifest, rows, nil
	}
	todosPath, err := artifactMemberPath(artifactDir, manifest.Files.Todos, artifactTodosFileName)
	if err != nil {
		return nil, nil, err
	}
	rows, err := readArtifactTodos(todosPath)
	if err != nil {
		return nil, nil, err
	}
	if len(rows) != manifest.Counts.Todos {
		return nil, nil, fmt.Errorf("artifact todo count %d does not match manifest count %d", len(rows), manifest.Counts.Todos)
	}
	return &manifest, rows, nil
}

func artifactMemberPath(artifactDir, filename, expected string) (string, error) {
	if filename == "" {
		return "", fmt.Errorf("artifact %s file is required", strings.TrimSuffix(expected, ".csv"))
	}
	if filename != expected {
		return "", fmt.Errorf("artifact %s file must be %s", strings.TrimSuffix(expected, ".csv"), expected)
	}
	return filepath.Join(artifactDir, expected), nil
}

func writeArtifact(outDir string, manifest ImportArtifactManifest, rows []artifactTodoRow) error {
	if err := os.MkdirAll(outDir, 0o750); err != nil {
		return fmt.Errorf("create artifact directory: %w", err)
	}
	manifestData, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("encode artifact manifest: %w", err)
	}
	manifestData = append(manifestData, '\n')
	if err := os.WriteFile(filepath.Join(outDir, artifactManifestName), manifestData, 0o600); err != nil {
		return fmt.Errorf("write artifact manifest: %w", err)
	}
	if manifest.Files.Cards != "" {
		if err := writeArtifactCards(filepath.Join(outDir, manifest.Files.Cards), rows); err != nil {
			return err
		}
		return nil
	}
	if err := writeArtifactTodos(filepath.Join(outDir, manifest.Files.Todos), rows); err != nil {
		return err
	}
	return nil
}

func writeArtifactTodos(path string, rows []artifactTodoRow) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600) // #nosec G304 -- artifact file paths are compiled from the selected artifact directory
	if err != nil {
		return fmt.Errorf("write artifact todos: %w", err)
	}
	defer file.Close()

	writer := csv.NewWriter(file)
	if err := writer.Write(artifactTodoHeader); err != nil {
		return fmt.Errorf("write artifact todos header: %w", err)
	}
	for _, row := range rows {
		record, err := row.toCSVRecord()
		if err != nil {
			return err
		}
		if err := writer.Write(record); err != nil {
			return fmt.Errorf("write artifact todos row: %w", err)
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return fmt.Errorf("write artifact todos: %w", err)
	}
	return nil
}

func writeArtifactCards(path string, rows []artifactTodoRow) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600) // #nosec G304 -- artifact file paths are compiled from the selected artifact directory
	if err != nil {
		return fmt.Errorf("write artifact cards: %w", err)
	}
	defer file.Close()

	writer := csv.NewWriter(file)
	if err := writer.Write(artifactCardHeader); err != nil {
		return fmt.Errorf("write artifact cards header: %w", err)
	}
	for _, row := range rows {
		record, err := row.toCardCSVRecord()
		if err != nil {
			return err
		}
		if err := writer.Write(record); err != nil {
			return fmt.Errorf("write artifact cards row: %w", err)
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return fmt.Errorf("write artifact cards: %w", err)
	}
	return nil
}

func readArtifactTodos(path string) ([]artifactTodoRow, error) {
	file, err := os.Open(path) // #nosec G304 -- artifact readers validate user-selected artifact files
	if err != nil {
		return nil, fmt.Errorf("read artifact todos: %w", err)
	}
	defer file.Close()

	reader := csv.NewReader(file)
	reader.FieldsPerRecord = len(artifactTodoHeader)
	records, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("parse artifact todos: %w", err)
	}
	if len(records) == 0 {
		return nil, fmt.Errorf("artifact todos file is empty")
	}
	if strings.Join(records[0], "\x00") != strings.Join(artifactTodoHeader, "\x00") {
		return nil, fmt.Errorf("artifact todos header does not match Basecamp import CSV v1")
	}
	rows := make([]artifactTodoRow, 0, len(records)-1)
	for i, record := range records[1:] {
		row, err := artifactTodoRowFromCSVRecord(record)
		if err != nil {
			return nil, fmt.Errorf("artifact todos row %d: %w", i+1, err)
		}
		if strings.TrimSpace(row.Title) == "" {
			return nil, fmt.Errorf("artifact todos row %d has a blank title", i+1)
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func readArtifactCards(path string) ([]artifactTodoRow, error) {
	file, err := os.Open(path) // #nosec G304 -- artifact readers validate user-selected artifact files
	if err != nil {
		return nil, fmt.Errorf("read artifact cards: %w", err)
	}
	defer file.Close()

	reader := csv.NewReader(file)
	reader.FieldsPerRecord = len(artifactCardHeader)
	records, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("parse artifact cards: %w", err)
	}
	if len(records) == 0 {
		return nil, fmt.Errorf("artifact cards file is empty")
	}
	if strings.Join(records[0], "\x00") != strings.Join(artifactCardHeader, "\x00") {
		return nil, fmt.Errorf("artifact cards header does not match Basecamp import CSV v1")
	}
	rows := make([]artifactTodoRow, 0, len(records)-1)
	for i, record := range records[1:] {
		row, err := artifactCardRowFromCSVRecord(record)
		if err != nil {
			return nil, fmt.Errorf("artifact cards row %d: %w", i+1, err)
		}
		if strings.TrimSpace(row.Title) == "" {
			return nil, fmt.Errorf("artifact cards row %d has a blank title", i+1)
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func (r artifactTodoRow) toCSVRecord() ([]string, error) {
	attachments, err := encodeJSONStringSlice(r.AttachmentURLs)
	if err != nil {
		return nil, err
	}
	comments, err := encodeJSONStringSlice(r.Comments)
	if err != nil {
		return nil, err
	}
	customFields, err := encodeJSONStringMap(r.CustomFields)
	if err != nil {
		return nil, err
	}
	return []string{
		r.SourcePath,
		fmt.Sprintf("%d", r.SourceRow),
		r.SourceRecordID,
		r.ProjectID,
		r.ProjectName,
		formatOptionalInt64(r.TodolistID),
		r.TodolistName,
		r.Title,
		r.Description,
		r.DueOn,
		strings.Join(r.AssigneeEmails, ";"),
		strings.Join(r.AssigneeNames, ";"),
		r.Status,
		attachments,
		comments,
		customFields,
	}, nil
}

func (r artifactTodoRow) toCardCSVRecord() ([]string, error) {
	attachments, err := encodeJSONStringSlice(r.AttachmentURLs)
	if err != nil {
		return nil, err
	}
	comments, err := encodeJSONStringSlice(r.Comments)
	if err != nil {
		return nil, err
	}
	customFields, err := encodeJSONStringMap(r.CustomFields)
	if err != nil {
		return nil, err
	}
	return []string{
		r.SourcePath,
		fmt.Sprintf("%d", r.SourceRow),
		r.SourceRecordID,
		r.ProjectID,
		r.ProjectName,
		formatOptionalInt64(r.CardTableID),
		formatOptionalInt64(r.TodolistID),
		r.TodolistName,
		r.Title,
		r.Description,
		r.DueOn,
		strings.Join(r.AssigneeEmails, ";"),
		strings.Join(r.AssigneeNames, ";"),
		r.Status,
		attachments,
		comments,
		customFields,
	}, nil
}

func artifactTodoRowFromCSVRecord(record []string) (artifactTodoRow, error) {
	var row artifactTodoRow
	row.SourcePath = record[0]
	if _, err := fmt.Sscanf(record[1], "%d", &row.SourceRow); err != nil {
		return row, fmt.Errorf("invalid source_row %q", record[1])
	}
	row.SourceRecordID = record[2]
	row.ProjectID = record[3]
	row.ProjectName = record[4]
	var err error
	row.TodolistID, err = parseOptionalInt64(record[5])
	if err != nil {
		return row, fmt.Errorf("invalid todolist_id %q", record[5])
	}
	row.TodolistName = record[6]
	row.Title = record[7]
	row.Description = record[8]
	row.DueOn = record[9]
	row.AssigneeEmails = splitSemicolonList(record[10])
	row.AssigneeNames = splitSemicolonList(record[11])
	row.Status = record[12]
	if err := decodeJSONStringSlice(record[13], &row.AttachmentURLs); err != nil {
		return row, fmt.Errorf("invalid attachment_urls_json: %w", err)
	}
	if err := decodeJSONStringSlice(record[14], &row.Comments); err != nil {
		return row, fmt.Errorf("invalid comments_json: %w", err)
	}
	if err := decodeJSONStringMap(record[15], &row.CustomFields); err != nil {
		return row, fmt.Errorf("invalid custom_fields_json: %w", err)
	}
	return row, nil
}

func artifactCardRowFromCSVRecord(record []string) (artifactTodoRow, error) {
	var row artifactTodoRow
	row.SourcePath = record[0]
	if _, err := fmt.Sscanf(record[1], "%d", &row.SourceRow); err != nil {
		return row, fmt.Errorf("invalid source_row %q", record[1])
	}
	row.SourceRecordID = record[2]
	row.ProjectID = record[3]
	row.ProjectName = record[4]
	var err error
	row.CardTableID, err = parseOptionalInt64(record[5])
	if err != nil {
		return row, fmt.Errorf("invalid card_table_id %q", record[5])
	}
	row.TodolistID, err = parseOptionalInt64(record[6])
	if err != nil {
		return row, fmt.Errorf("invalid column_id %q", record[6])
	}
	row.TodolistName = record[7]
	row.Title = record[8]
	row.Description = record[9]
	row.DueOn = record[10]
	row.AssigneeEmails = splitSemicolonList(record[11])
	row.AssigneeNames = splitSemicolonList(record[12])
	row.Status = record[13]
	if err := decodeJSONStringSlice(record[14], &row.AttachmentURLs); err != nil {
		return row, fmt.Errorf("invalid attachment_urls_json: %w", err)
	}
	if err := decodeJSONStringSlice(record[15], &row.Comments); err != nil {
		return row, fmt.Errorf("invalid comments_json: %w", err)
	}
	if err := decodeJSONStringMap(record[16], &row.CustomFields); err != nil {
		return row, fmt.Errorf("invalid custom_fields_json: %w", err)
	}
	return row, nil
}

func splitAssignees(values []string) ([]string, []string) {
	emails := make([]string, 0)
	names := make([]string, 0)
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if emailRE.MatchString(value) {
			emails = append(emails, value)
		} else {
			names = append(names, value)
		}
	}
	return emails, names
}

func artifactTodolistNames(rows []artifactTodoRow) []string {
	seen := make(map[string]struct{})
	out := make([]string, 0)
	for _, row := range rows {
		name := strings.TrimSpace(row.TodolistName)
		if name == "" {
			name = "Imported todos"
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out
}

func artifactCardColumnNames(rows []artifactTodoRow) []string {
	seen := make(map[string]struct{})
	out := make([]string, 0)
	for _, row := range rows {
		name := strings.TrimSpace(row.TodolistName)
		if name == "" {
			name = "Imported cards"
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out
}

func encodeJSONStringSlice(values []string) (string, error) {
	if values == nil {
		values = []string{}
	}
	data, err := json.Marshal(values)
	if err != nil {
		return "", fmt.Errorf("encode JSON array: %w", err)
	}
	return string(data), nil
}

func encodeJSONStringMap(values map[string]string) (string, error) {
	if values == nil {
		values = map[string]string{}
	}
	data, err := json.Marshal(values)
	if err != nil {
		return "", fmt.Errorf("encode JSON object: %w", err)
	}
	return string(data), nil
}

func decodeJSONStringSlice(value string, target *[]string) error {
	if strings.TrimSpace(value) == "" {
		*target = nil
		return nil
	}
	return json.Unmarshal([]byte(value), target)
}

func decodeJSONStringMap(value string, target *map[string]string) error {
	if strings.TrimSpace(value) == "" {
		*target = nil
		return nil
	}
	return json.Unmarshal([]byte(value), target)
}

func splitSemicolonList(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ";")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}
