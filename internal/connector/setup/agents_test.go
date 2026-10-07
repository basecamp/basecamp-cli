//go:build unix

package setup

import (
	"context"
	"net/http"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/connector/admission"
)

const peerAgent int64 = 53309518

func TestApplyAgents(t *testing.T) {
	base := validFile(t)

	out, err := Apply(base, Changes{AllowAgents: []int64{peerAgent, 77, peerAgent}})
	require.NoError(t, err)
	assert.Equal(t, []int64{77, peerAgent}, out.Trust.AgentIDs, "sorted and deduplicated")
	assert.Equal(t, admission.TrustOperator, out.Trust.Mode, "allowing an agent changes no person's trust")
	assert.Zero(t, out.Trust.AgentThreadCap, "the default cap is left implicit")

	kept, err := Apply(out, Changes{Serve: []int64{999}})
	require.NoError(t, err)
	assert.Equal(t, out.Trust.AgentIDs, kept.Trust.AgentIDs, "a run that names no agent keeps the list")

	removed, err := Apply(out, Changes{DisallowAgents: []int64{77}})
	require.NoError(t, err)
	assert.Equal(t, []int64{peerAgent}, removed.Trust.AgentIDs)
	none, err := Apply(removed, Changes{DisallowAgents: []int64{peerAgent}})
	require.NoError(t, err)
	assert.Nil(t, none.Trust.AgentIDs, "back to the default: no agent")

	_, err = Apply(base, Changes{AllowAgents: []int64{5}, DisallowAgents: []int64{5}})
	require.ErrorContains(t, err, "both allowed and disallowed")

	five, thirty := 5, 30
	capped, err := Apply(out, Changes{AgentThreadCap: &five, AgentDailyCap: &thirty})
	require.NoError(t, err)
	assert.Equal(t, 5, capped.Trust.AgentThreadCap)
	assert.Equal(t, 30, capped.Trust.AgentDailyCap)
	zero := 0
	reset, err := Apply(capped, Changes{AgentThreadCap: &zero})
	require.NoError(t, err)
	assert.Equal(t, admission.DefaultAgentThreadCap, reset.Trust.ThreadCap(), "0 restores the default")
	assert.Equal(t, 30, reset.Trust.DailyCap())

	for _, bad := range []int{-1, admission.MaxAgentCap + 1} {
		_, err := Apply(out, Changes{AgentThreadCap: &bad})
		require.ErrorContains(t, err, "--agent-thread-cap")
	}
}

func TestAllowedAgentsRoundTripThroughConnectJSON(t *testing.T) {
	path, err := Path(configDir(t), "agent")
	require.NoError(t, err)
	f := validFile(t)
	f.Trust.AgentIDs = []int64{peerAgent}
	f.Trust.AgentThreadCap = 4
	require.NoError(t, save(path, f))

	loaded, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, f, loaded)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	p, err := admission.ParsePolicy(data)
	require.NoError(t, err)
	assert.Equal(t, []int64{peerAgent}, p.Trust.AgentIDs, "admission reads what setup wrote")
	assert.Equal(t, 4, p.Trust.ThreadCap())
	assert.Equal(t, admission.DefaultAgentDailyCap, p.Trust.DailyCap())
}

func TestVerifyTrustChecksAllowedAgents(t *testing.T) {
	ctx := context.Background()
	r := &fakeReader{people: map[int64]Person{
		operatorID: {ID: operatorID, Name: "Operator"},
		peerAgent:  {ID: peerAgent, Name: "Zacharias Knudsen (agent)", PersonableType: PersonableAgent},
		9:          {ID: 9, Name: "A person"},
	}}
	verify := func(r Reader, trust Trust) map[string]string {
		out := map[string]string{}
		for _, c := range VerifyTrust(ctx, r, trust, agentID) {
			out[c.Name] = c.Status
		}
		return out
	}
	op := Person{ID: operatorID}

	assert.Equal(t, StatusPass, verify(r, Trust{Operator: op, Agents: []int64{peerAgent}})["Allowed agent 53309518"])
	assert.Equal(t, StatusFail, verify(r, Trust{Operator: op, Agents: []int64{9}})["Allowed agent 9"],
		"a person is trusted with --allow, not --allow-agent")
	assert.Equal(t, StatusFail, verify(r, Trust{Operator: op, Agents: []int64{agentID}})["Allowed agent 52007412"],
		"the agent never wakes itself")

	forbidden := &fakeReader{personErr: status(http.StatusForbidden)}
	assert.Equal(t, StatusFail, verify(forbidden, Trust{Operator: op, OperatorProfile: "me", Agents: []int64{peerAgent}})["Allowed agent 53309518"],
		"an agent that cannot be read is not verified")
	assert.Equal(t, StatusWarn, verify(forbidden, Trust{
		Operator: op, OperatorProfile: "me", Agents: []int64{peerAgent},
		Recorded: admission.Trust{AgentIDs: []int64{peerAgent}},
	})["Allowed agent 53309518"], "one already recorded is a warning")
}
