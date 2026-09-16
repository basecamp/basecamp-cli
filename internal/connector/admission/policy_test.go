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
    "48699913": {"path": "/work/connector", "class": "internal", "watch_completions": true},
    "555": {"path": "/work/other"}
  }
}`))
	require.NoError(t, err)
	assert.Equal(t, TrustOperator, p.Trust.Mode, "operator is the default mode")
	assert.Equal(t, Route{Path: "/work/connector", Class: "internal", WatchCompletions: true}, p.Projects[48699913])
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
		"route without a path":   func(p *Policy) { p.Projects[routedProj] = Route{Class: "internal"} },
		"route for a non-bucket": func(p *Policy) { p.Projects[-1] = Route{Path: "/x"} },
		"agent as operator":      func(p *Policy) { p.Trust.OperatorID = agentID },
		"agent in the allowlist": func(p *Policy) {
			p.Trust = Trust{Mode: TrustAllowlist, OperatorID: operatorID, AllowlistIDs: []int64{agentID}}
		},
		"agent in a longer allowlist": func(p *Policy) {
			p.Trust = Trust{Mode: TrustAllowlist, OperatorID: operatorID, AllowlistIDs: []int64{allowedID, agentID}}
		},
		"allowlist mode, project list": func(p *Policy) {
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
