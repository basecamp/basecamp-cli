package commands

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsNumeric(t *testing.T) {
	tests := []struct {
		input    string
		expected bool
	}{
		// Valid numeric strings
		{"0", true},
		{"1", true},
		{"123", true},
		{"123456789", true},

		// Invalid inputs
		{"", false},
		{"abc", false},
		{"123abc", false},
		{"abc123", false},
		{"12.34", false},
		{"-1", false},
		{" 123", false},
		{"123 ", false},
		{"12 34", false},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := isNumeric(tt.input)
			assert.Equal(t, tt.expected, result, "isNumeric(%q)", tt.input)
		})
	}
}
