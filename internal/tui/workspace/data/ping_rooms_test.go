package data

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type pingRoomsTransport struct{}

func (m *pingRoomsTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	header := make(http.Header)
	header.Set("Content-Type", "application/json")
	reply := func(status int, body string) (*http.Response, error) {
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: header, Request: req}, nil
	}

	ping := func(id, campfire int, title string, named bool) string {
		return `{"id":` + strconv.Itoa(id) + `,"created_at":"2026-10-19T15:00:00Z","updated_at":"2026-10-19T15:30:00Z","title":"` + title + `","named":` + strconv.FormatBool(named) +
			`,"avatar_url":"x","url":"x","app_url":"x","bookmark_url":"x",` +
			`"room_url":"https://3.basecampapi.com/99999/buckets/` + strconv.Itoa(id) + `/chats/` + strconv.Itoa(campfire) + `.json",` +
			`"links":{"lines":"x","files":"x"},"people":[]}`
	}

	switch {
	case req.URL.Path == "/99999/circles.json":
		return reply(200, "["+ping(500, 700, "Jason Fried", false)+","+ping(501, 701, "Jane, John", false)+"]")
	case strings.HasSuffix(req.URL.Path, "/chats/700/lines.json"):
		return reply(200, `[{"id":9,"content":"Lunch?","creator":{"name":"Jason Fried"},"created_at":"2026-10-19T15:45:00Z"}]`)
	case strings.HasSuffix(req.URL.Path, "/chats/701/lines.json"):
		return reply(200, `[]`)
	}
	return reply(404, `{"error":"Not Found"}`)
}

func TestFetchAccountPingRoomsBuildsRoomsFromPings(t *testing.T) {
	sdk := basecamp.NewClient(&basecamp.Config{BaseURL: "https://3.basecampapi.com"}, hubCheckinsTestTokenProvider{},
		basecamp.WithTransport(&pingRoomsTransport{}),
		basecamp.WithMaxRetries(1),
	)
	client := sdk.ForAccount("99999")

	rooms, err := fetchAccountPingRooms(context.Background(), client, AccountInfo{ID: "99999", Name: "Acme"})
	require.NoError(t, err)
	require.Len(t, rooms, 2)

	assert.Equal(t, int64(700), rooms[0].ChatID)
	assert.Equal(t, int64(500), rooms[0].ProjectID, "a ping is its own bucket")
	assert.Equal(t, "Jason Fried", rooms[0].Title)
	assert.Equal(t, "Jason Fried: Lunch?", rooms[0].LastMessage)
	assert.Equal(t, "Acme", rooms[0].Account)

	assert.Equal(t, int64(701), rooms[1].ChatID)
	assert.Equal(t, "Jane, John", rooms[1].Title, "group pings are listed too")
	assert.Empty(t, rooms[1].LastMessage)
	assert.NotZero(t, rooms[1].LastAtTS, "falls back to the ping's own activity time")
}
