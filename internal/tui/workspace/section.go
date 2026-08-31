package workspace

// section is a top-level destination: what the menu lists and where the number
// keys go. They are siblings rather than a ladder, which is why opening one
// comes back to home first — the trail reads Home › Calendar, never
// Home › Activity › Calendar.
type section struct {
	key   string
	label string
}

// sections is the menu, in order. The keys are what the labels are numbered
// with, and they work with the menu shut.
var sections = []section{
	{"1", "Activity"},
	{"2", "Calendar"},
	{"3", "Reports"},
	{"4", "Everything"},
}

// sectionForKey answers which destination a key press opens, if any.
func sectionForKey(key string) (section, bool) {
	for _, s := range sections {
		if s.key == key {
			return s, true
		}
	}
	return section{}, false
}
