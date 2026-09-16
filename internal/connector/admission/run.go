package admission

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
)

// DefaultWorkers is the admission fetcher pool size.
const DefaultWorkers = 4

// IDSource hands over the next event id to admit. Intake's Queue (basecamp-cli
// PR 729, internal/connector) satisfies it.
type IDSource interface {
	Take(ctx context.Context) (int64, error)
}

// Records loads a record for admission. ok is false when the id is unknown or
// the record is no longer seen — already decided by an earlier run — so it is
// skipped rather than decided twice.
//
// This is the seam where intake hands admission an event: the adapter over
// intake's Ledger.Get lands once PR 729 merges.
type Records interface {
	LoadSeen(ctx context.Context, id int64) (ev Event, ok bool, err error)
}

// RunOptions configures the admission loop.
type RunOptions struct {
	Source    IDSource
	Records   Records
	Admitter  *Admitter
	Committer *Committer
	// Workers is the fetcher pool size; DefaultWorkers when zero.
	Workers int
	// Lines receives one NDJSON line per committed verdict.
	Lines  io.Writer
	Logger *slog.Logger
}

// Run takes ids until ctx ends, deciding and committing each. It returns nil
// when ctx ends, and the first error that is not ctx's otherwise: a ledger
// that cannot load or commit is not something to skip past.
func Run(ctx context.Context, opts RunOptions) error {
	if opts.Source == nil || opts.Records == nil || opts.Admitter == nil || opts.Committer == nil {
		return errors.New("admission: run needs a source, records, an admitter and a committer")
	}
	workers := opts.Workers
	if workers <= 0 {
		workers = DefaultWorkers
	}
	log := opts.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	lines := &lineWriter{w: opts.Lines}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		wg       sync.WaitGroup
		errOnce  sync.Once
		firstErr error
	)
	fail := func(err error) {
		errOnce.Do(func() {
			firstErr = err
			cancel()
		})
	}
	for range workers {
		wg.Go(func() {
			for {
				id, err := opts.Source.Take(ctx)
				if err != nil {
					if ctx.Err() == nil {
						fail(err)
					}
					return
				}
				if err := admitOne(ctx, opts, lines, log, id); err != nil {
					if ctx.Err() == nil {
						fail(err)
					}
					return
				}
			}
		})
	}
	wg.Wait()
	return firstErr
}

func admitOne(ctx context.Context, opts RunOptions, lines *lineWriter, log *slog.Logger, id int64) error {
	ev, ok, err := opts.Records.LoadSeen(ctx, id)
	if err != nil {
		return fmt.Errorf("admission: load event %d: %w", id, err)
	}
	if !ok {
		return nil
	}
	v, err := opts.Admitter.Decide(ctx, ev)
	if err != nil {
		return err
	}
	v, err = opts.Committer.Commit(ctx, v)
	if errors.Is(err, ErrAlreadyDecided) {
		// Another fetch, or an earlier run, decided it first. Its verdict
		// stands and was reported when it was written.
		log.Debug("admission verdict already written", "event_id", id)
		return nil
	}
	if err != nil {
		return fmt.Errorf("admission: commit event %d: %w", id, err)
	}
	log.Debug("admission verdict", "event_id", v.EventID, "state", v.State, "reason", v.Reason, "trigger", v.Trigger)
	return lines.write(v)
}

// Line is the one line admission writes to stdout per verdict. It names the
// event, the decision and where it points; no field carries content.
type Line struct {
	Type         string  `json:"type"`
	EventID      int64   `json:"event_id"`
	EventType    string  `json:"event_type"`
	Trigger      Trigger `json:"trigger,omitempty"`
	Class        string  `json:"class,omitempty"`
	Route        string  `json:"route,omitempty"`
	BucketID     int64   `json:"bucket_id"`
	RecordingID  int64   `json:"recording_id"`
	RecordingURL string  `json:"recording_url,omitempty"`
	RequesterID  int64   `json:"requester_id"`
	State        State   `json:"state"`
	Reason       Reason  `json:"reason,omitempty"`
}

// LineFor builds the stdout line for a verdict.
func LineFor(v Verdict) Line {
	line := Line{
		Type:        "event",
		EventID:     v.EventID,
		EventType:   v.EventType,
		Trigger:     v.Trigger,
		Class:       v.Class,
		Route:       v.Route,
		BucketID:    v.BucketID,
		RecordingID: v.RecordingID,
		RequesterID: v.RequesterID,
		State:       v.State,
		Reason:      v.Reason,
	}
	line.RecordingURL = v.RecordingURL
	return line
}

type lineWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lineWriter) write(v Verdict) error {
	if l.w == nil {
		return nil
	}
	b, err := json.Marshal(LineFor(v))
	if err != nil {
		return fmt.Errorf("admission: encode line: %w", err)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, err := l.w.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("admission: write line: %w", err)
	}
	return nil
}
