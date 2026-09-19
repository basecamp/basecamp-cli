package admission

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParsePolicy(t *testing.T) {
	p, err := ParsePolicy([]byte(`{
  "driver": "spawn",
  "trust": {"operator_id": 26909558},
  "projects": {
    "48699913": {"class": "internal", "watch_completions": true},
    "555": {}
  }
}`))
	require.NoError(t, err)
	assert.Equal(t, TrustOperator, p.Trust.Mode, "operator is the default mode")
	assert.Equal(t, Project{Class: "internal", WatchCompletions: true}, p.Projects[48699913])
	assert.False(t, p.Projects[555].WatchCompletions)

	p.AgentID = agentID
	require.NoError(t, p.Validate())
}

func TestValidateFailsClosed(t *testing.T) {
	for name, mutate := range map[string]func(*Policy){
		"no agent":                   func(p *Policy) { p.AgentID = 0 },
		"no operator":                func(p *Policy) { p.Trust.OperatorID = 0 },
		"unknown mode":               func(p *Policy) { p.Trust.Mode = "domain" },
		"empty mode":                 func(p *Policy) { p.Trust.Mode = "" },
		"allowlist outside its mode": func(p *Policy) { p.Trust.AllowlistIDs = []int64{allowedID} },
		"non-positive allowlist id": func(p *Policy) {
			p.Trust = Trust{Mode: TrustAllowlist, OperatorID: operatorID, AllowlistIDs: []int64{0}}
		},
		"served project that is not a bucket": func(p *Policy) { p.Projects[-1] = Project{} },
		"agent as operator":                   func(p *Policy) { p.Trust.OperatorID = agentID },
		"agent in the allowlist": func(p *Policy) {
			p.Trust = Trust{Mode: TrustAllowlist, OperatorID: operatorID, AllowlistIDs: []int64{agentID}}
		},
		"agent in a longer allowlist": func(p *Policy) {
			p.Trust = Trust{Mode: TrustAllowlist, OperatorID: operatorID, AllowlistIDs: []int64{allowedID, agentID}}
		},
		"allowlist ids in project mode": func(p *Policy) {
			p.Trust = Trust{Mode: TrustProject, OperatorID: operatorID, AllowlistIDs: []int64{allowedID}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			p := basePolicy()
			mutate(&p)
			assert.Error(t, p.Validate())
		})
	}
	require.NoError(t, basePolicy().Validate())
}

// Copilot on #765: connect.json is the trust anchor, so a malformed entry
// must refuse rather than authorize.
//
// The path check that went with the routes was doing two jobs and looked
// like one. It required a path, and because a JSON null decodes into a
// struct as the zero value without an error, a null entry had no path and so
// was refused. Take the path away and the null becomes a perfectly valid
// served project: a half-edited file, a bad merge or a truncated write would
// grant authorization where it used to withhold it.
func TestANullProjectEntryIsRefusedRatherThanServed(t *testing.T) {
	_, err := ParsePolicy([]byte(`{"trust":{"mode":"operator","operator_id":26909558},"projects":{"48699913":null}}`))
	require.Error(t, err, "a null entry is not a served project")
	assert.Contains(t, err.Error(), "{}", "and the refusal says what a valid entry looks like")

	// An empty object is a served project with no settings, and stays one:
	// this refuses the null, not the absence of settings.
	p, err := ParsePolicy([]byte(`{"trust":{"mode":"operator","operator_id":26909558},"projects":{"48699913":{}}}`))
	require.NoError(t, err)
	assert.Contains(t, p.Projects, int64(48699913))
}

