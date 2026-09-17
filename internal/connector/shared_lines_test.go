package connector

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/connector/admission"
	"github.com/basecamp/basecamp-cli/internal/connector/ndjson"
)

// A sink that is safe for concurrent use but takes three bytes at a time, so
// two producers that do not share a lock tear each other's lines. It is a
// value type, which is the case the writer registry cannot settle by itself.
type valueSink struct {
	mu  *sync.Mutex
	buf *bytes.Buffer
}

func (s valueSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(p) > 3 {
		p = p[:3]
	}
	return s.buf.Write(p)
}

// Intake's pointer lines and admission's verdict lines share one stdout, so
// the composition hands them one writer.
func TestIntakeAndAdmissionShareOneLineWriter(t *testing.T) {
	sink := valueSink{mu: &sync.Mutex{}, buf: &bytes.Buffer{}}
	testSink = sink
	lines := ndjson.NewWriter(sink)

	intake, _, _ := newTestIntakeLines(t, lines)
	verdicts := &lineWriterFor{lines: lines}

	ctx := context.Background()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for id := int64(1); id <= 200; id++ {
			require.NoError(t, intake.ingest(ctx, testEvent(id), LanePoll))
		}
	}()
	go func() {
		defer wg.Done()
		for id := int64(1); id <= 200; id++ {
			require.NoError(t, verdicts.write(id))
		}
	}()
	wg.Wait()

	scanner := bufio.NewScanner(bytes.NewReader(sink.buf.Bytes()))
	lineCount := 0
	for scanner.Scan() {
		var v map[string]any
		require.NoError(t, json.Unmarshal(scanner.Bytes(), &v), "torn line %d: %q", lineCount, scanner.Text())
		lineCount++
	}
	assert.Equal(t, 400, lineCount)
}

// lineWriterFor stands in for admission's verdict lines over the same writer.
type lineWriterFor struct{ lines *ndjson.Writer }

func (l *lineWriterFor) write(id int64) error {
	return l.lines.WriteLine(admission.Line{Type: "event", EventID: id, EventType: "comment.created"})
}

var testSink valueSink

// linesSink is the sink the test's writer was built for, so a mutation can
// build a second writer over the same one.
func linesSink(*ndjson.Writer) valueSink { return testSink }
