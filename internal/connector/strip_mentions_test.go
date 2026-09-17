package connector

import (
	"slices"
	"strings"
	"testing"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
)

// StripMentionsOf is held to agreement with the reader admission decides the
// trigger with, basecamp.MentionedPersonIDs, over markup built to make two
// parsers disagree. Four properties, for every input:
//
//  1. no mention of the agent survives;
//  2. every other person the reader found is still found, in order;
//  3. text with no mention of the agent comes back unchanged;
//  4. every removed span is a mention element and nothing more — it closes
//     nothing it did not open, so a removal takes a mention and never the
//     instruction around it. This is checked against the span's own text
//     rather than against what the function meant to remove, so a span that
//     is too long cannot hide itself.
//
// The pieces are combined in threes, so each hostile form meets each other in
// both orders and inside or around an element.

func mentionPieces() []string {
	agent := mentionMarkup(adapterAgentID)
	other := mentionMarkup(otherPersonID)
	sgidOf := func(m string) string {
		i := strings.Index(m, `sgid="`) + len(`sgid="`)
		return m[i : i+strings.Index(m[i:], `"`)]
	}
	a, o := sgidOf(agent), sgidOf(other)
	return []string{
		agent,
		other,
		"plain text",
		`<bc-attachment sgid="BAh7notaperson" content-type="application/pdf"></bc-attachment>`,
		`<bc-attachment sgid='` + a + `'></bc-attachment>`,
		`<bc-attachment sgid=` + a + `></bc-attachment>`,
		`<BC-ATTACHMENT SGID="` + a + `"></BC-ATTACHMENT>`,
		`<bc-attachment sgid="&#x` + strings.ToLower(string("0123456789abcdef"[a[0]>>4])+string("0123456789abcdef"[a[0]&15])) + `;` + a[1:] + `"></bc-attachment>`,
		`<bc-attachment sgid="" sgid="` + a + `"></bc-attachment>`,
		`<bc-attachment data.sgid="` + a + `"></bc-attachment>`,
		`<bc-attachment.x sgid="` + a + `"></bc-attachment.x>`,
		`<bc-attachment caption="a > b" sgid="` + a + `"></bc-attachment>`,
		`<bc-attachment caption='<bc-attachment sgid="` + a + `">' sgid="` + o + `"></bc-attachment>`,
		`<bc-attachment sgid="` + a + `" />`,
		`<bc-attachment sgid="` + a + `">`,
		`</bc-attachment>`,
		`<!-- ` + agent + ` -->`,
		`<!-->`,
		`<!--`,
		`<?pi ` + agent + `?>`,
		`<!DOCTYPE x>`,
		`<figure><img src="a.png"></figure>`,
		`< bc-attachment sgid="` + a + `">`,
		`a < b`,
		`<bc-attachment sgid="` + a + `"><bc-attachment sgid="` + o + `"></bc-attachment></bc-attachment>`,
		`<bc-attachment sgid="`,
		`"`,
		`<div title="`,
		`<`,
		`<p `,
		`>`,
		`/`,
		`<b`,
		`bc-attachment sgid="` + a + `">`,
	}
}

