//go:build linux || darwin

package connect

import (
	"strconv"
	"strings"
	"testing"

	"github.com/basecamp/basecamp-cli/internal/connector/fakebasecamp"
)

// fakeTarget is the fake Basecamp as a Target: the default world with a
// bystander on the served project, in a home of the test's own, with the
// agent connected and set up through the CLI. A comment is put in the world
// and its event published live, as Basecamp publishes it first.
type fakeTarget struct {
	h *Harness
}

func newFakeTarget(t *testing.T) *fakeTarget {
	t.Helper()
	h := NewHarness(t, world())
	h.Setup(t)
	return &fakeTarget{h: h}
}

func (f *fakeTarget) CLI() *Harness { return f.h }

func (f *fakeTarget) Agent() ServingAgent {
	return ServingAgent{Profile: Agent, PersonID: fakebasecamp.AgentID, AccountID: fakebasecamp.AccountID}
}

func (f *fakeTarget) OperatorMentionsAgent(t *testing.T) Posted {
	t.Helper()
	return f.post(fakebasecamp.OperatorID, fakebasecamp.OperatorName)
}

func (f *fakeTarget) UntrustedMentionsAgent(t *testing.T) Posted {
	t.Helper()
	return f.post(BystanderID, BystanderName)
}

func (f *fakeTarget) post(author int64, name string) Posted {
	ev := comment(f.h, author, askAgent(), fakebasecamp.Live)
	return Posted{
		RecordingID: ev.RecordingID,
		ParentID:    kickoffID,
		BucketID:    fakebasecamp.ProjectID,
		ProjectName: fakebasecamp.ProjectName,
		Type:        "Comment",
		URL:         f.h.Fake.URL() + "/" + strconv.FormatInt(fakebasecamp.AccountID, 10) + "/comments/" + strconv.FormatInt(ev.RecordingID, 10),
		AuthorID:    author,
		AuthorName:  name,
		Instruction: strings.Replace(askAgent(), fakebasecamp.Mention(fakebasecamp.AgentID), " ", 1),
	}
}

// fakeOf is tg's fake Basecamp, for an assertion only the fake can make
// (every request it answered); false against a real Basecamp.
func fakeOf(tg Target) (*fakebasecamp.Server, bool) {
	f, ok := tg.(*fakeTarget)
	if !ok {
		return nil, false
	}
	return f.h.Fake, true
}
