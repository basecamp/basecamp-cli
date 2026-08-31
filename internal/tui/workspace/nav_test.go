package workspace

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNavStack(t *testing.T) {
	root := &stubView{title: "Home"}
	n := newNav(root)

	assert.Equal(t, 1, n.depth())
	assert.Same(t, root, n.current())
	assert.Equal(t, []string{"Home"}, n.trail())

	projects := &stubView{title: "Projects"}
	n.push(projects)
	website := &stubView{title: "Website redesign"}
	n.push(website)

	assert.Equal(t, 3, n.depth())
	assert.Same(t, website, n.current())
	assert.Equal(t, []string{"Home", "Projects", "Website redesign"}, n.trail())

	assert.True(t, n.pop())
	assert.Same(t, projects, n.current())
	assert.True(t, n.pop())
	assert.Same(t, root, n.current())
}

func TestNavPopStopsAtHome(t *testing.T) {
	root := &stubView{title: "Home"}
	n := newNav(root)

	assert.False(t, n.pop())
	assert.Equal(t, 1, n.depth())
	assert.Same(t, root, n.current())
}

// A popped view is released rather than left in the backing array, where it
// would hold whatever it read for as long as the workspace runs.
func TestNavPopReleasesTheView(t *testing.T) {
	n := newNav(&stubView{title: "Home"})
	n.push(&stubView{title: "Projects"})
	n.pop()

	assert.Nil(t, n.stack[:cap(n.stack)][1])
}
