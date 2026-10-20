package names

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/output"
)

func newPingPeopleFixture(t *testing.T) *Resolver {
	t.Helper()
	jane := person(1, "Jane Smith", "sgid-jane", "User")
	jane["email_address"] = "jane@example.com"
	jason := person(2, "Jason Fried", "sgid-jason", "User")
	jason["email_address"] = "jason@example.com"
	jasper := person(3, "Jasper Jones", "sgid-jasper", "User")
	s := &mentionServer{
		calls:    map[string]int{},
		pingable: []map[string]any{jane, jason, jasper},
	}
	server := httptest.NewServer(s)
	t.Cleanup(server.Close)

	sdkClient := basecamp.NewClient(&basecamp.Config{BaseURL: server.URL}, testTokenProvider{}, basecamp.WithMaxRetries(1))
	return NewResolver(sdkClient, nil, "99999")
}

func TestResolvePingableByIDEmailAndName(t *testing.T) {
	r := newPingPeopleFixture(t)
	ctx := context.Background()

	for _, input := range []string{"1", "jane@example.com", "JANE@EXAMPLE.COM", "Jane Smith", "jane smith", "Jane"} {
		p, err := r.ResolvePingable(ctx, input)
		require.NoError(t, err, input)
		assert.Equal(t, int64(1), p.ID, input)
	}
}

func TestResolvePingableAmbiguousName(t *testing.T) {
	r := newPingPeopleFixture(t)

	_, err := r.ResolvePingable(context.Background(), "Jas")
	var e *output.Error
	require.ErrorAs(t, err, &e)
	assert.Equal(t, output.CodeAmbiguous, e.Code)
}

func TestResolvePingableRefusesWhoCannotBePinged(t *testing.T) {
	r := newPingPeopleFixture(t)
	ctx := context.Background()

	_, err := r.ResolvePingable(ctx, "999")
	var e *output.Error
	require.ErrorAs(t, err, &e)
	assert.Equal(t, output.CodeNotFound, e.Code)
	assert.Contains(t, e.Hint, "basecamp people pingable")

	_, err = r.ResolvePingable(ctx, "Nobody Atall")
	require.ErrorAs(t, err, &e)
	assert.Equal(t, output.CodeNotFound, e.Code)
}

func TestResolveAmongOnlyTheGivenPeople(t *testing.T) {
	people := []Person{
		{ID: 1, Name: "Jane Smith", AttachableSGID: "sgid-jane"},
		{ID: 2, Name: "John Doe", AttachableSGID: "sgid-john"},
	}

	p, err := ResolveAmong("john", people)
	require.NoError(t, err)
	assert.Equal(t, "sgid-john", p.AttachableSGID)

	_, err = ResolveAmong("Jason", people)
	var e *output.Error
	require.ErrorAs(t, err, &e)
	assert.Equal(t, output.CodeNotFound, e.Code)

	_, err = ResolveAmong("J", people)
	require.ErrorAs(t, err, &e)
	assert.Equal(t, output.CodeAmbiguous, e.Code)
}
