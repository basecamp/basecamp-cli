package runner

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestASpecLaidOverAnotherKeepsWhatItDoesNotName(t *testing.T) {
	base := Spec{Adapter: "codex", Model: "gpt", Effort: "medium"}
	assert.Equal(t, base, Spec{}.Over(base))
	assert.Equal(t, Spec{Adapter: "codex", Model: "gpt", Effort: "high"}, Spec{Effort: "high"}.Over(base))
	assert.Equal(t, Spec{Adapter: "codex", Model: "mini", Effort: "medium"}, Spec{Adapter: "codex", Model: "mini"}.Over(base))
	// A model belongs to its agent: another agent does not inherit it.
	assert.Equal(t, Spec{Adapter: "claude", Effort: "medium"}, Spec{Adapter: "claude"}.Over(base))
	assert.Equal(t, "claude · opus · high", Spec{Adapter: "claude", Model: "opus", Effort: "high"}.String())
	assert.Equal(t, "pi", Spec{Adapter: "pi"}.String())
}

func TestASpecIsRefusedWhenItCouldNotBeRun(t *testing.T) {
	require.NoError(t, Spec{Adapter: "pi", Model: "openai-codex/gpt-6.1-sol:high", Effort: "xhigh"}.Validate(false))
	require.NoError(t, Spec{Model: "claude-opus-5-5[1m]"}.Validate(true))
	require.ErrorContains(t, Spec{}.Validate(false), "no adapter")
	require.ErrorContains(t, Spec{Adapter: "cursor"}.Validate(false), "expected one of claude, codex, pi")
	// Neither may be read as a flag by the CLI it is handed to.
	require.ErrorContains(t, Spec{Adapter: "claude", Model: "--dangerously-skip-permissions"}.Validate(false), "not a model name")
	require.ErrorContains(t, Spec{Adapter: "claude", Model: "a b"}.Validate(false), "not a model name")
	require.ErrorContains(t, Spec{Adapter: "claude", Effort: "-c"}.Validate(false), "not an effort level")
	require.ErrorContains(t, Spec{Adapter: "claude", Effort: `high" x="y`}.Validate(false), "not an effort level")
}

func TestRunnerJSONIsRefusedRatherThanMisread(t *testing.T) {
	dir := t.TempDir()
	config, err := LoadConfig(filepath.Join(dir, "missing.json"))
	require.NoError(t, err, "the file is optional")
	assert.Equal(t, Config{}, config)

	config, err = ParseConfig([]byte(`{
		"orchestrator": {"model": "sonnet"},
		"worker": {"adapter": "codex", "effort": "medium"},
		"max_workers": 2,
		"rules": [{"repo": "~/code/app", "project": 7, "worker": {"adapter": "claude"}}]}`))
	require.NoError(t, err)
	assert.Equal(t, 2, config.MaxWorkers)
	assert.Equal(t, "repo ~/code/app, project 7", config.Rules[0].describe())

	for wrong, why := range map[string]string{
		`{"rules":[{"projekt":1,"worker":{"adapter":"pi"}}]}`:                                         "unknown field",
		`{"worker":{"adapter":"gemini"}}`:                                                             "unknown adapter",
		`{"rules":[{"worker":{"adapter":"pi"}}]}`:                                                     "rule 1: it names no condition",
		`{"rules":[{"project":1,"worker":{}}]}`:                                                       "rule 1: its worker names nothing to change",
		`{"rules":[{"ping":true,"project":1,"worker":{"adapter":"pi"}}]}`:                             "a Ping is in no project",
		`{"rules":[{"project":1,"worker":{"adapter":"pi"}},{"within":-4,"worker":{"effort":"low"}}]}`: "rule 2: an id is negative",
		`{"max_workers":500}`:                                                                         "max_workers",
		`{} {}`:                                                                                       "more than one JSON value",
	} {
		_, err := ParseConfig([]byte(wrong))
		assert.ErrorContains(t, err, why, wrong)
	}

	path := filepath.Join(dir, ConfigFile)
	require.NoError(t, os.WriteFile(path, []byte(`{"worker":`), 0o600))
	_, err = LoadConfig(path)
	require.Error(t, err)
}

