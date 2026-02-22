package version

import (
	"encoding/json"
	"testing"
)

func TestIsDev(t *testing.T) {
	// Save original value
	original := Version
	defer func() { Version = original }()

	tests := []struct {
		version  string
		expected bool
	}{
		{"dev", true},
		{"1.0.0", false},
		{"0.1.0", false},
		{"v1.2.3", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.version, func(t *testing.T) {
			Version = tt.version
			if got := IsDev(); got != tt.expected {
				t.Errorf("IsDev() with Version=%q = %v, want %v", tt.version, got, tt.expected)
			}
		})
	}
}

func TestFull(t *testing.T) {
	original := Version
	defer func() { Version = original }()

	Version = "dev"
	if got := Full(); got != "basecamp version dev (built from source)" {
		t.Errorf("Full() with dev = %q, want %q", got, "basecamp version dev (built from source)")
	}

	Version = "1.2.3"
	if got := Full(); got != "basecamp version 1.2.3" {
		t.Errorf("Full() with 1.2.3 = %q, want %q", got, "basecamp version 1.2.3")
	}
}

func TestUserAgent(t *testing.T) {
	original := Version
	defer func() { Version = original }()

	Version = "dev"
	if got := UserAgent(); got != "basecamp-cli/dev (https://github.com/basecamp/basecamp-cli)" {
		t.Errorf("UserAgent() with dev = %q", got)
	}

	Version = "1.0.0"
	if got := UserAgent(); got != "basecamp-cli/1.0.0 (https://github.com/basecamp/basecamp-cli)" {
		t.Errorf("UserAgent() with 1.0.0 = %q", got)
	}
}

func TestSDKProvenanceJSONIsValid(t *testing.T) {
	// The embedded JSON must be valid
	if len(sdkProvenanceJSON) == 0 {
		t.Fatal("sdkProvenanceJSON is empty")
	}

	var p SDKProvenance
	if err := json.Unmarshal(sdkProvenanceJSON, &p); err != nil {
		t.Fatalf("sdkProvenanceJSON is not valid JSON: %v", err)
	}

	if p.SDK.Module == "" {
		t.Error("SDK module should not be empty")
	}
	if p.SDK.Version == "" {
		t.Error("SDK version should not be empty")
	}
}

func TestGetSDKProvenance(t *testing.T) {
	p := GetSDKProvenance()
	if p == nil {
		t.Fatal("GetSDKProvenance() returned nil")
	}

	if p.SDK.Module != "github.com/basecamp/basecamp-sdk/go" {
		t.Errorf("unexpected module: %s", p.SDK.Module)
	}
	if p.SDK.Version == "" {
		t.Error("SDK version should not be empty")
	}
	if p.API.Repo != "basecamp/bc3" {
		t.Errorf("unexpected API repo: %s", p.API.Repo)
	}
}

func TestGetSDKProvenanceIsCached(t *testing.T) {
	p1 := GetSDKProvenance()
	p2 := GetSDKProvenance()
	if p1 != p2 {
		t.Error("GetSDKProvenance() should return the same pointer on repeated calls")
	}
}
