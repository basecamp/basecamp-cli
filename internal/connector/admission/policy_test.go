package admission

import (
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
	} {
		t.Run(name, func(t *testing.T) {
			assert.Error(t, policy(entry), "the old validation refused this file, and so must reading it")
		})
	}

	// What the old writer really wrote still opens, and the key is still
	// discarded rather than kept.
	p, err := ParsePolicy([]byte(`{"trust":{"mode":"operator","operator_id":26909558},"projects":{"48699913":{"path":"/work/app","class":"internal"}}}`))
	require.NoError(t, err)
	assert.Equal(t, "internal", p.Projects[48699913].Class)

	// And a new file, which carries no path at all, is unaffected.
	require.NoError(t, policy(`{"class":"internal"}`))
	require.NoError(t, policy(`{}`))
}
