// Package importer provides deterministic import inspection and planning helpers.
package importer

import (
	"bytes"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
)

const inspectionSchemaVersion = 1

var (
	emailRE  = regexp.MustCompile(`(?i)^[a-z0-9._%+\-]+@[a-z0-9.\-]+\.[a-z]{2,}$`)
	urlRE    = regexp.MustCompile(`(?i)https?://[^\s,]+`)
	idLikeRE = regexp.MustCompile(`(?i)^([a-z]+-)?[a-z0-9][a-z0-9_.:\-/]{1,79}$`)
)

// InspectOptions controls CSV inspection detail.
type InspectOptions struct {
	SampleSize int
}

// Inspection describes a profiled CSV export.
type Inspection struct {
	SchemaVersion    int                        `json:"schema_version"`
	Status           string                     `json:"status"`
	Format           string                     `json:"format"`
	ExportPath       string                     `json:"export_path"`
	Fingerprint      Fingerprint                `json:"fingerprint"`
	Dialect          CSVDialect                 `json:"dialect"`
	RowCount         int                        `json:"row_count"`
	Columns          []ColumnProfile            `json:"columns"`
	DuplicateHeaders []DuplicateHeader          `json:"duplicate_headers"`
	RoleCandidates   map[string][]RoleCandidate `json:"role_candidates"`
	SampleRows       []SampleRow                `json:"sample_rows"`
	Warnings         []ImportWarning            `json:"warnings"`
	Questions        []MappingQuestion          `json:"questions"`
}

// Fingerprint identifies the exact inspected input bytes.
type Fingerprint struct {
	Algorithm string `json:"algorithm"`
	Value     string `json:"value"`
}

// CSVDialect records the parsing choices used for the CSV.
type CSVDialect struct {
	Delimiter string `json:"delimiter"`
	HasHeader bool   `json:"has_header"`
	Encoding  string `json:"encoding"`
}

// ColumnProfile contains deterministic statistics and generic shape evidence for one CSV column.
type ColumnProfile struct {
	Index          int      `json:"index"`
	Name           string   `json:"name"`
	NormalizedName string   `json:"normalized_name"`
	NonEmptyCount  int      `json:"non_empty_count"`
	UniqueCount    int      `json:"unique_count"`
	DuplicateName  bool     `json:"duplicate_name"`
	LooksLike      []string `json:"looks_like,omitempty"`
	Evidence       []string `json:"evidence,omitempty"`
}

// DuplicateHeader reports a repeated header while preserving every column index.
type DuplicateHeader struct {
	Name    string `json:"name"`
	Indexes []int  `json:"indexes"`
}

// RoleCandidate describes a column that can fill an import mapping role.
type RoleCandidate struct {
	ColumnIndex int      `json:"column_index"`
	ColumnName  string   `json:"column_name"`
	Confidence  float64  `json:"confidence"`
	Evidence    []string `json:"evidence"`
}

// SampleRow exposes bounded source values for mapping confirmation.
type SampleRow struct {
	RowNumber     int               `json:"row_number"`
	ValuesByIndex map[string]string `json:"values_by_index"`
}

// ImportWarning highlights inspection facts that affect safe mapping.
type ImportWarning struct {
	Code    string `json:"code"`
	Columns []int  `json:"columns,omitempty"`
	Message string `json:"message"`
}

// MappingQuestion asks for a specific user-confirmed mapping choice.
type MappingQuestion struct {
	ID      string `json:"id"`
	Prompt  string `json:"prompt"`
	Choices []int  `json:"choices,omitempty"`
}

type columnStats struct {
	profile        ColumnProfile
	values         []string
	nonEmpty       []string
	unique         map[string]struct{}
	emailCount     int
	urlCount       int
	idLikeCount    int
	dateCount      int
	longCount      int
	multilineCount int
}

