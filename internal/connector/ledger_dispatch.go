package connector

import (
	"context"
	"database/sql"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
)

// The dispatch lifecycle, as one state machine.
//
// Three things have state here, and a fourth is the guard on one of them.
// Since #815 removed the worker side, this build writes no task, no task event
// and no delivery: the dispatcher and the worker's basecamp_connect calls that
// did are gone. Ledgers an older build wrote still hold them, and the schema's
// triggers and index that held those writes to this contract are kept as they
// shipped, so this is still the shape of what such a ledger can contain.
// lifecycle (ledger_events.go) and move enforce the record table for every
// write this build still makes.
//
// # Record (events.state)
//
//	from        to          who                                      rule
//	seen        admitted    admission                                verdict, conversation not live
//	seen        queued      admission                                verdict, conversation live
//	seen        blocked     admission                                verdict
//	seen        discarded   admission, operator                      verdict, or import
//	blocked     admitted    admission                                re-decided
//	blocked     queued      admission                                re-decided, conversation live
//	blocked     blocked     admission                                re-decided, still blocked
//	blocked     discarded   admission, operator                      verdict, or discard
//	blocked     dispatched  nobody                                   an edge the lifecycle map has and no
//	                                                                 caller takes
//	admitted    dispatched  an older build's dispatcher              joined a task
//	queued      dispatched  an older build's dispatcher              joined a task
//	dispatched  dispatched  an older build's dispatcher              redispatched onto a new task
//	dispatched  admitted    an older build's dispatcher              never handed to a worker, task retired,
//	                                                                 or exposed at launch with a spawn
//	                                                                 proven failed: its one retry
//	dispatched  blocked     an older build's dispatcher              exposed at launch, spawn failed again
//	dispatched  completed   an older build's worker or dispatcher    the outcome, reported or settled
//	admitted    queued      lifecycle bookkeeping                    -
//	admitted    blocked     nobody                                   an edge the lifecycle map allows and no
//	                                                                 caller takes
//	admitted    discarded   handoff, operator                        handed to the session, or import
//	queued      blocked     nobody                                   an edge the lifecycle map allows and no
//	                                                                 caller takes
//	queued      discarded   handoff, operator                        handed to the session, or import
//	completed   -           nobody                                   terminal
//	discarded   -           nobody                                   terminal
//
// Writing the state a record already has is a repeat and always allowed. Any
// pair not in the table is refused. Into dispatched and out of it, the task
// decides, as a rule about the moves rather than about the state: a record
// enters dispatched only when a live task already carries it, and leaves it,
// other than to completed, only when none does
// (events_dispatched_while_on_a_live_task, and move). A record can therefore
// be dispatched with no live task, and one case is expected: work a worker was
// handed, whose task was superseded, waiting for its outcome (invariant 7).
//
// # Task (tasks)
//
//	live        created by an older build's dispatcher; its token was valid
//	superseded  by that dispatcher or a person's redispatch; terminal, and
//	            the row is never deleted
//
// A redispatch superseded the live task, which refused its token, retired its
// rows and returned what no worker was handed to admitted, and created a task
// for the events being run again, in one transaction.
//
// # Delivery (task_events.delivery), per event on a task
//
//	admitted → exposed    the worker's get_dispatch, or the dispatcher at launch
//	exposed  → delivered  the worker's ack_dispatch; the id the worker pointed
//	                      at, when it had one, was written with this move or
//	                      never (task_events_acknowledgement_settles_once)
//	exposed  → completed  the worker's complete_dispatch, or the dispatcher's
//	                      settlement of a worker gone before it acknowledged
//	delivered → completed the same two
//	exposed  → withdrawn  the dispatcher, for a spawn that failed before any
//	                      worker process existed: an exposure written at
//	                      launch that no worker pulled (pulled_at), on a
//	                      superseded task, with no live task carrying the
//	                      event; once, and the row moves no more
//
// Forward only, and never skipping exposure: nothing a worker was never
// handed was acknowledged or completed. A row is retired (retired_at) when
// its task is superseded.
//
// # Guard (task_events.guard)
//
//	armed → canceled  the worker's get_dispatch
//	armed → fired     the connector's thirty-second acknowledgement
//
// '' (none) and armed were only ever written when the row was created.
//
// # Invariants
//
//  1. One live task per event: task_events_one_live_task.
//  2. One task per conversation, and in the conversation's order
//     (task_events_one_live_task_per_conversation). The conversation a task
//     holds is written on the task's own rows, because retention clears a
//     terminal record's conversation_key while its task is still live.
//  3. A task's token was valid only while the task was live.
//  4. Nothing leaves dispatched while a worker may still act: a record with a
//     delivery exposed or delivered, on any task and not withdrawn, leaves
//     dispatched only to completed (move, and the
//     events_handed_work_settles_first trigger). This build still holds every
//     write of its own to it. The one release was the automatic retry of a
//     spawn that failed before any worker process existed.
//  5. A worker acted only on its own task's rows, reported only what it
//     pulled, and a reported outcome stands; the delivery triggers hold the
//     same shape for anything else writing to the file.
//  6. Finished work was never handed out for the first time: a completed
//     record was served, acknowledged and completed only by the worker that
//     pulled it (pulled_at).
//  7. Superseding retired the task's rows and returned only what it never
//     exposed to admitted; what a worker was handed stays dispatched (4) and
//     waits for its outcome.
//  8. Completed and discarded are terminal (events_terminal_is_terminal).
//
// Every transaction takes the write lock as it opens (_txlock=immediate), and
// each call ends it as soon as its ledger work is done.

