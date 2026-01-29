package format

import (
	"fmt"
	"strings"
)

// Person formats a person for picker display.
type Person struct {
	ID         int64
	Name       string
	Email      string
	AvatarURL  string
	Admin      bool
	Owner      bool
	TimeZone   string
	PersonType string // client, employee, etc.
}

// ToPickerTitle returns a formatted title for picker display.
func (p Person) ToPickerTitle() string {
	title := p.Name
	if title == "" {
		title = p.Email
	}
	if title == "" {
		title = fmt.Sprintf("Person #%d", p.ID)
	}

	// Add role indicators
	var badges []string
	if p.Owner {
		badges = append(badges, "owner")
	} else if p.Admin {
		badges = append(badges, "admin")
	}

	if len(badges) > 0 {
		title += " [" + strings.Join(badges, ", ") + "]"
	}

	return title
}

// ToPickerDescription returns a formatted description for picker display.
func (p Person) ToPickerDescription() string {
	var parts []string

	parts = append(parts, fmt.Sprintf("#%d", p.ID))

	if p.Email != "" {
		parts = append(parts, p.Email)
	}

	if p.PersonType != "" && p.PersonType != "employee" {
		parts = append(parts, p.PersonType)
	}

	return strings.Join(parts, " - ")
}

// PersonInitials returns initials from a name.
func PersonInitials(name string) string {
	words := strings.Fields(name)
	if len(words) == 0 {
		return "?"
	}

	var initials strings.Builder
	for i, word := range words {
		if i >= 2 {
			break
		}
		if len(word) > 0 {
			initials.WriteRune(rune(word[0]))
		}
	}

	return strings.ToUpper(initials.String())
}
