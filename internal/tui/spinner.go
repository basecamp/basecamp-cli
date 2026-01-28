package tui

import (
	"fmt"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// SpinnerStyle defines the visual style of a spinner.
type SpinnerStyle int

const (
	SpinnerDots SpinnerStyle = iota
	SpinnerLine
	SpinnerPulse
	SpinnerPoints
	SpinnerGlobe
	SpinnerMoon
	SpinnerMonkey
	SpinnerMeter
	SpinnerHamburger
)

// spinnerModel is the bubbletea model for a spinner.
type spinnerModel struct {
	spinner  spinner.Model
	message  string
	done     bool
	result   string
	err      error
	styles   *Styles
	quitting bool
}

// SpinnerOption configures a spinner.
type SpinnerOption func(*spinnerModel)

// WithSpinnerStyle sets the spinner animation style.
func WithSpinnerStyle(style SpinnerStyle) SpinnerOption {
	return func(m *spinnerModel) {
		switch style {
		case SpinnerDots:
			m.spinner.Spinner = spinner.Dot
		case SpinnerLine:
			m.spinner.Spinner = spinner.Line
		case SpinnerPulse:
			m.spinner.Spinner = spinner.Pulse
		case SpinnerPoints:
			m.spinner.Spinner = spinner.Points
		case SpinnerGlobe:
			m.spinner.Spinner = spinner.Globe
		case SpinnerMoon:
			m.spinner.Spinner = spinner.Moon
		case SpinnerMonkey:
			m.spinner.Spinner = spinner.Monkey
		case SpinnerMeter:
			m.spinner.Spinner = spinner.Meter
		case SpinnerHamburger:
			m.spinner.Spinner = spinner.Hamburger
		}
	}
}

// WithSpinnerColor sets the spinner color.
func WithSpinnerColor(color lipgloss.TerminalColor) SpinnerOption {
	return func(m *spinnerModel) {
		m.spinner.Style = lipgloss.NewStyle().Foreground(color)
	}
}

func newSpinnerModel(message string, opts ...SpinnerOption) spinnerModel {
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("205"))

	m := spinnerModel{
		spinner: s,
		message: message,
		styles:  NewStyles(),
	}

	for _, opt := range opts {
		opt(&m)
	}

	return m
}

func (m spinnerModel) Init() tea.Cmd {
	return m.spinner.Tick
}

type spinnerDoneMsg struct {
	result string
	err    error
}

func (m spinnerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			m.quitting = true
			return m, tea.Quit
		}
	case spinnerDoneMsg:
		m.done = true
		m.result = msg.result
		m.err = msg.err
		return m, tea.Quit
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m spinnerModel) View() string {
	if m.quitting {
		return ""
	}
	if m.done {
		if m.err != nil {
			return m.styles.Error.Render("✗ "+m.err.Error()) + "\n"
		}
		return m.styles.Success.Render("✓ "+m.result) + "\n"
	}
	return fmt.Sprintf("%s %s\n", m.spinner.View(), m.message)
}

// Spinner runs a spinner while a function executes.
type Spinner struct {
	message string
	opts    []SpinnerOption
}

// NewSpinner creates a new spinner with a message.
func NewSpinner(message string, opts ...SpinnerOption) *Spinner {
	return &Spinner{
		message: message,
		opts:    opts,
	}
}

// Run executes the given function while displaying a spinner.
// Returns the result and any error from the function.
func (s *Spinner) Run(fn func() (string, error)) (string, error) {
	m := newSpinnerModel(s.message, s.opts...)

	p := tea.NewProgram(m)

	// Run the function in a goroutine
	go func() {
		result, err := fn()
		time.Sleep(100 * time.Millisecond) // Brief pause so spinner is visible
		p.Send(spinnerDoneMsg{result: result, err: err})
	}()

	finalModel, err := p.Run()
	if err != nil {
		return "", err
	}

	final := finalModel.(spinnerModel) //nolint:errcheck // type assertion always succeeds here
	if final.quitting {
		return "", fmt.Errorf("canceled")
	}
	return final.result, final.err
}

// RunSimple executes a function while displaying a spinner.
// Use this for functions that don't return a result message.
func (s *Spinner) RunSimple(fn func() error) error {
	_, err := s.Run(func() (string, error) {
		return "Done", fn()
	})
	return err
}
