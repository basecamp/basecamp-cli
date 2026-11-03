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

const (
	groupPing int64 = 2085958513
	guest     int64 = 1007
	guest2    int64 = 1008
)

func TestApplyPings(t *testing.T) {
	base := validFile(t)

	out, err := Apply(base, Changes{AllowInPing: []PingPerson{{groupPing, guest2}, {groupPing, guest}, {groupPing, guest}}})
	require.NoError(t, err)
	assert.Equal(t, map[int64]admission.PingTrust{groupPing: {Participants: []int64{guest, guest2}}}, out.Pings, "sorted and deduplicated")
	assert.Nil(t, base.Pings, "the file passed in is not changed")
	assert.Equal(t, admission.TrustOperator, out.Trust.Mode, "letting someone into a Ping trusts nobody anywhere else")

	kept, err := Apply(out, Changes{Serve: []int64{999}})
	require.NoError(t, err)
	assert.Equal(t, out.Pings, kept.Pings, "a run that names no Ping keeps them")

	one, err := Apply(out, Changes{DisallowInPing: []PingPerson{{groupPing, guest2}}})
	require.NoError(t, err)
	assert.Equal(t, []int64{guest}, one.Pings[groupPing].Participants)
	assert.Equal(t, []int64{guest, guest2}, out.Pings[groupPing].Participants, "the file passed in is not changed")
	none, err := Apply(one, Changes{DisallowInPing: []PingPerson{{groupPing, guest}}})
	require.NoError(t, err)
	assert.Nil(t, none.Pings, "a Ping nobody is let into is no entry")

	_, err = Apply(base, Changes{AllowInPing: []PingPerson{{groupPing, guest}}, DisallowInPing: []PingPerson{{groupPing, guest}}})
	require.ErrorContains(t, err, "both")
	_, err = Apply(base, Changes{AllowInPing: []PingPerson{{0, guest}}})
	require.Error(t, err)
	_, err = Apply(base, Changes{AllowInPing: []PingPerson{{groupPing, -1}}})
	require.Error(t, err)
}

func TestPingsRoundTripThroughConnectJSON(t *testing.T) {
	path, err := Path(configDir(t), "agent")
	require.NoError(t, err)
	f := validFile(t)
	f.Pings = map[int64]admission.PingTrust{groupPing: {Participants: []int64{guest}}}
	require.NoError(t, save(path, f))

	loaded, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, f, loaded)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"pings": {`)
	p, err := admission.ParsePolicy(data)
	require.NoError(t, err)
	assert.Equal(t, f.Pings, p.Pings, "admission reads what setup wrote")

	f.Pings = nil
	require.NoError(t, save(path, f))
	data, err = os.ReadFile(path)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "pings", "a file with no Ping entry has no key an older CLI would refuse")
}

func TestAPingEntryRefusesAnAgent(t *testing.T) {
	f := validFile(t)
	f.Pings = map[int64]admission.PingTrust{groupPing: {Participants: []int64{agentID}}}
	require.Error(t, f.Validate())

	_, err := Parse([]byte(`{"version":1,"profile":"agent","account_id":"999","agent":{"person_id":52007412,"kind":"agent"},` +
		`"trust":{"mode":"operator","operator_id":26909558},"projects":{},"pings":{"2085958513":{"participant":[1007]}}}`))
	require.Error(t, err, "a misspelled key is refused, not read as nobody")
}

func TestVerifyPings(t *testing.T) {
	ctx := context.Background()
	r := &fakeReader{pings: map[int64]Ping{groupPing: {ID: groupPing, People: []Person{
		{ID: operatorID, Name: "Operator"},
		{ID: guest, Name: "Jorge"},
		{ID: 9, Name: "A client", Client: true},
		{ID: peerAgent, Name: "Peer (agent)", PersonableType: PersonableAgent},
	}}}}
	verify := func(r Reader, id int64, recorded bool) Check {
		t.Helper()
		pings := map[int64]admission.PingTrust{groupPing: {Participants: []int64{id}}}
		var rec map[int64]admission.PingTrust
		if recorded {
			rec = pings
		}
		checks := VerifyPings(ctx, r, pings, rec, agentID)
		require.Len(t, checks, 1)
		return checks[0]
	}

	pass := verify(r, guest, false)
	assert.Equal(t, StatusPass, pass.Status, pass.Message)
	assert.Contains(t, pass.Message, "Jorge")
	assert.Equal(t, "Ping 2085958513, person 1007", pass.Name)

	assert.Equal(t, StatusFail, verify(r, peerAgent, false).Status, "no agent is a participant")
	assert.Equal(t, StatusFail, verify(r, peerAgent, true).Status, "recorded or not")
	assert.Equal(t, StatusFail, verify(r, 9, false).Status, "a client is refused, as project trust refuses one")
	assert.Equal(t, StatusFail, verify(r, agentID, false).Status, "the agent itself")
	assert.Equal(t, StatusFail, verify(r, guest2, false).Status, "someone not in the Ping")
	assert.Equal(t, StatusWarn, verify(r, guest2, true).Status, "one recorded who has since left is a warning")

	gone := &fakeReader{pingErr: status(http.StatusNotFound)}
	assert.Equal(t, StatusFail, verify(gone, guest, false).Status, "a Ping the agent is not in")
	assert.Equal(t, StatusWarn, verify(gone, guest, true).Status, "one already recorded is a warning")
}

func TestApplyAgentsInPings(t *testing.T) {
	base := validFile(t)

	out, err := Apply(base, Changes{AllowAgentInPing: []PingPerson{{groupPing, peerAgent}, {groupPing, peerAgent}}})
	require.NoError(t, err)
	assert.Equal(t, map[int64]admission.PingTrust{groupPing: {Agents: []int64{peerAgent}}}, out.Pings)
	assert.Empty(t, out.Trust.AgentIDs, "letting an agent into a Ping allows it nowhere else")
	assert.Nil(t, base.Pings, "the file passed in is not changed")

	both, err := Apply(out, Changes{AllowInPing: []PingPerson{{groupPing, guest}}})
	require.NoError(t, err)
	assert.Equal(t, admission.PingTrust{Participants: []int64{guest}, Agents: []int64{peerAgent}}, both.Pings[groupPing], "people and agents share the entry")

	people, err := Apply(both, Changes{DisallowAgentInPing: []PingPerson{{groupPing, peerAgent}}})
	require.NoError(t, err)
	assert.Equal(t, admission.PingTrust{Participants: []int64{guest}}, people.Pings[groupPing])
	assert.Equal(t, []int64{peerAgent}, both.Pings[groupPing].Agents, "the file passed in is not changed")

	none, err := Apply(out, Changes{DisallowAgentInPing: []PingPerson{{groupPing, peerAgent}}})
	require.NoError(t, err)
	assert.Nil(t, none.Pings, "a Ping no agent and nobody is let into is no entry")

	_, err = Apply(base, Changes{AllowAgentInPing: []PingPerson{{groupPing, peerAgent}}, DisallowAgentInPing: []PingPerson{{groupPing, peerAgent}}})
	require.ErrorContains(t, err, "both")
	_, err = Apply(base, Changes{AllowAgentInPing: []PingPerson{{0, peerAgent}}})
	require.ErrorContains(t, err, "<ping-id>:<agent-id>")
	_, err = Apply(base, Changes{AllowAgentInPing: []PingPerson{{groupPing, 0}}})
	require.Error(t, err)
}

func TestAgentsInPingsRoundTripThroughConnectJSON(t *testing.T) {
	path, err := Path(configDir(t), "agent")
	require.NoError(t, err)
	f := validFile(t)
	f.Pings = map[int64]admission.PingTrust{groupPing: {Agents: []int64{peerAgent}}}
	require.NoError(t, save(path, f))

	loaded, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, f, loaded)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"agents": [`)
	p, err := admission.ParsePolicy(data)
	require.NoError(t, err)
	assert.Equal(t, f.Pings, p.Pings, "admission reads what setup wrote")
}

