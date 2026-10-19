package urlarg

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParsePingURLs(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  Parsed
	}{
		{
			name:  "app host ping",
			input: "https://app.basecamp.com/123/circles/456",
			want:  Parsed{AccountID: "123", Type: "circles", RecordingID: "456", PingID: "456"},
		},
		{
			name:  "web host ping",
			input: "https://3.basecamp.com/123/circles/456",
			want:  Parsed{AccountID: "123", Type: "circles", RecordingID: "456", PingID: "456"},
		},
		{
			name:  "api host ping json",
			input: "https://3.basecampapi.com/123/circles/456.json",
			want:  Parsed{AccountID: "123", Type: "circles", RecordingID: "456", PingID: "456"},
		},
		{
			name:  "app host ping line",
			input: "https://app.basecamp.com/123/circles/456@789",
			want:  Parsed{AccountID: "123", ProjectID: "456", Type: "lines", RecordingID: "789", PingID: "456"},
		},
		{
			name:  "local dev ping line",
			input: "http://3.basecamp.localhost:3001/123/circles/456@789",
			want:  Parsed{AccountID: "123", ProjectID: "456", Type: "lines", RecordingID: "789", PingID: "456"},
		},
		{
			name:  "app host sidebar ping",
			input: "https://app.basecamp.com/123/my/sidebar/circles/456",
			want:  Parsed{AccountID: "123", Type: "circles", RecordingID: "456", PingID: "456"},
		},
		{
			name:  "web host sidebar ping",
			input: "https://3.basecamp.com/123/my/sidebar/circles/456",
			want:  Parsed{AccountID: "123", Type: "circles", RecordingID: "456", PingID: "456"},
		},
		{
			name:  "app host sidebar ping line",
			input: "https://app.basecamp.com/123/my/sidebar/circles/456@789",
			want:  Parsed{AccountID: "123", ProjectID: "456", Type: "lines", RecordingID: "789", PingID: "456"},
		},
		{
			name:  "web host sidebar ping line",
			input: "https://3.basecamp.com/123/my/sidebar/circles/456@789",
			want:  Parsed{AccountID: "123", ProjectID: "456", Type: "lines", RecordingID: "789", PingID: "456"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.True(t, IsURL(tt.input))
			got := Parse(tt.input)
			require.NotNil(t, got)
			assert.Equal(t, tt.want, *got)
		})
	}
}

func TestParsePingLineURLNamesNoCampfire(t *testing.T) {
	got := Parse("https://3.basecamp.com/123/circles/456@789")
	require.NotNil(t, got)
	assert.Empty(t, got.CampfireID, "the Campfire is not in a Ping URL; it comes from the Ping")
}

func TestParseCircleCollectionsCarryNoPing(t *testing.T) {
	for _, u := range []string{
		"https://3.basecamp.com/123/circles",
		"https://3.basecamp.com/123/circles/people",
	} {
		got := Parse(u)
		require.NotNil(t, got, u)
		assert.Empty(t, got.PingID, u)
	}
}
