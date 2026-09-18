package setup

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/basecamp/basecamp-cli/internal/connector/admission"
)

// Changes is what one setup run asks to change. A zero field leaves the
// file's value alone, so setup can be run again to serve another project
// without restating everything else.
type Changes struct {
	// Trust is the trust mode, "" to keep the file's.
	Trust admission.TrustMode
	// Allow replaces the allowlist when non-empty; it implies allowlist mode.
	Allow []int64

	// Serve are the project (bucket) ids to serve. A project already served
	// keeps its class and watch_completions.
	Serve []int64
	// Classes sets a served project's class; an empty class clears it.
	Classes map[int64]string
	// WatchCompletions turns watch_completions on (true) or off (false) for
	// a served project.
	WatchCompletions map[int64]bool
	// Remove stops serving projects.
	Remove []int64

	Driver string
	// Worker is the coding agent, "" to keep the file's.
	Worker      string
	Concurrency int
	Deadline    time.Duration
}

// Apply returns f with ch applied. f is not modified. Everything that can
// be refused without the network is refused here; the result still has to
// pass Validate, which Save runs.
func Apply(f File, ch Changes) (File, error) {
	out := f
	out.Projects = make(map[int64]admission.Project, len(f.Projects))
	for id, r := range f.Projects {
		out.Projects[id] = r
	}
	out.Trust.AllowlistIDs = slices.Clone(f.Trust.AllowlistIDs)

	if err := applyTrust(&out.Trust, ch); err != nil {
		return File{}, err
	}

	for _, id := range ch.Remove {
		if slices.Contains(ch.Serve, id) {
			return File{}, fmt.Errorf("project %d is both served and removed in one run", id)
		}
		delete(out.Projects, id) // no longer serving a project that was not served is already done
	}
	for _, id := range ch.Serve {
		if id <= 0 {
			return File{}, fmt.Errorf("serve: %d is not a project id", id)
		}
		if _, served := out.Projects[id]; !served {
			// A project already served keeps its class and watch_completions;
			// serving it again is not a reset.
			out.Projects[id] = admission.Project{}
		}
	}
	for id, class := range ch.Classes {
		r, ok := out.Projects[id]
		if !ok {
			return File{}, fmt.Errorf("class for project %d: the project is not served; serve it with --serve %d", id, id)
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
			return File{}, fmt.Errorf("watch_completions for project %d: the project is not served; serve it with --serve %d", id, id)
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
