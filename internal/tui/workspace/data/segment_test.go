package data

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func makeRoom(account string, project, campfire int64) RoomID {
	return RoomID{AccountID: account, ProjectID: project, CampfireID: campfire}
}

func makeLine(id int64, creator, body string, ts time.Time) CampfireLineInfo {
	return CampfireLineInfo{
		ID:          id,
		Creator:     creator,
		Body:        body,
		CreatedAt:   ts.Format(time.RFC3339),
		CreatedAtTS: ts,
	}
}

func TestSegmentBasicGrouping(t *testing.T) {
	seg := NewSegmenter(DefaultSegmenterConfig())
	room := makeRoom("acct1", 1, 100)
	t0 := time.Date(2025, 1, 1, 10, 0, 0, 0, time.UTC)

	lines := []CampfireLineInfo{
		makeLine(1, "Alice", "hello", t0),
		makeLine(2, "Bob", "hi there", t0.Add(10*time.Second)),
		makeLine(3, "Alice", "how are you?", t0.Add(20*time.Second)),
	}
	seg.IngestSnapshot(room, "Room1", lines)

	segs := seg.Segments()
	require.Len(t, segs, 1)
	assert.Equal(t, 3, len(segs[0].Lines))
	assert.Equal(t, "Room1", segs[0].RoomName)
	assert.False(t, segs[0].Sealed)
}

func TestSegmentTemporalSplit(t *testing.T) {
	cfg := DefaultSegmenterConfig()
	cfg.SealAfter = 5 * time.Minute // fixed gap for predictability
	seg := NewSegmenter(cfg)
	room := makeRoom("acct1", 1, 100)
	t0 := time.Date(2025, 1, 1, 10, 0, 0, 0, time.UTC)

	lines := []CampfireLineInfo{
		makeLine(1, "Alice", "hello", t0),
		makeLine(2, "Bob", "hi", t0.Add(30*time.Second)),
		// Big gap — new segment
		makeLine(3, "Charlie", "new topic", t0.Add(20*time.Minute)),
	}
	seg.IngestSnapshot(room, "Room1", lines)

	segs := seg.Segments()
	require.Len(t, segs, 2)
	assert.Equal(t, 2, len(segs[0].Lines))
	assert.True(t, segs[0].Sealed)
	assert.Equal(t, 1, len(segs[1].Lines))
	assert.False(t, segs[1].Sealed)
}

func TestSegmentParticipantContinuity(t *testing.T) {
	cfg := DefaultSegmenterConfig()
	cfg.SealAfter = 5 * time.Minute
	seg := NewSegmenter(cfg)
	room := makeRoom("acct1", 1, 100)
	t0 := time.Date(2025, 1, 1, 10, 0, 0, 0, time.UTC)

	// Build up a conversation so returning speakers are recognized.
	// Alice starts, Alice continues (same speaker), Bob joins (temporal alone enough),
	// Alice returns — participant continuity (0.20) boosts the score.
	lines := []CampfireLineInfo{
		makeLine(1, "Alice", "starting discussion", t0),
		makeLine(2, "Alice", "more thoughts", t0.Add(10*time.Second)),
		makeLine(3, "Bob", "interesting point", t0.Add(20*time.Second)),
		// Alice returns — participant continuity fires (she's in last 5)
		makeLine(4, "Alice", "thanks bob", t0.Add(1*time.Minute)),
	}
	seg.IngestSnapshot(room, "Room1", lines)

	segs := seg.Segments()
	require.Len(t, segs, 1)
	assert.Equal(t, 4, len(segs[0].Lines))
	// Verify Alice (returning speaker) is in the last message
	assert.Equal(t, "Alice", segs[0].Lines[3].Creator)
}

func TestSegmentMentionChain(t *testing.T) {
	cfg := DefaultSegmenterConfig()
	cfg.SealAfter = 5 * time.Minute
	seg := NewSegmenter(cfg)
	room := makeRoom("acct1", 1, 100)
	t0 := time.Date(2025, 1, 1, 10, 0, 0, 0, time.UTC)

	mentionHTML := `Hey <bc-attachment sgid="abc" content-type="application/vnd.basecamp.mention">@Bob</bc-attachment> check this`
	lines := []CampfireLineInfo{
		makeLine(1, "Alice", mentionHTML, t0),
		// Bob replies — mention chain (0.15) + temporal (~0.32) > 0.35
		makeLine(2, "Bob", "on it", t0.Add(1*time.Minute)),
	}
	seg.IngestSnapshot(room, "Room1", lines)

	segs := seg.Segments()
	require.Len(t, segs, 1)
	assert.Equal(t, 2, len(segs[0].Lines))
}