// InspectCSV profiles a CSV file and returns deterministic mapping evidence.
func InspectCSV(path string, opts InspectOptions) (*Inspection, error) {
	if opts.SampleSize <= 0 {
		opts.SampleSize = 5
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read CSV: %w", err)
	}

	sum := sha256.Sum256(data)
	delimiter := detectDelimiter(data)
	records, err := readCSV(data, delimiter)
	if err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return nil, fmt.Errorf("CSV contains no rows")
	}

	headers := append([]string(nil), records[0]...)
	dataRows := records[1:]
	maxCols := len(headers)
	for _, row := range dataRows {
		if len(row) > maxCols {
			maxCols = len(row)
		}
	}
	for len(headers) < maxCols {
		headers = append(headers, fmt.Sprintf("Column %d", len(headers)+1))
	}

	duplicateByName := duplicateHeaderIndexes(headers)
	duplicateHeaders := make([]DuplicateHeader, 0)
	for name, indexes := range duplicateByName {
		if len(indexes) > 1 {
			duplicateHeaders = append(duplicateHeaders, DuplicateHeader{Name: name, Indexes: indexes})
		}
	}
	sort.Slice(duplicateHeaders, func(i, j int) bool {
		return duplicateHeaders[i].Indexes[0] < duplicateHeaders[j].Indexes[0]
	})

	stats := make([]*columnStats, maxCols)
	for i := range stats {
		name := headers[i]
		stats[i] = &columnStats{
			profile: ColumnProfile{
				Index:          i,
				Name:           name,
				NormalizedName: normalizeName(name),
				DuplicateName:  len(duplicateByName[normalizeName(name)]) > 1,
			},
			unique: make(map[string]struct{}),
		}
	}

	for _, row := range dataRows {
		for i := range stats {
			value := ""
			if i < len(row) {
				value = row[i]
			}
			stats[i].values = append(stats[i].values, value)
			trimmed := strings.TrimSpace(value)
			if trimmed == "" {
				continue
			}
			stats[i].profile.NonEmptyCount++
			stats[i].nonEmpty = append(stats[i].nonEmpty, trimmed)
			stats[i].unique[trimmed] = struct{}{}
			if emailRE.MatchString(trimmed) {
				stats[i].emailCount++
			}
			if urlRE.MatchString(trimmed) {
				stats[i].urlCount++
			}
			if looksIDLike(trimmed) {
				stats[i].idLikeCount++
			}
			if looksDateLike(trimmed) {
				stats[i].dateCount++
			}
			if len([]rune(trimmed)) > 100 {
				stats[i].longCount++
			}
			if strings.Contains(trimmed, "\n") || strings.Contains(trimmed, "\r") {
				stats[i].multilineCount++
			}
		}
	}

	idValueSets := make([]map[string]struct{}, 0)
	for _, st := range stats {
		st.profile.UniqueCount = len(st.unique)
		st.profile.LooksLike, st.profile.Evidence = inferLooksLike(st, len(dataRows))
		if hasLook(st.profile.LooksLike, "stable_id") {
			idValueSets = append(idValueSets, st.unique)
		}
	}
	for _, st := range stats {
		if !hasLook(st.profile.LooksLike, "stable_id") && !hasLook(st.profile.LooksLike, "parent_reference") && referencesKnownIDs(st, idValueSets) {
			st.profile.LooksLike = appendSortedUnique(st.profile.LooksLike, "parent_reference")
			st.profile.Evidence = append(st.profile.Evidence, "values match stable ID candidates")
		}
	}

	columns := make([]ColumnProfile, len(stats))
	for i, st := range stats {
		columns[i] = st.profile
	}

	inspection := &Inspection{
		SchemaVersion: inspectionSchemaVersion,
		Status:        "profiled",
		Format:        "csv",
		ExportPath:    path,
		Fingerprint: Fingerprint{
			Algorithm: "sha256-file-v1",
			Value:     hex.EncodeToString(sum[:]),
		},
		Dialect: CSVDialect{
			Delimiter: string(delimiter),
			HasHeader: true,
			Encoding:  "utf-8",
		},
		RowCount:         len(dataRows),
		Columns:          columns,
		DuplicateHeaders: duplicateHeaders,
		RoleCandidates:   inferRoleCandidates(stats, len(dataRows)),
		SampleRows:       sampleRows(dataRows, maxCols, opts.SampleSize),
	}
	inspection.Warnings = buildWarnings(inspection, stats)
	inspection.Questions = buildQuestions(inspection)
	return inspection, nil
}

