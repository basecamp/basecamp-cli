package workspace

// routerEntry holds a view and its associated scope for the navigation stack.
type routerEntry struct {
	view  View
	scope Scope
}

// Router manages the navigation stack with state preservation.
type Router struct {
	stack []routerEntry
}

// NewRouter creates an empty router.
func NewRouter() *Router {
	return &Router{}
}

// Reset clears the entire navigation stack.
func (r *Router) Reset() {
	r.stack = nil
}

// Push adds a view to the navigation stack.
func (r *Router) Push(view View, scope Scope) {
	r.stack = append(r.stack, routerEntry{view: view, scope: scope})
}

// Pop removes and returns the top view from the stack.
// Returns nil if the stack has one or fewer entries (never pops the root).
func (r *Router) Pop() View {
	if len(r.stack) <= 1 {
		return nil
	}
	r.stack = r.stack[:len(r.stack)-1]
	return r.Current()
}

// Current returns the current (top) view, or nil if empty.
func (r *Router) Current() View {
	if len(r.stack) == 0 {
		return nil
	}
	return r.stack[len(r.stack)-1].view
}

// CurrentScope returns the scope of the current view.
func (r *Router) CurrentScope() Scope {
	if len(r.stack) == 0 {
		return Scope{}
	}
	return r.stack[len(r.stack)-1].scope
}

// Depth returns the current stack depth.
func (r *Router) Depth() int {
	return len(r.stack)
}

// CanGoBack returns true if there is a previous view to return to.
func (r *Router) CanGoBack() bool {
	return len(r.stack) > 1
}

// Breadcrumbs returns the title chain for all views in the stack.
func (r *Router) Breadcrumbs() []string {
	crumbs := make([]string, len(r.stack))
	for i, entry := range r.stack {
		crumbs[i] = entry.view.Title()
	}
	return crumbs
}

// PopToDepth pops entries until the stack is at the given depth.
// Returns the view at the target depth, or nil if invalid.
func (r *Router) PopToDepth(depth int) View {
	if depth < 1 || depth > len(r.stack) {
		return nil
	}
	r.stack = r.stack[:depth]
	return r.Current()
}