// Copilot on #765: a field kept for compatibility is still load-bearing
// while it is being read.
//
// LegacyPath is decoded and thrown away, and discarding a value is not the
// same as not caring what it is. The old writer could only ever have put a
// clean absolute path there, so anything else means the file is not what it
// claims to be — and the old validation refused exactly those files. Reading
// the key without its shape check would let the files that used to be
// refused authorize their projects instead.
func TestAMalformedLegacyPathIsRefusedBeforeItIsDiscarded(t *testing.T) {
	policy := func(entry string) error {
		_, err := ParsePolicy([]byte(`{"trust":{"mode":"operator","operator_id":26909558},"projects":{"48699913":` + entry + `}}`))
		return err
	}
	for name, entry := range map[string]string{
		"null":         `{"path":null}`,
		"empty":        `{"path":""}`,
		"relative":     `{"path":"work/app"}`,
		"unclean":      `{"path":"/work/../etc"}`,
		"not a string": `{"path":42}`,
		// A filesystem path cannot hold a NUL: the kernel refuses one
		// outright, since a path reaches it as a NUL-terminated string. So
		// no connector ever wrote this, whatever path.IsAbs and path.Clean
		// make of it — both are happy with it.
		"a NUL byte": `{"path":"/work/\u0000app"}`,
	} {
		t.Run(name, func(t *testing.T) {
			assert.Error(t, policy(entry), "the old validation refused this file, and so must reading it")
		})
	}

	// Only a NUL, though. A Linux path may hold a newline, a tab or an
	// escape — those are bytes a connector really could have written, and
	// refusing them would be new strictness rather than a restored check,
	// which is the mistake this branch already made once elsewhere.
	require.NoError(t, policy(`{"path":"/work/a\nb"}`), "a newline is legal in a path, so a writer could have produced it")
	require.NoError(t, policy(`{"path":"/work/a\tb"}`), "and a tab")

	// The shape is the writer's, not this host's. A connector runs on Linux
	// only, so this key is a POSIX path — and this package builds
	// everywhere, so a check written against the reader's platform would
	// refuse a legitimate Linux-written file on Windows, rejecting exactly
	// what it exists to accept. This test file carries no build tag on
	// purpose: it is the one that would catch that, and it catches it only
	// where it runs.
	require.NoError(t, policy(`{"path":"/work/app"}`),
		"a POSIX absolute path is the shape the writer wrote, on every platform this parser is built for")
	assert.Error(t, policy(`{"path":"C:\\work\\app"}`),
		"and a path in the reader's dialect is not something that writer could have produced")

	// What the old writer really wrote still opens, and the key is still
	// discarded rather than kept.
	p, err := ParsePolicy([]byte(`{"trust":{"mode":"operator","operator_id":26909558},"projects":{"48699913":{"path":"/work/app","class":"internal"}}}`))
	require.NoError(t, err)
	assert.Equal(t, "internal", p.Projects[48699913].Class)

	// And a new file, which carries no path at all, is unaffected.
	require.NoError(t, policy(`{"class":"internal"}`))
	require.NoError(t, policy(`{}`))
}

// Copilot on #765: encoding/json matches a struct tag case-insensitively, so
// a value under "Path" lands in LegacyPath while checkLegacyPath, which looks
// the key up exactly, sees nothing to check. The reported case is one of six.
//
// This goes through ParsePolicy on purpose. The same documents were already
// covered through setup.Parse, which has the key walk and refused every one
// of them — and that coverage is what hid the gap, because the reader that
// authorizes is the other one. A test of the strict reader says nothing
// about the permissive one.
func TestParsePolicyRefusesKeysTheDecoderWouldMatchAnyway(t *testing.T) {
	const trust = `"trust":{"mode":"operator","operator_id":26909558}`
	for name, tc := range map[string]struct {
		doc   string
		would string
	}{
		"trust under a case variant": {
			doc:   `{` + trust + `,"Trust":{"mode":"allowlist","allowlist_ids":[999]},"projects":{"1":{}}}`,
			would: "escalate the trust mode to allowlist and trust person 999",
		},
		"projects under a case variant": {
			doc:   `{` + trust + `,"projects":{"1":{}},"Projects":{"2":{}}}`,
			would: "serve project 2, which the projects key does not name",
		},
		"project id with a leading zero": {
			doc:   `{` + trust + `,"projects":{"01":{}}}`,
			would: "serve project 1 under a key setup refuses",
		},
		"legacy path under a case variant": {
			doc:   `{` + trust + `,"projects":{"1":{"Path":null}}}`,
			would: "serve project 1 with the legacy-path check skipped entirely",
		},
		"legacy path under an upper-case variant": {
			doc:   `{` + trust + `,"projects":{"1":{"PATH":"../elsewhere"}}}`,
			would: "serve project 1 carrying a relative path no routing connector wrote",
		},
		"a key given twice": {
			doc:   `{` + trust + `,"projects":{"1":{}},"projects":{"2":{}}}`,
			would: "serve project 2 while the file appears to say project 1",
		},
	} {
		t.Run(name, func(t *testing.T) {
			p, err := ParsePolicy([]byte(tc.doc))
			require.Error(t, err, "accepted; it would %s", tc.would)
			assert.Equal(t, Policy{}, p, "a refused document yields no policy")
		})
	}

	// The control. Every document above is one key's spelling away from a
	// document that parses, so the refusals are the spelling and not some
	// other thing wrong with them — without this the table would pass just
	// as well against a ParsePolicy that refused everything.
	p, err := ParsePolicy([]byte(`{` + trust + `,"projects":{"1":{},"2":{"path":"/srv/app"}}}`))
	require.NoError(t, err)
	assert.Len(t, p.Projects, 2)
	assert.Equal(t, TrustOperator, p.Trust.Mode)
}

