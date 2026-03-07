package tui

import (
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/term"
)

const (
	blankBraille  = '⠀' // U+2800
	paintInterval = 25 * time.Millisecond
	textInterval  = 35 * time.Millisecond
	traceFrames   = 30 // target number of animation frames for the trace
)

// Paint trail colors — warm palette converging on brand yellow.
var (
	// darkTrail: coral → copper → amber → gold on dark backgrounds.
	darkTrail = []string{"#c85840", "#d07830", "#d89424", "#e0a01c"}
	// lightTrail: deeper warm tones visible on light backgrounds.
	lightTrail = []string{"#984038", "#906028", "#887020", "#808018"}
)

// paintCell is a single cell position in the wordmark grid.
type paintCell struct {
	row, col int
}

// TraceFunc computes the order in which non-blank cells are revealed.
// It receives the rune grid and returns cells in first-visit order.
type TraceFunc func(grid [][]rune, numLines int) []paintCell

// Named animation strategies.
var animations = map[string]TraceFunc{
	"warnsdorff": traceWarnsdorff,
	"outline-in": traceOutlineIn,
	"radial":     traceRadial,
	"scanline":   traceScanline,
}

// DefaultAnimation is the animation used when none is specified.
const DefaultAnimation = "outline-in"

// AnimationNames returns the registered animation strategy names.
func AnimationNames() []string {
	names := make([]string, 0, len(animations))
	for name := range animations {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// AnimateWordmark draws the wordmark with a paint animation.
// Honors BASECAMP_ANIM to override the default strategy.
func AnimateWordmark(w io.Writer, theme Theme) {
	name := os.Getenv("BASECAMP_ANIM")
	if name == "" {
		name = DefaultAnimation
	}
	AnimateWordmarkWith(w, theme, name)
}

// AnimateWordmarkWith draws the wordmark using the named animation strategy.
// Unknown strategy names fall back to DefaultAnimation. Falls back to static
// render if w is not a TTY or NO_COLOR is active.
func AnimateWordmarkWith(w io.Writer, theme Theme, strategy string) {
	if _, noColor := theme.Primary.(lipgloss.NoColor); !isWriterTTY(w) || noColor {
		fmt.Fprint(w, RenderWordmark(theme))
		return
	}

	traceFn, ok := animations[strategy]
	if !ok {
		traceFn = animations[DefaultAnimation]
	}

	grid, numLines := wordmarkGrid()
	order := traceFn(grid, numLines)
	if len(order) == 0 {
		fmt.Fprint(w, RenderWordmark(theme))
		return
	}

	batchSize := max(1, len(order)/traceFrames)
	numBatches := (len(order) + batchSize - 1) / batchSize

	styles, settled := trailStyles(theme)
	textStyle := lipgloss.NewStyle().Foreground(BrandColor).Bold(true)

	revealFrame := makeRevealGrid(grid, numLines)

	// Phase 1: Trace — reveal one batch per frame
	totalFrames := numBatches + settled
	for frame := 0; frame < totalFrames; frame++ {
		if frame < numBatches {
			start := frame * batchSize
			end := min(start+batchSize, len(order))
			for _, c := range order[start:end] {
				revealFrame[c.row][c.col] = frame
			}
		}

		if frame > 0 {
			fmt.Fprintf(w, "\033[%dA", numLines)
		}
		renderPaintFrame(w, grid, revealFrame, frame, styles, settled, numLines, "", textStyle)
		time.Sleep(paintInterval)
	}

	// Phase 2: Reveal "Basecamp" letter by letter
	for i := 1; i <= len(brandText); i++ {
		fmt.Fprintf(w, "\033[%dA", numLines)
		renderPaintFrame(w, grid, revealFrame, totalFrames, styles, settled, numLines, brandText[:i], textStyle)
		time.Sleep(textInterval)
	}
}

// ---------------------------------------------------------------------------
// Non-blocking animation
// ---------------------------------------------------------------------------

// AnimWriter is a mutex-protected io.Writer that tracks terminal rows written
// below the animation area. The animation goroutine uses this count to
// cursor-up past both the logo and any caller output, then cursor-down
// to restore the caller's write position. When cols > 0, it accounts for
// soft-wrapping at the terminal width.
type AnimWriter struct {
	w          io.Writer
	mu         sync.Mutex
	linesBelow int
	done       chan struct{}
	numLines   int // height of the animation area
	cols       int // terminal width; 0 = fall back to \n counting
	pendingW   int // visual width of the current unterminated line
}

// Write writes p through to the underlying writer and counts visual rows
// consumed, including rows added by soft-wrapping at the terminal width.
func (aw *AnimWriter) Write(p []byte) (int, error) {
	aw.mu.Lock()
	defer aw.mu.Unlock()
	n, err := aw.w.Write(p)
	rows, pw := visualLines(string(p[:n]), aw.cols, aw.pendingW)
	aw.linesBelow += rows
	aw.pendingW = pw
	return n, err
}

// Wait blocks until the animation goroutine finishes. Idempotent.
func (aw *AnimWriter) Wait() {
	<-aw.done
}

// AnimateWordmarkAsync starts the animation in a background goroutine and
// returns a writer for subsequent output plus a wait function. Output written
// through the returned writer appears below the animating logo. Call wait
// before any interactive prompts or output that bypasses the returned writer.
// Falls back to static render (returning w unchanged and no-op wait) when
// not a TTY or NO_COLOR is active.
func AnimateWordmarkAsync(w io.Writer, theme Theme) (io.Writer, func()) {
	if _, noColor := theme.Primary.(lipgloss.NoColor); !isWriterTTY(w) || noColor {
		fmt.Fprint(w, RenderWordmark(theme))
		return w, func() {}
	}

	name := os.Getenv("BASECAMP_ANIM")
	if name == "" {
		name = DefaultAnimation
	}

	traceFn, ok := animations[name]
	if !ok {
		traceFn = animations[DefaultAnimation]
	}

	grid, numLines := wordmarkGrid()
	order := traceFn(grid, numLines)
	if len(order) == 0 {
		fmt.Fprint(w, RenderWordmark(theme))
		return w, func() {}
	}

	batchSize := max(1, len(order)/traceFrames)
	numBatches := (len(order) + batchSize - 1) / batchSize

	styles, settled := trailStyles(theme)
	textStyle := lipgloss.NewStyle().Foreground(BrandColor).Bold(true)

	revealFrame := makeRevealGrid(grid, numLines)

	// Render frame 0 synchronously — first cells appear instantly
	end := min(batchSize, len(order))
	for _, c := range order[:end] {
		revealFrame[c.row][c.col] = 0
	}
	renderPaintFrame(w, grid, revealFrame, 0, styles, settled, numLines, "", textStyle)

	// Query terminal width for soft-wrap accounting
	var cols int
	if f, ok := w.(*os.File); ok {
		if width, _, err := term.GetSize(f.Fd()); err == nil {
			cols = width
		}
	}

	aw := &AnimWriter{
		w:        w,
		done:     make(chan struct{}),
		numLines: numLines,
		cols:     cols,
	}

	totalFrames := numBatches + settled
	go func() {
		defer close(aw.done)

		// Phase 1: Trace — one batch per frame (from frame 1)
		for frame := 1; frame < totalFrames; frame++ {
			if frame < numBatches {
				start := frame * batchSize
				end := min(start+batchSize, len(order))
				for _, c := range order[start:end] {
					revealFrame[c.row][c.col] = frame
				}
			}

			aw.mu.Lock()
			fmt.Fprintf(w, "\033[%dA", numLines+aw.linesBelow)
			renderPaintFrame(w, grid, revealFrame, frame, styles, settled, numLines, "", textStyle)
			if aw.linesBelow > 0 {
				fmt.Fprintf(w, "\033[%dB", aw.linesBelow)
			}
			aw.mu.Unlock()
			time.Sleep(paintInterval)
		}

		// Phase 2: Reveal "Basecamp" letter by letter
		for i := 1; i <= len(brandText); i++ {
			aw.mu.Lock()
			fmt.Fprintf(w, "\033[%dA", numLines+aw.linesBelow)
			renderPaintFrame(w, grid, revealFrame, totalFrames, styles, settled, numLines, brandText[:i], textStyle)
			if aw.linesBelow > 0 {
				fmt.Fprintf(w, "\033[%dB", aw.linesBelow)
			}
			aw.mu.Unlock()
			time.Sleep(textInterval)
		}
	}()

	return aw, aw.Wait
}

// ---------------------------------------------------------------------------
// Animation strategies
// ---------------------------------------------------------------------------

// traceWarnsdorff traces non-blank cells via DFS from the interior mountain
// peak using Warnsdorff's heuristic (prefer neighbors with fewest unvisited
// exits) and direction-continuity tiebreaker. Produces a finger-painting
// effect: peak → mountain trail → counterclockwise outline sweep.
func traceWarnsdorff(grid [][]rune, numLines int) []paintCell {
	peakRow, peakCol := findInteriorPeak(grid)
	if peakRow < 0 {
		return nil
	}

	dirs := [8][2]int{{-1, -1}, {-1, 0}, {-1, 1}, {0, -1}, {0, 1}, {1, -1}, {1, 0}, {1, 1}}

	type pos struct{ r, c int }
	visited := make(map[pos]bool)

	isNonBlank := func(r, c int) bool {
		return r >= 0 && r < numLines && c >= 0 && c < len(grid[r]) && grid[r][c] != blankBraille
	}

	unvisitedCount := func(r, c int) int {
		n := 0
		for _, d := range dirs {
			nr, nc := r+d[0], c+d[1]
			if isNonBlank(nr, nc) && !visited[pos{nr, nc}] {
				n++
			}
		}
		return n
	}

	var path []paintCell

	var dfs func(r, c, dr, dc int)
	dfs = func(r, c, dr, dc int) {
		p := pos{r, c}
		if visited[p] {
			return
		}
		visited[p] = true
		path = append(path, paintCell{r, c})

		type nb struct {
			r, c, exits, dot int
		}
		var neighbors []nb
		for _, d := range dirs {
			nr, nc := r+d[0], c+d[1]
			if !isNonBlank(nr, nc) || visited[pos{nr, nc}] {
				continue
			}
			neighbors = append(neighbors, nb{
				nr, nc,
				unvisitedCount(nr, nc),
				d[0]*dr + d[1]*dc, // direction continuity
			})
		}

		// Warnsdorff: fewest exits first, then prefer continuing in same direction
		sort.Slice(neighbors, func(i, j int) bool {
			if neighbors[i].exits != neighbors[j].exits {
				return neighbors[i].exits < neighbors[j].exits
			}
			return neighbors[i].dot > neighbors[j].dot
		})

		for _, n := range neighbors {
			dfs(n.r, n.c, n.r-r, n.c-c)
		}
	}

	// Initial direction: down-left, toward the mountain trail
	dfs(peakRow, peakCol, 1, -1)

	// Append any disconnected cells (shouldn't happen for this logo)
	for r, row := range grid {
		for c, ch := range row {
			if ch != blankBraille && !visited[pos{r, c}] {
				path = append(path, paintCell{r, c})
			}
		}
	}

	return path
}

// traceOutlineIn is the reverse of traceWarnsdorff: starts at the outline
// top-left, sweeps clockwise around the circle, then traces inward to the
// interior mountain peak.
func traceOutlineIn(grid [][]rune, numLines int) []paintCell {
	fwd := traceWarnsdorff(grid, numLines)
	rev := make([]paintCell, len(fwd))
	for i, c := range fwd {
		rev[len(fwd)-1-i] = c
	}
	return rev
}

// traceRadial reveals cells in concentric rings expanding from the centroid
// of all non-blank cells. Within each ring, cells are ordered by angle for
// a smooth circular sweep.
func traceRadial(grid [][]rune, numLines int) []paintCell {
	type cell struct {
		row, col int
		dist     float64
		angle    float64
	}

	// Find centroid of all non-blank cells
	var cells []cell
	var sumR, sumC float64
	for r, row := range grid {
		for c, ch := range row {
			if ch != blankBraille {
				sumR += float64(r)
				sumC += float64(c)
				cells = append(cells, cell{row: r, col: c})
			}
		}
	}
	if len(cells) == 0 {
		return nil
	}

	centR := sumR / float64(len(cells))
	centC := sumC / float64(len(cells))

	// Compute distance and angle from centroid
	for i := range cells {
		dr := float64(cells[i].row) - centR
		dc := float64(cells[i].col) - centC
		cells[i].dist = math.Sqrt(dr*dr + dc*dc)
		cells[i].angle = math.Atan2(dr, dc)
	}

	// Sort by distance (primary), angle (secondary)
	sort.Slice(cells, func(i, j int) bool {
		// Bin distances to create rings (~2 cell wide)
		bi, bj := int(cells[i].dist/2.0), int(cells[j].dist/2.0)
		if bi != bj {
			return bi < bj
		}
		return cells[i].angle < cells[j].angle
	})

	path := make([]paintCell, len(cells))
	for i, c := range cells {
		path[i] = paintCell{c.row, c.col}
	}
	return path
}

// traceScanline reveals cells left-to-right, top-to-bottom — a simple
// typewriter effect.
func traceScanline(grid [][]rune, numLines int) []paintCell {
	var path []paintCell
	for r := 0; r < numLines; r++ {
		for c, ch := range grid[r] {
			if ch != blankBraille {
				path = append(path, paintCell{r, c})
			}
		}
	}
	return path
}

// ---------------------------------------------------------------------------
// Shared helpers
// ---------------------------------------------------------------------------

// wordmarkGrid returns the wordmark as a rune grid and its line count.
func wordmarkGrid() ([][]rune, int) {
	lines := strings.Split(Wordmark, "\n")
	numLines := len(lines)
	grid := make([][]rune, numLines)
	for i, line := range lines {
		grid[i] = []rune(line)
	}
	return grid, numLines
}

// trailStyles builds the warm trail palette plus final brand color.
func trailStyles(theme Theme) ([]lipgloss.Style, int) {
	trail := darkTrail
	if !theme.Dark {
		trail = lightTrail
	}
	styles := make([]lipgloss.Style, len(trail)+1)
	for i, hex := range trail {
		styles[i] = lipgloss.NewStyle().Foreground(lipgloss.Color(hex))
	}
	styles[len(styles)-1] = lipgloss.NewStyle().Foreground(BrandColor)
	return styles, len(styles) - 1
}

// makeRevealGrid creates a frame-index grid initialized to -1 (unrevealed).
func makeRevealGrid(grid [][]rune, numLines int) [][]int {
	revealFrame := make([][]int, numLines)
	for r, row := range grid {
		revealFrame[r] = make([]int, len(row))
		for c := range revealFrame[r] {
			revealFrame[r][c] = -1
		}
	}
	return revealFrame
}

// visualLines counts terminal rows consumed by s, accounting for soft-wrapping
// at cols. pendingW is the visual width already on the current line from a
// previous write. Returns the number of rows and the updated pending width.
// When cols <= 0, falls back to counting literal newlines.
func visualLines(s string, cols, pendingW int) (rows, newPendingW int) {
	if cols <= 0 {
		return strings.Count(s, "\n"), 0
	}

	for _, seg := range strings.SplitAfter(s, "\n") {
		if seg == "" {
			continue
		}
		w := lipgloss.Width(strings.TrimSuffix(seg, "\n"))
		total := pendingW + w

		if strings.HasSuffix(seg, "\n") {
			rows += max(1, (total+cols-1)/cols)
			pendingW = 0
		} else {
			// Partial line: count wraps, update column position.
			// Use > (not >=) because terminals defer wrap at the right
			// margin — the cursor stays at cols until the next printable
			// character arrives, so exact-fit doesn't consume a row yet.
			if total > cols {
				wrapped := (total - 1) / cols
				rows += wrapped
				pendingW = total - wrapped*cols
			} else {
				pendingW = total
			}
		}
	}
	return rows, pendingW
}

// findInteriorPeak locates the interior mountain peak: the topmost row with
// 3+ separate non-blank groups has outline-left, mountain-interior,
// outline-right. The second group's first cell is the mountain peak.
func findInteriorPeak(grid [][]rune) (int, int) {
	peakRow, peakCol := -1, -1
	for r, row := range grid {
		groups := 0
		inGroup := false
		secondGroupStart := -1
		for c, ch := range row {
			if ch != blankBraille && !inGroup {
				inGroup = true
				groups++
				if groups == 2 {
					secondGroupStart = c
				}
			} else if ch == blankBraille {
				inGroup = false
			}
		}
		if groups >= 3 && secondGroupStart >= 0 {
			peakRow = r
			peakCol = secondGroupStart
			break
		}
	}
	if peakRow < 0 {
		// Fallback: topmost non-blank cell
		for r, row := range grid {
			for c, ch := range row {
				if ch != blankBraille {
					return r, c
				}
			}
		}
	}
	return peakRow, peakCol
}

func renderPaintFrame(w io.Writer, grid [][]rune, paintDist [][]int, frame int, styles []lipgloss.Style, settled, numLines int, text string, textStyle lipgloss.Style) {
	for r := 0; r < numLines; r++ {
		line := renderPaintLine(grid[r], paintDist[r], frame, styles, settled)
		if r == brandTextLine && text != "" {
			line += "   " + textStyle.Render(text)
		}
		fmt.Fprintf(w, "\r%s\033[K\n", line)
	}
}

// renderPaintLine renders a single line, grouping consecutive characters at the
// same color stage into single styled runs to minimize ANSI escape sequences.
func renderPaintLine(row []rune, dist []int, frame int, styles []lipgloss.Style, settled int) string {
	var b strings.Builder
	i := 0
	n := len(row)

	for i < n {
		ch := row[i]
		if ch == blankBraille || dist[i] < 0 {
			// Run of blank/unrevealed characters
			j := i + 1
			for j < n && (row[j] == blankBraille || dist[j] < 0) {
				j++
			}
			for k := i; k < j; k++ {
				b.WriteRune(blankBraille)
			}
			i = j
			continue
		}

		// Painted character — compute color stage from age
		age := frame - dist[i]
		if age > settled {
			age = settled
		}

		// Collect consecutive painted chars at the same stage
		j := i + 1
		for j < n && row[j] != blankBraille && dist[j] >= 0 {
			a := frame - dist[j]
			if a > settled {
				a = settled
			}
			if a != age {
				break
			}
			j++
		}

		// Render the run as a single styled string
		var run strings.Builder
		for k := i; k < j; k++ {
			run.WriteRune(row[k])
		}
		b.WriteString(styles[age].Render(run.String()))
		i = j
	}

	return b.String()
}

// isWriterTTY returns true if the writer is backed by a terminal file descriptor.
func isWriterTTY(w io.Writer) bool {
	if f, ok := w.(*os.File); ok {
		return term.IsTerminal(f.Fd())
	}
	return false
}
