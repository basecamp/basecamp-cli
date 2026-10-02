//go:build linux || darwin

package connect

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/connector"
	"github.com/basecamp/basecamp-cli/internal/connector/admission"
	"github.com/basecamp/basecamp-cli/internal/connector/fakebasecamp"
)

// How the connector fails, and how it recovers, end to end. Every fault is
// one Basecamp can really produce, put to the connector the way it would
// meet it: nothing in the CLI is told a test is running.

// Exit codes the connector ends with, as output.ExitCodeFor maps its errors.
const (
	exitAuth = 3
	exitLock = 5
)

// MsgShuttingDown is logged when a signal starts the connector's shutdown.
const MsgShuttingDown = "connector: shutting down"

// Ctrl-C on a running connector: it says it is shutting down, and exits
// 130, the shell's code for an interrupt, having written nothing it should
// not have.
func TestSIGINTShutsTheConnectorDownCleanly(t *testing.T) {
	t.Parallel()
	onEachTarget(t, func(t *testing.T, tg Target) {
		out := streamingOn(t, tg).Stop(t)

		shutdown := out.Logged(MsgShuttingDown)
		require.Len(t, shutdown, 1, out.Transcript())
		assert.Equal(t, "interrupt", shutdown[0].Attrs["signal"])
		for _, l := range out.Logs {
			assert.NotEqual(t, "ERROR", l.Level, "%q", l.Raw)
		}
	})
}

// A second connector for an agent already running, started from another
// repository on the same computer, refuses at once: it says which process
// holds the agent, and asks Basecamp for nothing, not even a token.
func TestASecondConnectorForTheSameAgentRefusesAtOnce(t *testing.T) {
	t.Parallel()
	onEachTarget(t, func(t *testing.T, tg Target) {
		first := streamingOn(t, tg)
		fake, observed := fakeOf(tg)
		before := 0
		if observed {
			before = len(fake.Requests())
		}

		second := tg.CLI().Start(t, t.TempDir(), "connect", "-P", tg.Agent().Profile).Wait(t)
		require.Equal(t, exitLock, second.ExitCode, second.Transcript())
		assert.Empty(t, second.Lines, "nothing on stdout")
		refusal := strings.Join(rawLogs(second), "\n")
		assert.Contains(t, refusal, strconv.Itoa(first.Pid()), "the refusal names the process that holds the agent")

		// Only the fake sees every request made of it.
		if observed {
			for _, r := range fake.Requests()[before:] {
				t.Errorf("the refused connector reached the fake: %s %s", r.Method, r.Path)
			}
		}
		first.Stop(t)
	})
}

// SecondAgentID is a second agent on the default project, connected through
// a client of its own.
const (
	SecondAgentID       int64 = 52007413
	SecondAgentName           = "Second Chef"
	SecondAgentClientID       = "second-agent-client"
)

// SecondAgent is the second agent, as a profile of its own.
var SecondAgent = Profile{Name: "second", ClientID: SecondAgentClientID}

