package tui

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/term"
)

const (
	blankBraille  = '⠀' // U+2800
	paintInterval = 25 * time.Millisecond
	textInterval  = 35 * time.Millisecond
)

// Paint trail colors — warm palette converging on brand yellow.
var (
	// darkTrail: coral → copper → amber → gold on dark backgrounds.
	darkTrail = []string{"#c85840", "#d07830", "#d89424", "#e0a01c"}
	// lightTrail: deeper warm tones visible on light backgrounds.
	lightTrail = []string{"#984038", "#906028", "#887020", "#808018"}
)

// AnimateWordmark draws the wordmark with a path-tracing paint animation.
// Non-blank characters are revealed in waves radiating from the mountain peak
// via BFS, each passing through a warm color trail before settling on brand
// yellow. If w is not a TTY or NO_COLOR is active, falls back to static render.
func AnimateWordmark(w io.Writer, theme Theme) {
	if _, noColor := theme.Primary.(lipgloss.NoColor); !isWriterTTY(w) || noColor {
		fmt.Fprint(w, RenderWordmark(theme))
		return
	}

	lines := strings.Split(Wordmark, "\n")
	numLines := len(lines)
	grid := make([][]rune, numLines)
	for i, line := range lines {
		grid[i] = []rune(line)
	}

	// BFS from peak to establish paint order
	order, maxDist := paintOrder(grid, numLines)

	// Build trail palette: intermediate warm colors + final brand yellow
	trail := darkTrail
	if !theme.Dark {
		trail = lightTrail
	}
	styles := make([]lipgloss.Style, len(trail)+1)
	for i, hex := range trail {
		styles[i] = lipgloss.NewStyle().Foreground(lipgloss.Color(hex))
	}
	styles[len(styles)-1] = lipgloss.NewStyle().Foreground(BrandColor)
	settled := len(styles) - 1

	textStyle := lipgloss.NewStyle().Foreground(BrandColor).Bold(true)

	// State: BFS distance at which each cell was painted (-1 = not yet visible)
	paintDist := make([][]int, numLines)
	for r, row := range grid {
		paintDist[r] = make([]int, len(row))
		for c := range paintDist[r] {
			paintDist[r][c] = -1
		}
	}

	// Index cells by BFS distance for O(1) lookup per frame
	byDist := make([][]paintCell, maxDist+1)
	for _, c := range order {
		byDist[c.dist] = append(byDist[c.dist], c)
	}

	// Phase 1: Paint — one BFS distance band per frame
	totalFrames := maxDist + 1 + settled // paint + trail settle
	for frame := 0; frame < totalFrames; frame++ {
		// Reveal current distance band
		if frame <= maxDist {
			for _, c := range byDist[frame] {
				paintDist[c.row][c.col] = frame
			}
		}

		if frame > 0 {
			fmt.Fprintf(w, "\033[%dA", numLines)
		}
		renderPaintFrame(w, grid, paintDist, frame, styles, settled, numLines, "", textStyle)
		time.Sleep(paintInterval)
	}

	// Phase 2: Reveal "Basecamp" letter by letter
	for i := 1; i <= len(brandText); i++ {
		fmt.Fprintf(w, "\033[%dA", numLines)
		renderPaintFrame(w, grid, paintDist, totalFrames, styles, settled, numLines, brandText[:i], textStyle)
		time.Sleep(textInterval)
	}
}

type paintCell struct {
	row, col, dist int
}

// paintOrder returns non-blank cells ordered by BFS distance from the peak
// (topmost non-blank character), plus the maximum distance reached.
func paintOrder(grid [][]rune, numLines int) ([]paintCell, int) {
	// Find the peak (topmost non-blank character)
	peakRow, peakCol := -1, -1
	for r, row := range grid {
		for c, ch := range row {
			if ch != blankBraille {
				peakRow = r
				peakCol = c
				goto found
			}
		}
	}
found:
	if peakRow < 0 {
		return nil, 0
	}

	// BFS (8-connected) from peak
	type pos struct{ r, c int }
	visited := make(map[pos]bool)
	var ordered []paintCell
	queue := []paintCell{{peakRow, peakCol, 0}}
	visited[pos{peakRow, peakCol}] = true
	maxDist := 0

	dirs := [8][2]int{{-1, -1}, {-1, 0}, {-1, 1}, {0, -1}, {0, 1}, {1, -1}, {1, 0}, {1, 1}}
	for len(queue) > 0 {
		curr := queue[0]
		queue = queue[1:]
		ordered = append(ordered, curr)
		if curr.dist > maxDist {
			maxDist = curr.dist
		}

		for _, d := range dirs {
			nr, nc := curr.row+d[0], curr.col+d[1]
			if nr < 0 || nr >= numLines || nc < 0 || nc >= len(grid[nr]) {
				continue
			}
			p := pos{nr, nc}
			if visited[p] || grid[nr][nc] == blankBraille {
				continue
			}
			visited[p] = true
			queue = append(queue, paintCell{nr, nc, curr.dist + 1})
		}
	}

	// Append any disconnected non-blank cells (shouldn't happen for this logo)
	for r, row := range grid {
		for c, ch := range row {
			if ch != blankBraille && !visited[pos{r, c}] {
				maxDist++
				ordered = append(ordered, paintCell{r, c, maxDist})
			}
		}
	}

	return ordered, maxDist
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
