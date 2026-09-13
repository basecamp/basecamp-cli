package auth

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// syncBuffer is a strings.Builder the drawing goroutine and the test can
// share.
type syncBuffer struct {
	cl collectLogger
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.cl.log(string(p))
	return len(p), nil
}

func (b *syncBuffer) String() string {
	b.cl.mu.Lock()
	defer b.cl.mu.Unlock()
	return strings.Join(b.cl.logs, "")
}

func TestApprovalWaitDrawsCountdownAndClears(t *testing.T) {
	base := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	clock := base
	buf := &syncBuffer{}
	wait := runApprovalWait(buf, base.Add(10*time.Minute), func() time.Time { return clock }, time.Millisecond)

	require.Eventually(t, func() bool { return strings.Contains(buf.String(), "code expires in 10:00") }, time.Second, time.Millisecond)
	clock = base.Add(19 * time.Second)
	require.Eventually(t, func() bool { return strings.Contains(buf.String(), "code expires in 9:41") }, time.Second, time.Millisecond)

	wait.Stop()
	wait.Stop() // idempotent

	out := buf.String()
	assert.True(t, strings.HasSuffix(out, "\r\033[2K"), "the line is cleared when the wait ends")
	assert.Contains(t, out, "Waiting for approval…")
	assert.NotContains(t, out, "\n", "the live line never scrolls")

	var none *approvalWait
	none.Stop()
}

func TestStartApprovalWaitNeedsATerminal(t *testing.T) {
	assert.Nil(t, startApprovalWait(nil, time.Now().Add(time.Minute)))
	assert.Nil(t, startApprovalWait(&strings.Builder{}, time.Now().Add(time.Minute)))
}

func TestRemainingAndExpiresIn(t *testing.T) {
	assert.Equal(t, "9:41", remaining(9*time.Minute+41*time.Second))
	assert.Equal(t, "0:05", remaining(4900*time.Millisecond))
	assert.Equal(t, "0:00", remaining(-time.Second))
	assert.Equal(t, "10 minutes", expiresIn(10*time.Minute))
	assert.Equal(t, "1 minute", expiresIn(time.Minute))
	assert.Equal(t, "90 seconds", expiresIn(90*time.Second))
	assert.Equal(t, "45 seconds", expiresIn(45*time.Second))
}
