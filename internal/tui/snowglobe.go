package tui

import (
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
)

const (
	snowglobeWidth  = 32
	snowglobeHeight = 28
)

// snowglobeRaster is a terminal-sized color map derived from the current
// Basecamp snowglobe mark. Spaces are transparent; digits and A index the
// glass-blue and mountain-green palette below.
const snowglobeRaster = `             666666
           6666666666
         65556666666566
        6355556666666556
       533355566666666556
      52333555556666666556
     5222335555555556556556
     22222335555555787555556
    312222333355558989755555
   512111233333337A7999755535
   21112422333324A98999975555
  31114777322324AA789999A73535
  21147778821149A878999AAA6335
 311147777A8449A9789999AAA93335
 2114777779AAAAA778999AAAAA7223
 2117777778AAAA87889999AAAAA523
 11477777888AA878889999AAAAA723
21177777778888788889999AAAAAA525
21477777788888888888999AAAAAA625
11477777788888888888999AAAAAA925
11777777788888888888999AAAAAA925
214777778888888888888999AAAAA625
 11478777888888888888999AAAA625
  11478888888888888899AAAA7423
   211477999AAAAAAAAAAA974225
    221124477789997776432235
      33221112333332223355
          333333333555`

var snowglobePalette = map[byte]color.Color{
	'1': lipgloss.Color("#e6f0fd"),
	'2': lipgloss.Color("#d5e2fd"),
	'3': lipgloss.Color("#c9dcfb"),
	'4': lipgloss.Color("#b3d9de"),
	'5': lipgloss.Color("#b6cef8"),
	'6': lipgloss.Color("#9bbff1"),
	'7': lipgloss.Color("#5ac26b"),
	'8': lipgloss.Color("#43b360"),
	'9': lipgloss.Color("#3aa562"),
	'A': lipgloss.Color("#19964e"),
}

// RenderSnowglobe renders the current Basecamp snowglobe mark as static
// terminal art. Colored terminals use half-block pixels for the blue glass and
// green mountain; NO_COLOR uses a two-tone text rendering.
func RenderSnowglobe(theme Theme) string {
	lines := strings.Split(snowglobeRaster, "\n")
	if _, noColor := theme.Primary.(lipgloss.NoColor); noColor {
		return renderSnowglobeNoColor(lines)
	}
	return renderSnowglobeColor(lines)
}

func renderSnowglobeColor(lines []string) string {
	var b strings.Builder
	for y := 0; y < snowglobeHeight; y += 2 {
		if y > 0 {
			b.WriteByte('\n')
		}
		for x := 0; x < snowglobeWidth; x++ {
			top := snowglobePixel(lines, x, y)
			bottom := snowglobePixel(lines, x, y+1)
			topColor, topSet := snowglobePalette[top]
			bottomColor, bottomSet := snowglobePalette[bottom]

			switch {
			case !topSet && !bottomSet:
				b.WriteByte(' ')
			case topSet && !bottomSet:
				b.WriteString(lipgloss.NewStyle().Foreground(topColor).Render("▀"))
			case !topSet && bottomSet:
				b.WriteString(lipgloss.NewStyle().Foreground(bottomColor).Render("▄"))
			case topColor == bottomColor:
				b.WriteString(lipgloss.NewStyle().Foreground(topColor).Render("█"))
			default:
				b.WriteString(lipgloss.NewStyle().Foreground(topColor).Background(bottomColor).Render("▀"))
			}
		}
	}
	return b.String()
}

func renderSnowglobeNoColor(lines []string) string {
	var b strings.Builder
	for y := 0; y < snowglobeHeight; y += 2 {
		if y > 0 {
			b.WriteByte('\n')
		}
		for x := 0; x < snowglobeWidth; x++ {
			top := snowglobePixel(lines, x, y)
			bottom := snowglobePixel(lines, x, y+1)
			switch {
			case snowglobeGreen(top) || snowglobeGreen(bottom):
				b.WriteRune('▓')
			case top != ' ' || bottom != ' ':
				b.WriteRune('░')
			default:
				b.WriteByte(' ')
			}
		}
	}
	return b.String()
}

func snowglobePixel(lines []string, x, y int) byte {
	if y < 0 || y >= len(lines) || x < 0 || x >= len(lines[y]) {
		return ' '
	}
	return lines[y][x]
}

func snowglobeGreen(pixel byte) bool {
	return pixel >= '7' && pixel <= '9' || pixel == 'A'
}
