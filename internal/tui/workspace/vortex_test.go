package workspace

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/basecamp/basecamp-cli/internal/tui"
)

// A board with no wormhole on it has nothing to turn, so it should not be waking
// up ten times a second.
func TestABoardWithNoWormholeNeverTurns(t *testing.T) {
	screen := aBoard(t)
	screen.Resize(120, 24)
	screen.columns = screen.columns[:6] // everything but the wormhole

	if cmd := screen.turnVortex(); cmd != nil {
		t.Error("a board with no wormhole armed a turn")
	}
	if screen.turning {
		t.Error("a board with no wormhole thinks it is turning")
	}
}

// One turn in flight at a time. Arming is what every key press does, so without
// this a board would collect a tick per keystroke and spin faster the more it
// was used.
func TestOnlyOneTurnIsEverInFlight(t *testing.T) {
	screen := aBoard(t)
	screen.Resize(120, 24)

	if cmd := screen.turnVortex(); cmd == nil {
		t.Fatal("a board with a wormhole armed nothing")
	}
	if cmd := screen.turnVortex(); cmd != nil {
		t.Error("a second arming added another turn while one was in flight")
	}

	// The turn arriving is what frees the next one, and moves the spiral on.
	was := screen.turn
	cmd, took := screen.Update(vortexTickMsg{})
	if !took || cmd == nil {
		t.Fatal("the turn arriving did not arm the next")
	}
	if screen.turn == was {
		t.Error("the spiral did not move on")
	}
}

// The spiral turns: two frames of it are not the same picture.
func TestTheSpiralTurns(t *testing.T) {
	screen := aBoard(t)
	screen.Resize(120, 24)
	hole := screen.columns[6]
	paint := screen.paintFor(hole, false)

	screen.turn = 0
	first := strings.Join(screen.vortexRows(paint, wormholeColumnWidth, 8), "\n")

	screen.turn = 5
	later := strings.Join(screen.vortexRows(paint, wormholeColumnWidth, 8), "\n")

	if ansi.Strip(first) == ansi.Strip(later) {
		t.Errorf("the spiral is the same five turns on:\n%s", ansi.Strip(first))
	}
	for _, shade := range vortexShades[1:] {
		if !strings.Contains(ansi.Strip(first), shade) {
			t.Errorf("the spiral never uses %q, so it has fewer shades than it thinks", shade)
		}
	}
}

// Every row of the spiral is exactly as wide as the column, like every other row
// the board draws: they are joined along their rows, and one that stops short
// lets the column beside it show through.
func TestTheSpiralFillsItsColumn(t *testing.T) {
	screen := aBoard(t)
	screen.Resize(120, 24)
	paint := screen.paintFor(screen.columns[6], false)

	rows := screen.vortexRows(paint, wormholeColumnWidth, 6)
	if len(rows) != 6 {
		t.Fatalf("the spiral drew %d rows, want 6", len(rows))
	}
	for index, row := range rows {
		if got := tui.DisplayWidth(ansi.Strip(row)); got != wormholeColumnWidth {
			t.Errorf("row %d is %d cells wide, want %d", index, got, wormholeColumnWidth)
		}
	}

	// Nothing to draw into is nothing drawn, rather than a panic on a zero
	// radius or a negative loop.
	if rows := screen.vortexRows(paint, wormholeColumnWidth, 0); rows != nil {
		t.Errorf("a spiral with no room drew %d rows", len(rows))
	}
	if rows := screen.vortexRows(paint, 0, 6); rows != nil {
		t.Errorf("a spiral with no width drew %d rows", len(rows))
	}
}