func readCSV(data []byte, delimiter rune) ([][]string, error) {
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	reader := csv.NewReader(bytes.NewReader(data))
	reader.Comma = delimiter
	reader.FieldsPerRecord = -1
	reader.LazyQuotes = true
	records, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("parse CSV: %w", err)
	}
	return records, nil
}

func detectDelimiter(data []byte) rune {
	candidates := []rune{',', '\t', ';', '|'}
	best := ','
	bestScore := -1
	for _, candidate := range candidates {
		records, err := readCSVPreview(data, candidate)
		if err != nil || len(records) == 0 {
			continue
		}
		score := 0
		width := 0
		consistent := true
		for i, record := range records {
			if len(record) > 1 {
				score += len(record)
			}
			if i == 0 {
				width = len(record)
			} else if len(record) != width {
				consistent = false
			}
		}
		if consistent {
			score += 10
		}
		if score > bestScore {
			bestScore = score
			best = candidate
		}
	}
	return best
}

func readCSVPreview(data []byte, delimiter rune) ([][]string, error) {
	reader := csv.NewReader(bytes.NewReader(bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})))
	reader.Comma = delimiter
	reader.FieldsPerRecord = -1
	reader.LazyQuotes = true
	var records [][]string
	for len(records) < 10 {
		record, err := reader.Read()
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
		records = append(records, record)
	}
	return records, nil
}

func duplicateHeaderIndexes(headers []string) map[string][]int {
	out := make(map[string][]int)
	for i, header := range headers {
		out[normalizeName(header)] = append(out[normalizeName(header)], i)
	}
	return out
}

func normalizeName(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	var b strings.Builder
	lastSpace := false
	for _, r := range s {
		if unicode.IsSpace(r) || r == '_' || r == '-' {
			if !lastSpace {
				b.WriteRune(' ')
				lastSpace = true
			}
			continue
		}
		b.WriteRune(r)
		lastSpace = false
	}
	return strings.TrimSpace(b.String())
}

func inferLooksLike(st *columnStats, rowCount int) ([]string, []string) {
	looks := make([]string, 0)
	evidence := make([]string, 0)
	name := st.profile.NormalizedName
	nonEmpty := st.profile.NonEmptyCount
	unique := len(st.unique)

	if nonEmpty > 0 && unique == nonEmpty && (nameMatches(name, "id", "key") || st.idLikeCount*2 >= nonEmpty) {
		looks = appendSortedUnique(looks, "stable_id")
		evidence = append(evidence, fmt.Sprintf("%d/%d non-empty values are unique", unique, nonEmpty))
	}
	if isTitleName(name) {
		looks = appendSortedUnique(looks, "title")
		evidence = append(evidence, "header indicates title text")
	}
	if nameMatches(name, "description", "notes", "content", "body") || (nonEmpty > 0 && st.longCount*2 >= nonEmpty) {
		looks = appendSortedUnique(looks, "description")
		evidence = append(evidence, "header or values indicate long text")
	}
	if nameMatches(name, "status", "state", "resolution") || (nonEmpty > 0 && unique <= max(8, rowCount/4) && (strings.Contains(name, "status") || strings.Contains(name, "state"))) {
		looks = appendSortedUnique(looks, "status")
		evidence = append(evidence, fmt.Sprintf("%d distinct non-empty values", unique))
	}
	if nameMatches(name, "list", "section", "column", "project", "folder", "space", "team", "category") && !strings.Contains(name, "id") {
		looks = appendSortedUnique(looks, "todolist")
		evidence = append(evidence, "header indicates grouping/container")
	}
	if nameMatches(name, "assignee", "owner", "responsible", "member", "members") || st.emailCount > 0 {
		looks = appendSortedUnique(looks, "assignees")
		if st.emailCount > 0 {
			evidence = append(evidence, fmt.Sprintf("%d email-like values", st.emailCount))
		} else {
			evidence = append(evidence, "header indicates people")
		}
	}
	if nameMatches(name, "due", "deadline") || (st.dateCount > 0 && strings.Contains(name, "date")) {
		looks = appendSortedUnique(looks, "due_on")
		evidence = append(evidence, fmt.Sprintf("%d date-like values", st.dateCount))
	}
	if nameMatches(name, "attachment", "attachments", "file", "files", "link", "url") || st.urlCount > 0 {
		looks = appendSortedUnique(looks, "attachment_urls")
		evidence = append(evidence, fmt.Sprintf("%d URL-like values", st.urlCount))
	}
	if nameMatches(name, "comment", "comments") || st.multilineCount > 0 {
		looks = appendSortedUnique(looks, "comments")
		evidence = append(evidence, "header or values indicate comments")
	}
	if nameMatches(name, "parent", "parent id", "depends", "dependency", "blocking", "blocked by", "related") {
		looks = appendSortedUnique(looks, "parent_reference")
		evidence = append(evidence, "header indicates parent or relationship reference")
	}
	return looks, evidence
}

