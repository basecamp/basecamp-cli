package commands

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHasImgTag(t *testing.T) {
	assert.True(t, hasImgTag(`<p>text <img src="./photo.png" alt="photo"> more</p>`))
	assert.True(t, hasImgTag(`<img src="test.png">`))
	assert.False(t, hasImgTag(`<p>no images here</p>`))
	assert.False(t, hasImgTag(""))
}

func TestIsRemoteURL(t *testing.T) {
	assert.True(t, isRemoteURL("https://example.com/img.png"))
	assert.True(t, isRemoteURL("http://example.com/img.png"))
	assert.False(t, isRemoteURL("./local.png"))
	assert.False(t, isRemoteURL("../up/local.png"))
	assert.False(t, isRemoteURL("/absolute/path.png"))
	assert.False(t, isRemoteURL("relative.png"))
	assert.False(t, isRemoteURL("data:image/png;base64,abc"))
}

func TestIsNonFileURI(t *testing.T) {
	// Known URI schemes → true
	assert.True(t, isNonFileURI("data:image/png;base64,abc"))
	assert.True(t, isNonFileURI("cid:part1@example.com"))
	assert.True(t, isNonFileURI("blob:https://example.com/uuid"))
	assert.True(t, isNonFileURI("ftp://host/file"))
	assert.True(t, isNonFileURI("mailto:user@example.com"))

	// http/https are excluded (handled by isRemoteURL)
	assert.False(t, isNonFileURI("http://example.com"))
	assert.False(t, isNonFileURI("https://example.com"))

	// Local paths → false
	assert.False(t, isNonFileURI("./local.png"))
	assert.False(t, isNonFileURI("../up/local.png"))
	assert.False(t, isNonFileURI("/absolute/path.png"))
	assert.False(t, isNonFileURI("relative.png"))
	assert.False(t, isNonFileURI("?"))
	assert.False(t, isNonFileURI(""))

	// Bare filename with digits/dots/hyphens (not a URI scheme)
	assert.False(t, isNonFileURI("photo-2024.png"))
	assert.False(t, isNonFileURI("my.screenshot.png"))

	// Windows drive-letter paths → false (not URI schemes)
	assert.False(t, isNonFileURI(`C:\images\pic.png`))
	assert.False(t, isNonFileURI(`D:/photos/img.jpg`))

	// file:// URIs are local paths, not non-file URIs
	assert.False(t, isNonFileURI("file:///tmp/img.png"))
	assert.False(t, isNonFileURI("file:///Users/me/photo.png"))
}

func TestImgTagPattern(t *testing.T) {
	tests := []struct {
		name    string
		html    string
		wantSrc string
		wantAlt string
	}{
		{
			name:    "src and alt",
			html:    `<img src="./photo.png" alt="my photo">`,
			wantSrc: "./photo.png",
			wantAlt: "my photo",
		},
		{
			name:    "src only",
			html:    `<img src="test.png">`,
			wantSrc: "test.png",
			wantAlt: "",
		},
		{
			name:    "remote url",
			html:    `<img src="https://example.com/img.png" alt="remote">`,
			wantSrc: "https://example.com/img.png",
			wantAlt: "remote",
		},
		{
			name:    "placeholder",
			html:    `<img src="?" alt="chart">`,
			wantSrc: "?",
			wantAlt: "chart",
		},
		{
			name:    "empty src",
			html:    `<img src="" alt="empty">`,
			wantSrc: "",
			wantAlt: "empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matches := imgTagPattern.FindStringSubmatch(tt.html)
			require.NotNil(t, matches)
			assert.Equal(t, tt.wantSrc, matches[1])
			if tt.wantAlt != "" {
				assert.Equal(t, tt.wantAlt, matches[2])
			}
		})
	}
}

func TestImgTagPatternMultiple(t *testing.T) {
	html := `<p><img src="a.png" alt="first"> text <img src="b.png" alt="second"></p>`
	matches := imgTagPattern.FindAllStringSubmatch(html, -1)
	assert.Len(t, matches, 2)
	assert.Equal(t, "a.png", matches[0][1])
	assert.Equal(t, "b.png", matches[1][1])
}

func TestImgTagPatternNoMatch(t *testing.T) {
	matches := imgTagPattern.FindStringSubmatch(`<p>no images</p>`)
	assert.Nil(t, matches)
}

func TestClassifyImageSrc(t *testing.T) {
	tests := []struct {
		name       string
		src        string
		alt        string
		wantAction imgAction
		wantErr    bool
	}{
		// Skip: remote URLs
		{"https url", "https://example.com/img.png", "photo", imgSkip, false},
		{"http url", "http://example.com/img.png", "photo", imgSkip, false},

		// Skip: non-file URIs
		{"data uri", "data:image/png;base64,abc", "chart", imgSkip, false},
		{"cid uri", "cid:part1@example.com", "logo", imgSkip, false},
		{"blob uri", "blob:https://example.com/uuid", "file", imgSkip, false},
		{"ftp uri", "ftp://host/file.png", "remote", imgSkip, false},
		{"mailto uri", "mailto:user@example.com", "email", imgSkip, false},

		// Upload: local paths
		{"relative path", "./photo.png", "photo", imgUpload, false},
		{"parent path", "../assets/photo.png", "photo", imgUpload, false},
		{"absolute path", "/tmp/photo.png", "photo", imgUpload, false},
		{"bare filename", "photo.png", "photo", imgUpload, false},
		{"filename with dots", "my.screenshot.2024.png", "shot", imgUpload, false},
		{"filename with hyphens", "photo-final-v2.png", "photo", imgUpload, false},
		{"nested relative", "assets/images/photo.png", "photo", imgUpload, false},
		{"windows backslash", `C:\images\pic.png`, "win", imgUpload, false},
		{"windows forward slash", "D:/photos/img.jpg", "win", imgUpload, false},
		{"file url", "file:///tmp/photo.png", "photo", imgUpload, false},

		// Placeholder: error
		{"question mark", "?", "chart", imgPlaceholder, true},
		{"empty string", "", "missing", imgPlaceholder, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			action, err := classifyImageSrc(tt.src, tt.alt)
			assert.Equal(t, tt.wantAction, action)
			if tt.wantErr {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), "placeholder image requires a file path")
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
