package tui

import (
	"errors"

	"github.com/charmbracelet/huh"
)

// Confirm shows a yes/no confirmation prompt.
func Confirm(message string, defaultValue bool) (bool, error) {
	var result bool
	err := huh.NewConfirm().
		Title(message).
		Affirmative("Yes").
		Negative("No").
		Value(&result).
		Run()
	if err != nil {
		return defaultValue, err
	}
	return result, nil
}

// ConfirmDangerous shows a confirmation prompt for dangerous actions.
func ConfirmDangerous(message string) (bool, error) {
	var result bool
	err := huh.NewConfirm().
		Title(message).
		Description("This action cannot be undone.").
		Affirmative("Yes, I'm sure").
		Negative("Cancel").
		Value(&result).
		Run()
	if err != nil {
		return false, err
	}
	return result, nil
}

// Input shows a text input prompt.
func Input(title, placeholder string) (string, error) {
	var result string
	err := huh.NewInput().
		Title(title).
		Placeholder(placeholder).
		Value(&result).
		Run()
	return result, err
}

// InputRequired shows a required text input prompt.
func InputRequired(title, placeholder string) (string, error) {
	var result string
	err := huh.NewInput().
		Title(title).
		Placeholder(placeholder).
		Value(&result).
		Validate(func(s string) error {
			if s == "" {
				return errors.New("this field is required")
			}
			return nil
		}).
		Run()
	return result, err
}

// TextArea shows a multiline text input prompt.
func TextArea(title, placeholder string) (string, error) {
	var result string
	err := huh.NewText().
		Title(title).
		Placeholder(placeholder).
		Value(&result).
		Run()
	return result, err
}

// SelectOption represents an option in a select prompt.
type SelectOption struct {
	Value string
	Label string
}

// Select shows a single-select prompt.
func Select(title string, options []SelectOption) (string, error) {
	huhOptions := make([]huh.Option[string], len(options))
	for i, opt := range options {
		huhOptions[i] = huh.NewOption(opt.Label, opt.Value)
	}

	var result string
	err := huh.NewSelect[string]().
		Title(title).
		Options(huhOptions...).
		Value(&result).
		Run()
	return result, err
}

// SelectWithDescription shows a select prompt with descriptions.
func SelectWithDescription(title, description string, options []SelectOption) (string, error) {
	huhOptions := make([]huh.Option[string], len(options))
	for i, opt := range options {
		huhOptions[i] = huh.NewOption(opt.Label, opt.Value)
	}

	var result string
	err := huh.NewSelect[string]().
		Title(title).
		Description(description).
		Options(huhOptions...).
		Value(&result).
		Run()
	return result, err
}

// MultiSelect shows a multi-select prompt.
func MultiSelect(title string, options []SelectOption) ([]string, error) {
	huhOptions := make([]huh.Option[string], len(options))
	for i, opt := range options {
		huhOptions[i] = huh.NewOption(opt.Label, opt.Value)
	}

	var result []string
	err := huh.NewMultiSelect[string]().
		Title(title).
		Options(huhOptions...).
		Value(&result).
		Run()
	return result, err
}

// FormField represents a field in a form.
type FormField struct {
	Key         string
	Title       string
	Placeholder string
	Required    bool
	Default     string
}

// Form shows a multi-field form and returns a map of key -> value.
func Form(title string, fields []FormField) (map[string]string, error) {
	results := make(map[string]string)
	values := make([]*string, len(fields))

	huhFields := make([]huh.Field, len(fields))
	for i, f := range fields {
		value := f.Default
		values[i] = &value

		input := huh.NewInput().
			Title(f.Title).
			Placeholder(f.Placeholder).
			Value(values[i])

		if f.Required {
			input = input.Validate(func(s string) error {
				if s == "" {
					return errors.New("this field is required")
				}
				return nil
			})
		}

		huhFields[i] = input
	}

	form := huh.NewForm(
		huh.NewGroup(huhFields...).Title(title),
	)

	if err := form.Run(); err != nil {
		return nil, err
	}

	for i, f := range fields {
		results[f.Key] = *values[i]
	}

	return results, nil
}

// Note shows an informational note (non-interactive).
func Note(title, body string) error {
	return huh.NewNote().
		Title(title).
		Description(body).
		Run()
}

// ConfirmSetDefault asks the user if they want to save a value as the default.
func ConfirmSetDefault(valueName string) (bool, error) {
	var result bool
	err := huh.NewConfirm().
		Title("Save as default?").
		Description("Set " + valueName + " as the default for future commands.").
		Affirmative("Yes").
		Negative("No").
		Value(&result).
		Run()
	if err != nil {
		return false, err
	}
	return result, nil
}

// SelectScope shows a prompt for selecting the config scope (global or local).
func SelectScope() (string, error) {
	options := []SelectOption{
		{Value: "local", Label: "Local (.basecamp/config.json)"},
		{Value: "global", Label: "Global (~/.config/basecamp/config.json)"},
	}
	return Select("Where should this be saved?", options)
}
