package importer

import (
	"encoding/json"
	"io/fs"
	"path/filepath"
	"testing"
)

func TestInspectCSVProfilesEveryFixture(t *testing.T) {
	var paths []string
	root := "../../testdata/import/csv"
	if err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && filepath.Ext(path) == ".csv" {
			paths = append(paths, path)
		}
		return nil
	}); err != nil {
		t.Fatalf("walk fixtures: %v", err)
	}
	if len(paths) == 0 {
		t.Fatal("expected CSV fixtures")
	}

	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			inspection, err := InspectCSV(path, InspectOptions{SampleSize: 2})
			if err != nil {
				t.Fatalf("InspectCSV() error = %v", err)
			}
			if inspection.SchemaVersion != 1 {
				t.Fatalf("schema_version = %d, want 1", inspection.SchemaVersion)
			}
			if inspection.Status != "profiled" {
				t.Fatalf("status = %q, want profiled", inspection.Status)
			}
			if inspection.Format != "csv" {
				t.Fatalf("format = %q, want csv", inspection.Format)
			}
			if inspection.Fingerprint.Algorithm != "sha256-file-v1" || inspection.Fingerprint.Value == "" {
				t.Fatalf("fingerprint not populated: %+v", inspection.Fingerprint)
			}
			if inspection.Dialect.Delimiter == "" || !inspection.Dialect.HasHeader {
				t.Fatalf("dialect not populated: %+v", inspection.Dialect)
			}
			if inspection.RowCount <= 0 {
				t.Fatalf("row_count = %d, want > 0", inspection.RowCount)
			}
			if len(inspection.Columns) == 0 {
				t.Fatal("expected column profiles")
			}
			if len(inspection.Questions) == 0 {
				t.Fatal("expected mapping questions")
			}
			if !hasQuestion(inspection, "confirm_title_column") {
				t.Fatal("expected title confirmation question")
			}
			if len(inspection.RoleCandidates["title"]) == 0 && !hasWarning(inspection, "no_obvious_title_column") {
				t.Fatal("expected no_obvious_title_column warning when no title candidate is detected")
			}
			if _, err := json.Marshal(inspection); err != nil {
				t.Fatalf("marshal inspection: %v", err)
			}
		})
	}
}

func hasWarning(inspection *Inspection, code string) bool {
	for _, warning := range inspection.Warnings {
		if warning.Code == code {
			return true
		}
	}
	return false
}

func hasQuestion(inspection *Inspection, id string) bool {
	for _, question := range inspection.Questions {
		if question.ID == id {
			return true
		}
	}
	return false
}

func TestInspectCSVDetectsDuplicateHeadersByIndex(t *testing.T) {
	inspection, err := InspectCSV("../../testdata/import/csv/synthetic/jira-simple.csv", InspectOptions{SampleSize: 1})
	if err != nil {
		t.Fatalf("InspectCSV() error = %v", err)
	}

	if len(inspection.DuplicateHeaders) == 0 {
		t.Fatal("expected duplicate header report")
	}
	var found bool
	for _, duplicate := range inspection.DuplicateHeaders {
		if duplicate.Name == "labels" && len(duplicate.Indexes) == 2 && duplicate.Indexes[0] == 6 && duplicate.Indexes[1] == 7 {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected Labels duplicate at indexes 6 and 7, got %+v", inspection.DuplicateHeaders)
	}
	if !inspection.Columns[6].DuplicateName || !inspection.Columns[7].DuplicateName {
		t.Fatalf("expected duplicate_name on both Labels columns")
	}
}

func TestInspectCSVProducesStableCoreMappingForLinear(t *testing.T) {
	inspection, err := InspectCSV("../../testdata/import/csv/synthetic/linear-simple.csv", InspectOptions{SampleSize: 1})
	if err != nil {
		t.Fatalf("InspectCSV() error = %v", err)
	}

	assertTopCandidate := func(role string, index int) {
		t.Helper()
		candidates := inspection.RoleCandidates[role]
		if len(candidates) == 0 {
			t.Fatalf("role %s has no candidates", role)
		}
		if candidates[0].ColumnIndex != index {
			t.Fatalf("role %s top candidate index = %d, want %d (%+v)", role, candidates[0].ColumnIndex, index, candidates)
		}
	}

	assertTopCandidate("record_id", 0)
	assertTopCandidate("title", 2)
	assertTopCandidate("description", 3)
	assertTopCandidate("status", 4)
	assertTopCandidate("assignees", 10)
}