// Outcome is what a worker reports for an event.
type Outcome string

const (
	OutcomeSucceeded Outcome = "succeeded"
	OutcomeFailed    Outcome = "failed"
)

func loadRecord(ctx context.Context, tx *sql.Tx, id int64) (Record, error) {
	rows, err := tx.QueryContext(ctx, selectRecords+` WHERE id = ?`, id)
	if err != nil {
		return Record{}, fmt.Errorf("connector: load event %d: %w", id, err)
	}
	records, err := scanRecords(rows)
	if err != nil {
		return Record{}, err
	}
	if len(records) == 0 {
		return Record{}, fmt.Errorf("connector: event %d: %w", id, ErrNoSuchRecord)
	}
	return records[0], nil
}

// StripMentionsOf removes every mention of personID from rich text, and
// leaves the rest as it was. A worker handed its own mention reads an
// instruction addressed to itself, which says nothing the dispatch does not
// already say.
//
// What counts as a mention is exactly what basecamp.MentionedPersonIDs — the
// reader admission decided the trigger with — counts: the tag walk below is
// that reader's, rule for rule (comments, "<!", "<?" and end tags skipped,
// punctuation part of a name, the first sgid attribute authoritative even when
// empty, entities decoded, an unterminated tag ending the markup). The two
// are held to agreement by a differential test and a fuzz target over hostile
// markup, not by review.
//
// A mention element runs from its start tag to the first end tag of the same
// name, unless it closes itself, another attachment starts first, or none
// closes, in which case the start tag stands alone. So no removal can swallow
// the instruction between a self-closing mention and some later stray closing
// tag. What it leaves behind is a space, not nothing:
// closing the gap could join a "<" before the element to the text after it
// into a tag that swallows what follows — someone else's mention included —
// and a space can never begin one.
func StripMentionsOf(richText string, personID int64) string {
	if !slices.Contains(basecamp.MentionedPersonIDs(richText), personID) {
		return richText
	}
	out, _ := stripOnce(richText, personID)
	if slices.Contains(basecamp.MentionedPersonIDs(out), personID) {
		// The walk is the reader's and a removal cannot join what is around
		// it, so one pass removes every mention the reader reads: this is a
		// backstop, and no corpus or fuzz input has reached it. Handing the
		// text out would put the agent's own mention in front of the worker,
		// and handing out nothing would lose the instruction; the escaped
		// text keeps the words and no markup.
		return html.EscapeString(out)
	}
	return out
}

