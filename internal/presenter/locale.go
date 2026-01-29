package presenter

import (
	"fmt"
	"os"
	"strings"
	"time"

	"golang.org/x/text/language"
	"golang.org/x/text/message"
	"golang.org/x/text/number"
)

// Locale holds resolved formatting conventions for dates and numbers.
type Locale struct {
	tag     language.Tag
	printer *message.Printer
}

// DetectLocale resolves the user's locale from environment variables.
// Falls back to en-US if nothing is set or parseable.
func DetectLocale() Locale {
	raw := os.Getenv("LC_ALL")
	if raw == "" {
		raw = os.Getenv("LC_TIME") // date/number-specific
	}
	if raw == "" {
		raw = os.Getenv("LANG")
	}
	return NewLocale(raw)
}

// NewLocale creates a Locale from a POSIX locale string (e.g. "de_DE.UTF-8")
// or BCP 47 tag (e.g. "de-DE"). Returns en-US for empty or unparseable input.
func NewLocale(raw string) Locale {
	// Strip encoding suffix: "en_US.UTF-8" → "en_US"
	if idx := strings.IndexByte(raw, '.'); idx != -1 {
		raw = raw[:idx]
	}
	// POSIX uses underscore, BCP 47 uses dash
	raw = strings.ReplaceAll(raw, "_", "-")

	tag, _ := language.Parse(raw)
	if tag == language.Und {
		tag = language.AmericanEnglish
	}

	return Locale{
		tag:     tag,
		printer: message.NewPrinter(tag),
	}
}

// FormatDate formats a time.Time as a locale-appropriate date string.
func (l Locale) FormatDate(t time.Time) string {
	return t.Format(l.dateLayout())
}

// FormatNumber formats a float64 with locale-appropriate grouping and decimal separators.
func (l Locale) FormatNumber(v float64) string {
	if v == float64(int64(v)) {
		return l.printer.Sprint(number.Decimal(int64(v)))
	}
	return l.printer.Sprint(number.Decimal(v, number.MaxFractionDigits(2)))
}

// Tag returns the resolved language tag.
func (l Locale) Tag() language.Tag {
	return l.tag
}

// dateLayout returns a Go time layout string for the locale's preferred date format.
// Uses region-based lookup with sensible defaults.
func (l Locale) dateLayout() string {
	region, _ := l.tag.Region()
	code := region.String()

	if layout, ok := dateLayouts[code]; ok {
		return layout
	}

	// Fall back by language
	base, _ := l.tag.Base()
	if layout, ok := dateLayoutsByLang[base.String()]; ok {
		return layout
	}

	return dateLayoutDefault
}

// Date layout constants using Go's reference time (Mon Jan 2 15:04:05 MST 2006).
const (
	layoutMDY    = "Jan 2, 2006" // US: Jan 15, 2026
	layoutDMY    = "2 Jan 2006"  // UK/EU: 15 Jan 2026
	layoutYMD    = "2006-01-02"  // ISO: 2026-01-15
	layoutDMYDot = "2. Jan 2006" // DE/AT: 15. Jan 2026

	dateLayoutDefault = layoutMDY
)

// dateLayouts maps ISO 3166-1 region codes to Go date layouts.
var dateLayouts = map[string]string{
	// Month-Day-Year regions
	"US": layoutMDY,
	"PH": layoutMDY,

	// Day-Month-Year regions (most of the world)
	"GB": layoutDMY,
	"AU": layoutDMY,
	"NZ": layoutDMY,
	"IE": layoutDMY,
	"ZA": layoutDMY,
	"IN": layoutDMY,
	"FR": layoutDMY,
	"ES": layoutDMY,
	"IT": layoutDMY,
	"PT": layoutDMY,
	"BR": layoutDMY,
	"NL": layoutDMY,
	"BE": layoutDMY,
	"MX": layoutDMY,
	"AR": layoutDMY,
	"CL": layoutDMY,
	"CO": layoutDMY,
	"PL": layoutDMY,
	"RU": layoutDMY,
	"TR": layoutDMY,
	"GR": layoutDMY,
	"DK": layoutDMY,
	"NO": layoutDMY,
	"SE": layoutDMY,
	"FI": layoutDMY,

	// Day. Month Year (German-speaking)
	"DE": layoutDMYDot,
	"AT": layoutDMYDot,
	"CH": layoutDMYDot,

	// Year-Month-Day regions
	"JP": layoutYMD,
	"CN": layoutYMD,
	"KR": layoutYMD,
	"TW": layoutYMD,
	"HU": layoutYMD,
	"LT": layoutYMD,
	"CA": layoutYMD, // Canada officially uses ISO
}

// dateLayoutsByLang provides fallbacks when region is unknown.
var dateLayoutsByLang = map[string]string{
	"en": layoutMDY,
	"de": layoutDMYDot,
	"fr": layoutDMY,
	"es": layoutDMY,
	"it": layoutDMY,
	"pt": layoutDMY,
	"nl": layoutDMY,
	"da": layoutDMY,
	"nb": layoutDMY,
	"nn": layoutDMY,
	"sv": layoutDMY,
	"fi": layoutDMY,
	"pl": layoutDMY,
	"ru": layoutDMY,
	"tr": layoutDMY,
	"ja": layoutYMD,
	"zh": layoutYMD,
	"ko": layoutYMD,
}

// relativeTimeFormat formats relative time strings.
// These remain English — true i18n of relative strings would require
// a message catalog, which is out of scope.
func relativeTimeFormat(n int, unit string) string {
	if n == 1 {
		switch unit {
		case "day":
			return "yesterday"
		case "minute":
			return "1 minute ago"
		case "hour":
			return "1 hour ago"
		}
	}
	return fmt.Sprintf("%d %ss ago", n, unit)
}
