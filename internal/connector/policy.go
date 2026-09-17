package connector

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"

	"github.com/basecamp/basecamp-cli/internal/connector/driver"
)

// Policy is the connector's v1 permission policy: work in the working
// directory and the agent's Basecamp MCP tools are allowed, and the rest is
// refused without asking anyone. It is policy, not containment: the worker
// runs with the operator's ambient authority, as it does today, and a
// sandbox launcher is what contains it.
type Policy struct {
	WorkDir string
}

var _ driver.PermissionPolicy = Policy{}

// DefaultPolicy is the v1 policy for a working directory.
func DefaultPolicy(workDir string) Policy { return Policy{WorkDir: workDir} }

// policyAllowedKinds are what a worker does without asking, besides edits
// inside the working directory.
var policyAllowedKinds = []driver.ToolKind{driver.ToolRead, driver.ToolSearch, driver.ToolThink}

// Rules implements driver.PermissionPolicy.
func (p Policy) Rules() driver.PermissionRules {
	return driver.PermissionRules{
		Mode:            driver.ModeEditsInWorkDir,
		WorkDir:         p.WorkDir,
		AllowKinds:      slices.Clone(policyAllowedKinds),
		AllowMCPServers: []string{MCPServerName},
	}
}

// Decide implements driver.PermissionPolicy.
func (p Policy) Decide(_ context.Context, req driver.PermissionRequest) driver.PermissionDecision {
	if strings.HasPrefix(req.Tool, "mcp__"+MCPServerName+"__") {
		return driver.PermissionDecision{Allow: true}
	}
	switch {
	case slices.Contains(policyAllowedKinds, req.Kind):
		return driver.PermissionDecision{Allow: p.inside(req.Locations)}
	case req.Kind == driver.ToolEdit:
		return driver.PermissionDecision{Allow: len(req.Locations) > 0 && p.inside(req.Locations)}
	}
	return driver.PermissionDecision{Allow: false}
}

// resolveExisting resolves the symlinks in the longest existing prefix of an
// absolute path and appends the rest, which does not exist yet and so cannot
// be a link.
func resolveExisting(path string) (string, bool) {
	rest := ""
	for current := path; ; {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			return filepath.Join(resolved, rest), true
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", false
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", false
		}
		rest = filepath.Join(filepath.Base(current), rest)
		current = parent
	}
}

// inside reports whether every location is within the working directory, as
// the filesystem resolves it: a symlink inside the directory that points out
// of it is outside. No locations means nothing outside is touched.
func (p Policy) inside(locations []string) bool {
	root, err := filepath.EvalSymlinks(filepath.Clean(p.WorkDir))
	if err != nil {
		return false
	}
	for _, loc := range locations {
		if !filepath.IsAbs(loc) {
			loc = filepath.Join(p.WorkDir, loc)
		}
		resolved, ok := resolveExisting(filepath.Clean(loc))
		if !ok {
			return false
		}
		rel, err := filepath.Rel(root, resolved)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return false
		}
	}
	return true
}
