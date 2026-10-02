//go:build linux || darwin

package connect

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The connector's two outputs, as a reader sees them. The wire types are
// written out here rather than borrowed from the connector, so a change to
// the protocol is a change this package notices.

// Line is one line of the connector's stdout.
type Line struct {
	// Raw is the line as written, without its newline.
	Raw string
	// Err is why the line is not a protocol line; nil for one that is.
	Err error

	// Exactly one of these is set on a protocol line: intake's pointer
	// (which carries no type), admission's verdict, or the handoff's
	// request.
	Pointer *PointerLine
	Event   *EventLine
	Request *RequestLine
}

// PointerLine is intake's line for an event it has newly seen.
type PointerLine struct {
	EventID       int64  `json:"event_id"`
	EventType     string `json:"event_type"`
	Kind          string `json:"kind"`
	Action        string `json:"action"`
	BucketID      int64  `json:"bucket_id"`
	CreatorID     int64  `json:"creator_id"`
	PerformedByID *int64 `json:"performed_by_id"`
	RecordingID   int64  `json:"recording_id"`
	CreatedAt     string `json:"created_at"`
	Lane          string `json:"lane"`
	State         string `json:"state"`
}

// EventLine is admission's line for a verdict.
type EventLine struct {
	Type         string `json:"type"`
	EventID      int64  `json:"event_id"`
	EventType    string `json:"event_type"`
	Trigger      string `json:"trigger"`
	Class        string `json:"class"`
	BucketID     int64  `json:"bucket_id"`
	RecordingID  int64  `json:"recording_id"`
	RecordingURL string `json:"recording_url"`
	RequesterID  int64  `json:"requester_id"`
	State        string `json:"state"`
	Reason       string `json:"reason"`
}

// RequestLine is the handoff's line for a trusted request.
type RequestLine struct {
	Type          string           `json:"type"`
	EventID       int64            `json:"event_id"`
	EventType     string           `json:"event_type"`
	Trigger       string           `json:"trigger"`
	Recording     RequestRecording `json:"recording"`
	ReplyTo       RequestReplyTo   `json:"reply_to"`
	RequesterID   int64            `json:"requester_id"`
	RequesterName string           `json:"requester_name"`
	Acknowledge   bool             `json:"acknowledge"`
	Content       string           `json:"content"`
	// ContentUpdatedAt is a timestamp, kept as written.
	ContentUpdatedAt string `json:"content_updated_at"`
}

// RequestRecording is the recording a request is about.
type RequestRecording struct {
	BucketID    int64  `json:"bucket_id"`
	ProjectName string `json:"project_name"`
	RecordingID int64  `json:"recording_id"`
	Type        string `json:"type"`
	Title       string `json:"title"`
	URL         string `json:"url"`
}

// RequestReplyTo is where a request's acknowledgement and reply go.
type RequestReplyTo struct {
	Kind        string `json:"kind"`
	RecordingID int64  `json:"recording_id"`
}

// parseLine reads one stdout line. A line that is not one JSON object, or
// whose type is not one the protocol has, says why in Err.
func parseLine(raw string) Line {
	l := Line{Raw: raw}
	var head struct {
		Type *string `json:"type"`
	}
	if err := strictUnmarshal(raw, &head); err != nil {
		l.Err = err
		return l
	}
	switch {
	case head.Type == nil:
		l.Pointer = &PointerLine{}
		l.Err = strictUnmarshal(raw, l.Pointer)
	case *head.Type == "event":
		l.Event = &EventLine{}
		l.Err = strictUnmarshal(raw, l.Event)
	case *head.Type == "request":
		l.Request = &RequestLine{}
		l.Err = strictUnmarshal(raw, l.Request)
	default:
		l.Err = fmt.Errorf("unknown line type %q", *head.Type)
	}
	return l
}

// strictUnmarshal decodes raw, which must be one JSON object and nothing
// else: json.Unmarshal refuses trailing data, and would take a null.
func strictUnmarshal(raw string, v any) error {
	if !strings.HasPrefix(raw, "{") {
		return errors.New("not a JSON object")
	}
	return json.Unmarshal([]byte(raw), v)
}

