package tui

import (
	"bytes"
	"fmt"
)

// The graphics query, and the question that follows it so an answer always comes
// back.
//
// A terminal that draws Kitty graphics answers a query with OK for the image id it
// was asked about. One that does not answers nothing at all — so a primary device
// attributes request goes out behind it, which every terminal answers. Whichever
// arrives is the answer: the OK means pictures, the attributes on their own mean
// none, and neither means a terminal that was not listening.
//
// The image is one transparent pixel and is never displayed: a=q asks about it, t=d
// says the pixel is in the payload, and f=24 with s=1,v=1 describes it. The id is
// arbitrary and only has to come back.
const (
	graphicsQueryID = 4242
	deviceAttrs     = "\x1b[c"
)

var (
	graphicsQuery  = fmt.Sprintf("\x1b_Ga=q,i=%d,s=1,v=1,f=24,t=d;AAAA\x1b\\", graphicsQueryID)
	graphicsAnswer = fmt.Sprintf("\x1b_Gi=%d;OK", graphicsQueryID)
)

// imageProbeRequest is what goes to the terminal to settle whether it draws
// pictures.
func imageProbeRequest() string { return graphicsQuery + deviceAttrs }

// insideARelayThatEatsPictures reports the one thing asking cannot settle: a
// program in the middle that passes the question along but not the answer's
// subject.
//
// The query travels further than the pixels do. herdr forwards it to the terminal
// behind it, so Ghostty answers OK — and then the image data never arrives, and the
// placeholder cells are drawn into empty space. tmux does the same and shows the
// cells for what they are made of. Either way the reader gets a hole where a
// picture should be, which is worse than the filename that was there before.
//
// This is a list of two, and both are on it because they were seen doing it — not
// because a variable's name suggested a multiplexer. Anything not on it gets asked,
// and BASECAMP_IMAGE_PROTOCOL=kitty overrides the list for a relay that does pass
// graphics through.
func insideARelayThatEatsPictures(lookupEnv func(string) string) bool {
	return lookupEnv("HERDR_ENV") != "" || lookupEnv("TMUX") != ""
}

// readImageAnswer reads what the terminal said. It reports whether pictures are
// drawn, and whether the terminal answered at all — silence is not a no, it is a
// terminal that was not listening, and the caller keeps what it had rather than
// deciding on nothing.
func readImageAnswer(data []byte) (draws, answered bool) {
	switch {
	case bytes.Contains(data, []byte(graphicsAnswer)):
		return true, true
	case containsDeviceAttributes(data):
		return false, true
	default:
		return false, false
	}
}

// containsDeviceAttributes finds a primary device attributes report — ESC [ ? … c
// — which is the answer every terminal gives, and the one that says the graphics
// query ahead of it went unanswered.
func containsDeviceAttributes(data []byte) bool {
	for i := 0; i+2 < len(data); i++ {
		if data[i] == 0x1b && data[i+1] == '[' && data[i+2] == '?' && endsAsAttributes(data[i+3:]) {
			return true
		}
	}
	return false
}

// endsAsAttributes reports whether what follows the report's opening is a run of
// parameters closed by its own final byte.
func endsAsAttributes(data []byte) bool {
	for _, b := range data {
		switch {
		case b == 'c':
			return true
		case b >= '0' && b <= '9', b == ';':
		default:
			return false
		}
	}
	return false
}
