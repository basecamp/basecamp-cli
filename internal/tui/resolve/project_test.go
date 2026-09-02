package resolve

import (
	"strings"
	"testing"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/stretchr/testify/assert"
)

// The ★ prefix marks a starred project, so it reflects Starred, not Bookmarked:
// a project filed into a stack is bookmarked but unstarred and gets no star.
func TestProjectToPickerItem_StarMarkerReflectsStarred(t *testing.T) {
	stackedNotStarred := projectToPickerItem(basecamp.Project{ID: 1, Name: "Stacked", Bookmarked: true, Starred: false})
	assert.False(t, strings.HasPrefix(stackedNotStarred.Title, "★"), "a bookmarked-but-unstarred project gets no star")

	starred := projectToPickerItem(basecamp.Project{ID: 2, Name: "Starred", Bookmarked: true, Starred: true})
	assert.True(t, strings.HasPrefix(starred.Title, "★"), "a starred project gets a star prefix")
}