// Two agents connected on one computer are two connectors, and each hands
// off only what was asked of it.
func TestTwoAgentsRunSideBySideAndEachGetsItsOwnMentions(t *testing.T) {
	t.Parallel()
	fakeOnly(t, "it needs a second agent connected in the same world")
	w := world()
	w.People[SecondAgentID] = &fakebasecamp.Person{ID: SecondAgentID, Name: SecondAgentName, Agent: true}
	w.Agents[SecondAgentClientID] = &fakebasecamp.AgentClient{
		ID: SecondAgentClientID, Secret: "second-secret", PersonID: SecondAgentID, Scope: fakebasecamp.ScopeFull,
	}
	p := w.Projects[fakebasecamp.ProjectID]
	p.Members = append(p.Members, SecondAgentID)
	h := NewHarness(t, w)
	h.Setup(t)
	h.SetupProfile(t, SecondAgent)

	first := h.Connect(t)
	second := h.ConnectProfile(t, SecondAgent)
	first.WaitStreaming(t, 1)
	second.WaitStreaming(t, 1)

	toFirst := comment(h, fakebasecamp.OperatorID, mentioning(fakebasecamp.AgentID), fakebasecamp.Live)
	toSecond := comment(h, fakebasecamp.OperatorID, mentioning(SecondAgentID), fakebasecamp.Live)
	// Each decides both, and hands off its own: a request line comes after
	// its verdict, so waiting for it waits for both.
	first.WaitFor(t, "its request and the other's verdict", func(o Output) bool {
		return len(o.RequestsFor(toFirst.ID)) > 0 && len(o.EventsFor(toSecond.ID)) > 0
	})
	second.WaitFor(t, "its request and the other's verdict", func(o Output) bool {
		return len(o.RequestsFor(toSecond.ID)) > 0 && len(o.EventsFor(toFirst.ID)) > 0
	})
	firstOut, secondOut := first.Stop(t), second.Stop(t)

	assert.Len(t, firstOut.RequestsFor(toFirst.ID), 1)
	assert.Equal(t, "not_addressed", firstOut.EventsFor(toSecond.ID)[0].Reason)
	assert.Empty(t, firstOut.RequestsFor(toSecond.ID))
	assert.Len(t, secondOut.RequestsFor(toSecond.ID), 1)
	assert.Equal(t, "not_addressed", secondOut.EventsFor(toFirst.ID)[0].Reason)
	assert.Empty(t, secondOut.RequestsFor(toFirst.ID))
	assert.Equal(t, strconv.FormatInt(fakebasecamp.AgentID, 10), firstOut.Logged(MsgRunning)[0].Attrs["agent_person_id"])
	assert.Equal(t, strconv.FormatInt(SecondAgentID, 10), secondOut.Logged(MsgRunning)[0].Attrs["agent_person_id"])
}

// disconnected is the connector's refusal, in the words guided setup uses
// for the same thing.
const disconnected = "was disconnected in Basecamp, or connected on another computer"

// The operator disconnects the agent in Basecamp while its connector runs.
// Basecamp revokes the agent's tokens and clears its secret, and leaves the
// live connection up, so the connector learns of it from the next read it
// makes: the operator's next mention. It exits 3, and says what happened and
// how to reconnect.
func TestAnAgentDisconnectedWhileRunningStopsTheConnector(t *testing.T) {
	t.Parallel()
	fakeOnly(t, "it disconnects the agent in Basecamp")
	h, c := streaming(t)
	h.Fake.Disconnect(fakebasecamp.AgentClientID)
	ev := comment(h, fakebasecamp.OperatorID, askAgent(), fakebasecamp.Live)
	out := c.Wait(t)

	requireDisconnected(t, out)
	assert.Empty(t, out.RequestsFor(ev.ID), "nothing is handed off as an agent Basecamp no longer takes")
}

// The same disconnect, met by the feed instead: the live connection drops,
// and the reconnect's stream ticket is refused.
func TestAnAgentDisconnectedWhileRunningStopsTheConnectorOnReconnect(t *testing.T) {
	t.Parallel()
	fakeOnly(t, "it disconnects the agent in Basecamp and drops the live connection")
	h, c := streaming(t)
	h.Fake.Disconnect(fakebasecamp.AgentClientID)
	require.Equal(t, 1, h.Fake.DropCable())
	requireDisconnected(t, c.Wait(t))
}

