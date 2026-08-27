package tui

import (
	"bytes"
	"errors"
	"testing"

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