// LogLine is one line of the connector's stderr.
type LogLine struct {
	// Raw is the line as written, without its newline.
	Raw string
	// Level and Msg are the slog record's; both are empty for a line that
	// is not a slog record.
	Level string
	Msg   string
	// Attrs are the record's other attributes, by key, unquoted.
	Attrs map[string]string
}

// parseLogLine reads one stderr line as slog's text handler writes it:
// key=value pairs separated by spaces, where a value is bare or a Go-quoted
// string. A line that does not read that way keeps only Raw.
func parseLogLine(raw string) LogLine {
	l := LogLine{Raw: raw}
	attrs := map[string]string{}
	rest := raw
	for rest != "" {
		eq := strings.IndexByte(rest, '=')
		if eq <= 0 || strings.ContainsAny(rest[:eq], " \"") {
			return l
		}
		key := rest[:eq]
		rest = rest[eq+1:]
		var value string
		if strings.HasPrefix(rest, `"`) {
			quoted, err := strconv.QuotedPrefix(rest)
			if err != nil {
				return l
			}
			if value, err = strconv.Unquote(quoted); err != nil {
				return l
			}
			rest = rest[len(quoted):]
		} else {
			end := strings.IndexByte(rest, ' ')
			if end < 0 {
				end = len(rest)
			}
			value, rest = rest[:end], rest[end:]
		}
		attrs[key] = value
		rest = strings.TrimPrefix(rest, " ")
	}
	level, hasLevel := attrs["level"]
	msg, hasMsg := attrs["msg"]
	if !hasLevel || !hasMsg {
		return l
	}
	delete(attrs, "level")
	delete(attrs, "msg")
	delete(attrs, "time")
	l.Level, l.Msg, l.Attrs = level, msg, attrs
	return l
}

// The checks on stdout only mean something if they can fail: a line that
// is not one JSON object of a known shape is refused.
func TestParseLineRefusesWhatIsNotAProtocolLine(t *testing.T) {
	for _, raw := range []string{
		``,
		`connector: running`,
		`null`,
		`[1]`,
		`"request"`,
		`{"type":"bogus","event_id":1}`,
		`{"event_id":1} {"event_id":2}`,
		`{"type":"request","event_id":"one"}`,
		`{"event_id":1`,
	} {
		assert.Error(t, parseLine(raw).Err, "%q", raw)
	}

	l := parseLine(`{"event_id":7,"lane":"poll","state":"seen"}`)
	require.NoError(t, l.Err)
	require.NotNil(t, l.Pointer)
	assert.Equal(t, int64(7), l.Pointer.EventID)
	assert.Equal(t, "poll", l.Pointer.Lane)
	l = parseLine(`{"type":"event","event_id":8,"state":"discarded","reason":"untrusted_performer"}`)
	require.NoError(t, l.Err)
	require.NotNil(t, l.Event)
	assert.Equal(t, "untrusted_performer", l.Event.Reason)
	l = parseLine(`{"type":"request","event_id":9,"reply_to":{"kind":"comment","recording_id":3}}`)
	require.NoError(t, l.Err)
	require.NotNil(t, l.Request)
	assert.Equal(t, RequestReplyTo{Kind: "comment", RecordingID: 3}, l.Request.ReplyTo)
}

func TestParseLogLineReadsSlogText(t *testing.T) {
	l := parseLogLine(`time=2026-10-02T13:41:50.583-04:00 level=WARN msg="the hold stands; \"release\" it" since=2026-10-02 by="Rob Z"`)
	assert.Equal(t, "WARN", l.Level)
	assert.Equal(t, `the hold stands; "release" it`, l.Msg)
	assert.Equal(t, map[string]string{"since": "2026-10-02", "by": "Rob Z"}, l.Attrs)

	for _, raw := range []string{`panic: boom`, `WARNING: DATA RACE`, `level=INFO`, `msg="unterminated`} {
		l := parseLogLine(raw)
		assert.Empty(t, l.Msg, "%q", raw)
		assert.Equal(t, raw, l.Raw)
	}
}
