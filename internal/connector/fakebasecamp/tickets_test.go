package fakebasecamp_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Tickets names every stream ticket handed out, so a test can check that
// none of them leaks into what a command prints.
func TestTicketsAreEveryTicketIssued(t *testing.T) {
	s, bearer := start(t)
	assert.Empty(t, s.Tickets())

	issued := make([]string, 0, 2)
	for range 2 {
		status, _, body := do(t, s, http.MethodPost, "/999/events/stream_ticket.json", bearer, "")
		require.Equal(t, http.StatusOK, status, body)
		var ticket struct {
			Ticket string `json:"ticket"`
		}
		require.NoError(t, json.Unmarshal([]byte(body), &ticket))
		issued = append(issued, ticket.Ticket)
	}
	assert.ElementsMatch(t, issued, s.Tickets())
}
