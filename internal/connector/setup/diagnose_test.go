package setup

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/connector/admission"
)

// indentedFile is a valid connect.json as setup writes it, one key a line,
// so a test can name the line and column a person would look at.
func indentedFile(t *testing.T) string {
	t.Helper()
	f := New("agent")
	f.AccountID = "2914079"
	f.Agent = Agent{PersonID: 52007412, Kind: KindAgent}
	f.Trust.OperatorID = 26909558
	f.Projects[222] = admission.Project{WatchCompletions: true}
	data, err := json.MarshalIndent(f, "", "  ")
	require.NoError(t, err)
	_, err = Parse(data)
	require.NoError(t, err, "the fixture itself must be valid")
	return string(data)
}

// lineOf is the 1-based line of the first line of s containing needle.
func lineOf(t *testing.T, s, needle string) int {
	t.Helper()
	for i, line := range strings.Split(s, "\n") {
		if strings.Contains(line, needle) {
			return i + 1
		}
	}
	t.Fatalf("%q not in fixture", needle)
	return 0
}

func TestParseNamesTheLineAndKeyOfAMisspelledProjectSetting(t *testing.T) {
	data := indentedFile(t)
	broken := strings.Replace(data, `"watch_completions"`, `"watch_completion"`, 1)
	require.NotEqual(t, data, broken)

	_, err := Parse([]byte(broken))
	require.Error(t, err)
	line := lineOf(t, broken, `"watch_completion"`)
	assert.Equal(t,
		`parse connect.json: project 222: line `+itoa(line)+`, column 7: json: unknown field "watch_completion"; did you mean "watch_completions"?`,
		err.Error())
}

func TestParseNamesTheLineAndKeyOfAMisspelledTopLevelKey(t *testing.T) {
	data := indentedFile(t)
	broken := strings.Replace(data, `"trust"`, `"turst"`, 1)
	require.NotEqual(t, data, broken)

	_, err := Parse([]byte(broken))
	require.Error(t, err)
	line := lineOf(t, broken, `"turst"`)
	assert.Equal(t,
		`parse connect.json: line `+itoa(line)+`, column 3: json: unknown field "turst"; did you mean "trust"?`,
		err.Error())
}

