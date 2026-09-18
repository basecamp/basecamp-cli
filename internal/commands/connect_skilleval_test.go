package commands

import (
	"fmt"
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
// parser, because nothing else checks them at all: CI's Skill Evals job
// exits 0 without running a case when ANTHROPIC_API_KEY is unset, which it
// is on this repository.
//
// What the evals are supposed to guarantee about setup commands is one
// sentence — every `connect setup` in the trace is a command the CLI would
// accept — and four rounds of review found four spellings that slipped past
// patterns written as lists of wrong. So this holds the property rather than
// the pattern: for each case, every invalid command below must be caught by
// some reject, and no valid one may be. The invalid list is validated
// against parsePositiveID itself, so "invalid" means what the CLI means.
type skillEvalCase struct {
	Accept []string `yaml:"accept"`
	Reject []string `yaml:"reject"`
}

// serveValues are the values a --serve flag may carry in a trace, with what
// the CLI does with each. The quoting is the shell's, so a quoted id is the
// same id.
var serveValues = []struct {
	arg   string // as it appears on the command line, quoting included
	value string // what the shell hands the CLI
}{
	{"222", "222"},
	{"'222'", "222"},
	{"222=work", "222=work"},
	{"222=/home/me/x", "222=/home/me/x"},
	{"'222=work'", "222=work"},
	{"abc", "abc"},
	{"222abc", "222abc"},
	{"-1", "-1"},
	{"222,333", "222,333"},
}

func TestConnectSkillEvalsRejectEverySetupCommandTheCLIWould(t *testing.T) {
	dir := filepath.Join("..", "..", "skill-evals", "cases", "basecamp-connect")
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.NotEmpty(t, entries)

	// Only the shape of a --serve value is held here. Which id a scenario
	// should pick, and which other flags it must not use, are the cases'
	// own business — not-ready-agent-reads rejects --unserve because that
	// scenario is about adding a project, and first-time-setup rejects the
	// wrong project's id. Asserting over those would be asserting the
	// scenarios rather than the guarantee.

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
			// property holds there a fortiori: no setup command may appear,
			// so no invalid one can.
			if caught("connect setup -P helper --serve 222 --json") &&
				caught("connect setup -P helper --concurrency 4 --json") {
				t.Log("case forbids connect setup outright; the property holds without a value rule")
				return
			}
			checked++

			for _, v := range serveValues {
				cmd := "connect setup -P helper --serve " + v.arg + " --json"
				id, parseErr := parsePositiveID("--serve", v.value)
				cliAccepts := parseErr == nil && id > 0
				if cliAccepts {
					assert.False(t, caught(cmd), "the CLI accepts %q, so no reject may fire on it", cmd)
					continue
				}
				assert.True(t, caught(cmd),
					"the CLI refuses %q (%v), so a reject must catch it — an eval that lets it through reports coverage it does not have",
					cmd, parseErr)
			}

			// The flag that no longer exists, in either spelling.
			for _, cmd := range []string{
				"connect setup -P helper --route 222=/home/me/x --json",
				"connect setup -P helper --remove-route 222 --json",
			} {
				assert.True(t, caught(cmd), "a removed flag must be caught: %q", cmd)
			}
		})
	}
	assert.GreaterOrEqual(t, checked, 3, "every setup-issuing case is covered")
	fmt.Fprintf(os.Stderr, "skill-eval setup guarantee checked on %d cases\n", checked)
}