// The agent is connected on another computer while its connector runs here.
// Basecamp rotates the agent's secret, and leaves the token this computer
// holds alive, so the connector runs on until that token comes due. The
// renewal is refused invalid_client, and the connector exits 3, saying so.
func TestAnAgentConnectedElsewhereStopsTheConnectorAtItsNextRenewal(t *testing.T) {
	t.Parallel()
	fakeOnly(t, "it connects the agent again on another computer, and needs tokens that last a second")
	h := NewHarness(t, shortTokens(world()))
	h.Setup(t)
	c := h.Connect(t)
	c.WaitStreaming(t, 1)

	h.AnotherComputer(t).MustRun(t, "auth", "agent", "connect", "-P", Agent, "--no-browser")

	h.waitTokenDue(t, DefaultAgent)
	ev := comment(h, fakebasecamp.OperatorID, askAgent(), fakebasecamp.Live)
	out := c.Wait(t)

	requireDisconnected(t, out)
	assert.Empty(t, out.RequestsFor(ev.ID))
	invalid := false
	for _, r := range h.Fake.Requests() {
		invalid = invalid || (r.Route == fakebasecamp.RouteToken && r.Status == http.StatusUnauthorized)
	}
	assert.True(t, invalid, "the renewal was refused")
}

// The token endpoint is rate-limited when the connector starts and needs a
// new token: it says it is waiting, waits as long as Basecamp asked, and
// comes up and hands off a mention.
func TestARateLimitedTokenAtStartIsWaitedOut(t *testing.T) {
	t.Parallel()
	fakeOnly(t, "it rate-limits the token endpoint, and needs tokens that last a second")
	h := NewHarness(t, shortTokens(world()))
	h.Setup(t)
	h.waitTokenDue(t, DefaultAgent)
	limited := fakebasecamp.TokenRateLimited(1)
	limited.Times = 1
	fault := h.Fake.Inject(limited)

	c := h.Connect(t)
	c.WaitStreaming(t, 1)
	ev := comment(h, fakebasecamp.OperatorID, askAgent(), fakebasecamp.Live)
	c.WaitFor(t, "the request line", func(o Output) bool { return len(o.RequestsFor(ev.ID)) > 0 })
	out := c.Stop(t)

	waited := false
	for _, l := range out.Logs {
		waited = waited || (l.Level == "WARN" && strings.HasPrefix(l.Msg, "connector: could not get the agent's token yet"))
	}
	assert.True(t, waited, "the wait is said\n%s", out.Transcript())
	assert.Equal(t, 1, fault.Hits(), "the token was rate-limited once")
	assert.Len(t, out.RequestsFor(ev.ID), 1)
}

// The live connection drops, and an event Basecamp commits while it is down
// reaches only the poll lane. The reconnect's catch-up walk delivers it,
// and it is handed off once.
func TestAnEventCommittedWhileTheCableIsDownArrivesByCatchUp(t *testing.T) {
	t.Parallel()
	fakeOnly(t, "it holds the reconnect and publishes on the poll lane alone")
	h, c := streaming(t)
	// Hold the reconnect at its stream ticket, so the event is committed
	// while the connector is certainly between connections.
	reconnect := h.Fake.Gate(fakebasecamp.RouteStreamTicket)
	require.Equal(t, 1, h.Fake.DropCable())
	h.await(t, "the reconnect", func() bool { return reconnect.Waiting() > 0 })
	ev := comment(h, fakebasecamp.OperatorID, askAgent(), fakebasecamp.Poll)
	reconnect.Release()

	c.WaitFor(t, "the request line", func(o Output) bool { return len(o.RequestsFor(ev.ID)) > 0 })
	c.WaitStreaming(t, 2)
	out := c.Stop(t)

	pointers := out.PointersFor(ev.ID)
	require.Len(t, pointers, 1)
	assert.Equal(t, "poll", pointers[0].Lane)
	assert.Len(t, out.RequestsFor(ev.ID), 1)
}

// The live connection stays open and goes quiet: no pings. The connector
// calls it stale, reconnects, and goes on handing off what is published
// live. Staleness is seven and a half seconds without a ping, which no
// option shortens, so this runs once.
func TestAStaleCableIsReplaced(t *testing.T) {
	t.Parallel()
	fakeOnly(t, "it silences the live connection's pings")
	h, c := streaming(t)
	h.Fake.StopPings()
	c.WaitStreaming(t, 2)
	ev := comment(h, fakebasecamp.OperatorID, askAgent(), fakebasecamp.Live)
	c.WaitFor(t, "the request line", func(o Output) bool { return len(o.RequestsFor(ev.ID)) > 0 })
	out := c.Stop(t)
	assert.Len(t, out.RequestsFor(ev.ID), 1)
}

