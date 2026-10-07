package connector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"time"

	"github.com/basecamp/basecamp-cli/internal/connector/admission"
	"github.com/basecamp/basecamp-cli/internal/connector/ndjson"
	"github.com/basecamp/basecamp-cli/internal/richtext"
)

// The handoff is what the connector does with a request it trusts: it writes
// it to stdout as an event line and is done with it. Whoever reads the lines,
// usually the person's own Claude session through the basecamp-connect skill,
// acknowledges it, does the work and replies. The connector starts no worker.

// HandoffLine is one trusted request, as the reader gets it. It is the
// instruction a worker used to pull, written out: the agent's own mention
// stripped, nothing more than the record carries.
//
// Its keys are a contract with whoever reads the lines, the basecamp-connect
// skill first: renaming or dropping one breaks a reader nothing here runs.
// requestLineContract holds the names, and the skill's example is held to
// the line exactly, so a key added here is documented where readers learn it.
type HandoffLine struct {
	Type      string `json:"type"` // "request"
	EventID   int64  `json:"event_id"`
	EventType string `json:"event_type"`
	Trigger   string `json:"trigger"`

	Recording   HandoffRecording `json:"recording"`
	ReplyTo     InstructionReply `json:"reply_to"`
	RequesterID int64            `json:"requester_id"`
	// RequesterName is known when the requester wrote the recording.
	RequesterName string `json:"requester_name,omitempty"`
	// Role is "operator" or "participant": whether the request carries an
	// operator's word or only a participant's. The key and its values are
	// the local agent connector's, so one session can read either.
	Role string `json:"role"`
	// Owner says the operator connect.json names — for a personal agent, its
	// owner — asked, and wrote the words when the request is words: not
	// merely someone with an operator's role. Controlling the agent's own
	// connector (start, stop, restart, setup, which build it runs) is the
	// owner's alone, so a reader gates that on this one field rather than
	// comparing ids itself. Admission settles it (Snapshot.Owner); a record
	// admitted before it did says false.
	Owner bool `json:"owner"`
	// Acknowledge says a person asked for something. A comment on a thread
	// the agent follows, or a completion, is context and is not acknowledged.
	Acknowledge bool `json:"acknowledge"`

	Content          string    `json:"content"`
	ContentUpdatedAt time.Time `json:"content_updated_at"`
}

// HandoffRecording points at the recording the request is about.
type HandoffRecording struct {
	BucketID    int64  `json:"bucket_id"`
	ProjectName string `json:"project_name,omitempty"`
	RecordingID int64  `json:"recording_id"`
	Type        string `json:"type"`
	Title       string `json:"title"`
	URL         string `json:"url"`
}

// Discard reasons the handoff writes.
const (
	// ReasonHandedOff: the request was written to stdout for the session.
	ReasonHandedOff = "handed_off"
	// ReasonUnreadable: the request's record had no content to hand off, so
	// nothing was written.
	ReasonUnreadable = "unreadable"
	// ReasonBeforeThisRun: the request was made before this run started.
	// Nobody was there to take it, and it is not run late.
	ReasonBeforeThisRun = "before_this_run"
)

// HandoffGrace is how long before the run started a request may have been
// made and still be handed off: a mention written a moment before the
// session started is still meant for it.
const HandoffGrace = time.Minute

// HandoffOptions configure RunHandoff.
type HandoffOptions struct {
	Ledger *Ledger
	// Served is connect.json's served projects as they are now.
	Served func() (map[int64]admission.Project, error)
	// Buckets narrows the served projects to a run's --project, when given.
	Buckets []int64
	// AgentID is the agent's Person id, whose mentions are stripped.
	AgentID int64
	Lines   *ndjson.Writer
	Logger  *slog.Logger
	// Started is when the run started. Zero means now.
	Started time.Time
	// Interval is how often admitted records are looked for. Zero means
	// DefaultHandoffInterval.
	Interval time.Duration
}

func (o HandoffOptions) log() *slog.Logger {
	if o.Logger == nil {
		return slog.New(slog.DiscardHandler)
	}
	return o.Logger
}

// DefaultHandoffInterval is how often the handoff looks for admitted records.
const DefaultHandoffInterval = 250 * time.Millisecond

// RunHandoff writes every admitted record as an event line until ctx ends.
func RunHandoff(ctx context.Context, opts HandoffOptions) error {
	switch {
	case opts.Ledger == nil:
		return errors.New("connector: the handoff needs the ledger")
	case opts.Served == nil:
		return errors.New("connector: the handoff needs the served projects")
	case opts.Lines == nil:
		return errors.New("connector: the handoff needs somewhere to write")
	case opts.AgentID <= 0:
		return errors.New("connector: the handoff needs the agent's Person id")
	}
	if opts.Started.IsZero() {
		opts.Started = time.Now()
	}
	if opts.Interval <= 0 {
		opts.Interval = DefaultHandoffInterval
	}
	tick := time.NewTicker(opts.Interval)
	defer tick.Stop()
	for {
		if err := handOffReady(ctx, opts); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
	}
}

// handOffReady hands off what is admitted now, a conversation at a time.
func handOffReady(ctx context.Context, opts HandoffOptions) error {
	for {
		served, err := opts.Served()
		if err != nil {
			// Nothing can say which projects are served: hand nothing off,
			// and look again next time.
			opts.log().Warn("connector: which projects are served could not be read; nothing is handed off until it can be", "error", err)
			return nil
		}
		buckets := slices.Collect(maps.Keys(served))
		if len(opts.Buckets) > 0 {
			buckets = slices.DeleteFunc(buckets, func(b int64) bool { return !slices.Contains(opts.Buckets, b) })
		}
		records, err := opts.Ledger.handoffRecords(ctx, buckets, 50)
		if err != nil {
			return err
		}
		if len(records) == 0 {
			return nil
		}
		for _, record := range records {
			if err := handOff(ctx, opts, record); err != nil {
				return err
			}
		}
	}
}

