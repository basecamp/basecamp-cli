package commands

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/output"
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

func TestApplySubscribeFlags_MutualExclusion(t *testing.T) {
	ctx := context.Background()
	// subscribeChanged=true, noSubscribe=true
	_, err := applySubscribeFlags(ctx, nil, "someone", true, true)

	require.Error(t, err)
	var e *output.Error
	require.True(t, errors.As(err, &e), "expected *output.Error, got %T", err)
	assert.Contains(t, e.Message, "mutually exclusive")
}

func TestApplySubscribeFlags_NoSubscribe(t *testing.T) {
	ctx := context.Background()
	// subscribeChanged=false, noSubscribe=true
	result, err := applySubscribeFlags(ctx, nil, "", false, true)

	require.NoError(t, err)
	require.NotNil(t, result, "expected non-nil pointer for --no-subscribe")
	assert.Empty(t, *result, "expected empty slice for --no-subscribe")
}

func TestApplySubscribeFlags_Neither(t *testing.T) {
	ctx := context.Background()
	// subscribeChanged=false, noSubscribe=false
	result, err := applySubscribeFlags(ctx, nil, "", false, false)

	require.NoError(t, err)
	assert.Nil(t, result, "expected nil when neither flag is set")
}

func TestApplySubscribeFlags_ExplicitEmptyString(t *testing.T) {
	// --subscribe "" (explicitly set but empty value) should be a hard error
	ctx := context.Background()
	// subscribeChanged=true (flag was explicitly passed), value=""
	_, err := applySubscribeFlags(ctx, nil, "", true, false)

	require.Error(t, err)
	var e *output.Error
	require.True(t, errors.As(err, &e), "expected *output.Error, got %T", err)
	assert.Contains(t, e.Message, "at least one person")
}

func TestApplySubscribeFlags_WhitespaceOnlyRequiresAtLeastOne(t *testing.T) {
	ctx := context.Background()
	// subscribeChanged=true, value=" "
	_, err := applySubscribeFlags(ctx, nil, " ", true, false)

	require.Error(t, err)
	var e *output.Error
	require.True(t, errors.As(err, &e), "expected *output.Error, got %T", err)
	assert.Contains(t, e.Message, "at least one person")
}

func TestApplySubscribeFlags_CommaOnlyRequiresAtLeastOne(t *testing.T) {
	// --subscribe ",,," should fail: only delimiters, no actual tokens
	ctx := context.Background()
	// subscribeChanged=true, value=",,,"
	_, err := applySubscribeFlags(ctx, nil, ",,,", true, false)

	require.Error(t, err)
	var e *output.Error
	require.True(t, errors.As(err, &e), "expected *output.Error, got %T", err)
	assert.Contains(t, e.Message, "at least one person")
}
