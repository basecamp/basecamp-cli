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
// parser, because CI does not run a case: the Skill Evals job needs
// ANTHROPIC_API_KEY, which this repository does not have, and the job now
// reports Skipped rather than success so nobody reads a green tick as an eval
// that ran.
//
// # What this holds, and what it does not
//
// The accept and reject patterns, and only for the shape of a --serve value
// and the removed --route flags. This is a guard on one invariant, not on the
// eval files.
//
// The mock, expect_sequence and accept_response patterns are read by the Ruby
// runner under its own regex semantics, and this test models none of them —
// it compiles patterns with Go's regexp, which is RE2 and not Onigmo, so it
// cannot speak for them even where it reads them. That gap used to be the end
// of the sentence (Copilot on #765). It is now covered from the other side:
// scripts/check-eval-patterns.rb compiles every pattern in every case file
// under Onigmo itself, and make check and the Integration Tests job run it.
// A pattern that does not compile is caught there; what it means is still
// only caught by running the evals.
//
// One of the patterns is a blunt instrument and says so: a value of all
// digits can still be too large for an int64, and no shape can see a numeric
// bound, so the rule caps a value at 18 digits. That is comfortably above any
// real project id and one short of MaxInt64's 19, which makes MaxInt64 itself
// a declared narrowing rather than a value the patterns quietly mishandle.
//
// accept was outside the claim for two rounds, honestly declared and then
// twice the source of a finding: the rejects learned to allow the ordinary
// double-quoted spelling and the accepts did not move with them, so a trace
// the CLI would take failed the eval. A gap named is still a gap, and this
// one had stopped being theoretical.
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