// The second layer, and the one CheckCanonicalKeys does not stand in for.
// Project is exported and its UnmarshalJSON runs wherever a caller decodes
// an entry — setup's own refuseMalformedProjects does exactly that — so it
// has to fail closed on a document nobody walked first. A guarantee that
// holds only because two functions happen to run in one order is one a later
// edit removes without touching either of them.
func TestAProjectEntryFailsClosedOnItsOwn(t *testing.T) {
	for name, entry := range map[string]string{
		"a case variant of path":       `{"Path":null}`,
		"an upper-case variant":        `{"PATH":"../elsewhere"}`,
		"a mixed-case variant":         `{"pAtH":"work/app"}`,
		"the canonical spelling still": `{"path":""}`,
	} {
		t.Run(name, func(t *testing.T) {
			var p Project
			assert.Error(t, json.Unmarshal([]byte(entry), &p),
				"the decoder fills LegacyPath from this key, so the check has to find it there")
		})
	}

	// Unchanged for everything that was already right: the value check reads
	// the value, and a well-formed one under any spelling is still a
	// well-formed value. What refuses the spelling is the document walk.
	var p Project
	require.NoError(t, json.Unmarshal([]byte(`{"path":"/work/app","class":"internal"}`), &p))
	assert.Equal(t, "internal", p.Class)
}

// Copilot on #765, the fourth fail-open in this one shim: encoding/json
// substitutes U+FFFD for malformed UTF-8 and for an unpaired surrogate
// escape, silently, so "/work/\ud800" reaches the shape checks as
// "/work/�" — absolute, clean, and authorizing.
//
// Same mechanism as the case-variant keys, a different decoder behavior:
// there it folded a key looked up exactly, here it rewrites bytes validated
// afterwards. The check has to ask whether the value it is about to validate
// is the value in the file, before it validates anything.
func TestALegacyPathTheDecoderHadToRewriteIsRefused(t *testing.T) {
	policy := func(entry string) error {
		_, err := ParsePolicy([]byte(`{"trust":{"mode":"operator","operator_id":26909558},"projects":{"48699913":` + entry + `}}`))
		return err
	}
	for name, entry := range map[string]string{
		"an unpaired high surrogate": `{"path":"/work/\ud800"}`,
		"an unpaired low surrogate":  `{"path":"/work/\udc00"}`,
		"a high surrogate then text": `{"path":"/work/\ud800app"}`,
		"a reversed surrogate pair":  `{"path":"/work/\udc00\ud800"}`,
		"raw malformed UTF-8":        "{\"path\":\"/work/\xff\"}",
		"a truncated UTF-8 sequence": "{\"path\":\"/work/\xe2\x82\"}",
		"malformed bytes alone":      "{\"path\":\"\xc3\x28\"}",
	} {
		t.Run(name, func(t *testing.T) {
			assert.Error(t, policy(entry), "the decoder replaced this with U+FFFD, so the checks ran on a value no writer wrote")
		})
	}

	// A well-formed pair is not lossy, and neither is a path that really
	// holds U+FFFD. Reject the lossy input, not the character.
	require.NoError(t, policy(`{"path":"/work/😀"}`), "a well-formed surrogate pair is an emoji, not a replacement")
	require.NoError(t, policy(`{"path":"/work/�"}`), "U+FFFD written as an escape is a path a writer could hold")
	require.NoError(t, policy("{\"path\":\"/work/\\u00e9\"}"), "an ordinary escaped rune")
	require.NoError(t, policy("{\"path\":\"/work/�\"}"), "and U+FFFD as its own literal bytes")

	// The reason this is a losslessness test and not a comparison against
	// json.Marshal of the decoded value. json.Marshal HTML-escapes & < and
	// >, so it spells these three with &, < and > and a
	// canonical comparison would refuse every one of them — ordinary
	// directory names, in a file this code is only reading in order to throw
	// the field away. Refusing a legitimate path is the mistake, not the
	// miss.
	require.NoError(t, policy(`{"path":"/work/r&d"}`), "an ampersand is an ordinary character in a directory name")
	require.NoError(t, policy(`{"path":"/work/a<b"}`))
	require.NoError(t, policy(`{"path":"/work/x>y"}`))
	// And the same three as the writer itself spells them, which is the
	// other half of why the comparison has two answers.
	require.NoError(t, policy(`{"path":"/work/r&d"}`), "the spelling json.Marshal produces parses too")
	require.NoError(t, policy(`{"path":"/work/a<b"}`))

	// An escaped backslash is two bytes and not the start of an escape: a
	// path ending in one must not make the scan read the closing quote as
	// part of a \u.
	require.NoError(t, policy(`{"path":"/work/a\\b"}`), "an escaped backslash is a literal backslash")
	require.NoError(t, policy(`{"path":"/work/a\\"}`))
}
