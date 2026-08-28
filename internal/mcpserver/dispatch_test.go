package mcpserver

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/mcp/catalog"
)

func TestBuildRequestPathAndQuery(t *testing.T) {
	op := &catalog.Operation{
		Action: "list_todos",
		Method: "GET",
		Path:   "/buckets/{projectId}/todolists/{todolistId}/todos.json",
		Params: []catalog.Param{
			{Name: "projectId", In: "path", Required: true, Schema: map[string]any{"type": "string"}},
			{Name: "todolistId", In: "path", Required: true, Schema: map[string]any{"type": "string"}},
			{Name: "status", In: "query", Schema: map[string]any{"type": "string"}},
			{Name: "page", In: "query", Schema: map[string]any{"type": "integer"}},
		},
	}

	t.Run("substitutes and encodes", func(t *testing.T) {
		path, body, err := buildRequest(op, map[string]any{
			"projectId":  float64(123),
			"todolistId": "456",
			"status":     "completed",
			"page":       float64(2),
		})
		require.NoError(t, err)
		assert.Equal(t, "/buckets/123/todolists/456/todos.json?page=2&status=completed", path)
		assert.Nil(t, body)
	})

	t.Run("optional query params may be omitted", func(t *testing.T) {
		path, _, err := buildRequest(op, map[string]any{"projectId": "1", "todolistId": "2"})
		require.NoError(t, err)
		assert.Equal(t, "/buckets/1/todolists/2/todos.json", path)
	})

	t.Run("path params are escaped", func(t *testing.T) {
		path, _, err := buildRequest(op, map[string]any{"projectId": "a/b", "todolistId": "2"})
		require.NoError(t, err)
		assert.Equal(t, "/buckets/a%2Fb/todolists/2/todos.json", path)
	})

	t.Run("array values on non-array params are rejected", func(t *testing.T) {
		_, _, err := buildRequest(op, map[string]any{
			"projectId": "1", "todolistId": "2", "status": []any{"a", "b"},
		})
		assert.ErrorContains(t, err, `query parameter "status"`)
	})

	t.Run("missing path param", func(t *testing.T) {
		_, _, err := buildRequest(op, map[string]any{"projectId": "1"})
		assert.ErrorContains(t, err, `missing required path parameter "todolistId"`)
	})

	t.Run("non-scalar path param", func(t *testing.T) {
		_, _, err := buildRequest(op, map[string]any{"projectId": []any{1}, "todolistId": "2"})
		assert.ErrorContains(t, err, `path parameter "projectId"`)
	})

	t.Run("stray param on a body-less operation", func(t *testing.T) {
		_, _, err := buildRequest(op, map[string]any{"projectId": "1", "todolistId": "2", "content": "x"})
		assert.ErrorContains(t, err, `unknown parameter "content"`)
	})
}

func TestBuildRequestRailsArrayParams(t *testing.T) {
	op := &catalog.Operation{
		Action: "search",
		Method: "GET",
		Path:   "/search.json",
		Params: []catalog.Param{
			{Name: "q", In: "query", Required: true, Schema: map[string]any{"type": "string"}},
			{Name: "type_names[]", In: "query", Schema: map[string]any{"type": "array"}},
		},
	}

	t.Run("repeats the bracketed name per element", func(t *testing.T) {
		path, _, err := buildRequest(op, map[string]any{
			"q":            "milk",
			"type_names[]": []any{"Todo", "Message"},
		})
		require.NoError(t, err)
		assert.Equal(t, "/search.json?q=milk&type_names%5B%5D=Todo&type_names%5B%5D=Message", path)
	})

	t.Run("accepts a single scalar too", func(t *testing.T) {
		path, _, err := buildRequest(op, map[string]any{"q": "milk", "type_names[]": "Todo"})
		require.NoError(t, err)
		assert.Equal(t, "/search.json?q=milk&type_names%5B%5D=Todo", path)
	})

	t.Run("rejects non-scalar elements", func(t *testing.T) {
		_, _, err := buildRequest(op, map[string]any{"q": "milk", "type_names[]": []any{map[string]any{}}})
		assert.ErrorContains(t, err, `query parameter "type_names[]"`)
	})
}

func TestBuildRequestBody(t *testing.T) {
	op := &catalog.Operation{
		Action:       "create_todo",
		Method:       "POST",
		Path:         "/buckets/{projectId}/todolists/{todolistId}/todos.json",
		BodyRequired: true,
		Params: []catalog.Param{
			{Name: "projectId", In: "path", Required: true, Schema: map[string]any{"type": "string"}},
			{Name: "todolistId", In: "path", Required: true, Schema: map[string]any{"type": "string"}},
		},
		Body: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"content":     map[string]any{"type": "string"},
				"description": map[string]any{"type": "string"},
			},
		},
	}

	t.Run("gathers remaining params into the body", func(t *testing.T) {
		path, body, err := buildRequest(op, map[string]any{
			"projectId":  "1",
			"todolistId": "2",
			"content":    "Buy milk",
		})
		require.NoError(t, err)
		assert.Equal(t, "/buckets/1/todolists/2/todos.json", path)
		assert.Equal(t, map[string]any{"content": "Buy milk"}, body)
	})

	t.Run("rejects properties the body schema does not declare", func(t *testing.T) {
		_, _, err := buildRequest(op, map[string]any{
			"projectId": "1", "todolistId": "2", "contnet": "typo",
		})
		assert.ErrorContains(t, err, `unknown parameter "contnet"`)
	})

	t.Run("required body is sent even when empty", func(t *testing.T) {
		_, body, err := buildRequest(op, map[string]any{"projectId": "1", "todolistId": "2"})
		require.NoError(t, err)
		assert.Equal(t, map[string]any{}, body)
	})

	t.Run("optional body is omitted when empty", func(t *testing.T) {
		optional := *op
		optional.BodyRequired = false
		_, body, err := buildRequest(&optional, map[string]any{"projectId": "1", "todolistId": "2"})
		require.NoError(t, err)
		assert.Nil(t, body)
	})
}

func TestScalarString(t *testing.T) {
	for _, tc := range []struct {
		in   any
		want string
	}{
		{"abc", "abc"},
		{true, "true"},
		{float64(42), "42"},
		{float64(4.5), "4.5"},
	} {
		got, err := scalarString(tc.in)
		require.NoError(t, err)
		assert.Equal(t, tc.want, got)
	}

	_, err := scalarString(map[string]any{})
	assert.ErrorContains(t, err, "must be a string, number, or boolean")
}

func TestNextPage(t *testing.T) {
	link := func(value string) http.Header {
		h := http.Header{}
		h.Set("Link", value)
		return h
	}

	assert.Equal(t, 4,
		nextPage(link(`<https://3.basecampapi.com/999/projects.json?page=4>; rel="next"`)))
	assert.Equal(t, 2,
		nextPage(link(`<https://x.test/a.json?page=1>; rel="prev", <https://x.test/a.json?page=2>; rel="next"`)))
	assert.Zero(t, nextPage(link(`<https://x.test/a.json?page=1>; rel="prev"`)), "no next link")
	assert.Zero(t, nextPage(link(`<https://x.test/a.json>; rel="next"`)), "next link without page")
	assert.Zero(t, nextPage(link(`<https://x.test/a.json?page=bogus>; rel="next"`)), "non-numeric page")
	assert.Zero(t, nextPage(http.Header{}), "no Link header")
}
