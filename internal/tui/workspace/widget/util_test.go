package widget

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTruncate_ShortString_Unchanged(t *testing.T) {
	assert.Equal(t, "hello", Truncate("hello", 10))
	assert.Equal(t, "hello", Truncate("hello", 5))
}

func TestTruncate_LongString_Truncated(t *testing.T) {
	assert.Equal(t, "hell...", Truncate("hello world", 7))
	assert.Equal(t, "hel...", Truncate("hello world", 6))
}

func TestTruncate_ExactWidth_Unchanged(t *testing.T) {
	assert.Equal(t, "hello", Truncate("hello", 5))
}

func TestTruncate_ZeroWidth(t *testing.T) {
	assert.Equal(t, "", Truncate("hello", 0))
}

func TestTruncate_TinyWidth(t *testing.T) {
	assert.Equal(t, "h", Truncate("hello", 1))
	assert.Equal(t, "he", Truncate("hello", 2))
	assert.Equal(t, "hel", Truncate("hello", 3))
}
