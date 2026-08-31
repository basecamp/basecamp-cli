package workspace

import (
	"charm.land/lipgloss/v2"
)

// overlayCentered draws a layer in the middle of the area beneath it, so the
// screen it interrupted stays visible around its border.
func overlayCentered(base, layer string, width, height int) string {
	x := max((width-lipgloss.Width(layer))/2, 0)
	y := max((height-lipgloss.Height(layer))/2, 0)
	return overlayAt(base, layer, x, y, width, height)
}

// overlayAt composites one layer over another at a given cell. Every overlay in
// the workspace ends up here, so there is one answer to how layers are drawn.
func overlayAt(base, layer string, x, y, width, height int) string {
	compositor := lipgloss.NewCompositor(
		lipgloss.NewLayer(base).Z(0),
		lipgloss.NewLayer(layer).X(x).Y(y).Z(1),
	)
	canvas := lipgloss.NewCanvas(width, height)
	compositor.Draw(canvas, canvas.Bounds())
	return canvas.Render()
}
