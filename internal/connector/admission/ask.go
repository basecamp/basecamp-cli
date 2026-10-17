package admission

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
)

// AskFingerprint is what an ask is declared with and checked against: the
// SHA-256, hex-encoded, of the words the ask is made of, exactly as the
// summary read returns them. Both ends read the same fields through the same
// read (basecamp.RecordingsService.Summarize), so an unedited post
// fingerprints the same, and any change at all — an edit's, or Basecamp's own
// — voids the go rather than approving words the operator may not have seen.
// Nothing is normalized: no whitespace folded, no HTML canonicalized.
//
// A post with a title of its own — a message's subject, a card's or to-do's
// title, a document's — is approved by its title as much as its content, so
// both are hashed, each prefixed with its length in bytes and a colon
// ("<len>:<title><len>:<content>"), so no split of one string into the two
// fields reads as another. A comment, a chat line and a check-in answer have
// no title of their own — a comment's title is its parent's, an answer's is
// its question's, and someone else's rename would change either — so their
// content alone is hashed, bare.
func AskFingerprint(recordingType, title, content string) string {
	var data string
	if askHasTitle(recordingType) {
		data = strconv.Itoa(len(title)) + ":" + title + strconv.Itoa(len(content)) + ":" + content
	} else {
		data = content
	}
	sum := sha256.Sum256([]byte(data))
	return hex.EncodeToString(sum[:])
}

// askHasTitle says a recording type's title is its own words.
func askHasTitle(recordingType string) bool {
	switch {
	case recordingType == "Comment", recordingType == "Question::Answer":
		return false
	case strings.HasPrefix(recordingType, "Chat::Lines::"):
		return false
	}
	return true
}

// AskTitle is the title an ask is approved by: its own, or none for a
// comment, a chat line or a check-in answer.
func AskTitle(recordingType, title string) string {
	if askHasTitle(recordingType) {
		return title
	}
	return ""
}
