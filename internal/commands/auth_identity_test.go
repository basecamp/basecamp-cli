package commands

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestLoginIdentityLabelWithoutNameOrEmail: an authorization document that
// reports only an identity id (an in-house bc3 token's) is named by its
// ids, never as a blank name with the ids in brackets.
func TestLoginIdentityLabelWithoutNameOrEmail(t *testing.T) {
	assert.Equal(t, "identity 28142355", (&loginIdentity{IdentityID: 28142355}).label())
	assert.Equal(t, "identity 28142355, person 51177542", (&loginIdentity{IdentityID: 28142355, PersonID: 51177542}).label())
	assert.Equal(t, "identity 28142355", (&loginIdentity{IdentityID: 28142355, Name: " \t"}).label())
	assert.Equal(t, "Ada <ada@example.com> (identity 28142355)", (&loginIdentity{IdentityID: 28142355, Name: "Ada", Email: "ada@example.com"}).label())
	assert.Equal(t, "<ada@example.com> (identity 28142355)", (&loginIdentity{IdentityID: 28142355, Email: "ada@example.com"}).label())
}
