package api

import (
	"encoding/json"
	"testing"
)

// BenchmarkParseNextLink benchmarks Link header parsing for pagination
func BenchmarkParseNextLink(b *testing.B) {
	b.Run("with_next", func(b *testing.B) {
		header := `<https://3.basecampapi.com/12345/projects.json?page=2>; rel="next", <https://3.basecampapi.com/12345/projects.json?page=10>; rel="last"`
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			parseNextLink(header)
		}
	})

	b.Run("no_next", func(b *testing.B) {
		header := `<https://3.basecampapi.com/12345/projects.json?page=1>; rel="first", <https://3.basecampapi.com/12345/projects.json?page=10>; rel="last"`
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			parseNextLink(header)
		}
	})

	b.Run("empty", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			parseNextLink("")
		}
	})

	b.Run("complex", func(b *testing.B) {
		header := `<https://3.basecampapi.com/12345/projects.json?page=1>; rel="first", <https://3.basecampapi.com/12345/projects.json?page=5>; rel="prev", <https://3.basecampapi.com/12345/projects.json?page=7>; rel="next", <https://3.basecampapi.com/12345/projects.json?page=100>; rel="last"`
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			parseNextLink(header)
		}
	})
}

// BenchmarkParseRetryAfter benchmarks Retry-After header parsing
func BenchmarkParseRetryAfter(b *testing.B) {
	b.Run("valid_seconds", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			parseRetryAfter("120")
		}
	})

	b.Run("empty", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			parseRetryAfter("")
		}
	})

	b.Run("invalid", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			parseRetryAfter("not-a-number")
		}
	})
}

// BenchmarkCacheKey benchmarks cache key generation (SHA256 hashing)
func BenchmarkCacheKey(b *testing.B) {
	c := NewCache("/tmp/bcq-bench-cache")

	b.Run("typical", func(b *testing.B) {
		url := "https://3.basecampapi.com/12345/projects.json"
		accountID := "12345"
		token := "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIiwibmFtZSI6IkpvaG4gRG9lIiwiaWF0IjoxNTE2MjM5MDIyfQ"
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			c.Key(url, accountID, token)
		}
	})

	b.Run("long_url", func(b *testing.B) {
		url := "https://3.basecampapi.com/12345/buckets/67890/todolists/11111/todos.json?status=active&page=1"
		accountID := "12345"
		token := "very-long-access-token-that-represents-oauth-bearer-token-from-basecamp-api"
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			c.Key(url, accountID, token)
		}
	})

	b.Run("no_token", func(b *testing.B) {
		url := "https://3.basecampapi.com/12345/projects.json"
		accountID := "12345"
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			c.Key(url, accountID, "")
		}
	})
}

// BenchmarkJSONUnmarshal benchmarks JSON parsing for typical API responses
func BenchmarkJSONUnmarshal(b *testing.B) {
	b.Run("single_object", func(b *testing.B) {
		data := []byte(`{"id":123456,"name":"Marketing Campaign 2024","status":"active","description":"Q4 marketing initiative"}`)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			var result map[string]any
			json.Unmarshal(data, &result)
		}
	})

	b.Run("small_array", func(b *testing.B) {
		data := []byte(`[{"id":1,"name":"Project A"},{"id":2,"name":"Project B"},{"id":3,"name":"Project C"}]`)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			var result []map[string]any
			json.Unmarshal(data, &result)
		}
	})

	b.Run("large_array", func(b *testing.B) {
		// Simulate a page of todos (50 items)
		items := make([]map[string]any, 50)
		for i := 0; i < 50; i++ {
			items[i] = map[string]any{
				"id":          i + 1,
				"title":       "Todo item with a reasonably long title for benchmarking",
				"completed":   i%2 == 0,
				"due_on":      "2024-12-31",
				"assignee_id": 12345,
			}
		}
		data, _ := json.Marshal(items)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			var result []map[string]any
			json.Unmarshal(data, &result)
		}
	})

	b.Run("nested_object", func(b *testing.B) {
		data := []byte(`{
			"id": 123,
			"name": "Project",
			"dock": [
				{"id": 1, "name": "todoset", "enabled": true},
				{"id": 2, "name": "message_board", "enabled": true},
				{"id": 3, "name": "schedule", "enabled": false}
			],
			"creator": {"id": 456, "name": "John Doe", "email": "john@example.com"}
		}`)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			var result map[string]any
			json.Unmarshal(data, &result)
		}
	})
}

// BenchmarkJSONMarshal benchmarks JSON serialization for request bodies
func BenchmarkJSONMarshal(b *testing.B) {
	b.Run("simple_request", func(b *testing.B) {
		body := map[string]any{
			"content": "This is a new todo item",
			"due_on":  "2024-12-31",
		}
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			json.Marshal(body)
		}
	})

	b.Run("complex_request", func(b *testing.B) {
		body := map[string]any{
			"content":      "This is a complex todo item with more fields",
			"due_on":       "2024-12-31",
			"description":  "A detailed description of the task that needs to be completed",
			"assignee_ids": []int{123, 456, 789},
			"notify":       true,
		}
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			json.Marshal(body)
		}
	})
}
