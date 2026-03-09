package richtext

import (
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// NormalizeDragPath normalizes a pasted/dragged path into a filesystem path.
// It handles quoted paths, file:// URLs, shell-escaped characters, and tilde
// expansion. Only inputs that look like filesystem paths (absolute, ~/,
// file://, or quoted versions of these) are transformed; other inputs are
// returned unchanged. Returns empty for empty input.
//
// This targets macOS and Linux terminals only. It compiles on Windows to avoid
// breaking cross-compilation, but Windows drag-and-drop (e.g. file:// URLs
// with drive letters) is not supported or tested.
func NormalizeDragPath(raw string) string {
	if raw == "" {
		return ""
	}

	s := raw

	// Strip matching quotes only when the inner content looks like a path
	if len(s) >= 2 {
		if (s[0] == '\'' && s[len(s)-1] == '\'') ||
			(s[0] == '"' && s[len(s)-1] == '"') {
			inner := s[1 : len(s)-1]
			if looksLikePath(inner) {
				s = inner
			} else {
				return raw
			}
		}
	}

	// file:// URL — url.Parse already percent-decodes the Path field
	if strings.HasPrefix(s, "file://") {
		if u, err := url.Parse(s); err == nil {
			s = u.Path
		}
	}

	// Only apply further normalization to path-like inputs
	if !looksLikePath(s) {
		return raw
	}

	// Shell unescape: \X → X (Unix only — on Windows \ is the path separator)
	if runtime.GOOS != "windows" {
		var b strings.Builder
		b.Grow(len(s))
		for i := 0; i < len(s); i++ {
			if s[i] == '\\' && i+1 < len(s) {
				i++
			}
			b.WriteByte(s[i])
		}
		s = b.String()
	}

	// Tilde expansion
	if strings.HasPrefix(s, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			s = filepath.Join(home, s[2:])
		}
	}

	if filepath.IsAbs(s) {
		return filepath.Clean(s)
	}
	return s
}

// looksLikePath returns true if s starts with a path-like prefix.
func looksLikePath(s string) bool {
	return strings.HasPrefix(s, "/") ||
		strings.HasPrefix(s, "~/") ||
		strings.HasPrefix(s, "file://")
}
