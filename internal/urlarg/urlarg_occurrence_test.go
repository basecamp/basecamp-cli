package urlarg

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	occurrence     = "https://3.basecamp.com/99999/buckets/123/schedule_entries/789/occurrences/20261022"
	occurrenceEdit = occurrence + "/edit"
)

// An occurrence URL's recording ID is the recurring entry's, so returning it
// would act on the whole series. Every extractor refuses it instead.
func TestExtractorsRefuseOccurrenceURLs(t *testing.T) {
	for _, url := range []string{occurrence, occurrenceEdit} {
		t.Run(url, func(t *testing.T) {
			_, err := ExtractID(url)
			assertOccurrence(t, err)

			_, _, err = ExtractWithProject(url)
			assertOccurrence(t, err)

			_, _, err = ExtractCommentWithProject(url)
			assertOccurrence(t, err)

			ids, err := ExtractIDs([]string{"111", url + ",222"})
			assertOccurrence(t, err)
			assert.Empty(t, ids, "a batch with an occurrence in it yields nothing to act on")
		})
	}
}

func TestExtractCommentWithProjectFollowsAFragmentOnAnOccurrence(t *testing.T) {
	id, project, err := ExtractCommentWithProject(occurrence + "#__recording_555")
	require.NoError(t, err)
	assert.Equal(t, "555", id)
	assert.Equal(t, "123", project)
}

func TestExtractorsAcceptTheEntryItself(t *testing.T) {
	for _, arg := range []string{"https://3.basecamp.com/99999/buckets/123/schedule_entries/789", "789"} {
		id, err := ExtractID(arg)
		require.NoError(t, err)
		assert.Equal(t, "789", id)

		id, _, err = ExtractWithProject(arg)
		require.NoError(t, err)
		assert.Equal(t, "789", id)
	}
}

func assertOccurrence(t *testing.T, err error) {
	t.Helper()
	var occ *OccurrenceError
	require.True(t, errors.As(err, &occ), "want an *OccurrenceError, got %v", err)
	assert.Equal(t, "789", occ.EntryID)
	assert.Equal(t, "20261022", occ.Date)
}
