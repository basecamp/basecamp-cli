package harness

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestInAgentSession(t *testing.T) {
	tests := []struct {
		name    string
		environ []string
		want    bool
	}{
		{"claude code", []string{"PATH=/bin", "CLAUDECODE=1"}, true},
		{"claude code session id", []string{"CLAUDE_CODE_SESSION_ID=abc"}, true},
		{"codex", []string{"CODEX_THREAD_ID=abc"}, true},
		{"codex home alone is a setting", []string{"CODEX_HOME=/home/x/.codex"}, false},
		{"empty value", []string{"CLAUDECODE="}, false},
		{"plain shell", []string{"PATH=/bin", "HOME=/home/x"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, inAgentSession(tt.environ))
		})
	}
}