func checkStrip(t *testing.T, input string) {
	t.Helper()
	before := basecamp.MentionedPersonIDs(input)
	got := StripMentionsOf(input, adapterAgentID)
	after := basecamp.MentionedPersonIDs(got)

	// Necessity: every span removed was a mention of the agent as the reader
	// sees it in place. Put any one back and the reader finds the agent again;
	// a span the reader did not read as the agent's mention — a lookalike, or
	// markup inside an attribute or after an unterminated tag — fails here.
	if slices.Contains(before, adapterAgentID) {
		out, removed := stripOnce(input, adapterAgentID)
		for _, span := range removed {
			restored := restoreSpan(input, removed, span)
			if !slices.Contains(basecamp.MentionedPersonIDs(restored), adapterAgentID) {
				t.Fatalf("removed %q, which the reader does not read as the agent's mention there\n in: %q\nout: %q", input[span[0]:span[1]], input, out)
			}
		}
	}

	_, removed := stripOnce(input, adapterAgentID)
	for _, span := range removed {
		if element := input[span[0]:span[1]]; !isOneMentionElement(element) {
			t.Fatalf("a removal took more than one mention element: %q\n in: %q\nout: %q", element, input, got)
		}
	}
	if slices.Contains(after, adapterAgentID) {
		t.Fatalf("the agent's mention survived\n in: %q\nout: %q", input, got)
	}
	if !slices.Contains(before, adapterAgentID) {
		if got != input {
			t.Fatalf("text without the agent's mention changed\n in: %q\nout: %q", input, got)
		}
		return
	}
	if isEscapeFallback(input, got) {
		return
	}
	want := slices.DeleteFunc(slices.Clone(before), func(id int64) bool { return id == adapterAgentID })
	if !slices.Equal(want, after) {
		t.Fatalf("another person's mention was lost or reordered: want %v, got %v\n in: %q\nout: %q", want, after, input, got)
	}
}

// isEscapeFallback reports output that is some pass of the strip escaped: the
// last resort for markup no number of passes cleans.
func isEscapeFallback(input, got string) bool {
	return strings.Contains(got, "&lt;") && !strings.Contains(input, "&lt;")
}

func TestStripMentionsOfAgreesWithTheReader(t *testing.T) {
	pieces := mentionPieces()
	fallbacks := 0
	for _, x := range pieces {
		for _, y := range pieces {
			for _, z := range pieces {
				input := x + y + z
				checkStrip(t, input)
				if isEscapeFallback(input, StripMentionsOf(input, adapterAgentID)) {
					fallbacks++
				}
			}
		}
	}
	if fallbacks > 0 {
		t.Fatalf("%d inputs needed the escape fallback; real markup must strip cleanly", fallbacks)
	}
}

func FuzzStripMentionsOf(f *testing.F) {
	pieces := mentionPieces()
	for _, x := range pieces {
		for _, y := range pieces {
			f.Add(x + y)
		}
	}
	f.Fuzz(func(t *testing.T, input string) {
		checkStrip(t, input)
	})
}

// restoreSpan is the stripped text with one removed span put back.
func restoreSpan(input string, removed [][2]int, keep [2]int) string {
	var b strings.Builder
	pos := 0
	for _, span := range removed {
		b.WriteString(input[pos:span[0]])
		if span == keep {
			b.WriteString(input[span[0]:span[1]])
		} else {
			b.WriteString(strippedMention)
		}
		pos = span[1]
	}
	b.WriteString(input[pos:])
	return b.String()
}

// isOneMentionElement reports a removed span that is one bc-attachment
// element: it opens with that tag, and every end tag inside it either closes
// something the span itself opened or is the element's own closing tag. A span that
// ran past its element and swallowed a "</p>" from the text around it fails
// here.
func isOneMentionElement(element string) bool {
	first, ok := nextMarkup(element, 0)
	if !ok || first.isEnd || !strings.EqualFold(first.name, "bc-attachment") || first.start != 0 {
		return false
	}
	var open []string
	for at := first.end; at < len(element); {
		t, ok := nextMarkup(element, at)
		if !ok {
			break
		}
		switch {
		case t.isEnd && strings.EqualFold(t.name, "bc-attachment"):
			// Its own closing tag ends it, whatever it left open inside.
			return t.end == len(element)
		case t.isEnd:
			depth := len(open) - 1
			for depth >= 0 && !strings.EqualFold(open[depth], t.name) {
				depth--
			}
			if depth < 0 {
				return false // it closed something it did not open
			}
			open = open[:depth]
		case !isVoidElement(t.name):
			open = append(open, t.name)
		}
		at = t.end
	}
	return true // the start tag stands alone
}