func TestWhatAStartNamedIsKeptBesideWhatTheFileAlreadyHolds(t *testing.T) {
	path := filepath.Join(t.TempDir(), ConfigFile)
	config, err := LoadConfig(path)
	require.NoError(t, err)
	assert.Equal(t, PermissionsFull, config.Mode(), "full until the person says otherwise")

	require.NoError(t, os.WriteFile(path, []byte(`{"max_workers": 2, "rules": [{"project": 7, "worker": {"effort": "high"}}]}`), 0o600))
	require.NoError(t, SaveStart(path, Spec{Adapter: "claude", Model: "sonnet"}, Spec{Adapter: "codex"}, PermissionsGuarded))
	config, err = LoadConfig(path)
	require.NoError(t, err)
	assert.Equal(t, Spec{Adapter: "claude", Model: "sonnet"}, config.Orchestrator)
	assert.Equal(t, Spec{Adapter: "codex"}, config.Worker)
	assert.Equal(t, PermissionsGuarded, config.Mode())
	assert.Equal(t, 2, config.MaxWorkers)
	require.Len(t, config.Rules, 1, "the rules the file held are kept")

	_, err = ParsePermissions("limited")
	require.ErrorContains(t, err, `permissions must be "full" or "guarded"`)
	_, err = ParseConfig([]byte(`{"permissions": "none"}`))
	require.ErrorContains(t, err, `permissions must be "full" or "guarded"`)

	// A file that cannot be read as written is left exactly as it is.
	broken := []byte(`{"rulez": []}`)
	require.NoError(t, os.WriteFile(path, broken, 0o600))
	require.Error(t, SaveStart(path, Spec{Adapter: "pi"}, Spec{}, PermissionsFull))
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, broken, after)
}

func TestTheFirstRuleThatMatchesChoosesTheWorker(t *testing.T) {
	config, err := ParseConfig([]byte(`{"rules": [
		{"repo": "~/code/app", "project": 7, "worker": {"model": "both"}},
		{"repo": "~/code", "worker": {"model": "repo"}},
		{"todolist": 30, "worker": {"model": "list"}},
		{"within": 40, "card_table": 50, "worker": {"model": "column-of-table"}},
		{"project": 7, "worker": {"adapter": "claude"}},
		{"ping": true, "worker": {"effort": "low"}}]}`))
	require.NoError(t, err)
	assert.True(t, config.NeedsRecordings())
	base := Spec{Adapter: "codex", Model: "gpt", Effort: "high"}
	model := func(route Route) string {
		spec, _ := config.WorkerFor(base, route, "/home/me")
		return spec.String()
	}
	assert.Equal(t, "codex · both · high", model(Route{Repo: "/home/me/code/app", Project: 7}))
	assert.Equal(t, "codex · repo · high", model(Route{Repo: "/home/me/code/app", Project: 8}), "every condition of a rule must hold")
	assert.Equal(t, "codex · repo · high", model(Route{Repo: "/home/me/code/app/sub", Project: 8}))
	assert.Equal(t, "claude · high", model(Route{Repo: "/home/me/codex", Project: 7}), "a folder is matched whole, not by prefix of its name")
	assert.Equal(t, "codex · list · high", model(Route{Project: 9, Recordings: []int64{10, 20, 30}}))
	assert.Equal(t, "codex · gpt · high", model(Route{Project: 9, Recordings: []int64{10, 40}}))
	assert.Equal(t, "codex · column-of-table · high", model(Route{Project: 9, Recordings: []int64{10, 40, 50}}))
	assert.Equal(t, "codex · gpt · low", model(Route{Ping: true}))
	assert.Equal(t, "codex · gpt · high", model(Route{Project: 9}))
	_, matched := config.WorkerFor(base, Route{Ping: true}, "")
	assert.Equal(t, "rule 6 (a Ping)", matched)
	assert.False(t, Config{Rules: []Rule{{Project: 1}}}.NeedsRecordings())
}
