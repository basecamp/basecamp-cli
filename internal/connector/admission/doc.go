// Package admission turns a seen account-feed event into a verdict: admitted,
// queued, blocked or discarded. It is step 15 of the connector plan, and it
// sits between intake (which writes a pointer to the ledger and hands its id
// to a queue) and dispatch (which starts a worker for an admitted record).
//
// # Order of work, cheapest first
//
//  1. The gate (Gate) is pure code over the pointer and the local policy: the
//     event type is in the trigger matrix, the performer is trusted for at
//     least one of the type's triggers, the bucket is in scope, and a trigger
//     that would be discarded without a route has one. Most account traffic
//     ends here, for the price of its pointer.
//  2. In project trust mode a performer other than the operator is confirmed
//     as a non-client member of the project. That is a read, cached per
//     project, and it is the one trust decision the pointer alone cannot make.
//  3. The RecordingSummary read (the SDK's step-10 helper) resolves the
//     recording through the typed read its event type names. A recording
//     still drafted or since trashed is stale.
//  4. The trigger rules decide what the event is to the agent: mentioned (the
//     agent's Person id is among the mention attachments — never a name),
//     subscribed (a subscription read on the commented recording), assigned
//     (the recording's events, at most five pages, for details.added_person_ids)
//     or completed (trusted completer and a stake: the watch_completions route
//     flag, an assignment, or a subscription).
//  5. The route, class, reply destination, conversation key and content
//     snapshot come from the policy and the summary, never from the pointer.
//
// # Trust
//
// Trust is keyed on Person id. The agent's own id never authorizes: an event
// the agent created, an event the agent performed, an event carrying an agent
// actor type, and an event performed on someone's behalf by any agent are all
// discarded before anything else is considered, so an agent mentioning an
// agent stops at zero hops. Assignments are operator-only in every trust
// mode. For the triggers that carry an instruction in content (mentioned,
// subscribed) the recording's author must be trusted too, because the
// performer of a *.created event is not always the person who wrote it — a
// to-do moved in from another project is created by the mover.
//
// # The seam with intake
//
// Intake (basecamp-cli PR 729, internal/connector) hands admission an event id
// through its Queue; the record behind that id is the ledger row, whose
// pointer fields are exactly Event's. Run is written against two small
// interfaces, IDSource (satisfied by that Queue's Take) and Records (a ledger
// read returning a seen record as an Event), plus Ledger for the commit. The
// adapter from intake's ledger to those interfaces lands once 729 merges; this
// package imports nothing from it.
//
// # What is not here
//
// Tasks, attempts, the outbox, acknowledgements, and the holding reply a
// blocked(no_route) record receives are dispatch and lifecycle (plan steps 17
// to 20). The boosted and queued triggers are step 25 and edited is step 28:
// each is a row added to the Matrix plus its rule, and the gate does not
// change.
package admission
