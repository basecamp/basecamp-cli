package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestProfileNameIsPathSafe(t *testing.T) {
	for _, name := range []string{"work", "my-profile", "a.b"} {
		assert.True(t, profileNameIsPathSafe(name), name)
	}
	for _, name := range []string{"", ".", "..", "../../../work/evil", "a/b", "/etc"} {
		assert.False(t, profileNameIsPathSafe(name), name)
	}
}
