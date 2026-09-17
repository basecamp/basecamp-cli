package connector

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Control characters built from their code points, so the canaries are
// unmistakable in review.
var (
	esc = string(rune(0x1b))
	bel = string(rune(0x07))
	csi = string(rune(0x9b)) // C1 Control Sequence Introducer
	osc = string(rune(0x9d)) // C1 Operating System Command
	st  = string(rune(0x9c)) // C1 String Terminator
)

// The pointer line is a wire, and it is also what a person watching the
// connector sees. The event type, kind and action come from Basecamp; JSON
// escapes C0 controls but passes C1 controls such as CSI through as raw UTF-8,
// which a terminal executes.
func TestPointerLineCarriesNoTerminalControls(t *testing.T) {
	var out bytes.Buffer
	intake, _, _ := newTestIntake(t, nil, &out)
	event := testEvent(1)
	event.EventType = "comment.created" + csi + "31m" + esc + "]0;owned" + bel
	event.Kind = "comment_created" + esc + "[2J"
	event.Action = "created" + osc + "8;;evil" + st
	require.NoError(t, intake.ingest(context.Background(), event, LanePoll))

	line := out.String()
	for name, control := range map[string]string{"ESC": esc, "CSI": csi, "OSC": osc, "ST": st, "BEL": bel} {
		assert.NotContains(t, line, control, name)
	}
	var pointer Pointer
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(line)), &pointer))
	assert.True(t, strings.HasPrefix(pointer.EventType, "comment.created"))
}

// shortWriter accepts at most three bytes per call.
type shortWriter struct{ bytes.Buffer }

func (w *shortWriter) Write(p []byte) (int, error) {
	if len(p) > 3 {
		p = p[:3]
	}
	return w.Buffer.Write(p)
}

// A writer that takes part of a line is written the rest: a torn line is worse
// than no line to whoever parses the stream.
func TestPointerLineIsWrittenWholeThroughShortWrites(t *testing.T) {
	var out shortWriter
	intake, _, _ := newTestIntake(t, nil, &out)
	require.NoError(t, intake.ingest(context.Background(), testEvent(1), LanePoll))

	var pointer Pointer
	require.NoError(t, json.Unmarshal(bytes.TrimSpace(out.Bytes()), &pointer))
	assert.Equal(t, int64(1), pointer.EventID)
	assert.True(t, bytes.HasSuffix(out.Bytes(), []byte("\n")))
}