func TestAPingsAgentEntryRefusesTheAgentItselfAndTheOperator(t *testing.T) {
	for _, id := range []int64{agentID, operatorID} {
		f := validFile(t)
		f.Pings = map[int64]admission.PingTrust{groupPing: {Agents: []int64{id}}}
		require.Error(t, f.Validate(), "person %d", id)
	}
}

func TestVerifyPingsAgents(t *testing.T) {
	ctx := context.Background()
	r := &fakeReader{pings: map[int64]Ping{groupPing: {ID: groupPing, People: []Person{
		{ID: operatorID, Name: "Operator"},
		{ID: guest, Name: "Jorge"},
		{ID: peerAgent, Name: "Goose", PersonableType: PersonableAgent},
	}}}}
	verify := func(r Reader, id int64, recorded bool) Check {
		t.Helper()
		pings := map[int64]admission.PingTrust{groupPing: {Agents: []int64{id}}}
		var rec map[int64]admission.PingTrust
		if recorded {
			rec = pings
		}
		checks := VerifyPings(ctx, r, pings, rec, agentID)
		require.Len(t, checks, 1)
		return checks[0]
	}

	pass := verify(r, peerAgent, false)
	assert.Equal(t, StatusPass, pass.Status, pass.Message)
	assert.Contains(t, pass.Message, "Goose")
	assert.Equal(t, "Ping 2085958513, agent 53309518", pass.Name)

	person := verify(r, guest, false)
	assert.Equal(t, StatusFail, person.Status, "a person is let in with --allow-in-ping")
	assert.Contains(t, person.Message, "--allow-in-ping")
	assert.Equal(t, StatusFail, verify(r, guest, true).Status, "recorded or not")
	assert.Equal(t, StatusFail, verify(r, agentID, false).Status, "the agent itself")
	assert.Equal(t, StatusFail, verify(r, 53309999, false).Status, "an agent not in the Ping")
	assert.Equal(t, StatusWarn, verify(r, 53309999, true).Status, "one recorded that has since left is a warning")

	gone := &fakeReader{pingErr: status(http.StatusForbidden)}
	assert.Equal(t, StatusFail, verify(gone, peerAgent, false).Status, "a Ping this agent is not in")
	assert.Equal(t, StatusWarn, verify(gone, peerAgent, true).Status, "one already recorded is a warning")
}
