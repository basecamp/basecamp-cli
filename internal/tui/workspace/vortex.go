package workspace

import (
	"math"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

// How often the spiral turns, and how far it turns each time — a whole turn in
// about two and a half seconds.
//
// Slower than the throbber on purpose: a wormhole is scenery rather than
// something the reader is waiting on, and scenery that hurries reads as an
// error.
const (
	vortexInterval = 110 * time.Millisecond
	vortexStep     = 0.26
)

// The shades a cell of the spiral is drawn with, emptiest first. Four steps is
// what it takes to read as turning rather than flickering — with two the arms
// snap between on and off.
var vortexShades = []string{" ", "░", "▒", "▓"}

// The shape of the spiral.
const (
	// vortexArms is how many times round the pattern repeats. Two reads as a
	// whirlpool; more, at eighteen cells across, reads as noise.
	vortexArms = 2.0

	// vortexTwist is how much the arms curl per cell of radius, and it is
	// subtracted rather than added so they sweep inward — a wormhole pulls.
	vortexTwist = 0.9

	// vortexGamma darkens the arms by pulling the middle of their range up.
	// Without it most of the spiral lands in the emptiest shade and the arms
	// come out as gaps with speckle between them.
	vortexGamma = 0.55

	// vortexBloom holds the middle at full strength before the fade to the rim
	// starts, which is what makes it a funnel rather than a pinwheel.
	vortexBloom = 1.9

	// cellAspect is how much taller a terminal cell is than it is wide. Without
	// it the spiral comes out an ellipse.
	cellAspect = 2.0
)

// vortexTickMsg turns the spiral one step.
type vortexTickMsg struct{}

// turnVortex arms the next turn, and only while there is a wormhole on the board
// to turn: a screen with none has no business waking up ten times a second.
//
// turning is what keeps one tick in flight rather than one per arming. The board
// arms it when it opens and again on whatever it next hears, because a screen
// that has been walked back to is not opened again — so the spiral picks up on
// the next thing to happen rather than the moment the reader returns.
func (t *cardTableScreen) turnVortex() tea.Cmd {
	if t.turning || !t.hasWormhole() {
		return nil
	}
	t.turning = true
	return tea.Tick(vortexInterval, func(time.Time) tea.Msg { return vortexTickMsg{} })
}

func (t *cardTableScreen) hasWormhole() bool {
	for _, column := range t.columns {
		if column.kind == columnWormhole {
			return true
		}
	}
	return false
}

// vortexRows is the spiral itself, drawn into whatever the destination text left
// of the column.
//
// A cell's shade comes from where it sits in the pattern — its angle round the
// middle and its distance from it — and the whole thing is rotated a little
// further on every turn. Nothing is kept between frames: the spiral is a
// function of the phase, so one number says which frame this is.
func (t *cardTableScreen) vortexRows(paint columnPaint, width, rows int) []string {
	if rows <= 0 || width <= 0 {
		return nil
	}

	// The middle of the funnel, and the distance from it to the far corner —
	// which is what the fade is measured against, so the spiral thins out at the
	// edges rather than being cut off at them.
	middleX, middleY := float64(width-1)/2, float64(rows-1)/2
	reach := math.Hypot(middleX, middleY*cellAspect)
	phase := float64(t.turn) * vortexStep

	drawn := make([]string, 0, rows)
	for y := range rows {
		var line strings.Builder
		for x := range width {
			line.WriteString(vortexShades[vortexShade(
				float64(x)-middleX,
				(float64(y)-middleY)*cellAspect,
				reach, phase,
			)])
		}
		drawn = append(drawn, paint.row(width, paint.vortex.Render(line.String())))
	}
	return drawn
}

// vortexShade is how dark one cell of the spiral is: which of the arms it falls
// under, faded by how far out it sits.
func vortexShade(dx, dy, reach, phase float64) int {
	radius := math.Hypot(dx, dy)
	if radius > reach || reach == 0 {
		return 0
	}

	wave := (math.Sin(vortexArms*math.Atan2(dy, dx)-vortexTwist*radius-phase) + 1) / 2
	depth := math.Pow(wave, vortexGamma) * min(vortexBloom*(1-radius/reach), 1)

	return max(min(int(depth*float64(len(vortexShades))), len(vortexShades)-1), 0)
}
