package cli

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/output"
)

// Cobra's arity messages are usage failures by construction. They used to fall
// through to the default classification (api_error, exit 4), which tells an
// agent to retry a call that can never succeed.
func TestTransformCobraErrorClassifiesArityAsUsage(t *testing.T) {
	for _, msg := range []string{
		"accepts at most 2 arg(s), received 3",
		"accepts 1 arg(s), received 2",
		"accepts between 1 and 2 arg(s), received 4",
	} {
		t.Run(msg, func(t *testing.T) {
			err := transformCobraError(errors.New(msg))

			var outErr *output.Error
			require.True(t, errors.As(err, &outErr), "expected *output.Error, got %T", err)
			assert.Equal(t, output.CodeUsage, outErr.Code)
			assert.Equal(t, msg, outErr.Message, "the wording is already clear; only the code was wrong")
		})
	}
}

// The zero-arg case keeps its friendlier rewrite.
func TestTransformCobraErrorKeepsZeroArgRewrite(t *testing.T) {
	err := transformCobraError(errors.New("accepts 1 arg(s), received 0"))

	var outErr *output.Error
	require.True(t, errors.As(err, &outErr))
	assert.Equal(t, output.CodeUsage, outErr.Code)
	assert.Equal(t, "ID required", outErr.Message)
}
