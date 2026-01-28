// Package names provides name resolution for projects, people, and todolists.
// It implements fuzzy matching with the following priority:
// 1. Numeric ID passthrough
// 2. Exact match (case-sensitive)
// 3. Case-insensitive match
// 4. Partial match (contains)
package names

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/bcq/internal/auth"
	"github.com/basecamp/bcq/internal/output"
)

// Resolver resolves names to IDs for projects, people, and todolists.
type Resolver struct {
	sdk  *basecamp.Client
	auth *auth.Manager

	// Session-scoped cache
	mu        sync.RWMutex
	projects  []Project
	people    []Person
	todolists map[string][]Todolist // keyed by project ID
}

// Project represents a Basecamp project for name resolution.
type Project struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// Person represents a Basecamp person for name resolution.
type Person struct {
	ID             int64  `json:"id"`
	Name           string `json:"name"`
	Email          string `json:"email_address"`
	PersonableType string `json:"personable_type,omitempty"` // e.g., "User", "Client"
}

// Todolist represents a Basecamp todolist for name resolution.
type Todolist struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// NewResolver creates a new name resolver.
func NewResolver(sdkClient *basecamp.Client, authMgr *auth.Manager) *Resolver {
	return &Resolver{
		sdk:       sdkClient,
		auth:      authMgr,
		todolists: make(map[string][]Todolist),
	}
}

// ResolveProject resolves a project name or ID to an ID.
// Returns the ID and the project name for display.
func (r *Resolver) ResolveProject(ctx context.Context, input string) (string, string, error) {
	// Numeric ID passthrough
	if id, err := strconv.ParseInt(input, 10, 64); err == nil {
		// Validate the ID exists by fetching projects
		projects, err := r.getProjects(ctx)
		if err != nil {
			return "", "", err
		}
		for _, p := range projects {
			if p.ID == id {
				return strconv.FormatInt(id, 10), p.Name, nil
			}
		}
		// ID not found - return as-is but let API handle validation
		return input, "", nil
	}

	// Fetch projects for name resolution
	projects, err := r.getProjects(ctx)
	if err != nil {
		return "", "", err
	}

	// Try resolution in priority order
	match, matches := resolve(input, projects, func(p Project) (int64, string) {
		return p.ID, p.Name
	})

	if match != nil {
		return strconv.FormatInt(match.ID, 10), match.Name, nil
	}

	if len(matches) > 1 {
		names := make([]string, len(matches))
		for i, m := range matches {
			names[i] = m.Name
		}
		return "", "", output.ErrAmbiguous("project", names)
	}

	// Not found - provide suggestions
	suggestions := suggest(input, projects, func(p Project) string { return p.Name })
	if len(suggestions) > 0 {
		return "", "", output.ErrNotFoundHint("Project", input, "Did you mean: "+strings.Join(suggestions, ", "))
	}
	return "", "", output.ErrNotFound("Project", input)
}

// ResolvePerson resolves a person name, email, or ID to an ID.
// Special case: "me" resolves to the current user.
// Returns the ID and the person's name for display.
func (r *Resolver) ResolvePerson(ctx context.Context, input string) (string, string, error) {
	// Handle "me" keyword
	if strings.ToLower(input) == "me" {
		userID := r.auth.GetUserID()
		if userID != "" {
			// Try to get the name from people list
			people, err := r.getPeople(ctx)
			if err == nil {
				for _, p := range people {
					if strconv.FormatInt(p.ID, 10) == userID {
						return userID, p.Name, nil
					}
				}
			}
			return userID, "me", nil
		}
		return "", "", output.ErrAuth("User ID not available. Run: bcq auth login")
	}

	// Numeric ID passthrough
	if id, err := strconv.ParseInt(input, 10, 64); err == nil {
		people, err := r.getPeople(ctx)
		if err != nil {
			return "", "", err
		}
		for _, p := range people {
			if p.ID == id {
				return strconv.FormatInt(id, 10), p.Name, nil
			}
		}
		return input, "", nil
	}

	// Fetch people for name resolution
	people, err := r.getPeople(ctx)
	if err != nil {
		return "", "", err
	}

	// Try email exact match first
	for _, p := range people {
		if strings.EqualFold(p.Email, input) {
			return strconv.FormatInt(p.ID, 10), p.Name, nil
		}
	}

	// Try name resolution
	match, matches := resolve(input, people, func(p Person) (int64, string) {
		return p.ID, p.Name
	})

	if match != nil {
		return strconv.FormatInt(match.ID, 10), match.Name, nil
	}

	if len(matches) > 1 {
		names := make([]string, len(matches))
		for i, m := range matches {
			names[i] = m.Name
		}
		return "", "", output.ErrAmbiguous("person", names)
	}

	// Not found - provide suggestions
	suggestions := suggest(input, people, func(p Person) string { return p.Name })
	if len(suggestions) > 0 {
		return "", "", output.ErrNotFoundHint("Person", input, "Did you mean: "+strings.Join(suggestions, ", "))
	}
	return "", "", output.ErrNotFound("Person", input)
}