func inferRoleCandidates(stats []*columnStats, rowCount int) map[string][]RoleCandidate {
	roles := []string{"record_id", "title", "description", "todolist", "status", "assignees", "due_on", "attachment_urls", "comments", "parent_reference", "custom_fields"}
	out := make(map[string][]RoleCandidate, len(roles))
	for _, role := range roles {
		var candidates []RoleCandidate
		for _, st := range stats {
			confidence, evidence := scoreRole(role, st, rowCount)
			if confidence <= 0 {
				continue
			}
			candidates = append(candidates, RoleCandidate{
				ColumnIndex: st.profile.Index,
				ColumnName:  st.profile.Name,
				Confidence:  confidence,
				Evidence:    evidence,
			})
		}
		sort.SliceStable(candidates, func(i, j int) bool {
			if candidates[i].Confidence == candidates[j].Confidence {
				return candidates[i].ColumnIndex < candidates[j].ColumnIndex
			}
			return candidates[i].Confidence > candidates[j].Confidence
		})
		out[role] = candidates
	}
	return out
}

func scoreRole(role string, st *columnStats, rowCount int) (float64, []string) {
	name := st.profile.NormalizedName
	nonEmpty := st.profile.NonEmptyCount
	unique := st.profile.UniqueCount
	var score float64
	var evidence []string
	add := func(points float64, text string) {
		score += points
		evidence = append(evidence, text)
	}

	switch role {
	case "record_id":
		if nameMatches(name, "id", "key", "task id", "issue id", "issue key", "card id") {
			add(0.55, "header indicates record ID")
		}
		if nonEmpty > 0 && unique == nonEmpty {
			add(0.25, "values are unique")
		}
		if nonEmpty > 0 && st.idLikeCount*2 >= nonEmpty {
			add(0.2, "values look like identifiers")
		}
	case "title":
		if isTitleName(name) {
			add(0.7, "header indicates todo title")
		}
		if nonEmpty > 0 && averageLength(st.nonEmpty) <= 120 {
			add(0.15, "values are title-length text")
		}
	case "description":
		if nameMatches(name, "description", "notes", "content") {
			add(0.65, "header indicates description")
		}
		if nonEmpty > 0 && (averageLength(st.nonEmpty) > 80 || st.multilineCount > 0) {
			add(0.25, "values contain long or multiline text")
		}
	case "todolist":
		if nameMatches(name, "list", "section", "column", "project", "folder", "space", "team") && !strings.Contains(name, "id") {
			add(0.6, "header indicates grouping")
		}
		if nonEmpty > 0 && unique < nonEmpty {
			add(0.2, "values repeat across rows")
		}
	case "status":
		if nameMatches(name, "status", "state") {
			add(0.7, "header indicates status")
		}
		if nonEmpty > 0 && unique <= max(12, rowCount/3) {
			add(0.15, "values are low-cardinality")
		}
	case "assignees":
		if nameMatches(name, "assignee", "owner", "responsible", "member", "members") {
			add(0.65, "header indicates assignees")
		}
		if st.emailCount > 0 {
			add(0.25, "values include email addresses")
		}
	case "due_on":
		if nameMatches(name, "due", "deadline") {
			add(0.65, "header indicates due date")
		}
		if nonEmpty > 0 && st.dateCount*2 >= nonEmpty {
			add(0.25, "values are date-like")
		}
	case "attachment_urls":
		if nameMatches(name, "attachment", "attachments", "file", "files", "link", "url") {
			add(0.55, "header indicates attachments or URLs")
		}
		if st.urlCount > 0 {
			add(0.35, "values include URLs")
		}
	case "comments":
		if nameMatches(name, "comment", "comments") {
			add(0.65, "header indicates comments")
		}
		if st.multilineCount > 0 {
			add(0.2, "values include multiline text")
		}
	case "parent_reference":
		if nameMatches(name, "parent", "parent id", "depends", "dependency", "blocking", "blocked by", "related") {
			add(0.75, "header indicates parent or relationship reference")
		}
	case "custom_fields":
		if nonEmpty > 0 {
			add(0.2, "column contains data")
		}
	}
	if score > 1 {
		score = 1
	}
	if score < 0.2 {
		return 0, nil
	}
	return roundConfidence(score), evidence
}