// strippedMention is what a removed mention leaves in the text.
const strippedMention = " "

// stripOnce removes each mention element of personID the walk finds, and
// returns the text and the removed spans, as offsets into text.
func stripOnce(text string, personID int64) (string, [][2]int) {
	var (
		out     strings.Builder
		removed [][2]int
	)
	pos := 0
	for pos < len(text) {
		t, ok := nextMarkup(text, pos)
		if !ok {
			break
		}
		if !t.isEnd && strings.EqualFold(t.name, "bc-attachment") {
			if id, isPerson := basecamp.PersonIDFromSGID(t.sgid); isPerson && id == personID {
				out.WriteString(text[pos:t.start])
				out.WriteString(strippedMention)
				if strings.HasSuffix(text[t.start:t.end], "/>") {
					// A self-closing tag is the whole element: what follows
					// is not its content, and a later stray closing tag is
					// not its end.
					pos = t.end
				} else {
					pos = mentionEnd(text, t.end)
				}
				removed = append(removed, [2]int{t.start, pos})
				continue
			}
		}
		out.WriteString(text[pos:t.end])
		pos = t.end
	}
	out.WriteString(text[pos:])
	return out.String(), removed
}

// mentionEnd is where the mention whose start tag ends at from ends: after the
// first </bc-attachment>, or at from when another attachment starts first,
// some other element closes first, or none closes.
func mentionEnd(text string, from int) int {
	var open []string
	for at := from; ; {
		t, ok := nextMarkup(text, at)
		if !ok {
			return from
		}
		switch {
		case strings.EqualFold(t.name, "bc-attachment"):
			if t.isEnd {
				return t.end
			}
			// Another attachment starts: this one was never closed.
			return from
		case t.isEnd:
			// An end tag for something opened inside the mention — a
			// mention's own figure closes its parts — is part of it. One that
			// closes nothing opened here belongs to an element around the
			// mention, so the closing tag further on is not this mention's:
			// the start tag stands alone rather than swallowing what follows.
			depth := len(open) - 1
			for depth >= 0 && !strings.EqualFold(open[depth], t.name) {
				depth--
			}
			if depth < 0 {
				return from
			}
			open = open[:depth]
		case !isVoidElement(t.name):
			open = append(open, t.name)
		}
		at = t.end
	}
}

// isVoidElement reports the elements of Basecamp's rich text that have no end
// tag, so an unclosed one of them does not look like something still open.
func isVoidElement(name string) bool {
	switch strings.ToLower(name) {
	case "br", "hr", "img", "source", "input", "meta", "link":
		return true
	}
	return false
}

// markup is one start or end tag the walk found: where it starts and ends,
// its name, whether it is an end tag, and a start tag's first sgid, decoded.
type markup struct {
	start, end int
	name       string
	isEnd      bool
	sgid       string
}

