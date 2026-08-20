package cli

import (
	"bytes"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/output"
)

// Cobra's arity messages are usage failures by construction. They used to fall
// through to the default classification (api_error, exit 7), which tells an
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

// A typed error already carries a code, an HTTP status and a retryable flag.
// Matching on its rendered text would flatten all of that into a bare usage
// string — so an API error that merely quotes an arity phrase is left alone.
func TestTransformCobraErrorPreservesTypedErrors(t *testing.T) {
	t.Run("SDK error", func(t *testing.T) {
		original := &basecamp.Error{
			Code:       basecamp.CodeAPI,
			Message:    "server rejected the payload: accepts 1 arg(s), received 2",
			HTTPStatus: 422,
			Retryable:  true,
		}

		err := transformCobraError(original)

		var sdkErr *basecamp.Error
		require.True(t, errors.As(err, &sdkErr), "expected the SDK error to survive, got %T", err)
		assert.Equal(t, basecamp.CodeAPI, sdkErr.Code)
		assert.Equal(t, 422, sdkErr.HTTPStatus)
		assert.True(t, sdkErr.Retryable)
	})

	t.Run("output error", func(t *testing.T) {
		original := output.ErrNotFound("todo", "123")

		err := transformCobraError(original)

		var outErr *output.Error
		require.True(t, errors.As(err, &outErr))
		assert.Equal(t, output.CodeNotFound, outErr.Code, "must not be reclassified as usage")
	})
}

// Anchored: a command's own error that merely contains the phrase is not an
// arity failure and must keep its own classification path.
func TestTransformCobraErrorIgnoresUnanchoredArityText(t *testing.T) {
	msg := "the API said: accepts 1 arg(s), received 2 (and then some)"

	err := transformCobraError(errors.New(msg))

	var outErr *output.Error
	assert.False(t, errors.As(err, &outErr), "should be left untouched, got %T", err)
	assert.Equal(t, msg, err.Error())
}

// An invalid --jq paired with an error raised before jq validation used to
// exit non-zero having printed nothing: the fallback writer tried to render the
// envelope through the broken filter, and its failure was discarded. The error
// the caller needs outranks the filter they asked for.
func TestInvalidJQFilterStillRendersAnEarlierError(t *testing.T) {
	var buf bytes.Buffer
	writer := output.New(output.Options{
		Format:   output.FormatJSON,
		Writer:   &buf,
		JQFilter: ".[invalid",
	})

	require.Error(t, writer.Err(output.ErrUsage("stray dash")),
		"a broken filter must report that it could not render")
	require.Empty(t, buf.String(), "and must not have written a usable envelope")

	buf.Reset()
	plain := output.New(output.Options{Format: output.FormatJSON, Writer: &buf})
	require.NoError(t, plain.Err(output.ErrUsage("stray dash")))
	assert.Contains(t, buf.String(), "stray dash")
}
