package format

import (
	"fmt"
	"strings"
)

// Project formats a project for picker display.
type Project struct {
	ID          int64
	Name        string
	Purpose     string
	Description string
	Status      string
	Bookmarked  bool
}

// ToPickerTitle returns a formatted title for picker display.
func (p Project) ToPickerTitle() string {
	title := p.Name
	if title == "" {
		title = fmt.Sprintf("Project #%d", p.ID)
	}

	if p.Bookmarked {
		title = "*" + title
	}

	return title
}

// ToPickerDescription returns a formatted description for picker display.
func (p Project) ToPickerDescription() string {
	var parts []string

	if p.Purpose != "" {
		parts = append(parts, truncate(p.Purpose, 40))
	}

	parts = append(parts, fmt.Sprintf("#%d", p.ID))

	if p.Status != "" && p.Status != "active" {
		parts = append(parts, "["+p.Status+"]")
	}

	return strings.Join(parts, " - ")
}
