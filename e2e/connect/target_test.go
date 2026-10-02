//go:build linux || darwin

package connect

import "testing"

// Target is the Basecamp a scenario runs against, in the words the scenario
// uses: who serves what, and what people do there. A scenario written
// against it runs on either target unchanged, and passing on the fake and on
// a local Basecamp is what holds the fake to Basecamp's behavior.
//
// There are two. fakeTarget serves a world of its own on the fake, set up
// the way a person sets an agent up. devTarget drives a local Basecamp with
// the real CLI and the developer's own profiles; it runs only under `make
// test-connect-dev`. Scenarios that need a fault only the fake can produce
// (a dropped cable, a rate-limited token, a disconnected agent) are written
// against the fake directly, and say so with fakeOnly.
type Target interface {
	// CLI runs the CLI against the target, as the developer would: its
	// environment points the CLI at this Basecamp, and the connector's state
	// is the test's alone.
	CLI() *Harness
	// Agent is the agent set up to serve the project, as its profile.
	Agent() ServingAgent
	// OperatorMentionsAgent has the operator, whom the agent takes
	// instructions from, post a comment that mentions the agent and asks
	// it for something, on a recording in the served project. It returns
	// the comment as Basecamp holds it.
	OperatorMentionsAgent(t *testing.T) Posted
	// UntrustedMentionsAgent has someone on the served project whom the
	// agent takes no instructions from post the same.
	UntrustedMentionsAgent(t *testing.T) Posted
}

// ServingAgent is the agent a target set up, and where.
type ServingAgent struct {
	// Profile is the CLI profile that holds the agent's credential.
	Profile string
	// PersonID is the agent's Person id.
	PersonID int64
	// AccountID is the account it serves in.
	AccountID int64
}

// Posted is a comment someone posted, as Basecamp holds it: what a request
// line for it says, field by field.
type Posted struct {
	// RecordingID is the comment's id. The comment.created event for it
	// carries it as its recording_id.
	RecordingID int64
	// ParentID is the recording the comment is on, where a reply goes.
	ParentID int64
	// BucketID and ProjectName are the served project's.
	BucketID    int64
	ProjectName string
	// Type, Title and URL are the comment's own.
	Type  string
	Title string
	URL   string
	// AuthorID and AuthorName are whoever posted it.
	AuthorID   int64
	AuthorName string
	// Instruction is the comment's content with the agent's mention taken
	// out, leaving a space where it was: what a request line hands off.
	Instruction string
}

// eventType is the event a posted comment makes.
const eventType = "comment.created"

// onEachTarget runs scenario against the fake, as part of every `go test`,
// or against the local Basecamp instead when `make test-connect-dev` asks
// for it. Each target is a subtest named for it, so a failure says which
// Basecamp behaved differently.
func onEachTarget(t *testing.T, scenario func(t *testing.T, tg Target)) {
	t.Helper()
	if !devRequested() {
		t.Run("fake", func(t *testing.T) {
			t.Parallel()
			scenario(t, newFakeTarget(t))
		})
		return
	}
	t.Run("dev", func(t *testing.T) {
		// One scenario at a time against a local Basecamp: they share its
		// agent, its credential and its project.
		devSerial.Lock()
		defer devSerial.Unlock()
		scenario(t, newDevTarget(t))
	})
}

// connectAs starts `basecamp connect` for tg's agent with extra flags.
func connectAs(t *testing.T, tg Target, extra ...string) *Connector {
	t.Helper()
	h := tg.CLI()
	return h.Start(t, h.Dir, append([]string{"connect", "-P", tg.Agent().Profile}, extra...)...)
}

// streamingOn starts the connector for tg's agent and waits until an event
// published live reaches it.
func streamingOn(t *testing.T, tg Target) *Connector {
	t.Helper()
	c := connectAs(t, tg)
	c.WaitStreaming(t, 1)
	return c
}

// PointersOn are the pointer lines for the event p made.
func (o Output) PointersOn(p Posted) []PointerLine {
	var out []PointerLine
	for _, l := range o.Pointers() {
		if l.RecordingID == p.RecordingID && l.EventType == eventType {
			out = append(out, l)
		}
	}
	return out
}

// EventsOn are the verdict lines for the event p made.
func (o Output) EventsOn(p Posted) []EventLine {
	var out []EventLine
	for _, l := range o.Events() {
		if l.RecordingID == p.RecordingID && l.EventType == eventType {
			out = append(out, l)
		}
	}
	return out
}

// RequestsOn are the request lines for the event p made.
func (o Output) RequestsOn(p Posted) []RequestLine {
	var out []RequestLine
	for _, l := range o.Requests() {
		if l.Recording.RecordingID == p.RecordingID && l.EventType == eventType {
			out = append(out, l)
		}
	}
	return out
}

// RequestsAbout are the request lines for anything on the recording p is
// on: p itself, the recording, or another comment on it.
func (o Output) RequestsAbout(p Posted) []RequestLine {
	var out []RequestLine
	for _, l := range o.Requests() {
		if l.Recording.RecordingID == p.RecordingID || l.Recording.RecordingID == p.ParentID || l.ReplyTo.RecordingID == p.ParentID {
			out = append(out, l)
		}
	}
	return out
}
