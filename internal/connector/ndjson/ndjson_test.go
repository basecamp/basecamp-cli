package ndjson

import (
	"bytes"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type threeBytes struct{ bytes.Buffer }

func (w *threeBytes) Write(p []byte) (int, error) {
	if len(p) > 3 {
		p = p[:3]
	}
	return w.Buffer.Write(p)
}

type stuck struct{}

func (stuck) Write([]byte) (int, error) { return 0, nil }

func TestWriteLineCompletesShortWrites(t *testing.T) {
	var w threeBytes
	require.NoError(t, NewWriter(&w).WriteLine(map[string]int{"event_id": 42}))
	assert.Equal(t, "{\"event_id\":42}\n", w.String())
}

func TestWriteLineReportsAWriterThatTakesNothing(t *testing.T) {
	assert.ErrorIs(t, NewWriter(stuck{}).WriteLine(1), io.ErrShortWrite)
}

func TestWriteLineWithNoWriterDiscards(t *testing.T) {
	assert.NoError(t, NewWriter(nil).WriteLine(1))
}
