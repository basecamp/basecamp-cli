package commands

import (
	"testing"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/output"
)

func TestWithCommentsInjectsIntoMap(t *testing.T) {
	data := map[string]any{
		"id":    float64(42),
		"title": "Buy milk",
	}
	comments := []basecamp.Comment{
		{ID: 1, Content: "first"},
		{ID: 2, Content: "second"},
	}

	result := withComments(data, comments)
	m, ok := result.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, float64(42), m["id"])
	assert.Equal(t, "Buy milk", m["title"])
	assert.Len(t, m["comments"], 2)
}

func TestWithCommentsNilIsNoOp(t *testing.T) {
	data := map[string]any{"id": float64(1)}
	result := withComments(data, nil)
	m := result.(map[string]any)
	_, ok := m["comments"]
	assert.False(t, ok, "nil comments should not inject a key")
}

func TestCommentFlagsShouldFetch(t *testing.T) {
	t.Run("defaultOn true", func(t *testing.T) {
		cf := &commentFlags{defaultOn: true}
		assert.True(t, cf.shouldFetch())
	})

	t.Run("defaultOn false", func(t *testing.T) {
		cf := &commentFlags{}
		assert.False(t, cf.shouldFetch())
	})

	t.Run("comments flag", func(t *testing.T) {
		cf := &commentFlags{comments: true}
		assert.True(t, cf.shouldFetch())
	})

	t.Run("no-comments", func(t *testing.T) {
		cf := &commentFlags{noComments: true}
		assert.False(t, cf.shouldFetch())
	})

	t.Run("no-comments overrides defaultOn", func(t *testing.T) {
		cf := &commentFlags{defaultOn: true, noComments: true}
		assert.False(t, cf.shouldFetch())
	})

	t.Run("all-comments", func(t *testing.T) {
		cf := &commentFlags{allComments: true}
		assert.True(t, cf.shouldFetch())
	})
}

func TestCommentEnrichmentApplyNotices(t *testing.T) {
	t.Run("truncation notice only", func(t *testing.T) {
		ce := &commentEnrichment{Notice: "Showing 10 of 50 comments"}
		opts := ce.applyNotices("")
		assert.Len(t, opts, 1)
	})

	t.Run("fetch failure routes to diagnostic", func(t *testing.T) {
		ce := &commentEnrichment{FetchNotice: "fetching failed"}
		opts := ce.applyNotices("1 attachment(s)")
		assert.Len(t, opts, 1)
	})

	t.Run("no notices produces no opts", func(t *testing.T) {
		ce := &commentEnrichment{}
		opts := ce.applyNotices("")
		assert.Empty(t, opts)
	})

	t.Run("attachment notice only", func(t *testing.T) {
		ce := &commentEnrichment{}
		opts := ce.applyNotices("1 attachment(s)")
		assert.Len(t, opts, 1)
	})
}

func TestCommentEnrichmentApply(t *testing.T) {
	t.Run("nil comments returns data unchanged and empty opts", func(t *testing.T) {
		ce := &commentEnrichment{}
		data := map[string]any{"id": float64(1)}
		result, opts := ce.apply(data, "")
		m := result.(map[string]any)
		_, hasComments := m["comments"]
		assert.False(t, hasComments)
		assert.Empty(t, opts)
	})

	t.Run("comments and notice produce data and opts", func(t *testing.T) {
		ce := &commentEnrichment{
			Comments: []basecamp.Comment{{ID: 1, Content: "hi"}},
			Notice:   "Showing 1 of 5 comments",
		}
		data := map[string]any{"id": float64(42)}
		result, opts := ce.apply(data, "2 attachment(s)")
		m := result.(map[string]any)
		assert.NotNil(t, m["comments"])
		assert.NotEmpty(t, opts)
	})

	t.Run("breadcrumbs are appended", func(t *testing.T) {
		ce := &commentEnrichment{
			Comments: []basecamp.Comment{{ID: 1}},
			Breadcrumbs: []output.Breadcrumb{
				{Action: "comments", Cmd: "basecamp comments list --all 42", Description: "View all comments"},
			},
		}
		data := map[string]any{"id": float64(42)}
		_, opts := ce.apply(data, "")

		// Apply opts to a Response to prove the breadcrumb survived composition.
		resp := &output.Response{}
		for _, opt := range opts {
			opt(resp)
		}
		require.Len(t, resp.Breadcrumbs, 1)
		assert.Equal(t, "basecamp comments list --all 42", resp.Breadcrumbs[0].Cmd)
	})
}
