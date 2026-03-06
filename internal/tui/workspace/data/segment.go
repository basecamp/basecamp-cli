package data

import (
	"sort"
	"time"
)

// Segment is a group of related campfire lines from one room.
type Segment struct {
	RoomID    RoomID
	RoomName  string
	Lines     []RiverLine
	StartTime time.Time
	EndTime   time.Time
	Sealed    bool // true when segment is complete (gap or room switch)
}

// SegmenterConfig tunes the conversation detection heuristics.
type SegmenterConfig struct {
	// SealAfter is the max silence before a segment is sealed.
	// 0 means use adaptive gap threshold.
	SealAfter time.Duration
	// MinGap is the minimum adaptive gap threshold.
	MinGap time.Duration // default 1 min
	// MaxGap is the maximum adaptive gap threshold.
	MaxGap time.Duration // default 10 min
	// AnnouncementMinChars for standalone announcement detection.
	AnnouncementMinChars int // default 200
	// ContinuityThreshold is the score below which a new segment starts.
	ContinuityThreshold float64 // default 0.35
}

// DefaultSegmenterConfig returns sensible defaults.
func DefaultSegmenterConfig() SegmenterConfig {
	return SegmenterConfig{
		MinGap:               1 * time.Minute,
		MaxGap:               10 * time.Minute,
		AnnouncementMinChars: 200,
		ContinuityThreshold:  0.35,
	}
}

// Segmenter groups incoming campfire lines into conversation segments.
type Segmenter struct {
	segments   []*Segment
	openSegs   map[string]*Segment // key is RoomID.Key()
	lastSeenID map[string]int64    // highest line ID seen per room
	config     SegmenterConfig
}

// NewSegmenter creates a Segmenter with the given config.
func NewSegmenter(config SegmenterConfig) *Segmenter {
	return &Segmenter{
		openSegs:   make(map[string]*Segment),
		lastSeenID: make(map[string]int64),
		config:     config,
	}
}

// IngestSnapshot processes a full snapshot of lines from one room.
// Only lines with IDs newer than lastSeenID are processed (dedup).
func (s *Segmenter) IngestSnapshot(room RoomID, roomName string, lines []CampfireLineInfo) {
	key := room.Key()
	lastID := s.lastSeenID[key]
	for _, line := range lines {
		if line.ID <= lastID {
			continue
		}
		s.ingestOne(room, roomName, line)
	}
	if len(lines) > 0 {
		s.lastSeenID[key] = lines[len(lines)-1].ID
	}
}

func (s *Segmenter) ingestOne(room RoomID, roomName string, line CampfireLineInfo) {
	key := room.Key()
	rl := RiverLine{
		CampfireLineInfo: line,
		Room:             room,
		RoomName:         roomName,
	}

	open, exists := s.openSegs[key]

	// Check for announcement: long message from new speaker after gap
	if exists && len(open.Lines) > 0 {
		stripped := stripTags(line.Body)
		lastLine := open.Lines[len(open.Lines)-1]
		gap := line.CreatedAtTS.Sub(lastLine.CreatedAtTS)
		isNewSpeaker := line.Creator != lastLine.Creator
		if len([]rune(stripped)) > s.config.AnnouncementMinChars && isNewSpeaker && gap > 2*time.Minute {
			// Seal current segment and start standalone announcement
			s.sealSegment(key)
			exists = false
		}
	}

	if !exists || open == nil {
		// Start new segment
		seg := &Segment{
			RoomID:    room,
			RoomName:  roomName,
			Lines:     []RiverLine{rl},
			StartTime: line.CreatedAtTS,
			EndTime:   line.CreatedAtTS,
		}
		s.openSegs[key] = seg
		s.segments = append(s.segments, seg)
		return
	}

	// Compute continuity score
	score := s.continuityScore(open, rl)
	if score < s.config.ContinuityThreshold {
		// Seal current and start new
		s.sealSegment(key)
		seg := &Segment{
			RoomID:    room,
			RoomName:  roomName,
			Lines:     []RiverLine{rl},
			StartTime: line.CreatedAtTS,
			EndTime:   line.CreatedAtTS,
		}
		s.openSegs[key] = seg
		s.segments = append(s.segments, seg)
		return
	}

	// Extend current segment
	open.Lines = append(open.Lines, rl)
	open.EndTime = line.CreatedAtTS
}

