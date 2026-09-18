package commands

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// The connector's skill evals are checked here, against this package's own
// parser, because nothing else checks them at all: CI's Skill Evals job exits
// 0 without running a case when ANTHROPIC_API_KEY is unset, which it is on
// this repository.
//
// # What this holds, and what it does not
//
// Only the reject patterns, and only for the shape of a --serve value and
// the removed --route flags. The accept, mock, expect_sequence and
// accept_response patterns are read by the Ruby runner under its own regex
// semantics and are not modeled here, so a malformed one of those can still
// land without CI noticing (Copilot on #765). This is a guard on one
// invariant, not on the eval files.
//
// The invariant: every `connect setup` in a trace carries a --serve value
// the CLI would accept. The accepts prove a correct command was issued; the
// rejects have to prove no incorrect one was, which is the half a model can
// otherwise satisfy by issuing a command the CLI refuses and then retrying —
// the broad setup mock answers success either way.
type skillEvalCase struct {
	Accept []string `yaml:"accept"`
	Reject []string `yaml:"reject"`
}

// traceServeValue is the --serve value a trace may carry: digits, above
// zero, bare or single-quoted. It is deliberately narrower than
// parsePositiveID, which also takes a leading plus — and the corpus below
// carries +222 so that narrowing is asserted rather than assumed.
var traceServeValue = regexp.MustCompile(`^0*[1-9][0-9]*$`)

// serveValues is the corpus. Choosing it is the check, not setup for the
// check: a guard is only as strong as the inputs it asserts over, and the
// first version of this omitted 0, the empty value and +222 — exactly the
// three that would have shown its rule disagreeing with the CLI in both
// directions, which is the defect this guard exists to catch, one level up.
var serveValues = []struct {
	arg   string // as it appears on the command line, quoting included
	value string // what the shell hands the CLI
}{
	{"222", "222"},
	{"'222'", "222"},
	{"007", "007"}, // leading zeros: the CLI reads 7
	{"'007'", "007"},
	{"+222", "+222"}, // the CLI takes it; a trace may not
	{"0", "0"},       // parses, but is not above zero
	{"000", "000"},
	{"''", ""}, // an empty value, as a shell would deliver it
	{"222=work", "222=work"},
	{"222=/home/me/x", "222=/home/me/x"},
	{"'222=work'", "222=work"},
	{"abc", "abc"},
	{"222abc", "222abc"},
	{"-1", "-1"},
	{"222,333", "222,333"},
}

func TestConnectSkillEvalRejectsHoldTheServeValueRule(t *testing.T) {
	dir := filepath.Join("..", "..", "skill-evals", "cases", "basecamp-connect")
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.NotEmpty(t, entries)

	checked := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yml") {
			continue
		}
		t.Run(e.Name(), func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
			require.NoError(t, err)
			var c skillEvalCase
			require.NoError(t, yaml.Unmarshal(raw, &c))

			rejects := make([]*regexp.Regexp, 0, len(c.Reject))
			for _, p := range c.Reject {
				re, err := regexp.Compile(p)
				require.NoError(t, err, "reject pattern %q", p)
				rejects = append(rejects, re)
			}
			caught := func(cmd string) bool {
				for _, re := range rejects {
					if re.MatchString(cmd) {
						return true
					}
				}
				return false
			}

			// A case may forbid setup outright — unconfirmed-identity does,
			// because the credential is not the agent the person named. The
			// invariant holds there a fortiori: no setup command may appear,
			// so none carrying a bad value can.
			if caught("connect setup -P helper --serve 222 --json") &&
				caught("connect setup -P helper --concurrency 4 --json") {
				t.Log("case forbids connect setup outright; the invariant holds without a value rule")
				return
			}
			checked++

			for _, v := range serveValues {
				cmd := "connect setup -P helper --serve " + v.arg + " --json"
				id, parseErr := parsePositiveID("--serve", v.value)
				cliAccepts := parseErr == nil && id > 0

				if traceServeValue.MatchString(v.value) {
					assert.False(t, caught(cmd), "a trace may carry %q, so no reject may fire on it", cmd)
					// The rule is a subset of the CLI's, not a different
					// one: anything these patterns allow must be a command
					// the CLI would take.
					assert.True(t, cliAccepts,
						"the patterns allow %q, so the CLI must accept %q — the rule may be narrower than the CLI, never wider",
						cmd, v.value)
					continue
				}
				assert.True(t, caught(cmd),
					"a trace may not carry %q, so a reject must catch it — an eval that lets it through reports coverage it does not have", cmd)
			}

			// The one value the CLI takes and a trace may not, named so the
			// narrowing is a decision on the record rather than a gap.
			plus := "connect setup -P helper --serve +222 --json"
			assert.True(t, caught(plus), "a leading plus is rejected in a trace")
			plusID, plusErr := parsePositiveID("--serve", "+222")
			assert.NoError(t, plusErr)
			assert.Equal(t, int64(222), plusID, "and the CLI would have taken it; this is a narrowing, not a disagreement")

			// A --serve= with nothing after it, and the flags that no longer
			// exist.
			for _, cmd := range []string{
				"connect setup -P helper --serve= --json",
				"connect setup -P helper --route 222=/home/me/x --json",
				"connect setup -P helper --remove-route 222 --json",
			} {
				assert.True(t, caught(cmd), "must be caught: %q", cmd)
			}
		})
	}
	assert.GreaterOrEqual(t, checked, 3, "every setup-issuing case is covered")
}
