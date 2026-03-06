package summarize

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBucketZoom(t *testing.T) {
	tests := []struct {
		input int
		want  ZoomLevel
	}{
		{0, Zoom40},
		{1, Zoom40},
		{40, Zoom40},
		{41, Zoom80},
		{80, Zoom80},
		{81, Zoom200},
		{200, Zoom200},
		{201, Zoom500},
		{500, Zoom500},
		{501, Zoom1000},
		{1000, Zoom1000},
		{5000, Zoom1000},
	}
	for _, tt := range tests {
		got := BucketZoom(tt.input)
		if got != tt.want {
			t.Errorf("BucketZoom(%d) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestExtractSummary_Empty(t *testing.T) {
	result := ExtractSummary(nil, Zoom40)
	if result != "" {
		t.Errorf("ExtractSummary(nil) = %q, want empty", result)
	}
}

func TestExtractSummary_SingleSegment(t *testing.T) {
	segs := []Segment{{Author: "Alice", Time: "10:00", Text: "Hello world"}}

	// Zoom40: "1 messages from Alice"
	result := ExtractSummary(segs, Zoom40)
	if result == "" {
		t.Fatal("expected non-empty result for Zoom40")
	}
	if runeLen(result) > 40 {
		t.Errorf("Zoom40 result too long: %d runes: %q", runeLen(result), result)
	}

	// Zoom80
	result = ExtractSummary(segs, Zoom80)
	if runeLen(result) > 80 {
		t.Errorf("Zoom80 result too long: %d runes: %q", runeLen(result), result)
	}

	// Zoom200
	result = ExtractSummary(segs, Zoom200)
	if runeLen(result) > 200 {
		t.Errorf("Zoom200 result too long: %d runes: %q", runeLen(result), result)
	}
}

func TestExtractSummary_Zoom40(t *testing.T) {
	segs := []Segment{
		{Author: "Alice", Text: "Hello"},
		{Author: "Bob", Text: "Hi there"},
		{Author: "Alice", Text: "How are you?"},
	}
	result := ExtractSummary(segs, Zoom40)
	if runeLen(result) > 40 {
		t.Errorf("Zoom40 result too long: %d runes: %q", runeLen(result), result)
	}
	// Should mention message count
	if result == "" {
		t.Error("expected non-empty result")
	}
}

func TestExtractSummary_Zoom80(t *testing.T) {
	segs := []Segment{
		{Author: "Alice", Text: "First message"},
		{Author: "Bob", Text: "Last message here"},
	}
	result := ExtractSummary(segs, Zoom80)
	if runeLen(result) > 80 {
		t.Errorf("Zoom80 result too long: %d runes: %q", runeLen(result), result)
	}
}

func TestExtractSummary_Zoom200(t *testing.T) {
	segs := []Segment{
		{Author: "Alice", Text: "This is the first message in the conversation"},
		{Author: "Bob", Text: "Some middle stuff"},
		{Author: "Charlie", Text: "Another middle"},
		{Author: "Dave", Text: "This is the last message in the conversation"},
	}
	result := ExtractSummary(segs, Zoom200)
	if runeLen(result) > 200 {
		t.Errorf("Zoom200 result too long: %d runes: %q", runeLen(result), result)
	}
}

func TestExtractSummary_Zoom500(t *testing.T) {
	segs := make([]Segment, 10)
	for i := range segs {
		segs[i] = Segment{Author: "User", Text: "Message content here that is reasonably long"}
	}
	result := ExtractSummary(segs, Zoom500)
	if runeLen(result) > 500 {
		t.Errorf("Zoom500 result too long: %d runes: %q", runeLen(result), result)
	}
}

func TestExtractSummary_Zoom1000(t *testing.T) {
	segs := make([]Segment, 20)
	for i := range segs {
		segs[i] = Segment{Author: "User", Text: "Message content here"}
	}
	result := ExtractSummary(segs, Zoom1000)
	if runeLen(result) > 1000 {
		t.Errorf("Zoom1000 result too long: %d runes: %q", runeLen(result), result)
	}
}

func TestCacheKey_Deterministic(t *testing.T) {
	k1 := CacheKey("campfire:1", Zoom200, "abc123")
	k2 := CacheKey("campfire:1", Zoom200, "abc123")
	if k1 != k2 {
		t.Errorf("same inputs produced different keys: %s vs %s", k1, k2)
	}
}

func TestCacheKey_DifferentContent(t *testing.T) {
	k1 := CacheKey("campfire:1", Zoom200, "abc123")
	k2 := CacheKey("campfire:1", Zoom200, "def456")
	if k1 == k2 {
		t.Error("different content hashes produced same key")
	}
}

func TestCacheKey_DifferentZoom(t *testing.T) {
	k1 := CacheKey("campfire:1", Zoom40, "abc123")
	k2 := CacheKey("campfire:1", Zoom200, "abc123")
	if k1 == k2 {
		t.Error("different zoom levels produced same key")
	}
}

func TestSummaryCache_PutGet(t *testing.T) {
	dir := t.TempDir()
	cache := NewSummaryCache(dir, time.Hour, 100)

	cache.Put("key1", "hello world")
	got, ok := cache.Get("key1")
	if !ok {
		t.Fatal("expected cache hit")
	}
	if got != "hello world" {
		t.Errorf("got %q, want %q", got, "hello world")
	}
}

func TestSummaryCache_Miss(t *testing.T) {
	dir := t.TempDir()
	cache := NewSummaryCache(dir, time.Hour, 100)

	_, ok := cache.Get("nonexistent")
	if ok {
		t.Fatal("expected cache miss")
	}
}

func TestSummaryCache_TTLExpiry(t *testing.T) {
	dir := t.TempDir()
	cache := NewSummaryCache(dir, 10*time.Millisecond, 100)

	cache.Put("key1", "hello")
	time.Sleep(50 * time.Millisecond)

	_, ok := cache.Get("key1")
	if ok {
		t.Fatal("expected cache miss after TTL expiry")
	}
}

func TestSummaryCache_Eviction(t *testing.T) {
	dir := t.TempDir()
	cache := NewSummaryCache(dir, time.Hour, 2)

	cache.Put("key1", "first")
	time.Sleep(time.Millisecond) // ensure different timestamps
	cache.Put("key2", "second")
	time.Sleep(time.Millisecond)
	cache.Put("key3", "third")

	// key1 should have been evicted from memory (oldest)
	if len(cache.memory) > 2 {
		t.Errorf("memory map should have at most 2 entries, got %d", len(cache.memory))
	}
}

func TestSummaryCache_DiskPersistence(t *testing.T) {
	dir := t.TempDir()

	// Write with one cache instance
	cache1 := NewSummaryCache(dir, time.Hour, 100)
	cache1.Put("key1", "persisted")

	// Read with a new cache instance (empty memory)
	cache2 := NewSummaryCache(dir, time.Hour, 100)
	got, ok := cache2.Get("key1")
	if !ok {
		t.Fatal("expected disk cache hit")
	}
	if got != "persisted" {
		t.Errorf("got %q, want %q", got, "persisted")
	}

	// Verify it was promoted to memory
	if _, ok := cache2.memory["key1"]; !ok {
		t.Error("expected entry to be promoted to memory cache")
	}
}

func TestSummaryCache_DiskFile(t *testing.T) {
	dir := t.TempDir()
	cache := NewSummaryCache(dir, time.Hour, 100)
	cache.Put("testkey", "test value")

	// Verify the file exists on disk
	path := filepath.Join(dir, "testkey.json")
	if _, err := os.Stat(path); err != nil {
		t.Errorf("expected cache file at %s: %v", path, err)
	}
}

func TestSummarizer_SummarizeSync_Fallback(t *testing.T) {
	segs := []Segment{
		{Author: "Alice", Text: "Hello"},
		{Author: "Bob", Text: "World"},
	}

	s := NewSummarizer(nil, nil, 3)
	result := s.SummarizeSync(Request{
		ContentKey:  "test:1",
		Content:     segs,
		TargetChars: 80,
		Hint:        HintGap,
	})

	if result.Summary == "" {
		t.Error("expected non-empty summary from fallback")
	}
	if result.FromCache {
		t.Error("expected FromCache=false for fallback")
	}
	if result.ContentKey != "test:1" {
		t.Errorf("got ContentKey %q, want %q", result.ContentKey, "test:1")
	}
}

func TestSummarizer_SummarizeSync_CachedResult(t *testing.T) {
	dir := t.TempDir()
	cache := NewSummaryCache(dir, time.Hour, 100)

	segs := []Segment{{Author: "Alice", Text: "Hello"}}

	// Pre-populate cache
	zoom := BucketZoom(80)
	key := CacheKey("test:1", zoom, contentHash(segs))
	cache.Put(key, "cached summary")

	s := NewSummarizer(nil, cache, 3)
	result := s.SummarizeSync(Request{
		ContentKey:  "test:1",
		Content:     segs,
		TargetChars: 80,
	})

	if result.Summary != "cached summary" {
		t.Errorf("got %q, want %q", result.Summary, "cached summary")
	}
	if !result.FromCache {
		t.Error("expected FromCache=true")
	}
}

func TestSummarizer_Summarize_NilProvider(t *testing.T) {
	s := NewSummarizer(nil, nil, 3)
	cmd := s.Summarize(Request{
		ContentKey:  "test:1",
		Content:     []Segment{{Author: "A", Text: "B"}},
		TargetChars: 80,
	})
	if cmd != nil {
		t.Error("expected nil cmd when provider is nil")
	}
}

func TestContentHash_Deterministic(t *testing.T) {
	segs := []Segment{
		{Author: "Alice", Time: "10:00", Text: "Hello"},
		{Author: "Bob", Time: "10:01", Text: "World"},
	}
	h1 := contentHash(segs)
	h2 := contentHash(segs)
	if h1 != h2 {
		t.Errorf("same content produced different hashes: %s vs %s", h1, h2)
	}
}

func TestContentHash_DifferentContent(t *testing.T) {
	segs1 := []Segment{{Author: "Alice", Text: "Hello"}}
	segs2 := []Segment{{Author: "Alice", Text: "World"}}
	h1 := contentHash(segs1)
	h2 := contentHash(segs2)
	if h1 == h2 {
		t.Error("different content produced same hash")
	}
}

func TestContentHash_Empty(t *testing.T) {
	h := contentHash(nil)
	if h == "" {
		t.Error("expected non-empty hash even for nil segments")
	}
}

func TestBuildPrompt(t *testing.T) {
	segs := []Segment{
		{Author: "Alice", Time: "10:00", Text: "Hello"},
	}

	prompt := BuildPrompt(segs, 200, HintGap)
	if prompt == "" {
		t.Error("expected non-empty prompt")
	}

	prompt = BuildPrompt(segs, 40, HintTicker)
	if prompt == "" {
		t.Error("expected non-empty prompt for ticker hint")
	}

	prompt = BuildPrompt(segs, 500, HintScan)
	if prompt == "" {
		t.Error("expected non-empty prompt for scan hint")
	}

	// Unknown hint
	prompt = BuildPrompt(segs, 100, "unknown")
	if prompt == "" {
		t.Error("expected non-empty prompt for unknown hint")
	}
}

func TestDetectProvider(t *testing.T) {
	// "none" explicitly disables provider detection.
	p := DetectProvider("none", "", "", "")
	if p != nil {
		t.Error("expected nil provider for 'none'")
	}

	// "disabled" also returns nil.
	p = DetectProvider("disabled", "", "", "")
	if p != nil {
		t.Error("expected nil provider for 'disabled'")
	}
}

func TestTruncateRunes(t *testing.T) {
	tests := []struct {
		input    string
		max      int
		wantLen  int
		wantLast rune
	}{
		{"hello", 10, 5, 'o'},
		{"hello", 5, 5, 'o'},
		{"hello", 4, 4, '\u2026'},
		{"hello", 1, 1, '\u2026'},
		{"", 5, 0, 0},
	}
	for _, tt := range tests {
		got := truncateRunes(tt.input, tt.max)
		runes := []rune(got)
		if len(runes) != tt.wantLen {
			t.Errorf("truncateRunes(%q, %d) len = %d, want %d; got %q",
				tt.input, tt.max, len(runes), tt.wantLen, got)
		}
		if tt.wantLast != 0 && len(runes) > 0 && runes[len(runes)-1] != tt.wantLast {
			t.Errorf("truncateRunes(%q, %d) last rune = %c, want %c",
				tt.input, tt.max, runes[len(runes)-1], tt.wantLast)
		}
	}
}

func TestExtractSummary_TwoSegments_Zoom200(t *testing.T) {
	segs := []Segment{
		{Author: "Alice", Text: "First"},
		{Author: "Bob", Text: "Second"},
	}
	result := ExtractSummary(segs, Zoom200)
	if result == "" {
		t.Error("expected non-empty result")
	}
	if runeLen(result) > 200 {
		t.Errorf("result too long: %d runes", runeLen(result))
	}
}

func TestExtractSummary_FourSegments_Zoom500(t *testing.T) {
	segs := []Segment{
		{Author: "Alice", Text: "First"},
		{Author: "Bob", Text: "Second"},
		{Author: "Charlie", Text: "Third"},
		{Author: "Dave", Text: "Fourth"},
	}
	result := ExtractSummary(segs, Zoom500)
	if result == "" {
		t.Error("expected non-empty result")
	}
	// With <= 4 segments, all should be included
	if runeLen(result) > 500 {
		t.Errorf("result too long: %d runes", runeLen(result))
	}
}
