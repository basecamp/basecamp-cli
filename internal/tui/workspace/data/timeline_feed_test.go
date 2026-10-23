package data

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// activityDaysTransport serves progress.json the way bc3 does: one day per
// request, today unless `date` names another day.
type activityDaysTransport struct {
	today string
	days  map[string][]int64
	asked []string
}

func (m *activityDaysTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	header := make(http.Header)
	header.Set("Content-Type", "application/json")
	if req.URL.Path != "/99999/reports/progress.json" {
		return &http.Response{StatusCode: 404, Body: io.NopCloser(strings.NewReader(`{}`)), Header: header, Request: req}, nil
	}
	date := req.URL.Query().Get("date")
	m.asked = append(m.asked, date)
	if date == "" {
		date = m.today
	}
	events := make([]string, 0, len(m.days[date]))
	for _, id := range m.days[date] {
		events = append(events, `{"id":`+strconv.FormatInt(id, 10)+`,"created_at":"`+date+`T12:00:00Z","kind":"todo_created","action":"created","target":"Todo","title":"x"}`)
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("[" + strings.Join(events, ",") + "]")), Header: header, Request: req}, nil
}

func fetchActivityDays(t *testing.T, transport *activityDaysTransport) []int64 {
	t.Helper()
	return fetchActivityDaysAt(t, transport, time.Date(2026, 10, 23, 0, 30, 0, 0, time.Local))
}

func fetchActivityDaysAt(t *testing.T, transport *activityDaysTransport, now time.Time) []int64 {
	t.Helper()
	originalNow := hubNow
	hubNow = func() time.Time { return now }
	t.Cleanup(func() { hubNow = originalNow })

	sdk := basecamp.NewClient(&basecamp.Config{BaseURL: "https://3.basecampapi.com"}, hubCheckinsTestTokenProvider{},
		basecamp.WithTransport(transport),
		basecamp.WithMaxRetries(1),
	)
	events, err := fetchTimelineEvents(context.Background(), sdk.ForAccount("99999"), AccountInfo{ID: "99999", Name: "Acme"})
	require.NoError(t, err)
	ids := make([]int64, 0, len(events))
	for _, e := range events {
		ids = append(ids, e.ID)
	}
	return ids
}

func TestFetchTimelineEventsReachesPastMidnight(t *testing.T) {
	transport := &activityDaysTransport{today: "2026-10-23", days: map[string][]int64{
		"2026-10-23": {1},
		"2026-10-22": {2, 3},
		"2026-10-20": {4},
	}}

	assert.Equal(t, []int64{1, 2, 3, 4}, fetchActivityDays(t, transport))
}

func TestFetchTimelineEventsStopsOnceTheFeedIsFull(t *testing.T) {
	today := make([]int64, timelineFeedSize)
	for i := range today {
		today[i] = int64(i + 1)
	}
	transport := &activityDaysTransport{today: "2026-10-23", days: map[string][]int64{
		"2026-10-23": today,
		"2026-10-22": {1000},
	}}

	assert.Len(t, fetchActivityDays(t, transport), timelineFeedSize)
	assert.Equal(t, []string{""}, transport.asked)
}

func TestFetchTimelineEventsStopsAtTheLookback(t *testing.T) {
	transport := &activityDaysTransport{today: "2026-10-23", days: map[string][]int64{
		"2026-10-16": {1},
	}}

	assert.Empty(t, fetchActivityDays(t, transport))
	assert.Equal(t, []string{"", "2026-10-23", "2026-10-22", "2026-10-21", "2026-10-20", "2026-10-19", "2026-10-18", "2026-10-17"}, transport.asked)
}

func TestFetchTimelineEventsKeepsAnEventSeenTwiceOnce(t *testing.T) {
	transport := &activityDaysTransport{today: "2026-10-22", days: map[string][]int64{
		"2026-10-22": {1, 2},
		"2026-10-21": {3},
	}}

	assert.Equal(t, []int64{1, 2, 3}, fetchActivityDays(t, transport))
}

func TestFetchTimelineEventsMissesNoDayWhenBasecampIsAhead(t *testing.T) {
	transport := &activityDaysTransport{today: "2026-10-24", days: map[string][]int64{
		"2026-10-24": {1},
		"2026-10-23": {2},
		"2026-10-22": {3},
	}}

	assert.Equal(t, []int64{1, 2, 3}, fetchActivityDays(t, transport))
}

// Chile starts daylight saving at midnight on 2026-09-06, so that day has no
// 00:30 and stepping a timestamp back a day from 2026-09-07 00:30 lands on
// 2026-09-05.
func TestFetchTimelineEventsMissesNoDayAcrossAMidnightClockChange(t *testing.T) {
	santiago, err := time.LoadLocation("America/Santiago")
	require.NoError(t, err)
	transport := &activityDaysTransport{today: "2026-09-07", days: map[string][]int64{
		"2026-09-07": {1},
		"2026-09-06": {2},
		"2026-09-05": {3},
	}}

	assert.Equal(t, []int64{1, 2, 3}, fetchActivityDaysAt(t, transport, time.Date(2026, 9, 7, 0, 30, 0, 0, santiago)))
}
