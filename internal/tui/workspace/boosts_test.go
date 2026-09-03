package workspace

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/basecamp/basecamp-cli/internal/tui"
)

const boosterAvatar = "https://bc3-production-assets-cdn.basecamp-static.com/1/people/a/avatar"

func aBooster() boost {
	return boost{id: 1, content: "👍", by: person{name: "Andy Smith", avatar: boosterAvatar}}
}

// A terminal that draws pictures gets the booster's face in the pill; the
// initials are what stand in for one that does not.
func TestABoostWearsTheBoostersFace(t *testing.T) {
	styles := tui.NewStylesWithTheme(tui.DefaultTheme(true))
	shown := newPictures(&Context{styles: styles})
	shown.renderer = drawnImage{cols: chipCols, rows: 1}

	initials := strings.Join(boostRows(styles, shown, []boost{aBooster()}, 60), "\n")
	if !strings.Contains(ansi.Strip(initials), "AS") {
		t.Errorf("with no face drawn the pill should carry the initials:\n%s", ansi.Strip(initials))
	}

	shown.placeFaces(map[faceAt]tui.RenderedImage{
		{source: boosterAvatar, cols: chipCols}: shown.renderer.Render(nil, 1, chipCols),
	})

	withFace := strings.Join(boostRows(styles, shown, []boost{aBooster()}, 60), "\n")
	if strings.Contains(ansi.Strip(withFace), "AS") {
		t.Errorf("the initials stayed beside the face:\n%s", ansi.Strip(withFace))
	}
	if !strings.Contains(withFace, shown.chip(boosterAvatar)) {
		t.Error("the face never reached the pill")
	}
}

// The face is two cells wide and one row tall, which is the room the pill
// already had for two letters: a row of reactions is the same height either way.
func TestABoostsFaceFitsWhereTheInitialsWere(t *testing.T) {
	styles := tui.NewStylesWithTheme(tui.DefaultTheme(true))
	shown := newPictures(&Context{styles: styles})
	shown.renderer = drawnImage{cols: chipCols, rows: 1}

	initials := boostRows(styles, shown, []boost{aBooster()}, 60)
	shown.placeFaces(map[faceAt]tui.RenderedImage{
		{source: boosterAvatar, cols: chipCols}: shown.renderer.Render(nil, 1, chipCols),
	})
	withFace := boostRows(styles, shown, []boost{aBooster()}, 60)

	if len(initials) != len(withFace) {
		t.Errorf("the pill is %d rows with a face and %d with initials", len(withFace), len(initials))
	}
	for row := range initials {
		want := tui.DisplayWidth(ansi.Strip(initials[row]))
		if got := tui.DisplayWidth(ansi.Strip(withFace[row])); got != want {
			t.Errorf("row %d is %d cells wide with a face and %d with initials", row, got, want)
		}
	}
}
