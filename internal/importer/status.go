package importer

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ArtifactStatus reports the local state of a compiled import artifact.
type ArtifactStatus struct {
	SchemaVersion     int                   `json:"schema_version"`
	Status            string                `json:"status"`
	ArtifactPath      string                `json:"artifact_path"`
	ArtifactFormat    string                `json:"artifact_format"`
	SourcePath        string                `json:"source_path"`
	SourceFingerprint Fingerprint           `json:"source_fingerprint"`
	Destination       DestinationConfig     `json:"destination"`
	Counts            PlanCounts            `json:"counts"`
	Files             ArtifactFiles         `json:"files"`
	Execution         *ExecutionLedger      `json:"execution,omitempty"`
	Checks            []ArtifactStatusCheck `json:"checks"`
	Guidance          string                `json:"guidance,omitempty"`
}

// ArtifactStatusCheck reports one local artifact status check.
type ArtifactStatusCheck struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

// StatusArtifact reads local artifact files and reports execution state without Basecamp access.
func StatusArtifact(artifactDir string) (*ArtifactStatus, error) {
	manifest, _, err := readArtifact(artifactDir)
	if err != nil {
		return nil, err
	}

	status := &ArtifactStatus{
		SchemaVersion:     planSchemaVersion,
		Status:            "not_executed",
		ArtifactPath:      artifactDir,
		ArtifactFormat:    manifest.ArtifactFormat,
		SourcePath:        manifest.SourcePath,
		SourceFingerprint: manifest.SourceFingerprint,
		Destination:       manifest.Destination,
		Counts:            manifest.Counts,
		Files:             manifest.Files,
		Checks:            []ArtifactStatusCheck{{Name: "artifact", Status: "passed", Message: "Artifact manifest and todo CSV are valid."}},
		Guidance:          "Run preflight before approved execution.",
	}

	ledgerPath := filepath.Join(artifactDir, artifactExecutionFileName)
	ledger, err := readExecutionLedger(ledgerPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			status.Checks = append(status.Checks, ArtifactStatusCheck{Name: "execution_ledger", Status: "not_found", Message: "No execution ledger exists for this artifact."})
			return status, nil
		}
		status.Status = "ledger_unreadable"
		status.Checks = append(status.Checks, ArtifactStatusCheck{Name: "execution_ledger", Status: "blocked", Message: fmt.Sprintf("Execution ledger cannot be read: %v", err)})
		status.Guidance = "Review or remove the unreadable execution ledger before using this artifact."
		return status, nil
	}

	status.Execution = ledger
	status.Status = ledger.Status
	status.Checks = append(status.Checks, ArtifactStatusCheck{Name: "execution_ledger", Status: "found", Message: "Execution ledger exists for this artifact."})
	switch ledger.Status {
	case "completed":
		status.Guidance = "Execution completed. The artifact cannot be executed again."
	case "failed":
		status.Guidance = "Execution failed after possible partial writes. Review Basecamp and the ledger before creating a fresh follow-up artifact."
	case "started":
		status.Guidance = "Execution started and did not record completion. Review Basecamp and the ledger before creating a fresh follow-up artifact."
	default:
		status.Guidance = "Execution ledger has an unrecognized status. Review the ledger before using this artifact."
	}
	return status, nil
}

func readExecutionLedger(path string) (*ExecutionLedger, error) {
	var ledger ExecutionLedger
	if err := readJSONData(path, &ledger); err != nil {
		return nil, err
	}
	return &ledger, nil
}