func buildWarnings(inspection *Inspection, stats []*columnStats) []ImportWarning {
	warnings := make([]ImportWarning, 0)
	if len(inspection.DuplicateHeaders) > 0 {
		cols := make([]int, 0)
		for _, dup := range inspection.DuplicateHeaders {
			cols = append(cols, dup.Indexes...)
		}
		warnings = append(warnings, ImportWarning{Code: "duplicate_headers", Columns: cols, Message: "Duplicate headers are present. Column indexes distinguish repeated names."})
	}
	if len(inspection.RoleCandidates["title"]) == 0 {
		warnings = append(warnings, ImportWarning{Code: "no_obvious_title_column", Message: "No obvious todo title column was detected. Confirm a title column before planning an import."})
	}
	for _, st := range stats {
		if hasLook(st.profile.LooksLike, "assignees") && st.profile.NonEmptyCount > 0 && st.emailCount == 0 {
			warnings = append(warnings, ImportWarning{Code: "people_look_like_display_names", Columns: []int{st.profile.Index}, Message: "Assignee values look like display names rather than email addresses. Confirm mapping before assigning Basecamp people."})
		}
		if hasLook(st.profile.LooksLike, "attachment_urls") && (st.urlCount > 0 || nameMatches(st.profile.NormalizedName, "attachment", "attachments")) {
			warnings = append(warnings, ImportWarning{Code: "attachment_fields_detected", Columns: []int{st.profile.Index}, Message: "Attachment or URL fields are present. Confirm how these values should be preserved before importing."})
		}
		if hasLook(st.profile.LooksLike, "parent_reference") {
			warnings = append(warnings, ImportWarning{Code: "parent_references_detected", Columns: []int{st.profile.Index}, Message: "Parent or relationship references are present. Confirm how hierarchy should be represented in Basecamp."})
		}
	}
	return warnings
}

func buildQuestions(inspection *Inspection) []MappingQuestion {
	questions := make([]MappingQuestion, 0)
	questions = append(questions, MappingQuestion{ID: "confirm_title_column", Prompt: "Which column should become the Basecamp todo title?", Choices: candidateIndexes(inspection.RoleCandidates["title"])})
	if len(inspection.RoleCandidates["todolist"]) > 0 {
		questions = append(questions, MappingQuestion{ID: "confirm_todolist_column", Prompt: "Which column should group todos into Basecamp todolists?", Choices: candidateIndexes(inspection.RoleCandidates["todolist"])})
	}
	if len(inspection.RoleCandidates["assignees"]) > 0 {
		questions = append(questions, MappingQuestion{ID: "confirm_assignee_policy", Prompt: "Should assignee values be imported, and how should ambiguous people be handled?", Choices: candidateIndexes(inspection.RoleCandidates["assignees"])})
	}
	if len(inspection.RoleCandidates["due_on"]) > 1 {
		questions = append(questions, MappingQuestion{ID: "confirm_due_date_column", Prompt: "Which date column should become the Basecamp todo due date?", Choices: candidateIndexes(inspection.RoleCandidates["due_on"])})
	}
	questions = append(questions, MappingQuestion{ID: "confirm_custom_fields_policy", Prompt: "Should unmapped non-empty columns be preserved as metadata?"})
	return questions
}

