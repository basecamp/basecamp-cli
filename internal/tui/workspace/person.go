package workspace

import (
	"strings"
	"time"
	"unicode"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
)

// person is who somebody is, as the API's own payload already describes them.
//
// Carried along rather than read when it is wanted: every comment and every
// boost arrives with its author's whole profile attached, so opening a card
// about somebody costs nothing.
type person struct {
	id       int64
	name     string
	title    string
	bio      string
	location string
	timeZone string
	email    string
	avatar   string
	company  string
}

func toPerson(who *basecamp.Person) person {
	if who == nil {
		return person{}
	}

	out := person{
		id:       who.ID,
		name:     who.Name,
		title:    who.Title,
		bio:      who.Bio,
		location: who.Location,
		timeZone: who.TimeZone,
		email:    who.EmailAddress,
		avatar:   who.AvatarURL,
	}
	if out.bio == "" {
		// BC5 renamed it; older responses send only the one.
		out.bio = who.Tagline
	}
	if who.Company != nil {
		out.company = who.Company.Name
	}
	return out
}

// known reports whether there is anybody here to say anything about.
func (p person) known() bool { return p.name != "" }

// initials are the two letters that stand for somebody where there is no room
// for a picture of them, the way the web's own default avatar reads.
func (p person) initials() string {
	var letters []rune
	for _, word := range strings.Fields(p.name) {
		for _, letter := range word {
			if unicode.IsLetter(letter) {
				letters = append(letters, unicode.ToUpper(letter))
				break
			}
		}
		if len(letters) == 2 {
			break
		}
	}
	if len(letters) == 0 {
		return "??"
	}
	return string(letters)
}

// firstName is what somebody is called in a sentence about them.
func (p person) firstName() string {
	if first, _, found := strings.Cut(p.name, " "); found {
		return first
	}
	if p.name == "" {
		return "this"
	}
	return p.name
}

// where is where somebody works, as one line: their title and their company are
// two halves of the same answer, and most people have only the first.
func (p person) where() string {
	return strings.Join(nonEmpty(p.title, p.company), " at ")
}

// clock is what time it is for somebody, which is the thing a card about a
// colleague in another country is really for.
//
// Empty when they have not said where they are, or when their zone is one this
// machine does not know — a wrong time is worse than none.
func (p person) clock(now time.Time) string {
	if p.timeZone == "" {
		return ""
	}
	where, err := time.LoadLocation(p.timeZone)
	if err != nil {
		return ""
	}
	return now.In(where).Format("15:04 MST")
}
