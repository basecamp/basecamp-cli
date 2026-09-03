package tui

import (
	"image/color"

	"charm.land/lipgloss/v2"
)

// The colors a column can be painted, by the name Basecamp stores. They are the
// web's own — --color-<name>-50 out of global-colors.scss, in both of its
// palettes — rather than anything out of the theme: the reader chose "purple"
// and a purple column has to read as one wherever it is drawn.
//
// White is missing on purpose. It is the default, and what the web does with it
// is leave the column uncolored — see Colored::DEFAULT_COLOR. Gray and brown are
// here under the names the API uses; the web calls them primary and sand once
// they reach CSS.
var cardColors = map[string]struct{ light, dark color.Color }{
	"red":    {light: lipgloss.Color("#fd6c44"), dark: lipgloss.Color("#da5b45")},
	"orange": {light: lipgloss.Color("#f58e02"), dark: lipgloss.Color("#d7751a")},
	"yellow": {light: lipgloss.Color("#e9b60f"), dark: lipgloss.Color("#cd9c35")},
	"green":  {light: lipgloss.Color("#56b66f"), dark: lipgloss.Color("#409f65")},
	"aqua":   {light: lipgloss.Color("#0db3c1"), dark: lipgloss.Color("#2699aa")},
	"blue":   {light: lipgloss.Color("#42a3ff"), dark: lipgloss.Color("#3a8ae6")},
	"purple": {light: lipgloss.Color("#ae80ff"), dark: lipgloss.Color("#a364e5")},
	"pink":   {light: lipgloss.Color("#ef63b8"), dark: lipgloss.Color("#ce5997")},
	"brown":  {light: lipgloss.Color("#b59977"), dark: lipgloss.Color("#9e8464")},
	"gray":   {light: lipgloss.Color("#9a9fa2"), dark: lipgloss.Color("#828b8f")},
}

// CardColor answers what a column color's name is drawn as, and whether it names
// a color at all. An unpainted column, and every column under NO_COLOR, gets
// nothing and falls back to the theme's own border.
func (t Theme) CardColor(name string) (color.Color, bool) {
	if t.Colorless() {
		return nil, false
	}
	painted, known := cardColors[name]
	if !known {
		return nil, false
	}
	if t.Dark {
		return painted.dark, true
	}
	return painted.light, true
}

// Tint mixes a color into the background at strength, which is how a colored
// column washes the space behind its cards. The web mixes the same color into
// the canvas with color-mix; this is that, done where the terminal can read it.
//
// Both colors are 16-bit per channel by the time they arrive here, so the mix
// happens there and comes back down to the 8 bits a terminal takes.
func Tint(background, with color.Color, strength float64) color.Color {
	br, bg, bb, _ := background.RGBA()
	wr, wg, wb, _ := with.RGBA()

	mix := func(base, over uint32) uint8 {
		return uint8((float64(base)*(1-strength) + float64(over)*strength) / 257)
	}
	return color.RGBA{R: mix(br, wr), G: mix(bg, wg), B: mix(bb, wb), A: 0xff}
}
