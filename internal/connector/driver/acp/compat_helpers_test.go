//go:build unix

package acp

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// hostDigestShowsToken reads compatibility check 5's host probe: the SHA-256
// the worker's shell computed of the host token variable as it saw it. A
// probe that is not a digest — no hashing tool on the machine (stock macOS has
// shasum, not sha256sum), or a pipeline that printed nothing — proves nothing,
// and is an error rather than a pass.
func hostDigestShowsToken(probe, host string) (bool, error) {
	digest := strings.TrimSpace(probe)
	if digest == "NOHASH" {
		return false, errors.New("the worker's shell has neither sha256sum nor shasum")
	}
	if len(digest) != 64 {
		return false, fmt.Errorf("the probe wrote %d characters, not a SHA-256 digest", len(digest))
	}
	if _, err := hex.DecodeString(digest); err != nil {
		return false, fmt.Errorf("the probe wrote something that is not a digest: %w", err)
	}
	sum := sha256.Sum256([]byte(host))
	return digest == hex.EncodeToString(sum[:]), nil
}

func TestTheHostTokenProbeProvesNothingWithoutADigest(t *testing.T) {
	host := "test-host-token-not-real"
	sum := sha256.Sum256([]byte(host))
	seen, err := hostDigestShowsToken(hex.EncodeToString(sum[:])+"\n", host)
	require.NoError(t, err)
	assert.True(t, seen)

	empty := sha256.Sum256(nil)
	seen, err = hostDigestShowsToken(hex.EncodeToString(empty[:]), host)
	require.NoError(t, err)
	assert.False(t, seen, "the digest of nothing: the shell did not see the token")

	for _, probe := range []string{"", "\n", "NOHASH", "sha256sum: not found", strings.Repeat("z", 64)} {
		_, err := hostDigestShowsToken(probe, host)
		assert.Error(t, err, "%q is not a digest, and must not pass as proof", probe)
	}
}
