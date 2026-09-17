// Package connector is the intake half of `basecamp connect`: the account
// event feed, the durable ledger behind it, and the recovery that makes a
// crash a delay rather than a loss.
//
// # What intake is, and what it deliberately is not
//
// Feed rows are pointers — id, type, bucket, creator, recording — and nothing
// else. No title, no body, no URL, no names. Intake writes that pointer to the
// ledger if the id is new and hands the id to a queue. That is the whole of
// the work on the feed's delivery path: a slow admission or a busy dispatcher
// is absorbed by the backlog rather than felt by the socket.
//
// Absorbed, not unbounded. The backlog is a bounded queue, and at its pause
// threshold an offer waits for room — which is intake deliberately stopping
// its read of the feed, so the backlog cannot grow until it is the process's
// memory that fails. The decoupling holds up to that depth; past it,
// backpressure is the design, and the pause is reported rather than hidden.
//
// Deciding whether an event deserves an agent's attention is admission's job
// and it costs a read per event it cares about. Intake does none of it. It
// also does no filtering of its own: the feed carries no `column_id` or
// `todolist_id`, so narrowing to a column or a list means re-fetching the
// parent listing, and that price belongs where the read already happens.
//
// # The two lanes
//
// The live lane is a WebSocket; the poll lane is an HTTP walk that runs about
// thirty seconds behind it, deliberately, so a page never stops mid-commit.
// The same event therefore arrives twice in the ordinary case, and dedupe is
// by event id across both lanes and across restarts — which is what makes the
// ledger, not a set in memory, the dedupe authority.
//
// Only the poll lane advances the durable position. A live event id is far
// ahead of the poll lane, and a checkpoint taken from one would skip
// everything the safety delay had not yet served.
//
// # Quiet is not caught up
//
// Three separate things in this feed look like "nothing more to read" and are
// not:
//
//   - An empty page. A request crosses up to a thousand ledger rows and serves
//     at most a hundred matches; rows the filters exclude still advance the
//     cursor. Follow `next` until it is absent, never stop at the first empty
//     page.
//   - A missing `next`. The walk reached its frozen head, which is not the end
//     of history: a page cut short by the safety horizon withholds the link on
//     purpose. Poll again later.
//   - A silent inbox. Delivery has write-time brakes — a per-account rate
//     ceiling and an agent-to-agent chain breaker — and a braked event writes
//     no addressing and tells the client nothing. A quiet feed is not proof of
//     a quiet project, so nothing here ever reports "up to date".
package connector