// nextMarkup returns the next start or end tag at or after pos, walking the
// text as basecamp.MentionedPersonIDs does. ok is false when the markup ends:
// no "<" left, or a comment, declaration or tag left unterminated, after
// which nothing is markup.
func nextMarkup(text string, pos int) (markup, bool) {
	for pos < len(text) {
		i := strings.IndexByte(text[pos:], '<')
		if i < 0 {
			return markup{}, false
		}
		start := pos + i
		pos = start + 1
		rest := text[pos:]
		switch {
		case strings.HasPrefix(rest, "!--"):
			stop := strings.Index(rest, "-->")
			if stop < 0 {
				return markup{}, false
			}
			pos += stop + 3
			continue
		case strings.HasPrefix(rest, "/"):
			stop := strings.IndexByte(rest, '>')
			if stop < 0 {
				return markup{}, false
			}
			nameEnd := 1
			for nameEnd < len(rest) && isMarkupNameChar(rest[nameEnd]) {
				nameEnd++
			}
			return markup{start: start, end: pos + stop + 1, name: rest[1:nameEnd], isEnd: true}, true
		case strings.HasPrefix(rest, "!"), strings.HasPrefix(rest, "?"):
			stop := strings.IndexByte(rest, '>')
			if stop < 0 {
				return markup{}, false
			}
			pos += stop + 1
			continue
		}
		nameEnd := 0
		for nameEnd < len(rest) && isMarkupNameChar(rest[nameEnd]) {
			nameEnd++
		}
		if nameEnd == 0 {
			continue // a bare "<" in text
		}
		sgid, end, ok := scanAttributes(text, pos+nameEnd)
		if !ok {
			return markup{}, false
		}
		return markup{start: start, end: end, name: rest[:nameEnd], sgid: sgid}, true
	}
	return markup{}, false
}

// isMarkupNameChar is what may follow "<" in a tag name: everything but space,
// "/", ">", "<", "=" and quotes, so "<bc-attachment.x" is its own name.
func isMarkupNameChar(c byte) bool {
	return !isMarkupSpace(c) && c != '/' && c != '>' && c != '<' && c != '=' && c != '"' && c != '\''
}

func isMarkupSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f'
}

// scanAttributes walks a start tag's attributes from pos to its ">", and
// returns the first sgid attribute's decoded value (empty when absent or
// empty), the index after the ">", and whether the tag closed.
func scanAttributes(text string, pos int) (sgid string, end int, ok bool) {
	seen := false
	for pos < len(text) {
		for pos < len(text) && (isMarkupSpace(text[pos]) || text[pos] == '/') {
			pos++
		}
		if pos >= len(text) {
			return sgid, pos, false
		}
		if text[pos] == '>' {
			return sgid, pos + 1, true
		}
		nameStart := pos
		for pos < len(text) && !isMarkupSpace(text[pos]) && text[pos] != '=' && text[pos] != '>' && text[pos] != '/' {
			pos++
		}
		name := text[nameStart:pos]
		for pos < len(text) && isMarkupSpace(text[pos]) {
			pos++
		}
		value := ""
		if pos < len(text) && text[pos] == '=' {
			pos++
			for pos < len(text) && isMarkupSpace(text[pos]) {
				pos++
			}
			if pos < len(text) && (text[pos] == '"' || text[pos] == '\'') {
				quote := text[pos]
				pos++
				closing := strings.IndexByte(text[pos:], quote)
				if closing < 0 {
					return sgid, len(text), false
				}
				value = text[pos : pos+closing]
				pos += closing + 1
			} else {
				valueStart := pos
				for pos < len(text) && !isMarkupSpace(text[pos]) && text[pos] != '>' {
					pos++
				}
				value = text[valueStart:pos]
			}
		}
		if name == "" {
			pos++
			continue
		}
		if !seen && strings.EqualFold(name, "sgid") {
			seen = true
			sgid = html.UnescapeString(value)
		}
	}
	return sgid, pos, false
}

// StateDirName is the connector's state directory for one account and agent,
// "<account>-<agent person id>", inside StateRoot.
func StateDirName(accountID string, agentID int64) string {
	return accountID + "-" + strconv.FormatInt(agentID, 10)
}

// StateRoot is where every connector state directory lives:
// $XDG_STATE_HOME/basecamp/connect, or ~/.local/state/basecamp/connect when
// XDG_STATE_HOME is unset or not absolute, as the XDG specification says.
func StateRoot() (string, error) {
	base := os.Getenv("XDG_STATE_HOME")
	if !filepath.IsAbs(base) {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return "", fmt.Errorf("connector: no state home: %w", err)
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(filepath.Clean(base), "basecamp", "connect"), nil
}

// LedgerFile is the ledger's file name inside the state directory.
const LedgerFile = "ledger.db"
