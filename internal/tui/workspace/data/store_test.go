package data

import (
	"testing"
	"time"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewStore(t *testing.T) {
	s := NewStore()

	assert.False(t, s.HasProjects())
	assert.Empty(t, s.Projects())
	assert.Equal(t, time.Duration(0), s.ProjectsAge())
}

func TestStore_SetProjects(t *testing.T) {
	s := NewStore()

	projects := []basecamp.Project{
		{ID: 1, Name: "HQ"},
		{ID: 2, Name: "Marketing"},
		{ID: 3, Name: "Engineering"},
	}

	s.SetProjects(projects)

	assert.True(t, s.HasProjects())
	assert.Len(t, s.Projects(), 3)
}

func TestStore_Projects(t *testing.T) {
	s := NewStore()

	projects := []basecamp.Project{
		{ID: 10, Name: "Alpha"},
		{ID: 20, Name: "Beta"},
	}
	s.SetProjects(projects)

	got := s.Projects()
	require.Len(t, got, 2)
	assert.Equal(t, int64(10), got[0].ID)
	assert.Equal(t, "Alpha", got[0].Name)
	assert.Equal(t, int64(20), got[1].ID)
	assert.Equal(t, "Beta", got[1].Name)
}

func TestStore_ProjectByID(t *testing.T) {
	s := NewStore()

	projects := []basecamp.Project{
		{ID: 100, Name: "Project A"},
		{ID: 200, Name: "Project B"},
		{ID: 300, Name: "Project C"},
	}
	s.SetProjects(projects)

	p := s.Project(200)
	require.NotNil(t, p)
	assert.Equal(t, int64(200), p.ID)
	assert.Equal(t, "Project B", p.Name)
}

func TestStore_ProjectByIDNotFound(t *testing.T) {
	s := NewStore()

	projects := []basecamp.Project{
		{ID: 1, Name: "Only"},
	}
	s.SetProjects(projects)

	assert.Nil(t, s.Project(999))
}

func TestStore_ProjectByIDEmptyStore(t *testing.T) {
	s := NewStore()

	assert.Nil(t, s.Project(1))
}

func TestStore_HasProjects(t *testing.T) {
	s := NewStore()

	assert.False(t, s.HasProjects(), "empty store has no projects")

	s.SetProjects([]basecamp.Project{})
	assert.True(t, s.HasProjects(), "HasProjects is true even with empty slice (data was loaded)")

	s.SetProjects([]basecamp.Project{{ID: 1, Name: "X"}})
	assert.True(t, s.HasProjects())
}

func TestStore_ProjectsAge(t *testing.T) {
	s := NewStore()

	assert.Equal(t, time.Duration(0), s.ProjectsAge(), "unloaded projects have zero age")

	s.SetProjects([]basecamp.Project{{ID: 1, Name: "Test"}})
	time.Sleep(10 * time.Millisecond)

	age := s.ProjectsAge()
	assert.Greater(t, age, time.Duration(0), "age should be positive after loading")
	assert.Less(t, age, time.Second, "age should be small in test")
}

func TestStore_SetProjectsReplacesOld(t *testing.T) {
	s := NewStore()

	s.SetProjects([]basecamp.Project{
		{ID: 1, Name: "Old A"},
		{ID: 2, Name: "Old B"},
	})

	s.SetProjects([]basecamp.Project{
		{ID: 3, Name: "New C"},
	})

	assert.Len(t, s.Projects(), 1)
	assert.Equal(t, "New C", s.Projects()[0].Name)

	assert.Nil(t, s.Project(1), "old project should be gone")
	assert.Nil(t, s.Project(2), "old project should be gone")
	assert.NotNil(t, s.Project(3), "new project should exist")
}

func TestStore_SetProjectsUpdatesAge(t *testing.T) {
	s := NewStore()

	s.SetProjects([]basecamp.Project{{ID: 1, Name: "First"}})
	time.Sleep(20 * time.Millisecond)
	firstAge := s.ProjectsAge()

	s.SetProjects([]basecamp.Project{{ID: 2, Name: "Second"}})
	secondAge := s.ProjectsAge()

	assert.Less(t, secondAge, firstAge, "age should reset on new SetProjects call")
}

func TestStore_ProjectPointsToSliceEntry(t *testing.T) {
	s := NewStore()

	projects := []basecamp.Project{
		{ID: 1, Name: "Original"},
	}
	s.SetProjects(projects)

	p := s.Project(1)
	require.NotNil(t, p)
	assert.Equal(t, "Original", p.Name)

	// The pointer from Project() should reference the stored slice entry,
	// so the list and the lookup should be consistent
	stored := s.Projects()
	assert.Equal(t, p.Name, stored[0].Name)
}

func TestStore_ProjectAllIDs(t *testing.T) {
	s := NewStore()

	projects := []basecamp.Project{
		{ID: 1, Name: "A"},
		{ID: 2, Name: "B"},
		{ID: 3, Name: "C"},
		{ID: 4, Name: "D"},
		{ID: 5, Name: "E"},
	}
	s.SetProjects(projects)

	// Every project should be findable by ID
	for _, p := range projects {
		found := s.Project(p.ID)
		require.NotNil(t, found, "project %d should be in the index", p.ID)
		assert.Equal(t, p.Name, found.Name)
	}
}

func TestStore_SetProjectsEmptySlice(t *testing.T) {
	s := NewStore()

	s.SetProjects([]basecamp.Project{{ID: 1, Name: "Exists"}})
	assert.Len(t, s.Projects(), 1)

	s.SetProjects([]basecamp.Project{})
	assert.Empty(t, s.Projects())
	assert.True(t, s.HasProjects(), "HasProjects should still be true (data was fetched)")
	assert.Nil(t, s.Project(1), "old index should be cleared")
}
