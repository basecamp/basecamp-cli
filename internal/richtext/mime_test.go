package richtext

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetectMIME(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"report.pdf", "application/pdf"},
		{"image.PNG", "image/png"},
		{"data.json", "application/json"},
		{"README.md", "text/markdown"},
		{"photo.jpeg", "image/jpeg"},
		{"archive.zip", "application/zip"},
		{"style.css", "application/octet-stream"}, // not in our map, file doesn't exist
		{"doc.docx", "application/vnd.openxmlformats-officedocument.wordprocessingml.document"},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := DetectMIME(tt.path)
			if got != tt.want {
				t.Errorf("DetectMIME(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestDetectMIMEFromContent(t *testing.T) {
	// Create a real PNG file header to test content detection fallback
	dir := t.TempDir()
	path := filepath.Join(dir, "mystery")

	// PNG magic bytes
	png := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}
	if err := os.WriteFile(path, png, 0o644); err != nil {
		t.Fatal(err)
	}

	got := DetectMIME(path)
	if got != "image/png" {
		t.Errorf("DetectMIME(PNG bytes) = %q, want image/png", got)
	}
}

func TestValidateFile(t *testing.T) {
	dir := t.TempDir()

	// Valid file
	valid := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(valid, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ValidateFile(valid); err != nil {
		t.Errorf("ValidateFile(valid) = %v, want nil", err)
	}

	// Non-existent
	if err := ValidateFile(filepath.Join(dir, "nope.txt")); err == nil {
		t.Error("ValidateFile(nonexistent) = nil, want error")
	}

	// Directory
	if err := ValidateFile(dir); err == nil {
		t.Error("ValidateFile(dir) = nil, want error")
	}

	// Unreadable
	unreadable := filepath.Join(dir, "noperm.txt")
	if err := os.WriteFile(unreadable, []byte("x"), 0o000); err != nil {
		t.Fatal(err)
	}
	if err := ValidateFile(unreadable); err == nil {
		t.Error("ValidateFile(unreadable) = nil, want error")
	}
}
