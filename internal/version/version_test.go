package version

import "testing"

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
	if got := Full(); got != "bcq version dev (built from source)" {
		t.Errorf("Full() with dev = %q, want %q", got, "bcq version dev (built from source)")
	}

	Version = "1.2.3"
	if got := Full(); got != "bcq version 1.2.3" {
		t.Errorf("Full() with 1.2.3 = %q, want %q", got, "bcq version 1.2.3")
	}
}

func TestUserAgent(t *testing.T) {
	original := Version
	defer func() { Version = original }()

	Version = "dev"
	if got := UserAgent(); got != "bcq/dev (https://github.com/basecamp/bcq)" {
		t.Errorf("UserAgent() with dev = %q", got)
	}

	Version = "1.0.0"
	if got := UserAgent(); got != "bcq/1.0.0 (https://github.com/basecamp/bcq)" {
		t.Errorf("UserAgent() with 1.0.0 = %q", got)
	}
}
