package workspace

// nav is the stack of screens the reader has walked into. The bottom is home
// and is never popped, so there is always something to draw.
type nav struct {
	stack []View
}

func newNav(root View) nav {
	return nav{stack: []View{root}}
}

// current is the screen on top: the one being drawn and given keys.
func (n *nav) current() View {
	return n.stack[len(n.stack)-1]
}

// push opens a screen over the current one, which keeps its state underneath
// and is what the reader comes back to.
func (n *nav) push(view View) {
	n.stack = append(n.stack, view)
}

// pop closes the top screen and reports whether there was one to close. At home
// there is not, and esc does nothing.
func (n *nav) pop() bool {
	if len(n.stack) == 1 {
		return false
	}
	n.stack[len(n.stack)-1] = nil
	n.stack = n.stack[:len(n.stack)-1]
	return true
}

// depth is how far in the reader has walked. Home is 1.
func (n *nav) depth() int {
	return len(n.stack)
}

// at is the screen at a depth, counting from home at 0, and nil past the top.
// It is how something that changed one screen reaches another that is showing
// the same thing.
func (n *nav) at(depth int) View {
	if depth < 0 || depth >= len(n.stack) {
		return nil
	}
	return n.stack[depth]
}

// trail is the titles of every screen from home up, which is what the
// breadcrumb draws.
func (n *nav) trail() []string {
	titles := make([]string, len(n.stack))
	for index, view := range n.stack {
		titles[index] = view.Title()
	}
	return titles
}
