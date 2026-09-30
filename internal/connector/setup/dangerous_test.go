//go:build unix

package setup

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/connector/admission"
)

func on() *bool  { v := true; return &v }
func off() *bool { v := false; return &v }

// Dangerous mode goes with an agent its owner alone drives, and is refused
// with anyone else, whichever of the two a run asks for.
func TestApplyKeepsDangerousModeToAnAgentYouAloneDrive(t *testing.T) {
	base := validFile(t)
	require.Equal(t, admission.TrustOperator, base.Trust.Mode)

	out, err := Apply(base, Changes{Dangerous: on()})
	require.NoError(t, err)
	assert.True(t, out.Dangerous)
	require.NoError(t, out.Validate())

	t.Run("turning it on while others can give it work", func(t *testing.T) {
		shared := base
		shared.Trust = admission.Trust{Mode: admission.TrustProject, OperatorID: operatorID}
		_, err := Apply(shared, Changes{Dangerous: on()})
		require.ErrorIs(t, err, ErrDangerousShared)
		_, err = Apply(base, Changes{Dangerous: on(), Trust: admission.TrustProject})
		require.ErrorIs(t, err, ErrDangerousShared, "asking for both in one run")
		_, err = Apply(base, Changes{Dangerous: on(), Allow: []int64{9}})
		require.ErrorIs(t, err, ErrDangerousShared)
	})

	t.Run("letting others in while it's on", func(t *testing.T) {
		_, err := Apply(out, Changes{Trust: admission.TrustProject})
		require.ErrorIs(t, err, ErrDangerousWhileShared)
		_, err = Apply(out, Changes{Allow: []int64{9}})
		require.ErrorIs(t, err, ErrDangerousWhileShared)
	})

	t.Run("turning it off and sharing in one run", func(t *testing.T) {
		shared, err := Apply(out, Changes{Dangerous: off(), Trust: admission.TrustProject})
		require.NoError(t, err)
		assert.False(t, shared.Dangerous)
		assert.Equal(t, admission.TrustProject, shared.Trust.Mode)
	})

	t.Run("making it yours alone and dangerous in one run", func(t *testing.T) {
		shared := base
		shared.Trust = admission.Trust{Mode: admission.TrustProject, OperatorID: operatorID}
		mine, err := Apply(shared, Changes{Dangerous: on(), Trust: admission.TrustOperator})
		require.NoError(t, err)
		assert.True(t, mine.Dangerous)
	})
}

// A connect.json edited by hand to be dangerous and shared, or dangerous on
// the acp driver, is refused wherever it's read: setup, status and the
// connector itself.
func TestAHandEditedDangerousFileIsRefusedOnRead(t *testing.T) {
	f := validFile(t)
	f.Dangerous = true
	f.Trust.Mode = admission.TrustProject
	data, err := json.Marshal(f)
	require.NoError(t, err)
	_, err = Parse(data)
	require.ErrorIs(t, err, ErrDangerousShared)

	f = validFile(t)
	f.Dangerous = true
	f.Driver = DriverACP
	data, err = json.Marshal(f)
	require.NoError(t, err)
	_, err = Parse(data)
	require.ErrorContains(t, err, "dangerous mode needs the")
}

// Off, it isn't written at all, so every connect.json from before it still
// reads the same.
func TestDangerousModeOffIsNotWritten(t *testing.T) {
	data, err := json.Marshal(validFile(t))
	require.NoError(t, err)
	assert.NotContains(t, string(data), "dangerous")
}