func sampleRows(rows [][]string, maxCols, sampleSize int) []SampleRow {
	limit := sampleSize
	if len(rows) < limit {
		limit = len(rows)
	}
	out := make([]SampleRow, 0, limit)
	for i := 0; i < limit; i++ {
		values := make(map[string]string)
		for col := 0; col < maxCols; col++ {
			value := ""
			if col < len(rows[i]) {
				value = rows[i][col]
			}
			value = strings.TrimSpace(value)
			if value == "" {
				continue
			}
			values[fmt.Sprintf("%d", col)] = truncateRunes(value, 200)
		}
		out = append(out, SampleRow{RowNumber: i + 1, ValuesByIndex: values})
	}
	return out
}

func candidateIndexes(candidates []RoleCandidate) []int {
	out := make([]int, len(candidates))
	for i, candidate := range candidates {
		out[i] = candidate.ColumnIndex
	}
	return out
}

func isTitleName(name string) bool {
	if strings.Contains(name, "project name") || strings.Contains(name, "user name") || strings.Contains(name, "file name") {
		return false
	}
	return exactOrSuffixName(name,
		"title", "name", "summary", "task name", "card name", "subject", "headline",
		"action", "task", "activity", "chore", "assignment", "deliverable", "procedure", "step",
		"hypothesis", "matter", "line item", "visit reason", "concern", "item", "agenda item",
		"milestone", "initiative", "issue", "device", "asset", "campaign", "deal", "post", "work order",
	)
}

func nameMatches(name string, terms ...string) bool {
	for _, term := range terms {
		term = normalizeName(term)
		if name == term || strings.Contains(name, term) {
			return true
		}
	}
	return false
}

func exactOrSuffixName(name string, terms ...string) bool {
	for _, term := range terms {
		term = normalizeName(term)
		if name == term || strings.HasSuffix(name, " "+term) {
			return true
		}
	}
	return false
}

func looksIDLike(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || strings.Contains(value, " ") || strings.Contains(value, "@") || urlRE.MatchString(value) || looksDateLike(value) {
		return false
	}
	hasDigit := false
	for _, r := range value {
		if unicode.IsDigit(r) {
			hasDigit = true
			break
		}
	}
	return hasDigit && idLikeRE.MatchString(value)
}

func looksDateLike(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	layouts := []string{
		time.RFC3339,
		"2006-01-02",
		"2006-01-02 15:04:05",
		"2006-01-02 15:04",
		"2006/01/02",
		"01/02/2006",
		"1/2/2006",
		"02 Jan 2006",
		"Jan 2, 2006",
		"January 2, 2006",
	}
	for _, layout := range layouts {
		if _, err := time.Parse(layout, value); err == nil {
			return true
		}
	}
	return false
}

func referencesKnownIDs(st *columnStats, idValueSets []map[string]struct{}) bool {
	if st.profile.NonEmptyCount == 0 || len(idValueSets) == 0 {
		return false
	}
	matches := 0
	for _, value := range st.nonEmpty {
		for _, set := range idValueSets {
			if _, ok := set[value]; ok {
				matches++
				break
			}
		}
	}
	return matches > 0
}

func hasLook(looks []string, target string) bool {
	for _, look := range looks {
		if look == target {
			return true
		}
	}
	return false
}

func appendSortedUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	values = append(values, value)
	sort.Strings(values)
	return values
}

func averageLength(values []string) float64 {
	if len(values) == 0 {
		return 0
	}
	total := 0
	for _, value := range values {
		total += len([]rune(value))
	}
	return float64(total) / float64(len(values))
}

func roundConfidence(v float64) float64 {
	return float64(int(v*100+0.5)) / 100
}

func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "…"
}