func TestSegmentQuestionAnswer(t *testing.T) {
	cfg := DefaultSegmenterConfig()
	cfg.SealAfter = 5 * time.Minute
	seg := NewSegmenter(cfg)
	room := makeRoom("acct1", 1, 100)
	t0 := time.Date(2025, 1, 1, 10, 0, 0, 0, time.UTC)

	lines := []CampfireLineInfo{
		makeLine(1, "Alice", "what do you think about the design?", t0),
		// Bob answers — question-answer (0.10) + temporal (~0.33) > 0.35
		makeLine(2, "Bob", "looks great to me", t0.Add(50*time.Second)),
	}
	seg.IngestSnapshot(room, "Room1", lines)

	segs := seg.Segments()
	require.Len(t, segs, 1)
	assert.Equal(t, 2, len(segs[0].Lines))
}

func TestSegmentBurstDetection(t *testing.T) {
	cfg := DefaultSegmenterConfig()
	cfg.SealAfter = 5 * time.Minute
	seg := NewSegmenter(cfg)
	room := makeRoom("acct1", 1, 100)
	t0 := time.Date(2025, 1, 1, 10, 0, 0, 0, time.UTC)

	// Rapid-fire messages — burst detection kicks in at >= 2 lines
	lines := []CampfireLineInfo{
		makeLine(1, "Alice", "hey", t0),
		makeLine(2, "Bob", "yo", t0.Add(5*time.Second)),
		makeLine(3, "Alice", "quick thing", t0.Add(10*time.Second)),
		makeLine(4, "Bob", "sure", t0.Add(15*time.Second)),
	}
	seg.IngestSnapshot(room, "Room1", lines)

	segs := seg.Segments()
	require.Len(t, segs, 1)
	assert.Equal(t, 4, len(segs[0].Lines))
}

func TestSegmentLexicalSimilarity(t *testing.T) {
	// Jaccard similarity provides a small contribution
	a := Tokenize("the deployment pipeline is broken")
	b := Tokenize("pipeline seems broken to me")
	sim := JaccardSimilarity(a, b)
	assert.Greater(t, sim, 0.0)

	// Completely different topics
	c := Tokenize("lunch menu today")
	d := Tokenize("deployment pipeline configuration")
	sim2 := JaccardSimilarity(c, d)
	assert.Less(t, sim2, sim)
}

func TestSegmentAnnouncementDetection(t *testing.T) {
	cfg := DefaultSegmenterConfig()
	cfg.SealAfter = 5 * time.Minute
	cfg.AnnouncementMinChars = 50 // lower threshold for testing
	seg := NewSegmenter(cfg)
	room := makeRoom("acct1", 1, 100)
	t0 := time.Date(2025, 1, 1, 10, 0, 0, 0, time.UTC)

	longBody := strings.Repeat("This is a long announcement message. ", 5)

	lines := []CampfireLineInfo{
		makeLine(1, "Alice", "chatting about stuff", t0),
		makeLine(2, "Bob", "yeah", t0.Add(30*time.Second)),
		// Long message from new speaker after >2 min gap → announcement
		makeLine(3, "Charlie", longBody, t0.Add(3*time.Minute)),
	}
	seg.IngestSnapshot(room, "Room1", lines)

	segs := seg.Segments()
	require.Len(t, segs, 2)
	assert.Equal(t, 2, len(segs[0].Lines))
	assert.True(t, segs[0].Sealed)
	assert.Equal(t, 1, len(segs[1].Lines))
	assert.Equal(t, "Charlie", segs[1].Lines[0].Creator)
}

func TestSegmentIngestSnapshotDedup(t *testing.T) {
	seg := NewSegmenter(DefaultSegmenterConfig())
	room := makeRoom("acct1", 1, 100)
	t0 := time.Date(2025, 1, 1, 10, 0, 0, 0, time.UTC)

	lines := []CampfireLineInfo{
		makeLine(1, "Alice", "hello", t0),
		makeLine(2, "Bob", "hi", t0.Add(10*time.Second)),
	}

	// Ingest twice — second call should be a no-op
	seg.IngestSnapshot(room, "Room1", lines)
	seg.IngestSnapshot(room, "Room1", lines)

	segs := seg.Segments()
	require.Len(t, segs, 1)
	assert.Equal(t, 2, len(segs[0].Lines))
}

func TestSegmentIngestSnapshotIncremental(t *testing.T) {
	seg := NewSegmenter(DefaultSegmenterConfig())
	room := makeRoom("acct1", 1, 100)
	t0 := time.Date(2025, 1, 1, 10, 0, 0, 0, time.UTC)

	batch1 := []CampfireLineInfo{
		makeLine(1, "Alice", "hello", t0),
	}
	batch2 := []CampfireLineInfo{
		makeLine(1, "Alice", "hello", t0),                        // dup
		makeLine(2, "Bob", "hi", t0.Add(10*time.Second)),         // new
		makeLine(3, "Alice", "how goes", t0.Add(20*time.Second)), // new
	}

	seg.IngestSnapshot(room, "Room1", batch1)
	seg.IngestSnapshot(room, "Room1", batch2)

	segs := seg.Segments()
	require.Len(t, segs, 1)
	assert.Equal(t, 3, len(segs[0].Lines))
}

