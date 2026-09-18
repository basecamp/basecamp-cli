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
// # The limit, which is the shell
//
// The corpus is a list of literal command lines. The patterns are regexes
// over text. Neither models /bin/sh, and a regex over a command line cannot:
// quoting, concatenation of adjacent fragments, variable expansion and
// command substitution all change what the CLI is handed, and each has more
// spellings than a pattern can enumerate. Three rounds of review each found
// another one.
//
// So the honest claim is narrow: for the spellings in serveValues, the
// patterns and parsePositiveID agree, and every narrowing between them is
// recorded there with a reason. A command line exotic enough — an expansion,
// a substitution, a spelling nobody has thought of — can still satisfy these
// patterns and be refused by the CLI. That is a limitation of the approach,
// not a gap to be closed by adding cases, and it is written here because the
// next person extending these patterns will otherwise believe they are
// converging on completeness.
//
// What the guard does catch, and what makes it worth having: any change to
// the patterns that makes them disagree with the parser on an ordinary
// spelling, and any narrowing added without being declared.
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

// serveValues is the corpus, and choosing it is the check rather than setup
// for the check: a guard is only as strong as the inputs it asserts over.
// The first version of this omitted 0, the empty value and +222 — exactly
// the three that would have shown its rule disagreeing with the CLI in both
// directions — and the second omitted the quoted-fragment forms the shell
// joins into one word. Both gaps were found by review, not by the guard.
//
// arg is the text on the command line; value is what /bin/sh hands the CLI
// after quoting and concatenation, which is what parsePositiveID sees.
// traceOK is the decision: whether a trace may carry this spelling at all.
// Where traceOK is false and the CLI would still accept value, the rule is
// deliberately narrower than the CLI, and the test says so out loud.
var serveValues = []struct {
	arg     string
	value   string
	traceOK bool
	why     string // only for a narrowing: why a trace may not use it
}{
	{arg: "222", value: "222", traceOK: true},
	{arg: "'222'", value: "222", traceOK: true},
	{arg: "007", value: "007", traceOK: true},   // leading zeros: the CLI reads 7
	{arg: "'007'", value: "007", traceOK: true}, //
	{arg: "0", value: "0"},                      // parses, not above zero
	{arg: "000", value: "000"},
	{arg: "''", value: ""}, // an empty value, as a shell delivers it
	{arg: "222=work", value: "222=work"},
	{arg: "222=/home/me/x", value: "222=/home/me/x"},
	{arg: "'222=work'", value: "222=work"},
	{arg: "abc", value: "abc"},
	{arg: "222abc", value: "222abc"},
	{arg: "-1", value: "-1"},
	{arg: "222,333", value: "222,333"},
	{arg: "'222'x", value: "222x"}, // the shell joins the fragments
	{arg: "x'222'", value: "x222"},

	// Double quotes are the other ordinary spelling, and the shell hands the
	// CLI the same value. Omitting them is how the previous corpus let an
	// undeclared narrowing through: "222" is not exotic (Copilot on #765).
	{arg: `"222"`, value: "222", traceOK: true},
	{arg: `"007"`, value: "007", traceOK: true},
	{arg: `"0"`, value: "0"},
	{arg: `""`, value: ""},
	{arg: `"222=work"`, value: "222=work"},
	{arg: `"222"x`, value: "222x"},

	// The narrowings. The CLI takes all three; a trace may not.
	{arg: "+222", value: "+222", why: "a leading plus is not how an id is written"},
	{arg: "'22''2'", value: "222", why: "fragments the shell joins are not a spelling to teach"},
	{arg: "222'333'", value: "222333", why: "same, the other way round"},
	{arg: `'222'"333"`, value: "222333", why: "same, across both quote styles"},
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

				if v.traceOK {
					assert.False(t, caught(cmd), "a trace may carry %q, so no reject may fire on it", cmd)
					// The rule is a subset of the CLI's, never a different
					// one: anything these patterns allow must be a command
					// the CLI would take.
					assert.True(t, cliAccepts,
						"the patterns allow %q, so the CLI must accept %q — the rule may be narrower than the CLI, never wider",
						cmd, v.value)
					assert.Empty(t, v.why, "a spelling a trace may carry is not a narrowing")
					continue
				}

				assert.True(t, caught(cmd),
					"a trace may not carry %q, so a reject must catch it — an eval that lets it through reports coverage it does not have", cmd)
				if cliAccepts {
					// A narrowing: the CLI would take it and a trace may
					// not. Recorded with a reason, so it is a decision on
					// the record rather than a disagreement nobody noticed.
					assert.NotEmpty(t, v.why,
						"%q is rejected here and accepted by the CLI, so the corpus must say why", v.arg)
				} else {
					assert.Empty(t, v.why, "the CLI refuses %q too; that is agreement, not a narrowing", v.arg)
				}
			}

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
