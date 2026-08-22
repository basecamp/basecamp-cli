package tui

import (
	"errors"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/huh"

	"github.com/basecamp/basecamp-cli/internal/stdinarg"
)

// ErrNotInteractive is returned instead of launching a form when stdio cannot
// drive one. huh runs the form as a bubbletea program, and redirecting stdin
// does not make that program fail: bubbletea v1 sees a non-terminal stdin and
// silently opens /dev/tty instead (tea.go:590-613), so the prompt sits waiting
// on the real terminal — a hang for a caller that redirected stdin precisely
// because nobody is there to type. Where there is no controlling terminal it
// fails instead, but only partway through, after the earlier steps have run.
// Neither outcome is usable, so refuse rather than launch.
//
// The floor lives here rather than at the call sites because a call-site audit
// is exactly what missed `basecamp setup`: huh calls tea.NewProgram inside
// form.go, so grepping for the launcher cannot see these functions at all.
// Gating the constructor bounds where a prompt can be reached, and covers the
// prompts nobody has written yet.
var ErrNotInteractive = errors.New("not an interactive terminal")

// canPrompt reports whether stdio can drive a huh form: stdin must be a
// terminal to deliver keystrokes, and stderr must be one because that is where
// huh draws. A character device is not enough, and stdout is not the stream to
// ask about — see stdinarg.InteractivePrompt.
func canPrompt() bool {
	return stdinarg.InteractivePrompt()
}

// canPick reports whether stdio can drive a bare bubbletea program. The picker
// draws to stdout, not stderr, so it asks a different pair than canPrompt.
func canPick() bool {
	return stdinarg.InteractiveStdio()
}

// escKeyMap returns a keymap where both Ctrl+C and Escape abort the form.
func escKeyMap() *huh.KeyMap {
	km := huh.NewDefaultKeyMap()
	km.Quit = key.NewBinding(key.WithKeys("ctrl+c", "esc"))
	return km
}

// runForm is the one place in this package a huh form is executed, and so the
// one place the floor has to hold. Every exported prompt below funnels through
// it. TestNoUnsanctionedLaunchers keeps it that way — it fails on a .Run() in
// this file outside runForm, and on a huh import or a tea.NewProgram anywhere
// outside the handful of sanctioned files — because widening a call-site audit
// is exactly what let `basecamp setup` slip through.
func runForm(form *huh.Form) error {
	if !canPrompt() {
		return ErrNotInteractive
	}
	return form.WithKeyMap(escKeyMap()).Run()
}

// runFields runs a single-group form over the given fields.
func runFields(fields ...huh.Field) error {
	return runForm(huh.NewForm(huh.NewGroup(fields...)))
}

// Confirm shows a yes/no confirmation prompt. Escape or Ctrl+C cancels.
func Confirm(message string, defaultValue bool) (bool, error) {
	var result bool
	field := huh.NewConfirm().
		Title(message).
		Affirmative("Yes").
		Negative("No").
		Value(&result)

	if err := runFields(field); err != nil {
		return defaultValue, err
	}
	return result, nil
}

// ConfirmDangerous shows a confirmation prompt for dangerous actions. Escape or Ctrl+C cancels.
func ConfirmDangerous(message string) (bool, error) {
	var result bool
	field := huh.NewConfirm().
		Title(message).
		Description("This action cannot be undone.").
		Affirmative("Yes, I'm sure").
		Negative("Cancel").
		Value(&result)

	if err := runFields(field); err != nil {
		return false, err
	}
	return result, nil
}

// Input shows a text input prompt. Escape or Ctrl+C cancels.
func Input(title, placeholder string) (string, error) {
	var result string
	field := huh.NewInput().
		Title(title).
		Placeholder(placeholder).
		Value(&result)

	return result, runFields(field)
}

// InputRequired shows a required text input prompt. Escape or Ctrl+C cancels.
func InputRequired(title, placeholder string) (string, error) {
	var result string
	field := huh.NewInput().
		Title(title).
		Placeholder(placeholder).
		Value(&result).
		Validate(func(s string) error {
			if s == "" {
				return errors.New("this field is required")
			}
			return nil
		})

	return result, runFields(field)
}

// TextArea shows a multiline text input prompt. Escape or Ctrl+C cancels.
func TextArea(title, placeholder string) (string, error) {
	var result string
	field := huh.NewText().
		Title(title).
		Placeholder(placeholder).
		Value(&result)

	return result, runFields(field)
}

// SelectOption represents an option in a select prompt.
type SelectOption struct {
	Value string
	Label string
}

// Select shows a single-select prompt. Escape or Ctrl+C cancels.
func Select(title string, options []SelectOption) (string, error) {
	huhOptions := make([]huh.Option[string], len(options))
	for i, opt := range options {
		huhOptions[i] = huh.NewOption(opt.Label, opt.Value)
	}

	var result string
	field := huh.NewSelect[string]().
		Title(title).
		Options(huhOptions...).
		Value(&result)

	return result, runFields(field)
}

// SelectWithDescription shows a select prompt with descriptions. Escape or Ctrl+C cancels.
func SelectWithDescription(title, description string, options []SelectOption) (string, error) {
	huhOptions := make([]huh.Option[string], len(options))
	for i, opt := range options {
		huhOptions[i] = huh.NewOption(opt.Label, opt.Value)
	}

	var result string
	field := huh.NewSelect[string]().
		Title(title).
		Description(description).
		Options(huhOptions...).
		Value(&result)

	return result, runFields(field)
}

// MultiSelect shows a multi-select prompt. Escape or Ctrl+C cancels.
func MultiSelect(title string, options []SelectOption) ([]string, error) {
	huhOptions := make([]huh.Option[string], len(options))
	for i, opt := range options {
		huhOptions[i] = huh.NewOption(opt.Label, opt.Value)
	}

	var result []string
	field := huh.NewMultiSelect[string]().
		Title(title).
		Options(huhOptions...).
		Value(&result)

	return result, runFields(field)
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

	form := huh.NewForm(huh.NewGroup(huhFields...).Title(title))
	if err := runForm(form); err != nil {
		return nil, err
	}

	for i, f := range fields {
		results[f.Key] = *values[i]
	}

	return results, nil
}

// Note shows an informational note. It takes no input, but huh still runs it as
// a bubbletea program reading os.Stdin, so it needs the same floor.
func Note(title, body string) error {
	return runFields(huh.NewNote().
		Title(title).
		Description(body))
}

// ConfirmSetDefault asks the user if they want to save a value as the default. Escape or Ctrl+C cancels.
func ConfirmSetDefault(valueName string) (bool, error) {
	var result bool
	field := huh.NewConfirm().
		Title("Save as default?").
		Description("Set " + valueName + " as the default for future commands.").
		Affirmative("Yes").
		Negative("No").
		Value(&result)

	if err := runFields(field); err != nil {
		return false, err
	}
	return result, nil
}

// SelectScope shows a prompt for selecting the config scope (global or local).
// It inherits Select's floor, so it returns ErrNotInteractive off a terminal.
//
//nolint:gocritic // delegates deliberately; the floor lives in runForm
func SelectScope() (string, error) {
	options := []SelectOption{
		{Value: "local", Label: "Local (.basecamp/config.json)"},
		{Value: "global", Label: "Global (~/.config/basecamp/config.json)"},
	}
	return Select("Where should this be saved?", options)
}
