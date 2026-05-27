package importer

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var slashDatePattern = regexp.MustCompile(`^(\d{1,2})/(\d{1,2})/(\d{4})$`)

func normalizedDueOnValues(rows [][]string, mapping *MappingConfig) ([]string, error) {
	out := make([]string, len(rows))
	if mapping.DueOn == nil {
		return out, nil
	}

	order, err := inferSlashDateOrder(rows, mapping.DueOn)
	if err != nil {
		return nil, err
	}
	for i, row := range rows {
		value := valueAt(row, mapping.DueOn.ColumnIndex)
		normalized, err := normalizeDueOnValue(value, order)
		if err != nil {
			return nil, fmt.Errorf("source row %d due_on %q: %w", i+1, value, err)
		}
		out[i] = normalized
	}
	return out, nil
}

func inferSlashDateOrder(rows [][]string, ref *ColumnRef) (string, error) {
	explicit := strings.ToLower(strings.TrimSpace(ref.DateOrder))
	if explicit != "" {
		if explicit != "mdy" && explicit != "dmy" {
			return "", fmt.Errorf("mapping due_on date_order %q is unsupported; use mdy or dmy", ref.DateOrder)
		}
		return explicit, nil
	}

	mdyRow, dmyRow := 0, 0
	mdyValue, dmyValue := "", ""
	ambiguousRow, ambiguousValue := 0, ""
	for i, row := range rows {
		value := valueAt(row, ref.ColumnIndex)
		if strings.TrimSpace(value) == "" || !slashDatePattern.MatchString(value) {
			continue
		}
		monthOrDay, dayOrMonth, _, err := slashDateParts(value)
		if err != nil {
			return "", fmt.Errorf("source row %d due_on %q: %w", i+1, value, err)
		}
		if monthOrDay > 12 && dayOrMonth > 12 {
			return "", fmt.Errorf("source row %d due_on %q is not a valid mdy or dmy date", i+1, value)
		}
		if dayOrMonth > 12 {
			mdyRow, mdyValue = i+1, value
			continue
		}
		if monthOrDay > 12 {
			dmyRow, dmyValue = i+1, value
			continue
		}
		if ambiguousRow == 0 {
			ambiguousRow, ambiguousValue = i+1, value
		}
	}

	if mdyRow != 0 && dmyRow != 0 {
		return "", fmt.Errorf("due_on slash dates contain conflicting date orders: source row %d value %q indicates mdy, while source row %d value %q indicates dmy", mdyRow, mdyValue, dmyRow, dmyValue)
	}
	if mdyRow != 0 {
		return "mdy", nil
	}
	if dmyRow != 0 {
		return "dmy", nil
	}
	if ambiguousRow != 0 {
		return "", fmt.Errorf("due_on slash date format is ambiguous at source row %d value %q; add date_order \"mdy\" or \"dmy\" to the due_on mapping", ambiguousRow, ambiguousValue)
	}
	return "", nil
}

func normalizeDueOnValue(value, slashOrder string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}

	if date, ok := parseDateWithLayouts(value, "2006-01-02", "2006/01/02"); ok {
		return date, nil
	}
	if date, ok := parseDateWithLayouts(value,
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05",
		"2006-01-02 15:04",
	); ok {
		return date, nil
	}
	if date, ok := parseDateWithLayouts(value,
		"January 2, 2006",
		"January 2 2006",
		"Jan 2, 2006",
		"Jan 2 2006",
		"2 January 2006",
		"2 Jan 2006",
	); ok {
		return date, nil
	}

	if slashDatePattern.MatchString(value) {
		if slashOrder == "" {
			return "", fmt.Errorf("slash date format is ambiguous; add date_order \"mdy\" or \"dmy\" to the due_on mapping")
		}
		return normalizeSlashDate(value, slashOrder)
	}
	if looksLikeTwoDigitYearSlashDate(value) {
		return "", fmt.Errorf("two-digit years are not accepted for import due dates")
	}

	return "", fmt.Errorf("unsupported date format")
}

func parseDateWithLayouts(value string, layouts ...string) (string, bool) {
	for _, layout := range layouts {
		parsed, err := time.Parse(layout, value)
		if err == nil {
			return parsed.Format("2006-01-02"), true
		}
	}
	return "", false
}

func normalizeSlashDate(value, order string) (string, error) {
	first, second, year, err := slashDateParts(value)
	if err != nil {
		return "", err
	}
	month, day := first, second
	if order == "dmy" {
		day, month = first, second
	}
	if month < 1 || month > 12 || day < 1 || day > 31 {
		return "", fmt.Errorf("invalid %s date", order)
	}
	date := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
	if date.Year() != year || int(date.Month()) != month || date.Day() != day {
		return "", fmt.Errorf("invalid %s date", order)
	}
	return date.Format("2006-01-02"), nil
}

func slashDateParts(value string) (int, int, int, error) {
	match := slashDatePattern.FindStringSubmatch(value)
	if match == nil {
		return 0, 0, 0, fmt.Errorf("expected slash date with four-digit year")
	}
	first, _ := strconv.Atoi(match[1])
	second, _ := strconv.Atoi(match[2])
	year, _ := strconv.Atoi(match[3])
	return first, second, year, nil
}

func looksLikeTwoDigitYearSlashDate(value string) bool {
	parts := strings.Split(value, "/")
	if len(parts) != 3 {
		return false
	}
	return len(parts[2]) == 2
}
