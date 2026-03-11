package commands

import (
	"fmt"
	"regexp"
	"strings"
)

// imgTagPattern matches <img> tags produced by MarkdownToHTML.
// MarkdownToHTML produces: <img src="path" alt="text">
// Captures: (1) src value, (2) optional alt value.
var imgTagPattern = regexp.MustCompile(`<img\s+src="([^"]*)"(?:\s+alt="([^"]*)")?[^>]*>`)

// hasImgTag is a quick check to avoid regex on HTML without images.
func hasImgTag(html string) bool {
	return strings.Contains(html, "<img ")
}

// isRemoteURL returns true for http:// and https:// URLs.
func isRemoteURL(src string) bool {
	return strings.HasPrefix(src, "http://") || strings.HasPrefix(src, "https://")
}

// uriSchemePattern matches URI schemes with 2+ characters before the colon.
// RFC 3986 allows single-letter schemes (ALPHA *(...)), but we require 2+
// to avoid misclassifying Windows drive letters (C:, D:) as URI schemes.
var uriSchemePattern = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9+\-.]+:`)

// isNonFileURI returns true for URI schemes that are clearly not local file
// paths (data:, cid:, blob:, ftp:, etc.). These should be left untouched.
// http/https are handled separately by isRemoteURL. file:// URIs are treated
// as local paths (handled by NormalizeDragPath).
func isNonFileURI(src string) bool {
	if strings.HasPrefix(src, "file:") {
		return false
	}
	return uriSchemePattern.MatchString(src) && !isRemoteURL(src)
}

// imgAction describes the resolution action for an image src attribute.
type imgAction int

const (
	imgSkip        imgAction = iota // remote URL or non-file URI — leave unchanged
	imgUpload                       // local file — upload and replace with bc-attachment
	imgPlaceholder                  // ? or empty — error (would prompt in TTY, future work)
)

// classifyImageSrc determines the action for an image src value.
// Returns the action and, for imgPlaceholder, a descriptive error.
func classifyImageSrc(src, alt string) (imgAction, error) {
	if isRemoteURL(src) || isNonFileURI(src) {
		return imgSkip, nil
	}
	if src == "?" || src == "" {
		return imgPlaceholder, fmt.Errorf("placeholder image requires a file path: ![%s](?)", alt)
	}
	return imgUpload, nil
}
