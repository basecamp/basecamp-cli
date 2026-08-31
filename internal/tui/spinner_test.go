package tui

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestRunWithSpinnerNonTTY(t *testing.T) {
	var buf bytes.Buffer
	called := false
	err := RunWithSpinner(&buf, DefaultTheme(true), "Working...", func() error {
		called = true
		return nil
	})

	assert.NoError(t, err)
	assert.True(t, called)
	assert.Empty(t, buf.String())
}

func TestRunWithSpinnerReturnsTaskError(t *testing.T) {
	var buf bytes.Buffer
	want := errors.New("task failed")
	err := RunWithSpinner(&buf, DefaultTheme(true), "Working...", func() error {
		return want
	})

	assert.ErrorIs(t, err, want)
	assert.Empty(t, buf.String())
}

func TestRunWithSpinnerTerminalPathClearsAfterSuccess(t *testing.T) {
	var buf bytes.Buffer
	err := runWithSpinner(&buf, NoColorTheme(), "Working...", func() error {
		time.Sleep(20 * time.Millisecond)
		return nil
	}, true, time.Millisecond, time.Millisecond)

	assert.NoError(t, err)
	assert.Contains(t, buf.String(), "Working...")
	assert.True(t, strings.HasSuffix(buf.String(), "\r\033[2K"))
	assert.NotContains(t, buf.String(), "\033[?25l")
}

func TestRunWithSpinnerTerminalPathClearsAfterError(t *testing.T) {
	var buf bytes.Buffer
	want := errors.New("task failed")
	err := runWithSpinner(&buf, NoColorTheme(), "Working...", func() error {
		time.Sleep(20 * time.Millisecond)
		return want
	}, true, time.Millisecond, time.Millisecond)

	assert.ErrorIs(t, err, want)
	assert.Contains(t, buf.String(), "Working...")
	assert.True(t, strings.HasSuffix(buf.String(), "\r\033[2K"))
	assert.NotContains(t, buf.String(), "\033[?25l")
}
