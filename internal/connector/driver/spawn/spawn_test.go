package spawn

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/connector/setup"
)

func TestEveryWorkerSetupAcceptsHasADriver(t *testing.T) {
	for _, worker := range setup.Workers {
		d, err := New(worker, Options{})
		require.NoError(t, err, worker)
		assert.Equal(t, worker, d.Name())
	}
	_, err := New("nobody", Options{})
	assert.Error(t, err)
}