func TestSegmentSealStale(t *testing.T) {
	seg := NewSegmenter(DefaultSegmenterConfig())
	room := makeRoom("acct1", 1, 100)
	t0 := time.Date(2025, 1, 1, 10, 0, 0, 0, time.UTC)

	lines := []CampfireLineInfo{
		makeLine(1, "Alice", "hello", t0),
	}
	seg.IngestSnapshot(room, "Room1", lines)

	// Before sealing
	segs := seg.Segments()
	require.Len(t, segs, 1)
	assert.False(t, segs[0].Sealed)

	// Seal stale
	seg.SealStale(t0.Add(30*time.Minute), 15*time.Minute)

	segs = seg.Segments()
	require.Len(t, segs, 1)
	assert.True(t, segs[0].Sealed)
}

func TestSegmentSealStaleNotYet(t *testing.T) {
	seg := NewSegmenter(DefaultSegmenterConfig())
	room := makeRoom("acct1", 1, 100)
	t0 := time.Date(2025, 1, 1, 10, 0, 0, 0, time.UTC)

	lines := []CampfireLineInfo{
		makeLine(1, "Alice", "hello", t0),
	}
	seg.IngestSnapshot(room, "Room1", lines)

	// Not old enough to seal
	seg.SealStale(t0.Add(5*time.Minute), 15*time.Minute)

	segs := seg.Segments()
	require.Len(t, segs, 1)
	assert.False(t, segs[0].Sealed)
}

func TestSegmentOrdering(t *testing.T) {
	cfg := DefaultSegmenterConfig()
	cfg.SealAfter = 2 * time.Minute
	seg := NewSegmenter(cfg)
	room := makeRoom("acct1", 1, 100)
	t0 := time.Date(2025, 1, 1, 10, 0, 0, 0, time.UTC)

	lines := []CampfireLineInfo{
		makeLine(1, "Alice", "first convo", t0),
		// Gap causes split
		makeLine(2, "Bob", "second convo", t0.Add(10*time.Minute)),
		// Another gap
		makeLine(3, "Charlie", "third convo", t0.Add(20*time.Minute)),
	}
	seg.IngestSnapshot(room, "Room1", lines)

	segs := seg.Segments()
	require.Len(t, segs, 3)

	// First two should be sealed, last should be open
	assert.True(t, segs[0].Sealed)
	assert.True(t, segs[1].Sealed)
	assert.False(t, segs[2].Sealed)

	// Sealed ordered by EndTime, open last
	assert.True(t, segs[0].EndTime.Before(segs[1].EndTime))
}

func TestSegmentMultiRoom(t *testing.T) {
	seg := NewSegmenter(DefaultSegmenterConfig())
	room1 := makeRoom("acct1", 1, 100)
	room2 := makeRoom("acct1", 2, 200)
	t0 := time.Date(2025, 1, 1, 10, 0, 0, 0, time.UTC)

	lines1 := []CampfireLineInfo{
		makeLine(1, "Alice", "hello room1", t0),
	}
	lines2 := []CampfireLineInfo{
		makeLine(1, "Bob", "hello room2", t0.Add(5*time.Second)),
	}

	seg.IngestSnapshot(room1, "Room1", lines1)
	seg.IngestSnapshot(room2, "Room2", lines2)

	segs := seg.Segments()
	require.Len(t, segs, 2)

	rooms := map[string]bool{}
	for _, s := range segs {
		rooms[s.RoomName] = true
	}
	assert.True(t, rooms["Room1"])
	assert.True(t, rooms["Room2"])
}

func TestSegmentAdaptiveGapThreshold(t *testing.T) {
	cfg := DefaultSegmenterConfig()
	// Don't set SealAfter — use adaptive
	seg := NewSegmenter(cfg)

	// Build a segment with rapid 10-second cadence
	t0 := time.Date(2025, 1, 1, 10, 0, 0, 0, time.UTC)
	testSeg := &Segment{
		Lines: []RiverLine{
			{CampfireLineInfo: makeLine(1, "A", "x", t0)},
			{CampfireLineInfo: makeLine(2, "B", "y", t0.Add(10*time.Second))},
			{CampfireLineInfo: makeLine(3, "A", "z", t0.Add(20*time.Second))},
			{CampfireLineInfo: makeLine(4, "B", "w", t0.Add(30*time.Second))},
		},
	}

	threshold := seg.adaptiveGapThreshold(testSeg)
	// Median gap is 10s, threshold is 10s*3 = 30s, clamped to MinGap (1 min)
	assert.Equal(t, cfg.MinGap, threshold)
}

