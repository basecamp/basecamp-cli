package connector

import (
	"context"
	"errors"
	"io/fs"
	"os"
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
	case req.Kind == driver.ToolThink:
		// The only allowed kind that touches no file.
		return driver.PermissionDecision{Allow: true}
	case slices.Contains(policyAllowedKinds, req.Kind), req.Kind == driver.ToolEdit:
		// A call on the filesystem that names no path is one the policy
		// cannot place inside the working directory, so it is refused.
		return driver.PermissionDecision{Allow: len(req.Locations) > 0 && p.inside(req.Locations)}
	}
	return driver.PermissionDecision{Allow: false}
}

// maxLinkHops bounds how many links one path may be resolved through, as the
// kernel's ELOOP does. A loop of links names no file, and a path this cannot
// resolve is refused rather than guessed at.
const maxLinkHops = 32

// resolveExisting resolves the symlinks in the longest existing prefix of an
// absolute path and appends the rest, which does not exist yet and so cannot
// be a link.
//
// It walks the components itself rather than leaning on EvalSymlinks alone,
// because EvalSymlinks answers ENOENT to two opposite questions: a component
// that is not there, and a symlink that IS there and points at something
// that is not. Treating the second as a name yet to be created approved a
// write to <workdir>/link when the link pointed at /elsewhere/missing —
// which is where the write would land, creating a file outside the working
// directory (Copilot on #738). A link that exists is followed to wherever it
// points, existing or not, and a link that cannot be read resolves to
// nothing.
func resolveExisting(path string) (string, bool) {
	return resolveHops(path, maxLinkHops)
}

func resolveHops(path string, hops int) (string, bool) {
	if hops <= 0 || !filepath.IsAbs(path) {
		return "", false
	}
	rest := ""
	for current := filepath.Clean(path); ; {
		info, err := os.Lstat(current)
		switch {
		case err == nil && info.Mode()&fs.ModeSymlink != 0:
			// A link that is there. Where it points is where a write to this
			// path lands, whether or not anything is there yet.
			target, err := os.Readlink(current)
			if err != nil {
				return "", false
			}
			if !filepath.IsAbs(target) {
				target = filepath.Join(filepath.Dir(current), target)
			}
			resolved, ok := resolveHops(target, hops-1)
			if !ok {
				return "", false
			}
			return filepath.Join(resolved, rest), true
		case err == nil:
			// Something that is there and is not a link; the links above it
			// are what is left to resolve.
			resolved, err := filepath.EvalSymlinks(current)
			if err != nil {
				return "", false
			}
			return filepath.Join(resolved, rest), true
		case errors.Is(err, fs.ErrNotExist):
			parent := filepath.Dir(current)
			if parent == current {
				return "", false
			}
			rest = filepath.Join(filepath.Base(current), rest)
			current = parent
		default:
			return "", false
		}
	}
}

// inside reports whether every location is within the working directory, as
// the filesystem resolves it: a symlink inside the directory that points out
// of it is outside.
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
