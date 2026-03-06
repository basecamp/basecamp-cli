//go:build !windows

package richtext

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNormalizeDragPath(t *testing.T) {
	home, _ := os.UserHomeDir()

	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "raw path passthrough",
			raw:  "/Users/joe/Documents/file.pdf",
			want: "/Users/joe/Documents/file.pdf",
		},
		{
			name: "shell-escaped spaces",
			raw:  `/Users/joe/My\ Documents/file\ (1).pdf`,
			want: "/Users/joe/My Documents/file (1).pdf",
		},
		{
			name: "shell-escaped parens",
			raw:  `/tmp/report\ \(final\).pdf`,
			want: "/tmp/report (final).pdf",
		},
		{
			name: "single-quoted path",
			raw:  `'/Users/joe/My Documents/file.pdf'`,
			want: "/Users/joe/My Documents/file.pdf",
		},
		{
			name: "double-quoted path",
			raw:  `"/Users/joe/My Documents/file.pdf"`,
			want: "/Users/joe/My Documents/file.pdf",
		},
		{
			name: "file URL",
			raw:  "file:///Users/joe/My%20Documents/file%20(1).pdf",
			want: "/Users/joe/My Documents/file (1).pdf",
		},
		{
			name: "file URL no escapes",
			raw:  "file:///tmp/simple.txt",
			want: "/tmp/simple.txt",
		},
		{
			name: "quoted file URL",
			raw:  "'file:///tmp/my%20file.pdf'",
			want: "/tmp/my file.pdf",
		},
		{
			name: "double-quoted file URL",
			raw:  `"file:///tmp/my%20file.pdf"`,
			want: "/tmp/my file.pdf",
		},
		{
			name: "tilde expansion",
			raw:  "~/Documents/file.pdf",
			want: filepath.Join(home, "Documents/file.pdf"),
		},
		{
			name: "tilde with shell escapes",
			raw:  `~/My\ Documents/file.pdf`,
			want: filepath.Join(home, "My Documents/file.pdf"),
		},
		{
			name: "empty string",
			raw:  "",
			want: "",
		},
		{
			name: "mismatched quotes preserved",
			raw:  `'/Users/joe/file.pdf"`,
			want: "'/Users/joe/file.pdf\"",
		},
		{
			name: "non-file URL passes through (not a valid path)",
			raw:  "https://example.com/file.pdf",
			want: "https:/example.com/file.pdf", // filepath.Clean collapses //
		},
		{
			name: "escaped backslash",
			raw:  `/tmp/a\\b`,
			want: `/tmp/a\b`,
		},
		{
			name: "trailing slash cleaned",
			raw:  "/tmp/dir/",
			want: "/tmp/dir",
		},
		{
			name: "dots cleaned",
			raw:  "/tmp/a/../b/./c",
			want: "/tmp/b/c",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NormalizeDragPath(tt.raw)
			if got != tt.want {
				t.Errorf("NormalizeDragPath(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}
