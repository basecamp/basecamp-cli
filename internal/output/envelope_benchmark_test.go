package output

import (
	"bytes"
	"encoding/json"
	"testing"
)

// BenchmarkNormalizeData benchmarks the data normalization function
func BenchmarkNormalizeData(b *testing.B) {
	b.Run("json_raw_message_array", func(b *testing.B) {
		raw := json.RawMessage(`[{"id":1,"name":"A"},{"id":2,"name":"B"},{"id":3,"name":"C"}]`)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			normalizeData(raw)
		}
	})

	b.Run("json_raw_message_object", func(b *testing.B) {
		raw := json.RawMessage(`{"id":123,"name":"Test Project","status":"active"}`)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			normalizeData(raw)
		}
	})

	b.Run("already_normalized_slice", func(b *testing.B) {
		data := []map[string]any{
			{"id": 1, "name": "A"},
			{"id": 2, "name": "B"},
		}
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			normalizeData(data)
		}
	})

	b.Run("already_normalized_map", func(b *testing.B) {
		data := map[string]any{"id": 123, "name": "Test"}
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			normalizeData(data)
		}
	})

	b.Run("struct_to_map", func(b *testing.B) {
		type Project struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
		}
		data := Project{ID: 123, Name: "Test"}
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			normalizeData(data)
		}
	})

	b.Run("large_array", func(b *testing.B) {
		items := make([]map[string]any, 50)
		for i := 0; i < 50; i++ {
			items[i] = map[string]any{"id": i, "name": "Item"}
		}
		data, _ := json.Marshal(items)
		raw := json.RawMessage(data)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			normalizeData(raw)
		}
	})

	b.Run("nil", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			normalizeData(nil)
		}
	})
}

// BenchmarkNormalizeUnmarshaled benchmarks array type conversion
func BenchmarkNormalizeUnmarshaled(b *testing.B) {
	b.Run("all_maps", func(b *testing.B) {
		data := []any{
			map[string]any{"id": 1, "name": "A"},
			map[string]any{"id": 2, "name": "B"},
			map[string]any{"id": 3, "name": "C"},
		}
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			normalizeUnmarshaled(data)
		}
	})

	b.Run("mixed_types", func(b *testing.B) {
		data := []any{
			map[string]any{"id": 1},
			"string value",
			42,
		}
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			normalizeUnmarshaled(data)
		}
	})

	b.Run("empty_array", func(b *testing.B) {
		data := []any{}
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			normalizeUnmarshaled(data)
		}
	})

	b.Run("non_array", func(b *testing.B) {
		data := map[string]any{"id": 123}
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			normalizeUnmarshaled(data)
		}
	})
}

// BenchmarkWriteJSON benchmarks JSON output writing
func BenchmarkWriteJSON(b *testing.B) {
	b.Run("simple_response", func(b *testing.B) {
		buf := &bytes.Buffer{}
		w := New(Options{Writer: buf, Format: FormatJSON})
		data := map[string]any{"id": 123, "name": "Test"}
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			buf.Reset()
			w.OK(data)
		}
	})

	b.Run("array_response", func(b *testing.B) {
		buf := &bytes.Buffer{}
		w := New(Options{Writer: buf, Format: FormatJSON})
		data := []map[string]any{
			{"id": 1, "name": "A"},
			{"id": 2, "name": "B"},
			{"id": 3, "name": "C"},
		}
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			buf.Reset()
			w.OK(data)
		}
	})

	b.Run("with_options", func(b *testing.B) {
		buf := &bytes.Buffer{}
		w := New(Options{Writer: buf, Format: FormatJSON})
		data := map[string]any{"id": 123, "name": "Test"}
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			buf.Reset()
			w.OK(data,
				WithSummary("Test summary"),
				WithContext("project", "123"),
				WithMeta("count", 1),
			)
		}
	})

	b.Run("large_response", func(b *testing.B) {
		buf := &bytes.Buffer{}
		w := New(Options{Writer: buf, Format: FormatJSON})
		items := make([]map[string]any, 100)
		for i := 0; i < 100; i++ {
			items[i] = map[string]any{
				"id":          i + 1,
				"title":       "A reasonably long todo item title for realistic benchmarking",
				"completed":   i%2 == 0,
				"due_on":      "2024-12-31",
				"description": "A longer description field that might contain more text",
			}
		}
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			buf.Reset()
			w.OK(items)
		}
	})
}

// BenchmarkWriteIDs benchmarks ID-only output
func BenchmarkWriteIDs(b *testing.B) {
	buf := &bytes.Buffer{}
	w := New(Options{Writer: buf, Format: FormatIDs})

	b.Run("single", func(b *testing.B) {
		data := map[string]any{"id": 123, "name": "Test"}
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			buf.Reset()
			w.OK(data)
		}
	})

	b.Run("multiple", func(b *testing.B) {
		data := []map[string]any{
			{"id": 1, "name": "A"},
			{"id": 2, "name": "B"},
			{"id": 3, "name": "C"},
			{"id": 4, "name": "D"},
			{"id": 5, "name": "E"},
		}
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			buf.Reset()
			w.OK(data)
		}
	})
}

// BenchmarkWriteCount benchmarks count output
func BenchmarkWriteCount(b *testing.B) {
	buf := &bytes.Buffer{}
	w := New(Options{Writer: buf, Format: FormatCount})

	b.Run("array", func(b *testing.B) {
		data := []map[string]any{
			{"id": 1}, {"id": 2}, {"id": 3}, {"id": 4}, {"id": 5},
		}
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			buf.Reset()
			w.OK(data)
		}
	})

	b.Run("single", func(b *testing.B) {
		data := map[string]any{"id": 123}
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			buf.Reset()
			w.OK(data)
		}
	})
}

// BenchmarkErrorOutput benchmarks error response generation
func BenchmarkErrorOutput(b *testing.B) {
	buf := &bytes.Buffer{}
	w := New(Options{Writer: buf, Format: FormatJSON})
	err := ErrNotFound("Project", "test-project")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf.Reset()
		w.Err(err)
	}
}
