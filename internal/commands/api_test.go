package commands

import (
	"errors"
	"testing"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/output"
)

func TestParsePath(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"projects.json", "projects.json"},
		{"/projects.json", "projects.json"},
		{"buckets/123/todos/456.json", "buckets/123/todos/456.json"},
		{"/buckets/123/todos/456.json", "buckets/123/todos/456.json"},
		{"https://3.basecampapi.com/999/projects.json", "/projects.json"},
		{"https://3.basecampapi.com/12345/buckets/1/todos/2.json", "/buckets/1/todos/2.json"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.want, parsePath(tt.input))
		})
	}
}

func TestAPIPathArgs(t *testing.T) {
	cmd := &cobra.Command{Use: "get <path>"}

	t.Run("no args returns path required", func(t *testing.T) {
		err := apiPathArgs(cmd, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "path required")
	})

	t.Run("one arg succeeds", func(t *testing.T) {
		assert.NoError(t, apiPathArgs(cmd, []string{"projects.json"}))
	})

	t.Run("two args returns path required", func(t *testing.T) {
		err := apiPathArgs(cmd, []string{"a", "b"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "path required")
	})
}

func TestConvertSDKErrorPreservesRequestID(t *testing.T) {
	sdkErr := &basecamp.Error{
		Code:       basecamp.CodeAPI,
		Message:    "server error",
		HTTPStatus: 500,
		RequestID:  "req-cli-123",
	}

	err := convertSDKError(sdkErr)

	var outErr *output.Error
	require.True(t, errors.As(err, &outErr), "expected *output.Error, got %T", err)
	assert.Equal(t, output.CodeAPI, outErr.Code)

	var gotSDK *basecamp.Error
	require.True(t, errors.As(err, &gotSDK), "expected wrapped *basecamp.Error, got %T", err)
	assert.Equal(t, "req-cli-123", gotSDK.RequestID)
}
