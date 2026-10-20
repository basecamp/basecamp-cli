package connector

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type membershipToken struct{}

func (membershipToken) AccessToken(context.Context) (string, error) { return "test-token", nil }

func membershipClient(t *testing.T, pings func(http.ResponseWriter)) *basecamp.AccountClient {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/999/projects.json":
			_, _ = w.Write([]byte(`[{"id":48699913,"name":"Launch"}]`))
		case "/999/circles.json":
			pings(w)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return basecamp.NewClient(&basecamp.Config{BaseURL: srv.URL}, membershipToken{}, basecamp.WithMaxRetries(1)).ForAccount("999")
}

// The live subscription holds the agent's Pings as it holds its projects, so
// the listing names both: a line in a Ping is not from a bucket the
// subscription lacks, and does not reconnect the feed.
func TestTheMembershipListingNamesTheAgentsPings(t *testing.T) {
	client := membershipClient(t, func(w http.ResponseWriter) {
		_, _ = w.Write([]byte(`[{"id":2085958521,"title":"Rob Zolkos","room_url":"https://3.basecampapi.com/999/buckets/2085958521/chats/1.json"}]`))
	})

	got, err := SDKMembership{Client: client}.Buckets(context.Background())
	require.NoError(t, err)
	assert.ElementsMatch(t, []int64{48699913, 2085958521}, got)
}

func TestPingsTurnedOffListTheProjectsAlone(t *testing.T) {
	client := membershipClient(t, func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"Pings have been disabled"}`))
	})

	got, err := SDKMembership{Client: client}.Buckets(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []int64{48699913}, got)
}

func TestAPingsListingThatFailsFailsTheListing(t *testing.T) {
	client := membershipClient(t, func(w http.ResponseWriter) { w.WriteHeader(http.StatusInternalServerError) })

	_, err := SDKMembership{Client: client}.Buckets(context.Background())
	require.Error(t, err, "a partial listing would revoke every Ping the subscription holds")
}
