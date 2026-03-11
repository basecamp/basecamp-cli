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

	"github.com/basecamp/basecamp-cli/internal/auth"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// Resolver resolves names to IDs for projects, people, and todolists.
type Resolver struct {
	sdk       *basecamp.Client
	auth      *auth.Manager
	accountID string

	// resolveMeFn overrides the "me" resolution path. Nil in production
	// (uses SDK People().Me), set in tests to return canned values.
	resolveMeFn func(context.Context) (int64, string, error)

	// Session-scoped cache
	mu        sync.RWMutex
	projects  []Project
	people    []Person
	pingable  []Person              // cached /people/pingable.json
	todolists map[string][]Todolist // keyed by project ID
	me        *Person               // cached /my/profile.json result
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
// The accountID is used to configure the SDK client for account-scoped API calls.
func NewResolver(sdkClient *basecamp.Client, authMgr *auth.Manager, accountID string) *Resolver {
	return &Resolver{
		sdk:       sdkClient,
		auth:      authMgr,
		accountID: accountID,
		todolists: make(map[string][]Todolist),
	}
}

// SetAccountID updates the account ID used by the resolver.
// This clears the cache since cached data is account-specific.
func (r *Resolver) SetAccountID(accountID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.accountID != accountID {
		r.accountID = accountID
		// Clear cache since data is account-specific
		r.projects = nil
		r.people = nil
		r.pingable = nil
		r.me = nil
		r.todolists = make(map[string][]Todolist)
	}
}

// forAccount returns an account-scoped client for the resolver's account.
// This should be called before making account-scoped API requests.
func (r *Resolver) forAccount() *basecamp.AccountClient {
	return r.sdk.ForAccount(r.accountID)
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
	// Handle "me" keyword — resolve via /my/profile.json which returns
	// the authenticated user's account-scoped person record directly.
	if strings.EqualFold(input, "me") {
		if r.resolveMeFn != nil {
			id, name, err := r.resolveMeFn(ctx)
			if err != nil {
				return "", "", err
			}
			return strconv.FormatInt(id, 10), name, nil
		}
		person, err := r.getMe(ctx)
		if err != nil {
			return "", "", err
		}
		return strconv.FormatInt(person.ID, 10), person.Name, nil
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

	// Fallback: try pingable people (/people/pingable.json) which includes
	// people not in /people.json (e.g., clients, external collaborators).
	// Degrade gracefully on error — the people list already provides suggestions.
	pingable, _ := r.getPingable(ctx)

	if len(pingable) > 0 {
		// Try email exact match
		for _, p := range pingable {
			if strings.EqualFold(p.Email, input) {
				return strconv.FormatInt(p.ID, 10), p.Name, nil
			}
		}

		// Try name resolution
		pingMatch, pingMatches := resolve(input, pingable, func(p Person) (int64, string) {
			return p.ID, p.Name
		})
		if pingMatch != nil {
			return strconv.FormatInt(pingMatch.ID, 10), pingMatch.Name, nil
		}
		if len(pingMatches) > 1 {
			pingNames := make([]string, len(pingMatches))
			for i, m := range pingMatches {
				pingNames[i] = m.Name
			}
			return "", "", output.ErrAmbiguous("person", pingNames)
		}
	}

	// Not found - provide suggestions from both lists (deduplicated by ID)
	seen := make(map[int64]struct{}, len(people))
	allPeople := make([]Person, 0, len(people)+len(pingable))
	for _, p := range people {
		seen[p.ID] = struct{}{}
		allPeople = append(allPeople, p)
	}
	for _, p := range pingable {
		if _, ok := seen[p.ID]; !ok {
			allPeople = append(allPeople, p)
		}
	}
	suggestions := suggest(input, allPeople, func(p Person) string { return p.Name })
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
	r.pingable = nil
	r.me = nil
	r.todolists = make(map[string][]Todolist)
}

// Data fetching with caching

func (r *Resolver) getMe(ctx context.Context) (*Person, error) {
	r.mu.RLock()
	if r.me != nil {
		defer r.mu.RUnlock()
		return r.me, nil
	}
	r.mu.RUnlock()

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.me != nil {
		return r.me, nil
	}

	person, err := r.forAccount().People().Me(ctx)
	if err != nil {
		return nil, convertSDKError(err)
	}

	r.me = &Person{
		ID:    person.ID,
		Name:  person.Name,
		Email: person.EmailAddress,
	}
	return r.me, nil
}

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

	// Fetch all pages from API
	pages, err := r.forAccount().GetAll(ctx, "/projects.json")
	if err != nil {
		return nil, convertSDKError(err)
	}

	projects := make([]Project, 0, len(pages))
	for _, page := range pages {
		var p Project
		if err := json.Unmarshal(page, &p); err != nil {
			return nil, err
		}
		projects = append(projects, p)
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

	// Fetch all pages from API
	pages, err := r.forAccount().GetAll(ctx, "/people.json")
	if err != nil {
		return nil, convertSDKError(err)
	}

	people := make([]Person, 0, len(pages))
	for _, page := range pages {
		var p Person
		if err := json.Unmarshal(page, &p); err != nil {
			return nil, err
		}
		people = append(people, p)
	}

	r.people = people
	return people, nil
}

func (r *Resolver) getPingable(ctx context.Context) ([]Person, error) {
	r.mu.RLock()
	if r.pingable != nil {
		defer r.mu.RUnlock()
		return r.pingable, nil
	}
	r.mu.RUnlock()

	r.mu.Lock()
	defer r.mu.Unlock()

	// Double-check after acquiring write lock
	if r.pingable != nil {
		return r.pingable, nil
	}

	sdkResult, err := r.forAccount().People().Pingable(ctx, nil)
	if err != nil {
		return nil, convertSDKError(err)
	}

	pingable := make([]Person, 0, len(sdkResult.People))
	for _, p := range sdkResult.People {
		pingable = append(pingable, Person{
			ID:    p.ID,
			Name:  p.Name,
			Email: p.EmailAddress,
		})
	}

	r.pingable = pingable
	return pingable, nil
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
	projectResp, err := r.forAccount().Get(ctx, "/projects/"+projectID+".json")
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

	// Find all todosets in dock
	var todosetIDs []int64
	for _, dock := range projectData.Dock {
		if dock.Name == "todoset" {
			todosetIDs = append(todosetIDs, dock.ID)
		}
	}

	if len(todosetIDs) == 0 {
		// Project has no todoset - return empty list
		r.todolists[projectID] = nil
		return nil, nil
	}

	// Fetch todolists from all todosets and merge results
	var todolists []Todolist
	for _, tsID := range todosetIDs {
		todolistsPath := fmt.Sprintf("/todosets/%d/todolists.json", tsID)
		pages, err := r.forAccount().GetAll(ctx, todolistsPath)
		if err != nil {
			sdkErr := convertSDKError(err)
			if oe := output.AsError(sdkErr); oe != nil {
				oe.Message = fmt.Sprintf("todoset %d: %s", tsID, oe.Message)
				return nil, oe
			}
			return nil, fmt.Errorf("failed to fetch todolists from todoset %d: %w", tsID, sdkErr)
		}
		for _, page := range pages {
			var tl Todolist
			if err := json.Unmarshal(page, &tl); err != nil {
				return nil, fmt.Errorf("failed to parse todolist from todoset %d: %w", tsID, err)
			}
			todolists = append(todolists, tl)
		}
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
	words := strings.FieldsSeq(needle)
	for word := range words {
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
	if err == nil {
		return nil
	}

	// Handle resilience sentinel errors (use errors.Is for wrapped errors)
	if errors.Is(err, basecamp.ErrRateLimited) {
		return &output.Error{
			Code:      basecamp.CodeRateLimit,
			Message:   "Rate limit exceeded",
			Hint:      "Too many requests. Please wait before trying again.",
			Retryable: true,
		}
	}
	if errors.Is(err, basecamp.ErrCircuitOpen) {
		return &output.Error{
			Code:      basecamp.CodeAPI,
			Message:   "Service temporarily unavailable",
			Hint:      "The circuit breaker is open due to recent failures. Please wait before trying again.",
			Retryable: true,
		}
	}
	if errors.Is(err, basecamp.ErrBulkheadFull) {
		return &output.Error{
			Code:      basecamp.CodeRateLimit,
			Message:   "Too many concurrent requests",
			Hint:      "Maximum concurrent operations reached. Please wait for other operations to complete.",
			Retryable: true,
		}
	}

	// Handle structured SDK errors
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