// ResolveTodolist resolves a todolist name or ID within a project.
// Returns the ID and the todolist name for display.
func (r *Resolver) ResolveTodolist(ctx context.Context, input, projectID string) (string, string, error) {
	// Numeric ID passthrough
	if id, err := strconv.ParseInt(input, 10, 64); err == nil {
		todolists, err := r.getTodolists(ctx, projectID)
		if err != nil {
			return "", "", err
		}
		for _, t := range todolists {
			if t.ID == id {
				return strconv.FormatInt(id, 10), t.Name, nil
			}
		}
		return input, "", nil
	}

	// Fetch todolists for name resolution
	todolists, err := r.getTodolists(ctx, projectID)
	if err != nil {
		return "", "", err
	}

	// Try resolution in priority order
	match, matches := resolve(input, todolists, func(t Todolist) (int64, string) {
		return t.ID, t.Name
	})

	if match != nil {
		return strconv.FormatInt(match.ID, 10), match.Name, nil
	}

	if len(matches) > 1 {
		names := make([]string, len(matches))
		for i, m := range matches {
			names[i] = m.Name
		}
		return "", "", output.ErrAmbiguous("todolist", names)
	}

	// Not found - provide suggestions
	suggestions := suggest(input, todolists, func(t Todolist) string { return t.Name })
	if len(suggestions) > 0 {
		return "", "", output.ErrNotFoundHint("Todolist", input, "Did you mean: "+strings.Join(suggestions, ", "))
	}
	return "", "", output.ErrNotFound("Todolist", input)
}

// ClearCache clears the session cache.
func (r *Resolver) ClearCache() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.projects = nil
	r.people = nil
	r.todolists = make(map[string][]Todolist)
}

// Data fetching with caching

func (r *Resolver) getProjects(ctx context.Context) ([]Project, error) {
	r.mu.RLock()
	if r.projects != nil {
		defer r.mu.RUnlock()
		return r.projects, nil
	}
	r.mu.RUnlock()

	r.mu.Lock()
	defer r.mu.Unlock()

	// Double-check after acquiring write lock
	if r.projects != nil {
		return r.projects, nil
	}

	// Fetch from API
	resp, err := r.sdk.Get(ctx, "/projects.json")
	if err != nil {
		return nil, convertSDKError(err)
	}

	var projects []Project
	if err := json.Unmarshal(resp.Data, &projects); err != nil {
		return nil, err
	}

	r.projects = projects
	return projects, nil
}

func (r *Resolver) getPeople(ctx context.Context) ([]Person, error) {
	r.mu.RLock()
	if r.people != nil {
		defer r.mu.RUnlock()
		return r.people, nil
	}
	r.mu.RUnlock()

	r.mu.Lock()
	defer r.mu.Unlock()

	// Double-check after acquiring write lock
	if r.people != nil {
		return r.people, nil
	}

	// Fetch from API
	resp, err := r.sdk.Get(ctx, "/people.json")
	if err != nil {
		return nil, convertSDKError(err)
	}

	var people []Person
	if err := json.Unmarshal(resp.Data, &people); err != nil {
		return nil, err
	}

	r.people = people
	return people, nil
}

