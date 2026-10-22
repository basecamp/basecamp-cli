package commands

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/basecamp/basecamp-cli/internal/connector"
)

// Which connector hears an agent's Pings. Only one should, and the CLI
// cannot see the others on other computers, so the defaults pick the one a
// person most likely means: the agent's first connection, whose credential
// is the agent's own client secret, and not a connection added after it,
// whose secret Basecamp starts with bcac_. A profile that is no agent
// connection — a person's login, such as a bot user's — hears them as it
// always has. Either flag overrides any of it, and a profile's first start
// saves what it found, so a restart keeps it: a first connection reconnected
// as an added one still answers them. The added connection's default is the
// one thing a restart doesn't keep, once the profile holds the agent's first
// connection: Basecamp hands that over only when no first connection is
// live, so nothing else answers Pings as one.
func TestWhichConnectorHearsPings(t *testing.T) {
	yes, no := true, false
	added := "bcac_" + "1111111111111111111111111111111111111111111"
	saved := func(hears bool, because string) *connector.PingsChoice {
		return &connector.PingsChoice{Hears: hears, Because: because, At: time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)}
	}
	for name, tc := range map[string]struct {
		flag      *bool
		saved     *connector.PingsChoice
		narrowed  bool
		oauthType string
		secret    string
		want      connectPings
		save      bool
	}{
		"the agent's first connection": {
			oauthType: "agent", secret: "not-a-real-secret",
			want: connectPings{Hears: true, Because: pingsBecauseFirstConnection}, save: true,
		},
		"a connection added after it": {
			oauthType: "agent", secret: added,
			want: connectPings{Hears: false, Because: pingsBecauseAddedConnection}, save: true,
		},
		"a person's login": {
			oauthType: "bc5", secret: "",
			want: connectPings{Hears: true, Because: pingsBecauseNotAnAgentConnection}, save: true,
		},
		"--pings on an added connection": {
			flag: &yes, oauthType: "agent", secret: added,
			want: connectPings{Hears: true, Because: pingsBecauseChosen}, save: true,
		},
		"--no-pings on a first connection": {
			flag: &no, oauthType: "agent", secret: "not-a-real-secret",
			want: connectPings{Hears: false, Because: pingsBecauseChosen}, save: true,
		},
		"--no-pings on a person's login": {
			flag: &no, oauthType: "bc5",
			want: connectPings{Hears: false, Because: pingsBecauseChosen}, save: true,
		},
		"a restart keeps what the first start found": {
			saved: saved(false, pingsBecauseAddedConnection), oauthType: "agent", secret: added,
			want: connectPings{Hears: false, Because: pingsBecauseAddedConnection},
		},
		"a first connection reconnected as an added one keeps hearing them": {
			saved: saved(true, pingsBecauseFirstConnection), oauthType: "agent", secret: added,
			want: connectPings{Hears: true, Because: pingsBecauseFirstConnection},
		},
		"an added connection that became the first hears them": {
			saved: saved(false, pingsBecauseAddedConnection), oauthType: "agent", secret: "not-a-real-secret",
			want: connectPings{Hears: true, Because: pingsBecauseFirstConnection}, save: true,
		},
		"--no-pings stays on a connection that became the first": {
			saved: saved(false, pingsBecauseChosen), oauthType: "agent", secret: "not-a-real-secret",
			want: connectPings{Hears: false, Because: pingsBecauseChosen},
		},
		"a restart keeps what a flag chose": {
			saved: saved(true, pingsBecauseChosen), oauthType: "agent", secret: added,
			want: connectPings{Hears: true, Because: pingsBecauseChosen},
		},
		"a flag replaces what was saved": {
			flag: &no, saved: saved(true, pingsBecauseFirstConnection), oauthType: "agent", secret: "not-a-real-secret",
			want: connectPings{Hears: false, Because: pingsBecauseChosen}, save: true,
		},
		"a run given --project, as before": {
			narrowed: true, oauthType: "agent", secret: "not-a-real-secret",
			want: connectPings{Hears: false, Because: pingsBecauseProject},
		},
		"a run given --project leaves the saved choice for the next": {
			narrowed: true, saved: saved(true, pingsBecauseChosen), oauthType: "agent", secret: "not-a-real-secret",
			want: connectPings{Hears: false, Because: pingsBecauseProject},
		},
		"--no-pings with --project is still a choice": {
			flag: &no, narrowed: true, oauthType: "agent", secret: "not-a-real-secret",
			want: connectPings{Hears: false, Because: pingsBecauseChosen}, save: true,
		},
	} {
		t.Run(name, func(t *testing.T) {
			got, save := resolveConnectPings(tc.flag, tc.saved, tc.narrowed, tc.oauthType, tc.secret)
			assert.Equal(t, tc.want, got)
			assert.Equal(t, tc.save, save, "whether this start saves its choice")
		})
	}
}

// Every reason has words a person reads, in status, doctor and the log.
func TestEveryPingsReasonSaysSomething(t *testing.T) {
	for _, because := range []string{pingsBecauseChosen, pingsBecauseFirstConnection, pingsBecauseAddedConnection, pingsBecauseNotAnAgentConnection, pingsBecauseProject} {
		for _, hears := range []bool{true, false} {
			words := connectPings{Hears: hears, Because: because}.Describe()
			assert.NotEmpty(t, words, because)
			assert.NotEqual(t, because, words, "the code is not the explanation")
			assert.NotContains(t, words, "_", "nor is any other")
		}
	}
	assert.Equal(t, "chosen with --pings", connectPings{Hears: true, Because: pingsBecauseChosen}.Describe())
	assert.Equal(t, "chosen with --no-pings", connectPings{Hears: false, Because: pingsBecauseChosen}.Describe())
	assert.NotEmpty(t, connectPings{Hears: true, Because: "something a newer build wrote"}.Describe())
}
