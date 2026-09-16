package admission

import (
	"context"
	"encoding/base64"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/stretchr/testify/require"
)

// People in the fixtures. The ids are arbitrary; what matters is who is who.
const (
	agentID     int64 = 52007412
	operatorID  int64 = 26909558
	allowedID   int64 = 1001
	memberID    int64 = 1002
	clientID    int64 = 1003
	strangerID  int64 = 1004
	otherAgent  int64 = 1005
	routedProj  int64 = 48699913
	unmapped    int64 = 777
	watchedProj int64 = 555
)

func basePolicy() Policy {
	return Policy{
		AgentID: agentID,
		Trust:   Trust{Mode: TrustOperator, OperatorID: operatorID},
		Projects: map[int64]Route{
			routedProj:  {Path: "/work/connector", Class: "internal"},
			watchedProj: {Path: "/work/board", Class: "internal", WatchCompletions: true},
		},
	}
}

// fakeReads records every read, so a test can say a verdict cost none.
type fakeReads struct {
	mu sync.Mutex

	summaries     map[int64]*basecamp.RecordingSummary
	summaryErrs   []error // returned in order, one per call, before summaries
	subscriptions map[int64]bool
	subErr        error
	assignments   map[int64][]int64 // event id → added person ids
	assignErr     error
	members       map[int64]map[int64]bool
	memberErr     error
	memberAsOf    []time.Time

	summaryCalls int
	subCalls     []int64
	assignCalls  int
	memberCalls  int
}

func newFakeReads() *fakeReads {
	return &fakeReads{
		summaries:     map[int64]*basecamp.RecordingSummary{},
		subscriptions: map[int64]bool{},
		assignments:   map[int64][]int64{},
		members:       map[int64]map[int64]bool{},
	}
}

func (f *fakeReads) reads() Reads {
	return Reads{Summaries: f, Subscriptions: f, Assignments: f, Members: f}
}

func (f *fakeReads) Summarize(_ context.Context, ref basecamp.RecordingRef) (*basecamp.RecordingSummary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.summaryCalls++
	if len(f.summaryErrs) > 0 {
		err := f.summaryErrs[0]
		f.summaryErrs = f.summaryErrs[1:]
		if err != nil {
			return nil, err
		}
	}
	s, ok := f.summaries[ref.RecordingID]
	if !ok {
		return nil, &basecamp.Error{Code: basecamp.CodeNotFound, Message: "not found"}
	}
	copied := *s
	return &copied, nil
}

func (f *fakeReads) Subscribed(_ context.Context, recordingID int64) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.subCalls = append(f.subCalls, recordingID)
	if f.subErr != nil {
		return false, f.subErr
	}
	return f.subscriptions[recordingID], nil
}

func (f *fakeReads) AddedPersonIDs(_ context.Context, _, eventID int64) ([]int64, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.assignCalls++
	if f.assignErr != nil {
		return nil, false, f.assignErr
	}
	added, ok := f.assignments[eventID]
	return added, ok, nil
}

func (f *fakeReads) NonClientMember(_ context.Context, bucketID, personID int64, asOf time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.memberCalls++
	f.memberAsOf = append(f.memberAsOf, asOf)
	if f.memberErr != nil {
		return false, f.memberErr
	}
	return f.members[bucketID][personID], nil
}

func (f *fakeReads) totalReads() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.summaryCalls + len(f.subCalls) + f.assignCalls + f.memberCalls
}

var errTransport = errors.New("connection reset")

func newAdmitter(t *testing.T, p Policy, f *fakeReads) *Admitter {
	t.Helper()
	a, err := NewAdmitter(p, f.reads(), WithSleep(func(context.Context, time.Duration) error { return nil }))
	require.NoError(t, err)
	return a
}

func decide(t *testing.T, a *Admitter, ev Event) Verdict {
	t.Helper()
	v, err := a.Decide(context.Background(), ev)
	require.NoError(t, err)
	return v
}

func person(id int64) *basecamp.Person { return &basecamp.Person{ID: id} }

func ptr(id int64) *int64 { return &id }

// mentionOf is a mention attachment for id, the way Basecamp renders one: the
// person is named only inside the sgid.
func mentionOf(t *testing.T, id int64) string {
	t.Helper()
	sgid := basecampSGID(id)
	got := basecamp.MentionedPersonIDs(`<bc-attachment sgid="` + sgid + `" content-type="application/vnd.basecamp.mention"></bc-attachment>`)
	require.Equal(t, []int64{id}, got, "fixture sgid must decode to the person")
	return `<bc-attachment sgid="` + sgid + `" content-type="application/vnd.basecamp.mention"></bc-attachment>`
}

func summaryWith(id, bucket int64, typ string, creator int64, content string) *basecamp.RecordingSummary {
	return &basecamp.RecordingSummary{
		ID:                 id,
		Status:             "active",
		Type:               typ,
		Title:              "A recording",
		AppURL:             "https://app.basecamp.com/2914079/buckets/1/recordings/1",
		Bucket:             &basecamp.Bucket{ID: bucket},
		Creator:            person(creator),
		MentionedPersonIDs: basecamp.MentionedPersonIDs(content),
		Content:            content,
		UpdatedAt:          time.Date(2026, 9, 16, 10, 0, 0, 0, time.UTC),
	}
}

// basecampSGID builds an unsigned attachable sgid naming a Person, in the JSON
// envelope Rails also emits. The SDK reads the id from the payload; no
// signature is involved, and none is needed to exercise mention detection.
func basecampSGID(id int64) string {
	payload := `{"_rails":{"data":"gid://bc3/Person/` + strconv.FormatInt(id, 10) + `","pur":"attachable"}}`
	return base64.RawURLEncoding.EncodeToString([]byte(payload))
}
