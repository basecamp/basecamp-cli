package summarize

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
)

// Request describes what to summarize and at what zoom level.
type Request struct {
	ContentKey  string    // human-readable grouping key, e.g. "campfire:123:gap:45-67"
	Content     []Segment // the messages to summarize
	TargetChars int       // desired output length
	Hint        string    // context hint: "gap", "ticker", "scan"
}

// Result holds a completed summary.
type Result struct {
	ContentKey string
	Summary    string
	FromCache  bool
}

// SummaryReadyMsg is sent when an async summary completes.
type SummaryReadyMsg struct {
	ContentKey string
}

// Summarizer provides synchronous and async summary generation.
// When a Provider is available, it uses LLM; otherwise extractive fallback.
type Summarizer struct {
	mu       sync.RWMutex
	provider Provider
	cache    *SummaryCache
	inflight map[string]struct{} // dedup in-flight requests
	sem      chan struct{}
}

// NewSummarizer creates a Summarizer with the given provider and cache.
// If provider is nil, SummarizeSync always returns extractive fallback.
func NewSummarizer(provider Provider, cache *SummaryCache, maxConcurrent int) *Summarizer {
	if maxConcurrent <= 0 {
		maxConcurrent = 3
	}
	return &Summarizer{
		provider: provider,
		cache:    cache,
		inflight: make(map[string]struct{}),
		sem:      make(chan struct{}, maxConcurrent),
	}
}

// SummarizeSync returns a summary immediately. Never blocks on LLM.
// Returns cached LLM result if available, otherwise extractive fallback.
func (s *Summarizer) SummarizeSync(req Request) Result {
	zoom := BucketZoom(req.TargetChars)
	cacheKey := CacheKey(req.ContentKey, zoom, contentHash(req.Content))

	// Check cache
	if s.cache != nil {
		if summary, ok := s.cache.Get(cacheKey); ok {
			return Result{ContentKey: req.ContentKey, Summary: summary, FromCache: true}
		}
	}

	// Extractive fallback (always available)
	summary := ExtractSummary(req.Content, zoom)
	return Result{ContentKey: req.ContentKey, Summary: summary}
}

// Summarize returns a tea.Cmd that runs an async LLM summary.
// On completion, emits SummaryReadyMsg. The caller should then
// call SummarizeSync to get the (now cached) result.
// Returns nil if no provider is configured or request is already in-flight.
// The parent context is used to cancel in-flight LLM calls on shutdown.
func (s *Summarizer) Summarize(ctx context.Context, req Request) tea.Cmd {
	if s.provider == nil {
		return nil
	}

	zoom := BucketZoom(req.TargetChars)
	cacheKey := CacheKey(req.ContentKey, zoom, contentHash(req.Content))

	// Already cached?
	if s.cache != nil {
		if _, ok := s.cache.Get(cacheKey); ok {
			return nil
		}
	}

	// Dedup in-flight
	s.mu.Lock()
	if _, ok := s.inflight[cacheKey]; ok {
		s.mu.Unlock()
		return nil
	}
	s.inflight[cacheKey] = struct{}{}
	s.mu.Unlock()

	return func() tea.Msg {
		defer func() {
			s.mu.Lock()
			delete(s.inflight, cacheKey)
			s.mu.Unlock()
		}()

		// Semaphore — context-aware to avoid blocking after cancellation.
		select {
		case s.sem <- struct{}{}:
		case <-ctx.Done():
			return nil
		}
		defer func() { <-s.sem }()

		prompt := BuildPrompt(req.Content, req.TargetChars, req.Hint)
		maxTokens := req.TargetChars / 3 // rough chars-to-tokens
		if maxTokens < 50 {
			maxTokens = 50
		}

		llmCtx, llmCancel := context.WithTimeout(ctx, 30*time.Second)
		defer llmCancel()
		result, err := s.provider.Complete(llmCtx, prompt, maxTokens)
		if err != nil {
			return nil // silently fall back to extractive
		}

		if s.cache != nil {
			s.cache.Put(cacheKey, result)
		}

		return SummaryReadyMsg{ContentKey: req.ContentKey}
	}
}

// contentHash computes a hash of the actual message content.
func contentHash(segments []Segment) string {
	var b strings.Builder
	for _, seg := range segments {
		fmt.Fprintf(&b, "%s:%s:%s\n", seg.Author, seg.Time, seg.Text)
	}
	h := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(h[:8]) // 16 hex chars is plenty
}
