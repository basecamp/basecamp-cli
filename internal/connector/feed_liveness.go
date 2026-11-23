package connector

import "time"

// A socket can stay connected and stop delivering. After a Cable failover
// the server kept answering keep-alives while most subscriptions were gone,
// and every frame re-arms the SDK's staleness window, so a ping-only socket
// is never stale. The repair poll caught everything about a minute late, for
// as long as the connector ran.
//
// What tells a deaf socket from a quiet one is the repair poll: it runs about
// thirty seconds behind the live lane, so in steady state the events it
// serves were already delivered live and it finds nothing new. When it keeps
// finding new events the socket never sent, the subscription is not
// delivering, and the connector resubscribes the way it does for a
// membership change.

const (
	// livenessWalks is how many repair walks a verdict spans: about three
	// minutes at the feed's sixty-second cadence.
	livenessWalks = 3
	// livenessMinPolled is the fewest new events the poll has to find across
	// the span, so one dropped push frame is not a dead socket.
	livenessMinPolled = 3
	// livenessMinWalks is how many of the span's walks must each have found
	// one, so one burst arriving between two polls is not either.
	livenessMinWalks = 2
	// livenessMaxBackoff caps how far the span doubles after resubscribes
	// that did not bring live delivery back (3 << 4: about 48 minutes), so a
	// Cable that is down for everyone is not redialed every three minutes.
	livenessMaxBackoff = 4
	// livenessTaintWalks is how many walks a taint discounts: the one it
	// happened in and the next. The poll serves an event about thirty
	// seconds after it happened, so what a walk could not see past its head
	// the next one finds.
	livenessTaintWalks = 2
)

// livenessBusy is how long intake may spend handing events over within one
// walk before the walk is not evidence. While intake works the feed is not
// read: the socket's frames wait in the SDK, and a poll that reaches them
// first suppresses their live copies. The poll trails by about thirty
// seconds, so a frame is overtaken only after the consumer has been held up
// for longer than that, and a third of it within one walk is the margin. A
// variable so tests can shorten it; a test that does must not run in
// parallel.
var livenessBusy = 10 * time.Second

// walkSample is what one repair walk found: new events the poll served, and
// new events the socket delivered since the walk before it.
type walkSample struct {
	polled int
	live   int
}

// liveness is the per-run state of the check. Every field is guarded by
// Intake.mu.
type liveness struct {
	// walks counts the poll walks this connection has started. The first is
	// the catch-up after the subscription, which finds what happened while
	// no socket was open and says nothing about the socket.
	walks int
	// polled and live count new events since the last sample.
	polled, live int
	// window is the most recent samples, oldest first.
	window []walkSample
	// tainted is how many more walks are no evidence, because something let
	// the poll find events the socket did deliver: intake busy for longer
	// than livenessBusy, or a buffer overflow.
	tainted int
	// held is how long intake spent on the feed's deliveries since the last
	// sample.
	held time.Duration
	// strikes counts resubscribes this check asked for that live delivery
	// has not yet answered. It and tainted outlive a connection; the rest
	// start again with each one.
	strikes int
}

// span is how many samples a verdict needs now.
func (l *liveness) span() int {
	return livenessWalks << min(l.strikes, livenessMaxBackoff)
}

// connected starts a fresh count for a new subscription.
func (l *liveness) connected() {
	l.walks, l.polled, l.live, l.window, l.held = 0, 0, 0, nil, 0
}

// note counts one new event from the feed's own lanes. A loss repair's
// events are not the feed's, and say nothing about the socket.
func (l *liveness) note(lane Lane) {
	switch lane {
	case LanePoll:
		l.polled++
	case LaneLive:
		l.live++
	case LaneRepair:
	}
}

// taint makes the current walk and the next no evidence either way.
func (l *liveness) taint() { l.tainted = livenessTaintWalks }

// busy adds time intake spent on one of the feed's deliveries.
func (l *liveness) busy(d time.Duration) { l.held += d }

// walkStarted marks a poll walk beginning.
func (l *liveness) walkStarted() { l.walks++ }

// walkDone takes a sample at the end of a walk and reports whether the socket
// should be replaced.
func (l *liveness) walkDone() bool {
	sample := walkSample{polled: l.polled, live: l.live}
	l.polled, l.live = 0, 0
	if l.held > livenessBusy {
		l.taint()
	}
	l.held = 0
	if l.walks <= 1 {
		// A catch-up straight after a connect runs within the poll's delay
		// of whatever tainted the walk before it, so it cannot have seen
		// those events yet: it spends none of the taint.
		return false
	}
	tainted := l.tainted > 0
	if tainted {
		l.tainted--
	}
	if tainted {
		// What this walk found by poll may have been waiting in the SDK
		// behind a busy intake, or dropped from its buffer: the poll
		// overtook a socket that did deliver. Start the span again.
		l.window = nil
		return false
	}
	l.window = append(l.window, sample)
	need := l.span()
	if len(l.window) > need {
		l.window = l.window[len(l.window)-need:]
	}
	if len(l.window) < need {
		return false
	}
	var polled, live, walksWithPolled int
	for _, s := range l.window {
		polled += s.polled
		live += s.live
		if s.polled > 0 {
			walksWithPolled++
		}
	}
	if polled >= livenessMinPolled && walksWithPolled >= livenessMinWalks && polled > live {
		l.strikes++
		l.window = nil
		return true
	}
	if live > 0 && live >= polled {
		// A full span in which the socket delivered at least what the poll
		// found: earlier resubscribes are answered.
		l.strikes = 0
	}
	return false
}
