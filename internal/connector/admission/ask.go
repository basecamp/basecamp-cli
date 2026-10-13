package admission

import (
	"crypto/sha256"
	"encoding/hex"
)

// AskContentHash is the fingerprint an ask is declared with and checked
// against: SHA-256 of the recording's content exactly as the summary read
// returns it, hex-encoded. Nothing is normalized — no whitespace folded, no
// HTML canonicalized — because both sides read the same field through the
// same read (basecamp.RecordingsService.Summarize), so an unedited post
// hashes the same, and any change at all, an edit's or Basecamp's, voids the
// go rather than approving words the operator may not have seen.
func AskContentHash(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}
