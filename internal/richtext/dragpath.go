//go:build !windows

package richtext

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// NormalizeDragPath normalizes a pasted/dragged path into a filesystem path.
// It handles quoted paths, file:// URLs, shell-escaped characters, and tilde
// expansion. The result is cleaned with filepath.Clean but not validated
// against the filesystem. Returns raw unchanged for empty input.
//
// This function targets macOS/Linux terminals where drag-and-drop produces
// shell-escaped paths, quoted paths, or file:// URLs.
func NormalizeDragPath(raw string) string {
	if raw == "" {
		return ""
	}

	s := raw

	// Strip matching quotes first — some terminals wrap file:// URLs in quotes
	if len(s) >= 2 {
		if (s[0] == '\'' && s[len(s)-1] == '\'') ||
			(s[0] == '"' && s[len(s)-1] == '"') {
			s = s[1 : len(s)-1]
		}
	}

	// file:// URL
	if strings.HasPrefix(s, "file://") {
		if u, err := url.Parse(s); err == nil {
			if unescaped, err := url.PathUnescape(u.Path); err == nil {
				s = unescaped
			}
		}
	}

	// Shell unescape: \X → X
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
		}
		b.WriteByte(s[i])
	}
	s = b.String()

	// Tilde expansion
	if strings.HasPrefix(s, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			s = filepath.Join(home, s[2:])
		}
	}

	return filepath.Clean(s)
}
