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
	"reflect"
	"sync"
)

// Writer serializes whole lines onto w. The zero value with a nil w discards.
type Writer struct {
	mu sync.Mutex
	w  io.Writer
}

// NewWriter returns the Writer for w. A nil w discards every line.
//
// There is one Writer, and so one lock, per sink for the life of the process.
// Intake and admission each build their writer from the stdout they are
// handed; if those were two Writers their locks would not coordinate, and a
// pointer line and a verdict line could tear each other inside one write.
// Keying on the sink makes that impossible whoever calls this, without every
// caller having to agree to pass one instance around.
//
// Only reference-like sinks are keyed — a pointer, a channel, an unsafe
// pointer — which covers every real one, os.Stdout included. A value-typed
// sink is a copy rather than the same sink, and hashing one can panic when it
// holds an uncomparable field, so it gets a Writer of its own. The map holds
// one entry per distinct sink for the life of the process; a connector has
// one stdout.
func NewWriter(w io.Writer) *Writer {
	if w == nil {
		return &Writer{}
	}
	switch reflect.TypeOf(w).Kind() {
	case reflect.Pointer, reflect.Chan, reflect.UnsafePointer:
	default:
		return &Writer{w: w}
	}
	sinksMu.Lock()
	defer sinksMu.Unlock()
	if existing, ok := sinks[w]; ok {
		return existing
	}
	writer := &Writer{w: w}
	sinks[w] = writer
	return writer
}

var (
	sinksMu sync.Mutex
	sinks   = map[io.Writer]*Writer{}
)

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