func TestParseSuggestsNothingForAKeyNothingIsNear(t *testing.T) {
	data := indentedFile(t)
	broken := strings.Replace(data, `"trust"`, `"zebra"`, 1)
	_, err := Parse([]byte(broken))
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unknown field "zebra"`)
	assert.NotContains(t, err.Error(), "did you mean")
}

// The card's own example: a stray character where a value belongs.
func TestParseNamesTheLineAndColumnOfASyntaxError(t *testing.T) {
	data := indentedFile(t)
	// "watch_completions": true  ->  "watch_completions": nope
	broken := strings.Replace(data, `"watch_completions": true`, `"watch_completions": nope`, 1)
	require.NotEqual(t, data, broken)

	_, err := Parse([]byte(broken))
	require.Error(t, err)
	line := lineOf(t, broken, `nope`)
	assert.Equal(t,
		`parse connect.json: line `+itoa(line)+`, column 29: invalid character 'o' in literal null (expecting 'u')`,
		err.Error())
}

func TestParseNamesWhereAMissingCommaIs(t *testing.T) {
	data := indentedFile(t)
	broken := strings.Replace(data, `"profile": "agent",`, `"profile": "agent"`, 1)
	require.NotEqual(t, data, broken)

	_, err := Parse([]byte(broken))
	require.Error(t, err)
	line := lineOf(t, broken, `"profile"`) + 1
	assert.Contains(t, err.Error(), "line "+itoa(line)+", column 3: invalid character '\"' after object key:value pair")
}

func TestParseSaysWhenTheFileEndsEarly(t *testing.T) {
	data := indentedFile(t)
	broken := strings.TrimSuffix(strings.TrimSpace(data), "}")

	_, err := Parse([]byte(broken))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "the file ends before everything it opens is closed")
	assert.Contains(t, err.Error(), "line "+itoa(strings.Count(broken, "\n")+1))
}

func TestParseNamesTheLineOfAValueOfTheWrongType(t *testing.T) {
	data := indentedFile(t)
	broken := strings.Replace(data, `"version": 1`, `"version": "1"`, 1)
	require.NotEqual(t, data, broken)

	_, err := Parse([]byte(broken))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "line "+itoa(lineOf(t, broken, `"version"`))+", column ")
}

func TestKeyOffsetFindsKeysNotValues(t *testing.T) {
	data := []byte(`{"a": "trust", "list": ["trust", {"trust": 1}], "trust": 2}`)
	got := keyOffset(data, "trust", nil)
	assert.Equal(t, strings.Index(string(data), `{"trust"`)+1, got, "the first key spelled trust, inside the array's object")
	assert.Equal(t, strings.LastIndex(string(data), `"trust"`), keyOffset(data, "trust", []string{}), "at the top level")
	assert.Equal(t, -1, keyOffset([]byte(`{"a": "zebra"}`), "zebra", nil))
}

// A key valid in one place and refused in another is placed where it was
// refused, not where it first appears.
func TestParsePlacesAProjectsUnknownKeyInThatProject(t *testing.T) {
	data := indentedFile(t)
	broken := strings.Replace(data, `"watch_completions": true`, `"watch_completions": true, "agent": true`, 1)
	require.NotEqual(t, data, broken)

	_, err := Parse([]byte(broken))
	require.Error(t, err)
	line := lineOf(t, broken, `"watch_completions"`)
	col := strings.Index(strings.Split(broken, "\n")[line-1], `"agent"`) + 1
	assert.Equal(t,
		`parse connect.json: project 222: line `+itoa(line)+`, column `+itoa(col)+`: json: unknown field "agent"`,
		err.Error())
	require.NotEqual(t, lineOf(t, broken, `"agent"`), line, "the top-level agent key is on another line, so this row can tell them apart")
}

func TestParsePlacesAProjectValueOfTheWrongType(t *testing.T) {
	data := indentedFile(t)
	broken := strings.Replace(data, `"watch_completions": true`, `"watch_completions": "yes"`, 1)
	_, err := Parse([]byte(broken))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "project 222: line "+itoa(lineOf(t, broken, `"yes"`))+", column ")
}

func TestParsePlacesTrailingData(t *testing.T) {
	data := indentedFile(t)
	broken := data + "\n\n  oops"
	_, err := Parse([]byte(broken))
	require.Error(t, err)
	assert.Equal(t, "parse connect.json: line "+itoa(lineOf(t, broken, "oops"))+", column 3: trailing data after the object", err.Error())
}

func TestKeysOfReadTheTagsParseDecodes(t *testing.T) {
	top := keysOf(typeAt(nil), false)
	for _, want := range []string{"version", "profile", "account_id", "agent", "trust", "projects", "ack_boost", "pings"} {
		assert.True(t, top[want], want)
	}
	for _, legacy := range []string{"worktrees", "driver", "worker", "concurrency", "deadline", "dangerous"} {
		assert.False(t, top[legacy], "nobody is pointed at a key the connector no longer reads: %s", legacy)
		assert.True(t, keysOf(typeAt(nil), true)[legacy], "and it is still accepted: %s", legacy)
	}
	assert.True(t, keysOf(typeAt([]string{"projects", "222"}), false)["watch_completions"])
	assert.True(t, keysOf(typeAt([]string{"agent"}), false)["person_id"])
	assert.Nil(t, typeAt([]string{"nowhere"}))
}

// A key valid at the top level and refused inside another object is placed
// where it was refused.
func TestParsePlacesANestedUnknownKeyWhereItIsRefused(t *testing.T) {
	data := indentedFile(t)
	broken := strings.Replace(data, `"kind": "agent"`, `"kind": "agent",
    "profile": "oops"`, 1)
	require.NotEqual(t, data, broken)
	_, err := Parse([]byte(broken))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "line "+itoa(lineOf(t, broken, `"oops"`))+", column 5: ")
}

// A suggestion is a key valid in the object the misspelling sits in.
func TestParseSuggestsOnlyAKeyValidWhereTheMisspellingIs(t *testing.T) {
	data := indentedFile(t)
	broken := strings.Replace(data, `"mode": "operator",`, `"mode": "operator",
    "agnt": [],`, 1)
	_, err := Parse([]byte(broken))
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unknown field "agnt"`)
	assert.NotContains(t, err.Error(), `did you mean "agent"`, "agent is a top-level key, not one of trust's")

	broken = strings.Replace(data, `"operator_id"`, `"operater_id"`, 1)
	_, err = Parse([]byte(broken))
	require.Error(t, err)
	assert.Contains(t, err.Error(), `did you mean "operator_id"?`)
}

func TestParsePlacesAKeyThatIsNotSpelledCanonically(t *testing.T) {
	data := indentedFile(t)
	broken := strings.Replace(data, `"watch_completions"`, `"watch_Completions"`, 1)
	_, err := Parse([]byte(broken))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "line "+itoa(lineOf(t, broken, "watch_Completions"))+", column 7: ")
	assert.Contains(t, err.Error(), `did you mean "watch_completions"?`)
}

func TestParsePlacesTheSecondOfADuplicatedKey(t *testing.T) {
	data := indentedFile(t)
	broken := strings.Replace(data, `"profile": "agent",`, `"profile": "agent",
  "profile": "agent",`, 1)
	_, err := Parse([]byte(broken))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "line "+itoa(lineOf(t, broken, `"profile"`)+1)+", column 3: ")
	assert.Contains(t, err.Error(), "appears twice")
}

func itoa(n int) string { return strconv.Itoa(n) }

// Two objects at one path, siblings in an array, are two objects: a key in
// each is no duplicate.
func TestADuplicateIsPlacedInTheObjectThatHasIt(t *testing.T) {
	data := []byte("{\"list\": [\n  {\"id\": 1},\n  {\"id\": 2},\n  {\"id\": 3, \"id\": 4}\n]}")
	err := suggestKey(data, errors.New(`key "id" appears twice in one object`))
	assert.Equal(t, `line 4, column 13: key "id" appears twice in one object`, err.Error())
}

// A project id is a valid key of the projects map; an unknown key elsewhere
// with the same spelling is the one refused.
func TestAnUnknownKeyIsNotPlacedOnAMapsId(t *testing.T) {
	data := strings.TrimSpace(indentedFile(t))
	broken := strings.TrimSuffix(data, "}") + `,
  "ack_boost": {"222": true}
}`
	project, stray := lineOf(t, broken, `"222": {`), lineOf(t, broken, `"ack_boost"`)
	require.Less(t, project, stray, "the valid project id comes first, so a first-match lookup would name it")
	_, err := Parse([]byte(broken))
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unknown field "222"`)
	assert.Contains(t, err.Error(), "line "+itoa(stray)+", column 17: ")
}
