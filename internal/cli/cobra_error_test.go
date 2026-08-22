package cli

import (
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

// jqUsable decides whether the fallback writer may keep a filter. It answers
// only the question that is knowable before any output exists — a filter that
// parses and compiles can still fail partway through producing results, which
// is why the jq-backed path never retries a failed write. That end-to-end
// property needs the real binary and is covered in e2e/stdin_dash.bats; this
// only pins the predicate.
func TestJQUsable(t *testing.T) {
	for _, tc := range []struct {
		name   string
		filter string
		want   bool
	}{
		{"valid", ".error", true},
		{"parse failure", ".[invalid", false},
		{"compile failure", ".foo | undefined_function", false},
		{"valid but fails at runtime", `.error, error("stop")`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, jqUsable(tc.filter))
		})
	}
}

func TestJQRenderErrorDiagnosticIsTerminalSafeAndSingleLine(t *testing.T) {
	err := output.ErrJQRuntime(errors.New("error: \x1b[31mPWN\x1b[0m\r\nforged\tline \x1b]8;;https://evil.example\aLINK\x1b]8;;\a \u009b31mC1\u009b0m"))

	got := jqRenderErrorDiagnostic(err)

	assert.NotContains(t, got, "\x1b")
	assert.NotContains(t, got, "\r")
	assert.NotContains(t, got, "\n")
	assert.NotContains(t, got, "\t")
	assert.NotContains(t, got, "\u009b")
	assert.Contains(t, got, "PWN")
	assert.Contains(t, got, "forged line LINK")
	assert.Contains(t, got, "C1")
}
