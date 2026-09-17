package admission

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"

	"github.com/basecamp/basecamp-cli/internal/connector/ndjson"
	"github.com/basecamp/basecamp-cli/internal/richtext"
)

// DefaultWorkers is the admission fetcher pool size.
const DefaultWorkers = 4

// IDSource hands over the next event id to admit. Intake's Queue (basecamp-cli
// PR 729, internal/connector) satisfies it.
type IDSource interface {
	Take(ctx context.Context) (int64, error)
}

// Records loads a record for admission. It returns a record still being
// decided — seen, or blocked (recovery and redispatch decide a blocked record
// again) — with Event.Revision set to the record's revision as loaded. ok is
// false when the id is unknown or the record is past deciding, so it is
// skipped rather than decided twice.
//
// This is the seam where intake hands admission an event: the adapter over
// intake's Ledger.Get lands once PR 729 merges.
type Records interface {
	LoadUndecided(ctx context.Context, id int64) (ev Event, ok bool, err error)
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
	Lines io.Writer
	// LineWriter, when set, is the writer the lines go through, and Lines is
	// ignored: one sink has one writer and one lock, and a caller wiring
	// admission beside intake passes the same writer to both.
	LineWriter *ndjson.Writer
	Logger     *slog.Logger
}

// Run takes ids until ctx ends, deciding and committing each. It returns nil
// when ctx is canceled (a shutdown), ctx's error when its deadline passed, and
// the first other error otherwise: a ledger that cannot load or commit, or a
// line that cannot be written, is not something to skip past. An event whose
// decision ctx interrupted is not committed; it stays as it was loaded.
//
// The stdout line is written after the commit, so it is best-effort: a
// shutdown between the two leaves the verdict written and its line unwritten,
// and a decided record is not loaded again. The ledger, not the stream, is the
// record of what was decided.
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
	// Built here, before the workers exist, so nothing is initialized on a
	// path two of them can take at once.
	lines := newLineWriter(opts.LineWriter, opts.Lines)

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
	if firstErr == nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return ctx.Err()
	}
	return firstErr
}

func admitOne(ctx context.Context, opts RunOptions, lines *lineWriter, log *slog.Logger, id int64) error {
	ev, ok, err := opts.Records.LoadUndecided(ctx, id)
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
	// Every string is stripped of terminal controls. The line is a wire, but
	// it is also what a person watching the connector sees, and the event
	// type and URL come from Basecamp: JSON escapes C0 controls but passes C1
	// controls such as U+009B (CSI) through as raw UTF-8. Whitespace is left
	// alone — JSON escapes newlines and tabs, and a route is a local path that
	// must survive as written.
	clean := richtext.SanitizeTerminal
	return Line{
		Type:         "event",
		EventID:      v.EventID,
		EventType:    clean(v.EventType),
		Trigger:      Trigger(clean(string(v.Trigger))),
		Class:        clean(v.Class),
		Route:        clean(v.Route),
		BucketID:     v.BucketID,
		RecordingID:  v.RecordingID,
		RecordingURL: clean(v.RecordingURL),
		RequesterID:  v.RequesterID,
		State:        State(clean(string(v.State))),
		Reason:       Reason(clean(string(v.Reason))),
	}
}

// lineWriter is the verdict stream. It holds a writer and nothing else: no
// flag, no once, no first-use path.
//
// It used to build the writer on the first verdict, which is a race the moment
// admission has more than one worker — two of them reaching their first line
// together read and assign the same field. Laziness is not a thing to guard
// here; it is a thing to remove. The writer is built once, before any worker
// starts, and injected.
type lineWriter struct {
	out *ndjson.Writer
}

// newLineWriter takes the writer a caller wiring admission beside intake
// passes in, or builds one for the sink. There is exactly one writer per sink
// either way: NewWriter returns the same writer for the same sink, so the one
// lock is the one lock.
func newLineWriter(out *ndjson.Writer, sink io.Writer) *lineWriter {
	if out == nil && sink != nil {
		out = ndjson.NewWriter(sink)
	}
	return &lineWriter{out: out}
}

func (l *lineWriter) write(v Verdict) error {
	if l.out == nil {
		return nil
	}
	if err := l.out.WriteLine(LineFor(v)); err != nil {
		return fmt.Errorf("admission: %w", err)
	}
	return nil
}
