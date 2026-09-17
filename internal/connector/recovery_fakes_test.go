//go:build unix

package connector

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp/eventfeed"
	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp/eventfeed/feedtest"
)

// The recovery harness's fake account: the feed (both lanes) and Basecamp
// (messages the agent created, and admission's reads).

// feedEntry is one event the fake account holds.
type feedEntry struct {
	Event eventfeed.Event `json:"event"`
	// FromRepairPoll hides the event from every repair poll before the n-th,
	// the way the poll lane's safety delay withholds a committing event.
	FromRepairPoll int `json:"from_repair_poll,omitempty"`
	// Live is also pushed on the socket, as soon as a connection confirms.
	Live bool `json:"live,omitempty"`
	// Never is never served by a poll: a recording deleted before it became
	// poll-visible.
	Never bool `json:"never,omitempty"`
}

// todoEvent is a to-do created by the operator that mentions the agent. A
// to-do is its own conversation, so events on one recording queue behind each
// other.
func todoEvent(id, recording int64) eventfeed.Event {
	return eventfeed.Event{
		ID: id, Kind: "todo_created", EventType: "todo.created", Action: "created",
		CreatedAt: time.Date(2026, 9, 17, 9, 0, 0, 0, time.UTC),
		BucketID:  harnessBucket, CreatorID: harnessOperator, RecordingID: recording,
	}
}

// publish adds events to the fake account's feed.
func (h *harness) publish(entries ...feedEntry) {
	h.t.Helper()
	require.NoError(h.t, appendFeed(h.dir, entries...))
}

func appendFeed(dir string, entries ...feedEntry) error {
	return withLockedFile(filepath.Join(dir, feedFile), func(f *os.File) error {
		for _, e := range entries {
			data, err := json.Marshal(e)
			if err != nil {
				return err
			}
			if _, err := f.Write(append(data, '\n')); err != nil {
				return err
			}
		}
		return nil
	})
}

// feedCache keeps the last parse of a feed file by its size: the file is only
// ever appended to, and a burst of ten thousand events is read on every poll.
var feedCache struct {
	sync.Mutex
	path    string
	size    int64
	entries []feedEntry
}

func readFeed(dir string) ([]feedEntry, error) {
	path := filepath.Join(dir, feedFile)
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	feedCache.Lock()
	defer feedCache.Unlock()
	if feedCache.path == path && feedCache.size == info.Size() {
		return feedCache.entries, nil
	}
	var out []feedEntry
	err = readJSONLines(path, func(line []byte) error {
		var e feedEntry
		if err := json.Unmarshal(line, &e); err != nil {
			return err
		}
		out = append(out, e)
		return nil
	})
	if err != nil {
		return nil, err
	}
	slices.SortFunc(out, func(a, b feedEntry) int { return int(a.Event.ID - b.Event.ID) })
	feedCache.path, feedCache.size, feedCache.entries = path, info.Size(), out
	return out, nil
}

// withLockedFile runs fn with the file open for appending under an exclusive
// flock: the connector and the fake agents write the same files.
func withLockedFile(path string, fn func(f *os.File) error) error {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }()
	return fn(f)
}

