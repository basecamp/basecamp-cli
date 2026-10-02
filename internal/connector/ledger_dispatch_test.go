package connector

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const otherPersonID int64 = 1001

// Invariant 4: nothing leaves dispatched while a worker may still act on it.
// A record an older build handed to a worker cannot be withdrawn, blocked or
// requeued, not through the ledger's write and not around it, until its
// outcome is in, even after its task was superseded.
func TestAHandedRecordLeavesDispatchedOnlyWhenCompleted(t *testing.T) {
	l := newTestLedger(t)
	ctx := context.Background()
	opAdmit(t, l, 1, "recording:10304028989")
	task := olderLaunch(t, l, 1)
	olderSupersede(t, l, task.TaskID)
	require.Equal(t, StateDispatched, getRecord(t, l, 1).State, "superseding does not release what a worker holds")

	for _, to := range []struct {
		state  RecordState
		reason string
	}{{StateAdmitted, ""}, {StateBlocked, "read_failed"}} {
		err := l.SetState(ctx, 1, to.state, to.reason)
		require.ErrorIs(t, err, ErrHeldByWorker, "to %s", to.state)
		_, err = l.db.ExecContext(ctx, `UPDATE events SET state = ? WHERE id = 1`, string(to.state))
		require.Error(t, err, "the database refuses it too")
	}
	assert.Equal(t, StateDispatched, getRecord(t, l, 1).State)

	require.NoError(t, l.SetState(ctx, 1, StateCompleted, ""))
}

func TestStripMentionsOf(t *testing.T) {
	agent := mentionMarkup(adapterAgentID)
	other := mentionMarkup(otherPersonID)
	withFigure := strings.Replace(agent, "></bc-attachment>", `><figure><img src="a.png"><figcaption>Agent</figcaption></figure></bc-attachment>`, 1)
	file := `<bc-attachment sgid="BAh7notaperson" content-type="application/pdf" filename="a.pdf"></bc-attachment>`
	selfClosing := strings.Replace(agent, "></bc-attachment>", " />", 1)
	unclosed := strings.Replace(agent, "</bc-attachment>", "", 1)

	// A removed mention leaves a space, so what was around it cannot join.
	for name, tc := range map[string]struct{ in, want string }{
		"the agent's mention":            {"<div>" + agent + " do it</div>", "<div>  do it</div>"},
		"with its figure":                {"<div>" + withFigure + " do it</div>", "<div>  do it</div>"},
		"another person's mention stays": {"<div>" + other + " and " + agent + "</div>", "<div>" + other + " and  </div>"},
		"a file stays":                   {file + agent, file + " "},
		"self-closing":                   {"a" + selfClosing + "b", "a b"},
		"self-closing, before another":   {selfClosing + other, " " + other},
		"unclosed, before another":       {unclosed + " x " + other, "  x " + other},
		"every occurrence":               {agent + " and " + agent, "  and  "},
		"no attachments":                 {"<div>plain</div>", "<div>plain</div>"},
		"single-quoted sgid":             {"a" + strings.ReplaceAll(agent, `"`, "'") + "b", "a b"},
		"a > inside another attribute":   {"a" + strings.Replace(agent, "<bc-attachment ", `<bc-attachment caption="x > y" `, 1) + "b", "a b"},
		"an entity in the sgid":          {"a" + entityEncodedSGID(agent) + "b", "a b"},
		"inside a comment it is text":    {"<!-- " + agent + " -->" + other, "<!-- " + agent + " -->" + other},
		"uppercase":                      {"a" + strings.ToUpper(agent[:14]) + agent[14:] + "b", "a b"},
		"the first sgid is the one":      {strings.Replace(agent, "<bc-attachment ", `<bc-attachment sgid="" `, 1), strings.Replace(agent, "<bc-attachment ", `<bc-attachment sgid="" `, 1)},
		"a stray < before it":            {"<" + agent + "hi " + other, "< hi " + other},
		"self-closing, then a stray close": {selfClosing + " please deploy</p><p>thanks</p></bc-attachment> tail",
			" " + " please deploy</p><p>thanks</p></bc-attachment> tail"},
		"unclosed, then a stray close": {unclosed + " please deploy</p><p>thanks</p></bc-attachment> tail",
			" " + " please deploy</p><p>thanks</p></bc-attachment> tail"},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, StripMentionsOf(tc.in, adapterAgentID))
		})
	}
}

// entityEncodedSGID writes the mention's sgid with its first character as a
// hex entity, the way a serializer may.
func entityEncodedSGID(mention string) string {
	i := strings.Index(mention, `sgid="`) + len(`sgid="`)
	return mention[:i] + fmt.Sprintf("&#x%x;", mention[i]) + mention[i+1:]
}
