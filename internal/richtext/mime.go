package richtext

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// maxFileSize is the maximum allowed file size for attachments (100MB).
const maxFileSize = 100 * 1024 * 1024

// mimeByExt maps common file extensions to MIME types.
var mimeByExt = map[string]string{
	".pdf":  "application/pdf",
	".zip":  "application/zip",
	".gz":   "application/gzip",
	".tar":  "application/x-tar",
	".json": "application/json",
	".xml":  "application/xml",
	".csv":  "text/csv",
	".txt":  "text/plain",
	".md":   "text/markdown",
	".html": "text/html",
	".htm":  "text/html",
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
	".svg":  "image/svg+xml",
	".ico":  "image/x-icon",
	".mp4":  "video/mp4",
	".webm": "video/webm",
	".mov":  "video/quicktime",
	".mp3":  "audio/mpeg",
	".wav":  "audio/wav",
	".ogg":  "audio/ogg",
	".doc":  "application/msword",
	".docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	".xls":  "application/vnd.ms-excel",
	".xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
	".ppt":  "application/vnd.ms-powerpoint",
	".pptx": "application/vnd.openxmlformats-officedocument.presentationml.presentation",
	".rb":   "text/x-ruby",
	".go":   "text/x-go",
	".py":   "text/x-python",
	".js":   "text/javascript",
	".ts":   "text/typescript",
	".sh":   "text/x-shellscript",
	".yaml": "text/yaml",
	".yml":  "text/yaml",
	".toml": "text/x-toml",
}

// DetectMIME returns the MIME type for a file path.
// It uses the extension map first, then falls back to reading file header bytes.
func DetectMIME(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	if mime, ok := mimeByExt[ext]; ok {
		return mime
	}

	// Fallback: read first 512 bytes for http.DetectContentType
	f, err := os.Open(path)
	if err != nil {
		return "application/octet-stream"
	}
	defer f.Close()

	buf := make([]byte, 512)
	n, _ := f.Read(buf)
	if n == 0 {
		return "application/octet-stream"
	}
	return http.DetectContentType(buf[:n])
}

// ValidateFile checks that a path refers to an existing, regular, readable file
// within the size limit. Returns nil on success.
func ValidateFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("cannot access %s: %w", filepath.Base(path), err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", filepath.Base(path))
	}
	if info.Size() > maxFileSize {
		return fmt.Errorf("%s exceeds maximum size of 100MB", filepath.Base(path))
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("%s is not readable: %w", filepath.Base(path), err)
	}
	f.Close()
	return nil
}
