package names

import (
	"fmt"
	"testing"
)

// Test data generators

func generateProjects(n int) []Project {
	projects := make([]Project, n)
	for i := 0; i < n; i++ {
		projects[i] = Project{ID: int64(i + 1), Name: fmt.Sprintf("Project %d", i+1)}
	}
	return projects
}

// Benchmarks for resolve() - the core resolution algorithm

func BenchmarkResolve(b *testing.B) {
	projects := generateProjects(100)
	extract := func(p Project) (int64, string) { return p.ID, p.Name }

	b.Run("exact_match", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			resolve("Project 50", projects, extract)
		}
	})

	b.Run("case_insensitive", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			resolve("project 50", projects, extract)
		}
	})

	b.Run("partial_match", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			resolve("ject 5", projects, extract)
		}
	})

	b.Run("no_match", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			resolve("nonexistent", projects, extract)
		}
	})

	b.Run("first_item", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			resolve("Project 1", projects, extract)
		}
	})

	b.Run("last_item", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			resolve("Project 100", projects, extract)
		}
	})
}

// Benchmark with different list sizes
func BenchmarkResolveScaling(b *testing.B) {
	sizes := []int{10, 50, 100, 500, 1000}
	extract := func(p Project) (int64, string) { return p.ID, p.Name }

	for _, size := range sizes {
		projects := generateProjects(size)
		midpoint := fmt.Sprintf("Project %d", size/2)

		b.Run(fmt.Sprintf("size_%d", size), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				resolve(midpoint, projects, extract)
			}
		})
	}
}

// Benchmarks for suggest() - suggestion generation

func BenchmarkSuggest(b *testing.B) {
	projects := generateProjects(100)
	getName := func(p Project) string { return p.Name }

	b.Run("common_prefix", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			suggest("Proj", projects, getName)
		}
	})

	b.Run("no_match", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			suggest("xyz", projects, getName)
		}
	})

	b.Run("word_match", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			suggest("50", projects, getName)
		}
	})
}

// Benchmarks for containsWord() - word matching

func BenchmarkContainsWord(b *testing.B) {
	b.Run("match_start", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			containsWord("marketing campaign project", "marketing")
		}
	})

	b.Run("match_middle", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			containsWord("marketing campaign project", "campaign")
		}
	})

	b.Run("match_end", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			containsWord("marketing campaign project", "project")
		}
	})

	b.Run("no_match", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			containsWord("marketing campaign project", "xyz")
		}
	})

	b.Run("short_word_skip", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			containsWord("marketing campaign project", "a")
		}
	})

	b.Run("multiple_words", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			containsWord("marketing campaign project", "sales campaign")
		}
	})
}

// Benchmark with realistic data patterns
func BenchmarkResolveRealistic(b *testing.B) {
	// Simulate realistic project names
	projects := []Project{
		{ID: 1, Name: "Marketing Campaign 2024"},
		{ID: 2, Name: "Product Launch Q1"},
		{ID: 3, Name: "Engineering Sprint"},
		{ID: 4, Name: "Customer Support"},
		{ID: 5, Name: "Sales Pipeline"},
		{ID: 6, Name: "Design System"},
		{ID: 7, Name: "Infrastructure"},
		{ID: 8, Name: "Mobile App"},
		{ID: 9, Name: "Web Platform"},
		{ID: 10, Name: "API Development"},
	}
	extract := func(p Project) (int64, string) { return p.ID, p.Name }

	b.Run("full_name", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			resolve("Engineering Sprint", projects, extract)
		}
	})

	b.Run("partial_unique", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			resolve("sprint", projects, extract)
		}
	})

	b.Run("partial_ambiguous", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			resolve("product", projects, extract)
		}
	})
}
