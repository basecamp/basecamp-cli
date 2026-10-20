package admission

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A bucket is read as a Ping through the Pings API. A 404 (no Ping the agent
// is in, which every project is) and a 403 (Pings turned off) both mean no
// Ping, and that answer is reused; a Ping is read fresh each time, since who
// is in it decides whether it is direct.
func TestThePingReadTellsAPingFromAProject(t *testing.T) {
	var calls sync.Map
	client := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, _ := calls.LoadOrStore(r.URL.Path, new(atomic.Int32))
		n.(*atomic.Int32).Add(1)
		switch r.URL.Path {
		case "/999/circles/1":
			_, _ = w.Write([]byte(`{"id":1,"room_url":"https://3.basecampapi.com/999/buckets/1/chats/2.json","people":[{"id":26909558,"name":"Rob"}]}`))
		case "/999/circles/2":
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"Not found"}`))
		case "/999/circles/3":
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":"Pings have been disabled"}`))
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	c := &clock{now: testNow}
	pings := NewPings(client, c.Now)
	count := func(path string) int32 {
		n, ok := calls.Load(path)
		if !ok {
			return 0
		}
		return n.(*atomic.Int32).Load()
	}

	for range 2 {
		ping, err := pings.Ping(context.Background(), 1)
		require.NoError(t, err)
		require.NotNil(t, ping)
		require.Len(t, ping.People, 1)
		assert.Equal(t, int64(26909558), ping.People[0].ID)
	}
	assert.EqualValues(t, 2, count("/999/circles/1"), "a Ping is read fresh")

	for _, bucket := range []int64{2, 3} {
		for range 2 {
			ping, err := pings.Ping(context.Background(), bucket)
			require.NoError(t, err)
			assert.Nil(t, ping)
		}
	}
	assert.EqualValues(t, 1, count("/999/circles/2"), "no Ping is remembered")
	assert.EqualValues(t, 1, count("/999/circles/3"))

	c.advance(NotAPingTTL)
	_, err := pings.Ping(context.Background(), 2)
	require.NoError(t, err)
	assert.EqualValues(t, 2, count("/999/circles/2"), "until the TTL")

	_, err = pings.Ping(context.Background(), 4)
	require.Error(t, err, "any other failure is the read's")
}
