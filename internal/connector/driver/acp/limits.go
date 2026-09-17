package acp

import "time"

// What bounds every buffer this driver keeps
//
// An ACP agent writes all of it: the lines it sends, the ids and paths it
// names, the options it offers, the requests it asks. None of it is the
// agent's to grow without end, so every collection and every wait this
// driver keeps is bounded here, in one place, rather than at the site that
// happens to fill it.
//
//   - Per line: maxLine caps a line read from the agent; a longer one ends
//     the session. agentText cuts the text of an error before it is
//     sanitized (rpc.go) and again after, to 120 runes.
//   - Per session: maxTools tool calls remembered, maxRecorded refusals
//     remembered as recorded, and updatesBuffer updates for a consumer that
//     has not read them, which are dropped rather than blocking it.
//   - Per turn: maxRefusals refusals kept on a result.
//   - Per tool call: maxToolCallID bytes of id and maxLocations paths.
//   - Per option list: maxOptionDepth of nesting.
//   - At once: maxHandlers agent requests being answered, maxDecisions of
//     them at the policy, maxBusy refusals waiting to be written. An agent
//     that outruns the last of these ends its session.
//   - In time: modeConfirmWait for a mode to be confirmed, decisionDrain for
//     the decisions still in flight when a turn ends, and the session's close
//     grace for every wait on the worker (Options.CloseGrace).

// maxLine is the longest line the connector reads from an agent. A session/load
// replay or a large tool result can be long; a line past this ends the session
// rather than growing without bound.
// A variable so tests need not write one.
var maxLine = 64 << 20

// maxHandlers bounds the agent requests answered at once, and maxBusy the
// refusals waiting to be written. A variable so tests need not send a
// thousand requests.
var (
	maxHandlers = 16
	maxBusy     = 256
)

// updatesBuffer is how many updates wait for a consumer that has not read
// them. An update is progress, not a record: past this the oldest are the
// ones that no longer matter, so emit drops rather than let an agent's pace
// be set by a reader's.
const updatesBuffer = 256

// maxOptionDepth bounds how deeply a select option's groups may nest: the
// agent writes that JSON, and a deep one would otherwise recurse until the
// process dies.
const maxOptionDepth = 8

// maxDecisions bounds the permission requests one session decides at once.
const maxDecisions = 8

// decisionDrain is how long a turn's end waits for permissions still being
// decided.
var decisionDrain = 2 * time.Second

// maxRefusals bounds the refusals one turn records; past it, a refusal is
// still an update. maxRecorded bounds the refusals a session remembers
// having recorded, maxTools the tool calls it remembers, maxToolCallID the
// id of one and maxLocations the paths it may name: the agent writes all of
// them, and a session's memory is not its to grow.
const (
	maxRefusals = 1024
	// Past maxRecorded a refusal is recorded again rather than remembered:
	// recording one twice is a count too high.
	maxRecorded   = 4096
	maxTools      = 1024
	maxToolCallID = 256
	maxLocations  = 64
)

// modeConfirmWait is how long a session with no mode config option has to
// report the mode it was set to. A variable so tests need not wait it out.
var modeConfirmWait = 10 * time.Second