// continuityScore computes a 0.0-1.0 score for whether a new line
// continues the current segment.
func (s *Segmenter) continuityScore(seg *Segment, newLine RiverLine) float64 {
	if len(seg.Lines) == 0 {
		return 0
	}
	lastLine := seg.Lines[len(seg.Lines)-1]
	gap := newLine.CreatedAtTS.Sub(lastLine.CreatedAtTS)

	var score float64

	// 1. Temporal proximity (weight 0.40)
	gapThreshold := s.adaptiveGapThreshold(seg)
	if gap <= 0 {
		score += 0.40
	} else if gap < gapThreshold {
		score += 0.40 * (1.0 - float64(gap)/float64(gapThreshold))
	}

	// 2. Participant continuity (weight 0.20)
	// Check if the new speaker appeared in the last 5 messages
	recentCount := 5
	if len(seg.Lines) < recentCount {
		recentCount = len(seg.Lines)
	}
	for i := len(seg.Lines) - recentCount; i < len(seg.Lines); i++ {
		if seg.Lines[i].Creator == newLine.Creator {
			score += 0.20
			break
		}
	}

	// 3. @mention chain (weight 0.15)
	// Check if last message mentions someone who is now speaking
	lastMentions := ExtractMentions(lastLine.Body)
	for _, name := range lastMentions {
		if name == newLine.Creator {
			score += 0.15
			break
		}
	}

	// 4. Question-answer (weight 0.10)
	if EndsWithQuestion(lastLine.Body) && newLine.Creator != lastLine.Creator {
		score += 0.10
	}

	// 5. Burst detection (weight 0.10)
	if gap < 30*time.Second && len(seg.Lines) >= 2 {
		score += 0.10
	}

	// 6. Lexical similarity (weight 0.05)
	lastTokens := Tokenize(lastLine.Body)
	newTokens := Tokenize(newLine.Body)
	jaccard := JaccardSimilarity(lastTokens, newTokens)
	score += 0.05 * jaccard

	return score
}

// adaptiveGapThreshold computes the gap threshold based on recent message cadence.
// Median inter-message gap from last 6 messages x 3, clamped to [MinGap, MaxGap].
func (s *Segmenter) adaptiveGapThreshold(seg *Segment) time.Duration {
	if s.config.SealAfter > 0 {
		return s.config.SealAfter
	}
	n := len(seg.Lines)
	if n < 2 {
		return s.config.MaxGap
	}

	// Collect inter-message gaps from last 6 messages
	count := 6
	if n-1 < count {
		count = n - 1
	}
	gaps := make([]time.Duration, 0, count)
	for i := n - count; i < n; i++ {
		if i > 0 {
			g := seg.Lines[i].CreatedAtTS.Sub(seg.Lines[i-1].CreatedAtTS)
			if g > 0 {
				gaps = append(gaps, g)
			}
		}
	}
	if len(gaps) == 0 {
		return s.config.MaxGap
	}

	// Median
	sort.Slice(gaps, func(i, j int) bool { return gaps[i] < gaps[j] })
	median := gaps[len(gaps)/2]

	threshold := median * 3
	if threshold < s.config.MinGap {
		threshold = s.config.MinGap
	}
	if threshold > s.config.MaxGap {
		threshold = s.config.MaxGap
	}
	return threshold
}

func (s *Segmenter) sealSegment(roomKey string) {
	if seg, ok := s.openSegs[roomKey]; ok {
		seg.Sealed = true
		delete(s.openSegs, roomKey)
	}
}

// SealStale seals any open segments where the last message is older than maxAge.
func (s *Segmenter) SealStale(now time.Time, maxAge time.Duration) {
	for key, seg := range s.openSegs {
		if now.Sub(seg.EndTime) > maxAge {
			seg.Sealed = true
			delete(s.openSegs, key)
		}
	}
}

// Segments returns all segments, ordered for River display:
// sealed segments sorted by EndTime, then open segments with
// most recently active last.
func (s *Segmenter) Segments() []*Segment {
	var sealed, open []*Segment
	for _, seg := range s.segments {
		if seg.Sealed {
			sealed = append(sealed, seg)
		} else {
			open = append(open, seg)
		}
	}
	sort.Slice(sealed, func(i, j int) bool {
		return sealed[i].EndTime.Before(sealed[j].EndTime)
	})
	sort.Slice(open, func(i, j int) bool {
		return open[i].EndTime.Before(open[j].EndTime)
	})
	result := make([]*Segment, 0, len(sealed)+len(open))
	result = append(result, sealed...)
	result = append(result, open...)
	return result
}

// PruneRoom removes all segments and tracking state for a room.
// Called when a room falls out of the active Bonfire set.
func (s *Segmenter) PruneRoom(roomKey string) {
	delete(s.openSegs, roomKey)
	delete(s.lastSeenID, roomKey)
	filtered := s.segments[:0]
	for _, seg := range s.segments {
		if seg.RoomID.Key() != roomKey {
			filtered = append(filtered, seg)
		}
	}
	// Nil out stale slots so GC can collect pruned segments.
	for i := len(filtered); i < len(s.segments); i++ {
		s.segments[i] = nil
	}
	s.segments = filtered
}

// SegmentCount returns the total number of segments.
func (s *Segmenter) SegmentCount() int {
	return len(s.segments)
}
