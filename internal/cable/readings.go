package cable

import (
	"context"
	"time"

	actioncable "github.com/basecamp/actioncable-go"
)

// The channels a client has to join to be told the account's notifications
// changed. Only the first carries the news; the other two are what it takes to
// hear it at all.
const (
	// UnreadsChannel is what the web sidebar subscribes to.
	UnreadsChannel = "UnreadsChannel"

	// AppearanceChannel is what makes Basecamp broadcast to a connection.
	// User::Reader only broadcasts to a user it considers online, and appearing
	// is the only thing that makes one.
	AppearanceChannel = "AppearanceChannel"

	// MonitoringChannel answers a client's ping. Basecamp beats none of its own.
	MonitoringChannel = "MonitoringChannel"
)

// Both heartbeats are the web's own cadence. An appearance lasts 30 seconds, and
// actioncable-go calls a connection stale after six without a frame, so half of
// each is what keeps them both from lapsing.
const (
	appearInterval = 15 * time.Second
	pingInterval   = 3 * time.Second
)

// Heartbeats is what a connection has to keep saying to be sent anything: that
// its user is here, and that it is still listening.
type Heartbeats struct {
	appearance *actioncable.Subscription
	monitoring *actioncable.Subscription
}

// Beat joins the two channels the heartbeats are performed on.
func Beat(ctx context.Context, client *actioncable.Client) (*Heartbeats, error) {
	appearance, err := client.Subscribe(ctx, actioncable.Identifier{Channel: AppearanceChannel})
	if err != nil {
		return nil, err
	}

	monitoring, err := client.Subscribe(ctx, actioncable.Identifier{Channel: MonitoringChannel})
	if err != nil {
		return nil, err
	}

	return &Heartbeats{appearance: appearance, monitoring: monitoring}, nil
}

// Run beats both until ctx is done, reporting what it could not send. It says
// everything once before the first tick, so a connection is live from the
// moment it is watched rather than fifteen seconds later.
func (h *Heartbeats) Run(ctx context.Context, report func(error)) {
	appearing := time.NewTicker(appearInterval)
	defer appearing.Stop()

	pinging := time.NewTicker(pingInterval)
	defer pinging.Stop()

	h.Appear(ctx, report)
	h.Ping(ctx, report)

	for {
		select {
		case <-ctx.Done():
			return
		case <-appearing.C:
			h.Appear(ctx, report)
		case <-pinging.C:
			h.Ping(ctx, report)
		}
	}
}

// Appear says the user is here.
//
// It appears on nothing: appearing on a recording tells Basecamp the reader is
// looking at it, and Readable#appearant_ids excludes those readers from its
// unread updates — the opposite of what a watcher wants.
func (h *Heartbeats) Appear(ctx context.Context, report func(error)) {
	if err := h.appearance.Perform(ctx, "appear", map[string]any{"appearing_on": []string{}}); err != nil {
		reportTo(report, err)
	}
}

// Ping asks for the pong that keeps the connection from looking dead. Nothing
// reads the pong — arriving at all is what it is for.
func (h *Heartbeats) Ping(ctx context.Context, report func(error)) {
	if err := h.monitoring.Perform(ctx, "ping", nil); err != nil {
		reportTo(report, err)
	}
}

func reportTo(report func(error), err error) {
	if report != nil {
		report(err)
	}
}

// WatchReadings opens the stream that says the account's notifications changed.
//
// The channel receives once per broadcast, and closes when the connection is
// gone for good or ctx is done. It carries nothing: the broadcast's payload is
// signed ids for the web's own diffing, so a reader re-reads rather than
// applying it. Several rings collapse into one pending receive, which suits a
// reader that debounces — Basecamp writes a reading per recording, so one thing
// happening rings several times.
//
// The connection and both heartbeats belong to the returned channel: closing
// ctx is what shuts them down.
func WatchReadings(ctx context.Context, cableURL string, tokens Tokens, options ...actioncable.Option) (<-chan struct{}, error) {
	client, err := Dial(ctx, cableURL, tokens, options...)
	if err != nil {
		return nil, err
	}

	unreads, err := client.Subscribe(ctx, actioncable.Identifier{Channel: UnreadsChannel})
	if err != nil {
		_ = client.Close()
		return nil, err
	}

	beats, err := Beat(ctx, client)
	if err != nil {
		_ = client.Close()
		return nil, err
	}

	rings := make(chan struct{}, 1)
	go func() {
		defer close(rings)
		defer func() { _ = client.Close() }()

		go beats.Run(ctx, nil)

		for {
			select {
			case <-ctx.Done():
				return
			case _, open := <-unreads.Messages():
				if !open {
					return
				}
				select {
				case rings <- struct{}{}:
				default:
				}
			}
		}
	}()

	return rings, nil
}
