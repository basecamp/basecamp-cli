package connector

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"testing"
	"time"

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

// optionalKeys are the contract's keys a line may leave out: the ones whose
// value is not always known.
var optionalKeys = []string{"recording.project_name", "requester_name"}

// The line the connector writes carries exactly the keys the contract names,
// under those names: a key dropped or renamed breaks a reader, and a key
// added is added to the contract on purpose.
func TestTheRequestLineKeepsItsContract(t *testing.T) {
	assert.Equal(t, requestLineContract, writtenLineKeys(t))
}

// Every key but the optional ones is there at its zero value too. A false
// acknowledge or an empty title is a real line, and a key that omitempty
// drops there is gone from the line its readers get.
func TestARequestLineAtItsZeroValuesKeepsItsRequiredKeys(t *testing.T) {
	raw, err := json.Marshal(HandoffLine{})
	require.NoError(t, err)
	want := slices.DeleteFunc(slices.Clone(requestLineContract), func(k string) bool { return slices.Contains(optionalKeys, k) })
	assert.Equal(t, want, lineKeys(t, raw))
}

// skillExample finds the request line example in the skill.
var skillExample = regexp.MustCompile("(?s)### 2\\. The request line\\s*```json\\r?\\n(.*?)```")

// The skill's own example of the line names exactly the keys the line
// carries, so the session reading the line is taught the names it will see,
// and a key added to the line is documented where the reader learns it.
func TestTheSkillDocumentsTheRequestLineItGets(t *testing.T) {
	skill, err := os.ReadFile(filepath.Join("..", "..", "skills", "basecamp-connect", "SKILL.md"))
	require.NoError(t, err)
	example := skillExample.FindSubmatch(skill)
	require.NotNil(t, example, "the skill's request line example is where this test looks for it")
	assert.Equal(t, writtenLineKeys(t), lineKeys(t, example[1]))
}

// writtenLineKeys is the keys of a request line as the connector writes one,
// with every field set, so a key that omitempty would drop is still seen —
// including one added after this test was written.
func writtenLineKeys(t *testing.T) []string {
	t.Helper()
	var line HandoffLine
	fill(t, reflect.ValueOf(&line).Elem())
	raw, err := json.Marshal(line)
	require.NoError(t, err)
	return lineKeys(t, raw)
}

// fill sets every field of v to a value that is not its zero.
func fill(t *testing.T, v reflect.Value) {
	t.Helper()
	if v.Type() == reflect.TypeFor[time.Time]() {
		v.Set(reflect.ValueOf(time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)))
		return
	}
	switch v.Kind() {
	case reflect.Struct:
		for i := range v.NumField() {
			if v.Type().Field(i).IsExported() {
				fill(t, v.Field(i))
			}
		}
	case reflect.String:
		v.SetString("x")
	case reflect.Int, reflect.Int64, reflect.Int32:
		v.SetInt(1)
	case reflect.Bool:
		v.SetBool(true)
	default:
		t.Fatalf("the request line has a %s field; teach fill to set one", v.Kind())
	}
}

// lineKeys is a line's keys, nested ones as their full dotted path, sorted.
func lineKeys(t *testing.T, raw []byte) []string {
	t.Helper()
	var obj map[string]any
	require.NoError(t, json.Unmarshal(raw, &obj))
	var keys []string
	var walk func(prefix string, obj map[string]any)
	walk = func(prefix string, obj map[string]any) {
		for k, v := range obj {
			if nested, ok := v.(map[string]any); ok && len(nested) > 0 {
				walk(prefix+k+".", nested)
				continue
			}
			keys = append(keys, prefix+k)
		}
	}
	walk("", obj)
	slices.Sort(keys)
	return keys
}

// An empty object is a key like any other, and the skill example is found
// whatever line endings the checkout gave the file.
func TestTheContractWalkSeesEmptyObjectsAndCRLF(t *testing.T) {
	assert.Equal(t, []string{"a.b", "metadata"}, lineKeys(t, []byte(`{"metadata":{},"a":{"b":1}}`)))
	assert.NotNil(t, skillExample.FindSubmatch([]byte("### 2. The request line\r\n\r\n```json\r\n{}\r\n```")))
}