// handOff writes one record and closes it. A record made before the run
// started is closed without being written.
func handOff(ctx context.Context, opts HandoffOptions, record Record) error {
	if record.CreatedAt.Before(opts.Started.Add(-HandoffGrace)) {
		opts.log().Info("connector: a request from before this run started is not handed off", "event_id", record.ID, "created_at", record.CreatedAt)
		return opts.Ledger.SetState(ctx, record.ID, StateDiscarded, ReasonBeforeThisRun)
	}
	line, err := handoffLine(record, opts.AgentID)
	if err != nil {
		// Closed under its own reason, not handed_off: nothing was written.
		// Returning the error instead would stop the connector on every
		// start, for one record.
		opts.log().Warn("connector: a request could not be handed off", "event_id", record.ID, "error", err)
		return opts.Ledger.SetState(ctx, record.ID, StateDiscarded, ReasonUnreadable)
	}
	if err := opts.Lines.WriteLine(line); err != nil {
		return fmt.Errorf("connector: hand off event %d: %w", record.ID, err)
	}
	return opts.Ledger.SetState(ctx, record.ID, StateDiscarded, ReasonHandedOff)
}

// handoffLine is a record as the reader gets it.
func handoffLine(record Record, agentID int64) (HandoffLine, error) {
	var snapshot struct {
		Type          string    `json:"type"`
		Title         string    `json:"title"`
		AppURL        string    `json:"app_url"`
		Content       string    `json:"content"`
		UpdatedAt     time.Time `json:"updated_at"`
		ProjectName   string    `json:"project_name"`
		RequesterName string    `json:"requester_name"`
		Role          string    `json:"role"`
		Owner         bool      `json:"owner"`
	}
	if record.ContentDropped || len(record.Decision.Snapshot) == 0 {
		return HandoffLine{}, errors.New("the record has no content")
	}
	if err := json.Unmarshal(record.Decision.Snapshot, &snapshot); err != nil {
		return HandoffLine{}, fmt.Errorf("snapshot: %w", err)
	}
	return HandoffLine{
		Type:      "request",
		EventID:   record.ID,
		EventType: richtext.SanitizeTerminal(record.EventType),
		Trigger:   record.Decision.Trigger,
		Recording: HandoffRecording{
			BucketID:    record.BucketID,
			ProjectName: richtext.SanitizeTerminal(snapshot.ProjectName),
			RecordingID: record.RecordingID,
			Type:        richtext.SanitizeTerminal(snapshot.Type),
			Title:       richtext.SanitizeTerminal(snapshot.Title),
			URL:         richtext.SanitizeTerminal(record.Decision.RecordingURL),
		},
		ReplyTo:       InstructionReply{Kind: record.Decision.ReplyKind, RecordingID: record.Decision.ReplyRecordingID},
		RequesterID:   record.Decision.RequesterID,
		RequesterName: richtext.SanitizeTerminal(snapshot.RequesterName),
		Role:          handoffRole(snapshot.Role),
		Owner:         snapshot.Owner && handoffRole(snapshot.Role) == string(admission.RoleOperator),
		Acknowledge:   record.Decision.Acknowledge,
		// The line is read in a terminal as often as by a program: no
		// control sequences from Basecamp's text reach it.
		Content:          richtext.SanitizeTerminal(StripMentionsOf(snapshot.Content, agentID)),
		ContentUpdatedAt: snapshot.UpdatedAt,
	}, nil
}

// handoffRole is the role a line carries. Only a record admission settled as
// an operator's says operator. One admitted before admission settled roles,
// or carrying a value this build does not know, says participant: those are
// the rules that lend nobody an operator's standing.
func handoffRole(recorded string) string {
	if admission.Role(recorded) == admission.RoleOperator {
		return string(admission.RoleOperator)
	}
	return string(admission.RoleParticipant)
}

// handoffRecords lists the records waiting to be handed off in buckets,
// oldest first. Unlike the startable query a worker used, nothing here waits
// on a task: a ledger from the worker connector can hold a task its crash
// left open, or a follow-up joined to one, and neither may keep a request
// from being handed off. The hold still pauses everything.
func (l *Ledger) handoffRecords(ctx context.Context, buckets []int64, limit int) ([]Record, error) {
	if len(buckets) == 0 {
		return nil, nil
	}
	buckets = slices.Clone(buckets)
	slices.Sort(buckets)
	buckets = slices.Compact(buckets)
	args := make([]any, 0, len(buckets)+1)
	for _, b := range buckets {
		args = append(args, b)
	}
	args = append(args, limit)
	//nolint:gosec // G202: only placeholders are built into the query
	query := `
SELECT e.id FROM events e
WHERE e.state IN ('admitted', 'queued') AND e.content_dropped = 0 AND e.snapshot IS NOT NULL
  AND e.served = 1 AND e.bucket_id IN (` + placeholders(len(buckets)) + `)
  AND NOT EXISTS (SELECT 1 FROM hold_marker)
ORDER BY e.id LIMIT ?`
	rows, err := l.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("connector: records to hand off: %w", err)
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("connector: records to hand off: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	out := make([]Record, 0, len(ids))
	for _, id := range ids {
		r, ok, err := l.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, r)
		}
	}
	return out, nil
}
