package ndjson

import (
	"bufio"
	"bytes"
	"encoding/json"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// lockedShortWriter is a sink that is safe for concurrent use but takes at
// most three bytes per call, so two writers that do not share a lock can
// interleave inside a line.
type lockedShortWriter struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (w *lockedShortWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(p) > 3 {
		p = p[:3]
	}
	return w.buf.Write(p)
}

// One process stdout has one lock. Intake and admission each build their writer
// for the same sink; those writers must be one writer, or their lines tear
// each other.
func TestWritersForOneSinkShareOneLock(t *testing.T) {
	sink := &lockedShortWriter{}
	intake, admission := NewWriter(sink), NewWriter(sink)

	var wg sync.WaitGroup
	for _, w := range []*Writer{intake, admission} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 300 {
				require.NoError(t, w.WriteLine(map[string]any{"line": i, "padding": "0123456789abcdef"}))
			}
		}()
	}
	wg.Wait()

	scanner := bufio.NewScanner(bytes.NewReader(sink.buf.Bytes()))
	lines := 0
	for scanner.Scan() {
		var v map[string]any
		require.NoError(t, json.Unmarshal(scanner.Bytes(), &v), "line %d is not whole JSON: %q", lines, scanner.Text())
		lines++
	}
	assert.Equal(t, 600, lines)
}

func TestWritersForDifferentSinksAreIndependent(t *testing.T) {
	var a, b bytes.Buffer
	assert.NotSame(t, NewWriter(&a), NewWriter(&b))
	assert.Same(t, NewWriter(&a), NewWriter(&a))
}

// A sink whose type is comparable but whose fields are not panics when hashed.
// Only reference-like sinks are keyed, so such a sink never reaches the map.
type wrapperSink struct {
	inner any
	buf   *bytes.Buffer
}

func (w wrapperSink) Write(p []byte) (int, error) { return w.buf.Write(p) }

func TestAnUnhashableSinkIsNotKeyed(t *testing.T) {
	sink := wrapperSink{inner: []int{1, 2, 3}, buf: &bytes.Buffer{}}
	require.NotPanics(t, func() {
		require.NoError(t, NewWriter(sink).WriteLine(map[string]int{"line": 1}))
	})
	assert.Equal(t, "{\"line\":1}\n", sink.buf.String())
}