// Basecamp fails the recording read twice, then serves it. Admission
// retries within the one decision, so the mention is admitted in a single
// verdict and handed off once.
func TestAReadThatFailsTwiceThenServesIsAdmittedInOneDecision(t *testing.T) {
	t.Parallel()
	fakeOnly(t, "it fails recording reads")
	h, c := streaming(t)
	failing := h.Fake.Inject(fakebasecamp.ServerError(2, fakebasecamp.RouteComment))
	ev := comment(h, fakebasecamp.OperatorID, askAgent(), fakebasecamp.Live)
	c.WaitFor(t, "the request line", func(o Output) bool { return len(o.RequestsFor(ev.ID)) > 0 })
	out := c.Stop(t)

	assert.Equal(t, 2, failing.Hits())
	reads := 0
	for _, r := range h.Fake.Requests() {
		if r.Route == fakebasecamp.RouteComment && r.Path == "/"+strconv.FormatInt(fakebasecamp.AccountID, 10)+"/comments/"+strconv.FormatInt(ev.RecordingID, 10) {
			reads++
		}
	}
	assert.Equal(t, 3, reads, "two failures and the read that served")
	events := out.EventsFor(ev.ID)
	require.Len(t, events, 1, "one decision")
	assert.Equal(t, "admitted", events[0].State)
	assert.Len(t, out.RequestsFor(ev.ID), 1)
}

// A connector held by its operator admits a mention and hands nothing off.
// It is killed outright, the hold released, and the connector started
// again within the grace period: the request it admitted and never handed
// off is handed off now, once.
func TestARequestAdmittedBeforeACrashIsHandedOffOnRestart(t *testing.T) {
	t.Parallel()
	onEachTarget(t, func(t *testing.T, tg Target) {
		held := connectAs(t, tg, "--hold")
		held.WaitStreaming(t, 1)
		p := tg.OperatorMentionsAgent(t)
		before := held.WaitFor(t, "the event line", func(o Output) bool { return len(o.EventsOn(p)) > 0 })
		require.Equal(t, "admitted", before.EventsOn(p)[0].State)
		require.Empty(t, before.RequestsOn(p), "held: nothing is handed off")
		held.Kill(t)
		require.Equal(t, -1, held.Wait(t).ExitCode, "killed")

		tg.CLI().MustRun(t, "connect", "release", "-P", tg.Agent().Profile)
		c := connectAs(t, tg)
		c.WaitFor(t, "the request line", func(o Output) bool { return len(o.RequestsOn(p)) > 0 })
		out := c.Stop(t)
		assert.Len(t, out.RequestsOn(p), 1)
	})
}