func readJSONLines(path string, fn func(line []byte) error) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_SH); err != nil {
		return err
	}
	defer func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64<<10), 16<<20)
	for scanner.Scan() {
		if len(scanner.Bytes()) == 0 {
			continue
		}
		if err := fn(scanner.Bytes()); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func appendJSONLine(path string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return withLockedFile(path, func(f *os.File) error {
		_, err := f.Write(append(data, '\n'))
		return err
	})
}

// pollLog is one poll the connector made.
type pollLog struct {
	Repair   bool    `json:"repair"`
	Since    string  `json:"since,omitempty"`
	Position string  `json:"position,omitempty"`
	Served   []int64 `json:"served"`
	Stalled  bool    `json:"stalled,omitempty"`
}

func (h *harness) polls() []pollLog {
	h.t.Helper()
	var out []pollLog
	require.NoError(h.t, readJSONLines(filepath.Join(h.dir, pollsFile), func(line []byte) error {
		var p pollLog
		if err := json.Unmarshal(line, &p); err != nil {
			return err
		}
		out = append(out, p)
		return nil
	}))
	return out
}

// filePolls is the poll lane over the feed file, one per walk: the feed's
// connection or a loss's repair walk. Positions are "feed-<id>" and
// "repair-<id>", both meaning "after id".
type filePolls struct {
	dir    string
	ledger *Ledger
	kill   *killSpec
	fault  string
	repair bool

	mu     sync.Mutex
	polled bool
}

// pollsFor hands intake a poll source per walk, telling a repair walk from
// the feed's connection by who asked.
func pollsFor(dir string, ledger *Ledger, kill *killSpec, fault string) func() eventfeed.PollSource {
	return func() eventfeed.PollSource {
		pcs := make([]uintptr, 32)
		frames := runtime.CallersFrames(pcs[:runtime.Callers(2, pcs)])
		repair := false
		for {
			frame, more := frames.Next()
			if strings.HasSuffix(frame.Function, ".(*Intake).runRepair") {
				repair = true
			}
			if !more {
				break
			}
		}
		return &filePolls{dir: dir, ledger: ledger, kill: kill, fault: fault, repair: repair}
	}
}

const maxHarnessPage = 500

func (p *filePolls) Poll(ctx context.Context, cursor eventfeed.Cursor, _ eventfeed.Filters) (eventfeed.PollPage, error) {
	if err := ctx.Err(); err != nil {
		return eventfeed.PollPage{}, err
	}
	p.mu.Lock()
	first := !p.polled
	p.polled = true
	p.mu.Unlock()
	logPath := filepath.Join(p.dir, pollsFile)
	if p.repair {
		if err := p.awaitLosses(ctx); err != nil {
			return eventfeed.PollPage{}, err
		}
		if p.kill.at("repair-poll") {
			die()
		}
		if p.fault == "repair-stall" {
			if err := appendJSONLine(logPath, pollLog{Repair: true, Since: cursor.Since, Position: cursor.Position, Stalled: true}); err != nil {
				return eventfeed.PollPage{}, err
			}
			<-ctx.Done()
			return eventfeed.PollPage{}, ctx.Err()
		}
	} else {
		if p.kill.at("feed-poll") {
			die()
		}
		if p.fault == "stall-catch-up" && first {
			// The feed's first walk is held while the socket's burst piles up
			// in the live buffer behind it, until the overflow is on disk.
			if err := p.awaitLosses(ctx); err != nil {
				return eventfeed.PollPage{}, err
			}
		}
	}
	entries, err := readFeed(p.dir)
	if err != nil {
		return eventfeed.PollPage{}, err
	}
	var after int64
	switch {
	case strings.HasPrefix(cursor.Position, "feed-"), strings.HasPrefix(cursor.Position, "repair-"):
		_, n, _ := strings.Cut(cursor.Position, "-")
		after, _ = strconv.ParseInt(n, 10, 64)
	case cursor.Position != "":
		return eventfeed.PollPage{}, fmt.Errorf("a position this feed never issued: %q", cursor.Position)
	case cursor.Since == "now":
		for _, e := range entries {
			after = max(after, e.Event.ID)
		}
	case cursor.Since != "":
		after, _ = strconv.ParseInt(cursor.Since, 10, 64)
	}
	// The safety delay is counted in repair polls, for both walks: the n-th
	// repair poll, and every poll after it, sees what it sees.
	repairPolls := countRepairPolls(p.dir)
	if p.repair {
		repairPolls++
	}
	page := eventfeed.PollPage{}
	last := after
	for _, e := range entries {
		if e.Event.ID <= after || e.Never {
			continue
		}
		if e.FromRepairPoll > repairPolls {
			// Still inside the safety delay: withheld, and so is everything
			// after it, since a page never skips a committing event.
			break
		}
		if len(page.Events) == maxHarnessPage {
			break
		}
		page.Events = append(page.Events, e.Event)
		last = e.Event.ID
	}
	prefix := "feed-"
	if p.repair {
		prefix = "repair-"
	}
	page.Position = prefix + strconv.FormatInt(last, 10)
	served := make([]int64, 0, len(page.Events))
	for _, e := range page.Events {
		served = append(served, e.ID)
	}
	if err := appendJSONLine(logPath, pollLog{Repair: p.repair, Since: cursor.Since, Position: cursor.Position, Served: served}); err != nil {
		return eventfeed.PollPage{}, err
	}
	return page, nil
}

// awaitLosses holds a poll until the scenario's overflow losses are all on
// disk, so a kill in the walk never races the signal that records the next.
func (p *filePolls) awaitLosses(ctx context.Context) error {
	sc, err := readScenario(p.dir)
	if err != nil || sc.OverflowLosses == 0 {
		return err
	}
	return waitFor(ctx, func() (bool, error) {
		var n int
		err := p.ledger.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM losses`).Scan(&n)
		return n >= sc.OverflowLosses, err
	})
}

func countRepairPolls(dir string) int {
	n := 0
	_ = readJSONLines(filepath.Join(dir, pollsFile), func(line []byte) error {
		var l pollLog
		if json.Unmarshal(line, &l) == nil && l.Repair && !l.Stalled {
			n++
		}
		return nil
	})
	return n
}

// cable answers every connection's subscription, and serves the feed's
// live-only events on the first connection that confirms.
type cable struct {
	transport *feedtest.Transport
	dir       string
	served    map[int64]bool
}

func (c *cable) run(ctx context.Context) {
	type connState struct {
		welcomed   bool
		identifier string
	}
	conns := map[*feedtest.Conn]*connState{}
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		for _, conn := range c.transport.Conns() {
			s := conns[conn]
			if s == nil {
				s = &connState{}
				conns[conn] = s
			}
			if conn.Closed() {
				continue
			}
			if !s.welcomed {
				// Action Cable greets first; the subscribe follows.
				conn.Serve([]byte(`{"type":"welcome"}`))
				s.welcomed = true
			}
			if s.identifier == "" {
				for _, w := range conn.Writes() {
					var command struct {
						Command    string `json:"command"`
						Identifier string `json:"identifier"`
					}
					if json.Unmarshal(w, &command) == nil && command.Command == "subscribe" && command.Identifier != "" {
						frame, _ := json.Marshal(map[string]string{"type": "confirm_subscription", "identifier": command.Identifier})
						conn.Serve(frame)
						s.identifier = command.Identifier
						break
					}
				}
			}
			if s.identifier == "" {
				continue
			}
			entries, err := readFeed(c.dir)
			if err != nil {
				continue
			}
			var fresh []int64
			for _, e := range entries {
				if !e.Live || c.served[e.Event.ID] {
					continue
				}
				c.served[e.Event.ID] = true
				conn.Serve(liveFrame(s.identifier, e.Event))
				fresh = append(fresh, e.Event.ID)
			}
			if len(fresh) > 0 {
				// A push happens once in the world: a restarted connector's
				// socket does not hear it again.
				_ = appendJSONLine(filepath.Join(c.dir, liveFile), fresh)
			}
		}
	}
}

func liveFrame(identifier string, event eventfeed.Event) []byte {
	payload, _ := json.Marshal(map[string]any{
		"id": event.ID, "kind": event.Kind, "event_type": event.EventType, "action": event.Action,
		"created_at": event.CreatedAt.UTC().Format(time.RFC3339), "bucket_id": event.BucketID,
		"creator_id": event.CreatorID, "performed_by_id": nil, "actor_type": "person",
		"recording_id": event.RecordingID, "visible_to_clients": true,
	})
	id, _ := json.Marshal(identifier)
	frame, _ := json.Marshal(map[string]json.RawMessage{"identifier": id, "message": payload})
	return frame
}

// ---- the fake Basecamp ----

// storedMessage is a boost, comment or chat line the agent created: by the
// connector's outbox, or by a worker.
type storedMessage struct {
	ID          int64       `json:"id"`
	Kind        MessageKind `json:"kind"`
	BucketID    int64       `json:"bucket_id"`
	RecordingID int64       `json:"recording_id"`
	Content     string      `json:"content"`
	// By is "connector" or "worker".
	By string    `json:"by"`
	At time.Time `json:"at"`
}

func postMessage(dir string, m storedMessage) (int64, error) {
	var id int64
	err := withLockedFile(filepath.Join(dir, storeFile), func(f *os.File) error {
		n := 0
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return err
		}
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 64<<10), 16<<20)
		for scanner.Scan() {
			n++
		}
		if err := scanner.Err(); err != nil {
			return err
		}
		id = 7_000_000 + int64(n) + 1
		m.ID, m.At = id, time.Now().UTC()
		data, err := json.Marshal(m)
		if err != nil {
			return err
		}
		_, err = f.Write(append(data, '\n'))
		return err
	})
	return id, err
}

func storedMessages(dir string) ([]storedMessage, error) {
	var out []storedMessage
	err := readJSONLines(filepath.Join(dir, storeFile), func(line []byte) error {
		var m storedMessage
		if err := json.Unmarshal(line, &m); err != nil {
			return err
		}
		out = append(out, m)
		return nil
	})
	return out, err
}

func (h *harness) messages() []storedMessage {
	h.t.Helper()
	out, err := storedMessages(h.dir)
	require.NoError(h.t, err)
	return out
}

// connectorPosts are the lifecycle messages the connector posted.
func (h *harness) connectorPosts() []storedMessage {
	h.t.Helper()
	var out []storedMessage
	for _, m := range h.messages() {
		if m.By == "connector" {
			out = append(out, m)
		}
	}
	return out
}

// storePoster is the outbox's Basecamp.
type storePoster struct {
	dir  string
	kill *killSpec
}

func (p storePoster) Post(ctx context.Context, dest Destination, body string) (int64, error) {
	if p.kill.at("post-before") {
		die()
	}
	id, err := postMessage(p.dir, storedMessage{Kind: dest.Kind, BucketID: dest.BucketID, RecordingID: dest.RecordingID, Content: body, By: "connector"})
	if err != nil {
		return 0, err
	}
	if p.kill.at("post-after") {
		die()
	}
	return id, nil
}

func (p storePoster) List(_ context.Context, dest Destination, since time.Time) ([]PostedMessage, error) {
	all, err := storedMessages(p.dir)
	if err != nil {
		return nil, err
	}
	var out []PostedMessage
	for _, m := range all {
		if m.Kind == dest.Kind && m.RecordingID == dest.RecordingID && !m.At.Before(since) {
			out = append(out, PostedMessage{ID: m.ID, CreatedAt: m.At, Content: m.Content})
		}
	}
	return out, nil
}

// storeReplies is the dispatcher's reply lister over the fake Basecamp.
type storeReplies struct{ dir string }

func (r storeReplies) AgentReplies(_ context.Context, _ int64, kind string, recordingID int64, since time.Time) ([]AgentReply, error) {
	all, err := storedMessages(r.dir)
	if err != nil {
		return nil, err
	}
	var out []AgentReply
	for _, m := range all {
		if string(m.Kind) == kind && m.RecordingID == recordingID && !m.At.Before(since) {
			out = append(out, AgentReply{ID: m.ID, CreatedAt: m.At})
		}
	}
	return out, nil
}

// storeReads answers admission: every recording is a to-do the operator wrote
// that mentions the agent.
type storeReads struct {
	dir  string
	gate []int64
	kill *killSpec
}

func (r storeReads) Summarize(ctx context.Context, ref basecamp.RecordingRef) (*basecamp.RecordingSummary, error) {
	if r.kill.at("read:" + strconv.FormatInt(ref.RecordingID, 10)) {
		die()
	}
	if slices.Contains(r.gate, ref.RecordingID) {
		waiting := filepath.Join(r.dir, "read-waiting-"+strconv.FormatInt(ref.RecordingID, 10))
		release := filepath.Join(r.dir, "read-release-"+strconv.FormatInt(ref.RecordingID, 10))
		_ = os.WriteFile(waiting, nil, 0o600)
		if err := awaitFile(ctx, release); err != nil {
			return nil, err
		}
	}
	id := strconv.FormatInt(ref.RecordingID, 10)
	return &basecamp.RecordingSummary{
		ID: ref.RecordingID, Status: "active", Type: "Todo", Title: "To-do " + id,
		AppURL:             "https://app.basecamp.com/" + harnessAccount + "/buckets/" + strconv.FormatInt(ref.BucketID, 10) + "/todos/" + id,
		Bucket:             &basecamp.Bucket{ID: ref.BucketID},
		Creator:            &basecamp.Person{ID: harnessOperator},
		Content:            mentionMarkup(harnessAgent) + " please do the thing",
		MentionedPersonIDs: []int64{harnessAgent},
		UpdatedAt:          time.Date(2026, 9, 17, 9, 0, 0, 0, time.UTC),
	}, nil
}

func (storeReads) Subscribed(context.Context, int64) (bool, error) { return false, nil }

func (storeReads) AddedPersonIDs(context.Context, int64, int64) ([]int64, bool, error) {
	return nil, false, nil
}

// awaitFile waits for path to exist. It is how a process waits on a step
// another process takes.
func awaitFile(ctx context.Context, path string) error {
	for {
		if _, err := os.Stat(path); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Millisecond):
		}
	}
}