// caseProject is the project every case in this directory is about, and the
// id the corpus spells in different ways.
const caseProject = int64(222)

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

	// All digits and still refused: a numeric bound, which no shape-based
	// pattern can see. The corpus is where a boundary like this gets
	// noticed, and this one was not in it (Copilot on #765).
	{arg: "9223372036854775808", value: "9223372036854775808"}, // MaxInt64 + 1
	{arg: "999999999999999999", value: "999999999999999999", traceOK: true},

	// A well-formed id for the wrong project. traceOK is about the
	// spelling, and this spelling is impeccable: every form rule in every
	// case passes it, because form is all they ask about. It is here to
	// prove that — the identity rule below is what catches it, and without
	// this entry nothing would notice if that rule were deleted. 2223 in
	// particular has 222 as a prefix, which is how a rule written with \b
	// or without an anchored end would let it through (Copilot on #765).
	{arg: "2223", value: "2223", traceOK: true},
	{arg: `"2223"`, value: "2223", traceOK: true},

	// The right project, spelled with a leading zero. The CLI reads 222 and
	// the identity rule refuses it, through the same {4,} branch that
	// catches 2223 — so it is a narrowing, and one the identity rule
	// created the moment it existed. The guard found it the way it found
	// +222 and MaxInt64: by being asked (Copilot on #765).
	//
	// Declared rather than allowed. Letting 0*222 through means the rule
	// has to say "222 with any number of leading zeros" in every case file,
	// which is more pattern for a spelling nobody writes — and this branch
	// already refuses a leading-zero project id in connect.json itself, in
	// admission.canonicalKey, for the same reason.
	{arg: "0222", value: "0222", why: "a leading zero is not how an id is written, and canonicalKey refuses the same spelling in connect.json"},
	{arg: `"0222"`, value: "0222", why: "same, quoted"},

	// The narrowings. The CLI takes all four; a trace may not.
	{arg: "+222", value: "+222", why: "a leading plus is not how an id is written"},
	{arg: "'22''2'", value: "222", why: "fragments the shell joins are not a spelling to teach"},
	{arg: "222'333'", value: "222333", why: "same, the other way round"},
	{arg: `'222'"333"`, value: "222333", why: "same, across both quote styles"},
	{arg: "9223372036854775807", value: "9223372036854775807",
		why: "MaxInt64 itself: the digit cap that catches the values above it cannot spare this one, and no project id is anywhere near"},
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

			compile := func(kind string, pats []string) []*regexp.Regexp {
				out := make([]*regexp.Regexp, 0, len(pats))
				for _, p := range pats {
					re, err := regexp.Compile(p)
					require.NoError(t, err, "%s pattern %q", kind, p)
					out = append(out, re)
				}
				return out
			}
			matches := func(res []*regexp.Regexp, cmd string) bool {
				for _, re := range res {
					if re.MatchString(cmd) {
						return true
					}
				}
				return false
			}
			rejects := compile("reject", c.Reject)
			caught := func(cmd string) bool { return matches(rejects, cmd) }
			// Form and identity are two different questions, and one set of
			// rejects answers both. A rule that fires on a well-formed id
			// for another project, and not on this case's own, is asking
			// the identity question; everything else is asking about the
			// spelling. Partitioned by what the rules do rather than by how
			// they are written, so rewording one does not silently move it.
			var spelling, identity []*regexp.Regexp
			for _, re := range rejects {
				other := re.MatchString("connect setup -P helper --serve 223 --json")
				own := re.MatchString(fmt.Sprintf("connect setup -P helper --serve %d --json", caseProject))
				if other && !own {
					identity = append(identity, re)
					continue
				}
				spelling = append(spelling, re)
			}
			misspelled := func(cmd string) bool { return matches(spelling, cmd) }
			wrongProject := func(cmd string) bool { return matches(identity, cmd) }
			// Only the accepts that speak about --serve: a case also accepts
			// its profile and its operator, which say nothing about a value.
			serveAccepts := compile("accept", filterServe(c.Accept))

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
			// Not implied by the loop below: a case whose corpus happened to
			// hold only correct ids would pass every assertion with no
			// identity rule at all.
			require.NotEmpty(t, identity,
				"this case allows connect setup, so it must say which project a --serve may name; the form rules never will")

			for _, v := range serveValues {
				cmd := "connect setup -P helper --serve " + v.arg + " --json"
				id, parseErr := parsePositiveID("--serve", v.value)
				cliAccepts := parseErr == nil && id > 0

				if v.traceOK {
					// Only the spelling rules: an identity rule firing here
					// is the point of it, not a disagreement about form.
					assert.False(t, misspelled(cmd), "a trace may carry the spelling %q, so no rule about form may fire on it", cmd)
					// And the identity rule decides on the id alone. This is
					// the same scoping the accepts got: a case is about one
					// project, so a well-formed id for another is wrong
					// however well it is spelled.
					if id == caseProject {
						assert.False(t, wrongProject(cmd), "%q names this case's own project", cmd)
					} else {
						assert.True(t, wrongProject(cmd),
							"%q is well-formed and names project %d, not %d: no rule about form will ever catch it, so the case has to", cmd, id, caseProject)
					}
					// And the accepts must recognize it — but only when the
					// value names the project the case is about. An accept
					// is scenario-specific: 007 is a different project, and
					// a case is right not to accept the wrong one. The
					// spelling is what is under test here, not the id.
					if id == caseProject {
						assert.True(t, matches(serveAccepts, cmd),
							"a trace may carry %q, so an accept must recognize that spelling", cmd)
					}
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
				assert.False(t, matches(serveAccepts, cmd),
					"and no accept may recognize %q, or the eval would take it as the command it asked for", cmd)
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

			// A --serve with no value, in both spellings cobra refuses, and
			// the flags that no longer exist.
			for _, cmd := range []string{
				"connect setup -P helper --serve= --json",
				"connect setup -P helper --serve",
				"connect setup -P helper --serve ",
				"connect setup -P helper --route 222=/home/me/x --json",
				"connect setup -P helper --remove-route 222 --json",
			} {
				assert.True(t, caught(cmd), "must be caught: %q", cmd)
			}
		})
	}
	assert.GreaterOrEqual(t, checked, 3, "every setup-issuing case is covered")
}

// filterServe keeps the accept patterns that constrain a --serve value. A
// case also accepts its profile and its operator, and those say nothing
// about what a value may be.
func filterServe(pats []string) []string {
	out := make([]string, 0, len(pats))
	for _, p := range pats {
		if strings.Contains(p, "--serve") {
			out = append(out, p)
		}
	}
	return out
}