func TestSegmentAdaptiveGapThresholdSlowCadence(t *testing.T) {
	cfg := DefaultSegmenterConfig()
	seg := NewSegmenter(cfg)

	// Build a segment with 2-minute cadence
	t0 := time.Date(2025, 1, 1, 10, 0, 0, 0, time.UTC)
	testSeg := &Segment{
		Lines: []RiverLine{
			{CampfireLineInfo: makeLine(1, "A", "x", t0)},
			{CampfireLineInfo: makeLine(2, "B", "y", t0.Add(2*time.Minute))},
			{CampfireLineInfo: makeLine(3, "A", "z", t0.Add(4*time.Minute))},
			{CampfireLineInfo: makeLine(4, "B", "w", t0.Add(6*time.Minute))},
		},
	}

	threshold := seg.adaptiveGapThreshold(testSeg)
	// Median gap is 2m, threshold is 2m*3 = 6m, within [1m, 10m]
	assert.Equal(t, 6*time.Minute, threshold)
}

func TestSegmentAdaptiveGapSingleLine(t *testing.T) {
	cfg := DefaultSegmenterConfig()
	seg := NewSegmenter(cfg)

	testSeg := &Segment{
		Lines: []RiverLine{
			{CampfireLineInfo: makeLine(1, "A", "x", time.Now())},
		},
	}

	threshold := seg.adaptiveGapThreshold(testSeg)
	assert.Equal(t, cfg.MaxGap, threshold)
}

func TestSegmentEmptyInput(t *testing.T) {
	seg := NewSegmenter(DefaultSegmenterConfig())
	room := makeRoom("acct1", 1, 100)

	seg.IngestSnapshot(room, "Room1", nil)
	seg.IngestSnapshot(room, "Room1", []CampfireLineInfo{})

	assert.Len(t, seg.Segments(), 0)
	assert.Equal(t, 0, seg.SegmentCount())
}

func TestSegmentSingleMessage(t *testing.T) {
	seg := NewSegmenter(DefaultSegmenterConfig())
	room := makeRoom("acct1", 1, 100)
	t0 := time.Date(2025, 1, 1, 10, 0, 0, 0, time.UTC)

	lines := []CampfireLineInfo{
		makeLine(1, "Alice", "only message", t0),
	}
	seg.IngestSnapshot(room, "Room1", lines)

	segs := seg.Segments()
	require.Len(t, segs, 1)
	assert.Equal(t, 1, len(segs[0].Lines))
	assert.Equal(t, 1, seg.SegmentCount())
}

// --- textutil tests ---

func TestExtractMentions(t *testing.T) {
	html := `Hey <bc-attachment sgid="abc" content-type="application/vnd.basecamp.mention">@Alice</bc-attachment> and <bc-attachment sgid="def" content-type="application/vnd.basecamp.mention">@Bob</bc-attachment>`
	names := ExtractMentions(html)
	assert.Equal(t, []string{"Alice", "Bob"}, names)
}

func TestExtractMentionsNone(t *testing.T) {
	names := ExtractMentions("just plain text")
	assert.Empty(t, names)
}

func TestTokenize(t *testing.T) {
	tokens := Tokenize("<p>The deployment pipeline is broken!</p>")
	assert.Contains(t, tokens, "deployment")
	assert.Contains(t, tokens, "pipeline")
	assert.Contains(t, tokens, "broken")
	// Stopwords removed
	assert.NotContains(t, tokens, "the")
	assert.NotContains(t, tokens, "is")
}

func TestTokenizeEmpty(t *testing.T) {
	assert.Empty(t, Tokenize(""))
	assert.Empty(t, Tokenize("   "))
}

func TestJaccardSimilarityIdentical(t *testing.T) {
	a := []string{"deploy", "pipeline", "broken"}
	sim := JaccardSimilarity(a, a)
	assert.Equal(t, 1.0, sim)
}

func TestJaccardSimilarityDisjoint(t *testing.T) {
	a := []string{"deploy", "pipeline"}
	b := []string{"lunch", "menu"}
	sim := JaccardSimilarity(a, b)
	assert.Equal(t, 0.0, sim)
}

func TestJaccardSimilarityEmpty(t *testing.T) {
	assert.Equal(t, 0.0, JaccardSimilarity(nil, nil))
	assert.Equal(t, 0.0, JaccardSimilarity([]string{}, []string{}))
}

func TestEndsWithQuestion(t *testing.T) {
	assert.True(t, EndsWithQuestion("what do you think?"))
	assert.True(t, EndsWithQuestion("<p>what do you think?</p>"))
	assert.False(t, EndsWithQuestion("looks good to me"))
	assert.False(t, EndsWithQuestion(""))
}
