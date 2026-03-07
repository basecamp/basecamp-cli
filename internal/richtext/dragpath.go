package richtext

import (
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// NormalizeDragPath normalizes a pasted/dragged path into a filesystem path.
// It handles quoted paths, file:// URLs, shell-escaped characters (Unix only),
// and tilde expansion. The result is cleaned with filepath.Clean for absolute
// paths but returned unchanged for non-path inputs. Returns empty for empty input.
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

	// file:// URL — url.Parse already percent-decodes the Path field
	if strings.HasPrefix(s, "file://") {
		if u, err := url.Parse(s); err == nil {
			s = u.Path
		}
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
