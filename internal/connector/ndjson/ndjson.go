// Package ndjson writes the connector's stdout protocol: one JSON value per
// line, never torn.
//
// Intake's pointer lines and admission's verdict lines are both read by a
// process parsing the stream and watched by a person, so both need the same
// two guarantees. A line is written whole or reported failed, and concurrent
// writers never interleave inside a line. Callers sanitize API-controlled
// strings before encoding (richtext.SanitizeTerminal): JSON escapes C0 controls
// but passes C1 controls such as U+009B through as raw UTF-8.
package ndjson

import (
	"encoding/json"
	"fmt"
	"io"
	"sync"
)

// Writer serializes whole lines onto w. The zero value with a nil w discards.
type Writer struct {
	mu sync.Mutex
	w  io.Writer
}

// NewWriter returns a Writer over w. A nil w discards every line.
func NewWriter(w io.Writer) *Writer { return &Writer{w: w} }

// WriteLine encodes v and writes it followed by a newline, whole.
//
// A writer that takes part of the line is written the rest, and one that takes
// none without an error has failed (io.ErrShortWrite): a torn line is worse
// than no line to whoever parses the stream.
func (l *Writer) WriteLine(v any) error {
	if l == nil || l.w == nil {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("encode line: %w", err)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for rest := append(b, '\n'); len(rest) > 0; {
		n, err := l.w.Write(rest)
		if err != nil {
			return fmt.Errorf("write line: %w", err)
		}
		if n <= 0 {
			return fmt.Errorf("write line: %w", io.ErrShortWrite)
		}
		rest = rest[n:]
	}
	return nil
}