// Admission falls behind the feed: Basecamp holds every recording read
// open while events arrive live. The connector warns that its backlog
// reached the warning depth, and says so again when the reads come back and
// the backlog falls below it.
func TestABacklogPastItsWarningDepthIsReportedAndRecovers(t *testing.T) {
	t.Parallel()
	fakeOnly(t, "it holds every recording read open")
	h, c := streaming(t)
	reads := h.Fake.Gate(fakebasecamp.RecordingReads...)
	// Each admission worker takes one event off the backlog and is held at
	// its read, so the backlog reaches the warning depth only once that
	// many more have arrived.
	const backlog = connector.DefaultBacklogWarn + admission.DefaultWorkers
	recordings := make([]int64, backlog)
	h.Fake.Update(func(w *fakebasecamp.World) {
		w.Recordings[kickoffID] = &fakebasecamp.Recording{
			ID: kickoffID, Type: "Message", BucketID: fakebasecamp.ProjectID, CreatorID: fakebasecamp.OperatorID, Title: "Kickoff",
		}
		for i := range recordings {
			id := kickoffID + 1 + int64(i)
			recordings[i] = id
			w.Recordings[id] = &fakebasecamp.Recording{
				ID: id, Type: "Comment", BucketID: fakebasecamp.ProjectID, ParentID: kickoffID, CreatorID: fakebasecamp.OperatorID, Content: "<p>Noted</p>",
			}
		}
	})
	for _, id := range recordings {
		h.Fake.Emit(fakebasecamp.Event{
			EventType: "comment.created", BucketID: fakebasecamp.ProjectID, RecordingID: id, CreatorID: fakebasecamp.OperatorID,
		}, fakebasecamp.Live)
	}
	warned := c.WaitLogged(t, MsgBacklogWarn, 1).Logged(MsgBacklogWarn)[0]
	reads.Release()
	recovered := c.WaitLogged(t, MsgBacklogRecover, 1).Logged(MsgBacklogRecover)[0]
	c.Stop(t)

	assert.Equal(t, "WARN", warned.Level)
	assert.Equal(t, strconv.Itoa(connector.DefaultBacklogWarn), warned.Attrs["warn_at"])
	assert.Equal(t, "INFO", recovered.Level)
}

// Messages the connector's queue logs at the edges of its backlog.
const (
	MsgBacklogWarn    = "the backlog reached its warning depth; admission is falling behind the feed"
	MsgBacklogRecover = "the backlog fell back below its warning depth"
)

// requireDisconnected requires that the connector stopped because Basecamp
// no longer takes its agent's credential, and said so in the person's words.
func requireDisconnected(t *testing.T, out Output) {
	t.Helper()
	require.Equal(t, exitAuth, out.ExitCode, out.Transcript())
	assert.Contains(t, strings.Join(rawLogs(out), "\n"), disconnected, out.Transcript())
	assert.Contains(t, strings.Join(rawLogs(out), "\n"), "basecamp connect setup -P "+Agent)
}

// mentioning is a comment that asks one agent for something.
func mentioning(agentID int64) string {
	return "<p>Could you take this, " + fakebasecamp.Mention(agentID) + "?</p>"
}

// shortTokens is w with every agent's tokens lasting a second, so a test
// can see one come due without waiting an hour.
func shortTokens(w *fakebasecamp.World) *fakebasecamp.World {
	for _, a := range w.Agents {
		a.TokenLifetime = time.Second
	}
	return w
}

// waitTokenDue waits until the token p's profile holds is due for renewal:
// past the expiry `basecamp auth status` reports for it, which is the
// latest the CLI renews it. This is the one wait here on the clock, and it
// is the scenario's premise, not a guess at how long something takes: a
// token comes due only with time.
func (h *Harness) waitTokenDue(t *testing.T, p Profile) {
	t.Helper()
	res := h.MustRun(t, "auth", "status", "-P", p.Name, "--json")
	var status struct {
		Data struct {
			ExpiresAt time.Time `json:"expires_at"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(res.Stdout), &status), res.Stdout)
	require.False(t, status.Data.ExpiresAt.IsZero(), res.Stdout)
	// expires_at is whole seconds, and the CLI compares whole seconds.
	due := time.Until(status.Data.ExpiresAt.Add(time.Second))
	require.Less(t, due, waitFor, "the token is not short-lived")
	select {
	case <-time.After(due):
	case <-t.Context().Done():
		t.Fatal(t.Context().Err())
	}
}

// await waits for the fake to come to a state, failing the test when it
// does not within waitFor.
func (h *Harness) await(t *testing.T, what string, cond func() bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), waitFor)
	defer cancel()
	require.NoError(t, h.Fake.Await(ctx, cond), "waiting for %s", what)
}

func rawLogs(o Output) []string {
	out := make([]string, len(o.Logs))
	for i, l := range o.Logs {
		out[i] = l.Raw
	}
	return out
}
