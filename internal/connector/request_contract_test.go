package connector

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// requestLineContract is every key a "type":"request" line carries, nested
// keys as parent.child. The basecamp-connect skill, and any session or
// practice that drives the connector, keys on these names: renaming or
// dropping one breaks a reader that nothing here runs, so it is a breaking
// change made on purpose, here, or not at all. Adding a key is not.
var requestLineContract = []string{
	"acknowledge",
	"content",
	"content_updated_at",
	"event_id",
	"event_type",
	"recording.bucket_id",
	"recording.project_name",
	"recording.recording_id",
	"recording.title",
	"recording.type",
	"recording.url",
	"reply_to.kind",
	"reply_to.recording_id",
	"requester_id",
	"requester_name",
	"role",
	"trigger",
	"type",
}

// The line the connector writes carries every key the contract names, under
// that name.
func TestTheRequestLineKeepsItsContract(t *testing.T) {
	got := writtenLineKeys(t)
	for _, key := range requestLineContract {
		assert.Contains(t, got, key, "the request line no longer carries %q, which its readers key on", key)
	}
}

// The skill's own example of the line names exactly the keys the line
// carries, so the session reading the line is taught the names it will see,
// and a key added to the line is documented where the reader learns it.
func TestTheSkillDocumentsTheRequestLineItGets(t *testing.T) {
	skill, err := os.ReadFile(filepath.Join("..", "..", "skills", "basecamp-connect", "SKILL.md"))
	require.NoError(t, err)
	example := regexp.MustCompile("(?s)### 2\\. The request line\\s*```json\\n(.*?)```").FindSubmatch(skill)
	require.NotNil(t, example, "the skill's request line example is where this test looks for it")
	assert.Equal(t, writtenLineKeys(t), lineKeys(t, example[1]))
}

// writtenLineKeys is the keys of a request line as the connector writes one,
// every optional field set.
func writtenLineKeys(t *testing.T) []string {
	t.Helper()
	raw, err := json.Marshal(HandoffLine{
		Type:          "request",
		Recording:     HandoffRecording{ProjectName: "set, so omitempty keeps it"},
		RequesterName: "set, so omitempty keeps it",
	})
	require.NoError(t, err)
	return lineKeys(t, raw)
}

// lineKeys is a line's keys, nested ones as parent.child, sorted.
func lineKeys(t *testing.T, raw []byte) []string {
	t.Helper()
	var obj map[string]any
	require.NoError(t, json.Unmarshal(raw, &obj))
	var keys []string
	for k, v := range obj {
		if nested, ok := v.(map[string]any); ok {
			for nk := range nested {
				keys = append(keys, k+"."+nk)
			}
			continue
		}
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
