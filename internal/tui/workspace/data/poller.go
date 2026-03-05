package data

// PollMsg is sent when a poll interval fires. Tag identifies which poller triggered.
// Gen is a generation counter used by views to discard ticks from superseded poll
// chains (e.g. after a terminal-focus reschedule).
type PollMsg struct {
	Tag string
	Gen uint64
}
