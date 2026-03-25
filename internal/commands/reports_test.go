package commands

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestResolveReportsScheduleWindow(t *testing.T) {
	now := time.Date(2026, time.March, 25, 9, 30, 0, 0, time.UTC)

	tests := []struct {
		name      string
		startDate string
		endDate   string
		wantStart string
		wantEnd   string
	}{
		{
			name:      "defaults to today plus 30 days",
			wantStart: "2026-03-25",
			wantEnd:   "2026-04-24",
		},
		{
			name:      "default end is anchored to resolved start",
			startDate: "next month",
			wantStart: "2026-04-25",
			wantEnd:   "2026-05-25",
		},
		{
			name:      "explicit absolute start gets 30 day default end",
			startDate: "2026-04-10",
			wantStart: "2026-04-10",
			wantEnd:   "2026-05-10",
		},
		{
			name:      "explicit end is preserved",
			startDate: "next month",
			endDate:   "2026-05-01",
			wantStart: "2026-04-25",
			wantEnd:   "2026-05-01",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotStart, gotEnd := resolveReportsScheduleWindow(tt.startDate, tt.endDate, now)
			assert.Equal(t, tt.wantStart, gotStart)
			assert.Equal(t, tt.wantEnd, gotEnd)
		})
	}
}