func (r *Resolver) getTodolists(ctx context.Context, projectID string) ([]Todolist, error) {
	r.mu.RLock()
	if lists, ok := r.todolists[projectID]; ok {
		defer r.mu.RUnlock()
		return lists, nil
	}
	r.mu.RUnlock()

	r.mu.Lock()
	defer r.mu.Unlock()

	// Double-check after acquiring write lock
	if lists, ok := r.todolists[projectID]; ok {
		return lists, nil
	}

	// First get the project to find the todoset ID
	projectResp, err := r.sdk.Get(ctx, "/projects/"+projectID+".json")
	if err != nil {
		return nil, convertSDKError(err)
	}

	var projectData struct {
		Dock []struct {
			Name string `json:"name"`
			ID   int64  `json:"id"`
		} `json:"dock"`
	}
	if err := json.Unmarshal(projectResp.Data, &projectData); err != nil {
		return nil, err
	}

	// Find todoset in dock
	var todosetID int64
	for _, dock := range projectData.Dock {
		if dock.Name == "todoset" {
			todosetID = dock.ID
			break
		}
	}

	if todosetID == 0 {
		// Project has no todoset - return empty list
		r.todolists[projectID] = nil
		return nil, nil
	}

	// Fetch todolists from todoset
	todolistsPath := fmt.Sprintf("/buckets/%s/todosets/%d/todolists.json", projectID, todosetID)
	resp, err := r.sdk.Get(ctx, todolistsPath)
	if err != nil {
		return nil, convertSDKError(err)
	}

	var todolists []Todolist
	if err := json.Unmarshal(resp.Data, &todolists); err != nil {
		return nil, err
	}

	r.todolists[projectID] = todolists
	return todolists, nil
}

// Resolution helpers

// resolve performs name resolution in priority order:
// 1. Exact match (case-sensitive)
// 2. Case-insensitive match
// 3. Partial match (contains)
// Returns the single match if unambiguous, or all partial matches if ambiguous.
func resolve[T any](input string, items []T, extract func(T) (int64, string)) (*T, []T) {
	inputLower := strings.ToLower(input)

	// Phase 1: Exact match
	for i := range items {
		_, name := extract(items[i])
		if name == input {
			return &items[i], nil
		}
	}

	// Phase 2: Case-insensitive match
	var caseMatches []T
	for i := range items {
		_, name := extract(items[i])
		if strings.ToLower(name) == inputLower {
			caseMatches = append(caseMatches, items[i])
		}
	}
	if len(caseMatches) == 1 {
		return &caseMatches[0], nil
	}
	if len(caseMatches) > 1 {
		return nil, caseMatches
	}

	// Phase 3: Partial match (contains)
	var partialMatches []T
	for i := range items {
		_, name := extract(items[i])
		if strings.Contains(strings.ToLower(name), inputLower) {
			partialMatches = append(partialMatches, items[i])
		}
	}
	if len(partialMatches) == 1 {
		return &partialMatches[0], nil
	}
	return nil, partialMatches
}

// suggest returns up to 3 suggestions for similar names.
func suggest[T any](input string, items []T, getName func(T) string) []string {
	inputLower := strings.ToLower(input)
	var suggestions []string

	// Simple heuristic: names that share a common prefix or contain a word
	for _, item := range items {
		name := getName(item)
		nameLower := strings.ToLower(name)

		// Check for common prefix (at least 2 chars)
		commonLen := 0
		for i := 0; i < len(inputLower) && i < len(nameLower); i++ {
			if inputLower[i] == nameLower[i] {
				commonLen++
			} else {
				break
			}
		}

		if commonLen >= 2 || containsWord(nameLower, inputLower) {
			suggestions = append(suggestions, name)
			if len(suggestions) >= 3 {
				break
			}
		}
	}

	return suggestions
}

// containsWord checks if haystack contains any word from needle.
func containsWord(haystack, needle string) bool {
	words := strings.Fields(needle)
	for _, word := range words {
		if len(word) >= 2 && strings.Contains(haystack, word) {
			return true
		}
	}
	return false
}

// GetProjects returns all projects (useful for pickers).
func (r *Resolver) GetProjects(ctx context.Context) ([]Project, error) {
	return r.getProjects(ctx)
}

// GetPeople returns all people (useful for pickers).
func (r *Resolver) GetPeople(ctx context.Context) ([]Person, error) {
	return r.getPeople(ctx)
}

// GetTodolists returns all todolists for a project (useful for pickers).
func (r *Resolver) GetTodolists(ctx context.Context, projectID string) ([]Todolist, error) {
	return r.getTodolists(ctx, projectID)
}

// convertSDKError converts SDK errors to output errors for consistent error handling.
func convertSDKError(err error) error {
	var sdkErr *basecamp.Error
	if errors.As(err, &sdkErr) {
		return &output.Error{
			Code:       sdkErr.Code,
			Message:    sdkErr.Message,
			Hint:       sdkErr.Hint,
			HTTPStatus: sdkErr.HTTPStatus,
			Retryable:  sdkErr.Retryable,
		}
	}
	return err
}
