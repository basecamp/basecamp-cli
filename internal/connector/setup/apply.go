package setup

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/basecamp/basecamp-cli/internal/connector/admission"
)

// Changes is what one setup run asks to change. A zero field leaves the
// file's value alone, so setup can be run again to add a route without
// restating everything else.
type Changes struct {
	// Trust is the trust mode, "" to keep the file's.
	Trust admission.TrustMode
	// Allow replaces the allowlist when non-empty; it implies allowlist mode.
	Allow []int64

	// Routes maps a project (bucket) id to the directory its work runs in,
	// as the operator typed it; ResolveDir makes it the approved entry.
	Routes map[int64]string
	// Classes sets a routed project's class; an empty class clears it.
	Classes map[int64]string
	// WatchCompletions turns watch_completions on (true) or off (false) for
	// a routed project.
	WatchCompletions map[int64]bool
	// Remove drops projects' routes.
	Remove []int64

	Driver string
	// Worker is the coding agent, "" to keep the file's.
	Worker      string
	Concurrency int
	Deadline    time.Duration
	// Worktrees is nil to keep the file's value.
	Worktrees *bool
}

// Apply returns f with ch applied. f is not modified. Everything that can
// be refused without the network is refused here; the result still has to
// pass Validate, which Save runs.
func Apply(f File, ch Changes) (File, error) {
	out := f
	out.Projects = make(map[int64]admission.Route, len(f.Projects))
	for id, r := range f.Projects {
		out.Projects[id] = r
	}
	out.Trust.AllowlistIDs = slices.Clone(f.Trust.AllowlistIDs)

	if err := applyTrust(&out.Trust, ch); err != nil {
		return File{}, err
	}

	for _, id := range ch.Remove {
		if _, routed := ch.Routes[id]; routed {
			return File{}, fmt.Errorf("project %d is both routed and removed in one run", id)
		}
		delete(out.Projects, id) // removing a route that is not there is already done
	}
	for id, raw := range ch.Routes {
		if id <= 0 {
			return File{}, fmt.Errorf("route: %d is not a project id", id)
		}
		dir, err := ResolveDir(raw)
		if err != nil {
			return File{}, fmt.Errorf("route for project %d: %w", id, err)
		}
		r := out.Projects[id]
		r.Path = dir
		out.Projects[id] = r
	}
	for id, class := range ch.Classes {
		r, ok := out.Projects[id]
		if !ok {
			return File{}, fmt.Errorf("class for project %d: the project has no route; add one with --route %d=<dir>", id, id)
		}
		if class != "" && !ValidClass(class) {
			return File{}, fmt.Errorf("class %q for project %d: use lowercase letters, digits, - or _, at most 40", class, id)
		}
		r.Class = class
		out.Projects[id] = r
	}
	for id, on := range ch.WatchCompletions {
		r, ok := out.Projects[id]
		if !ok {
			return File{}, fmt.Errorf("watch_completions for project %d: the project has no route; add one with --route %d=<dir>", id, id)
		}
		r.WatchCompletions = on
		out.Projects[id] = r
	}

	if ch.Driver != "" {
		out.Driver = ch.Driver
	}
	if ch.Worker != "" {
		if !slices.Contains(Workers, ch.Worker) {
			return File{}, fmt.Errorf("worker %q is not one of %s", ch.Worker, strings.Join(Workers, ", "))
		}
		out.Worker = ch.Worker
	}
	if ch.Concurrency != 0 {
		out.Concurrency = ch.Concurrency
	}
	if ch.Deadline != 0 {
		out.Deadline = Duration(ch.Deadline)
	}
	if ch.Worktrees != nil {
		out.Worktrees = *ch.Worktrees
	}
	return out, nil
}

func applyTrust(t *admission.Trust, ch Changes) error {
	mode := ch.Trust
	if len(ch.Allow) > 0 {
		if mode != "" && mode != admission.TrustAllowlist {
			return fmt.Errorf("--allow names people to trust, which is allowlist mode, not %q", mode)
		}
		mode = admission.TrustAllowlist
	}
	if mode == "" {
		return nil
	}
	switch mode {
	case admission.TrustOperator, admission.TrustProject:
		// Leaving allowlist mode drops the list: admission refuses a list
		// outside its mode, and a list kept "for later" is trust nobody
		// can see.
		t.AllowlistIDs = nil
	case admission.TrustAllowlist:
		if len(ch.Allow) > 0 {
			ids := slices.Clone(ch.Allow)
			slices.Sort(ids)
			t.AllowlistIDs = slices.Compact(ids)
		}
		if len(t.AllowlistIDs) == 0 {
			return errors.New("allowlist mode needs at least one --allow <person-id>")
		}
	default:
		return fmt.Errorf("unknown trust mode %q: use operator, allowlist or project", mode)
	}
	t.Mode = mode
	return nil
}

// ResolveDir turns a route directory as typed into the entry connect.json
// approves: ~ expanded, absolute, symlinks resolved, and required to be an
// existing directory. Resolving symlinks means the approved entry names the
// directory work will really run in, so a link retargeted later does not
// move the route with it.
func ResolveDir(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", errors.New("the directory is empty")
	}
	path := raw
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("expand ~: %w", err)
		}
		path = filepath.Join(home, strings.TrimPrefix(path, "~"))
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("%s: %w", abs, err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", resolved)
	}
	return filepath.Clean(resolved), nil
}
