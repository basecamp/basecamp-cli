package fakebasecamp

import (
	"fmt"
	"net/http"
)

// The authorization server: discovery, the agent connection ceremony, and
// the client_credentials mint. Basecamp's contract for the ceremony is
// bc3's doc/oauth/agent_connections.md; internal/auth is built to it, and
// this answers it.

// asMetadata is RFC 8414 metadata naming the fake as its own issuer.
func (c *call) asMetadata() answer {
	return c.jsonAnswer(http.StatusOK, map[string]any{
		"issuer":                        c.base(),
		"token_endpoint":                c.base() + "/oauth/tokens",
		"device_authorization_endpoint": c.base() + "/oauth/device_authorizations",
		"grant_types_supported": []string{
			"urn:ietf:params:oauth:grant-type:device_code", "client_credentials", "refresh_token",
		},
	})
}

// resourceMetadata is RFC 9728 metadata pointing the resource at the fake's
// authorization server.
func (c *call) resourceMetadata() answer {
	return c.jsonAnswer(http.StatusOK, map[string]any{
		"resource":              c.base(),
		"authorization_servers": []string{c.base()},
	})
}

// agentConnections is the anonymous intake: a code for the operator to
// approve, and where to poll for the answer.
func (c *call) agentConnections() answer {
	if c.req.PostForm.Get("device_name") == "" {
		return oauthError(http.StatusBadRequest, "invalid_request")
	}
	code := fmt.Sprintf("fake-device-%d", c.s.nextSerialLocked())
	c.s.devices[code] = true
	return c.jsonAnswer(http.StatusOK, map[string]any{
		"device_code":               code,
		"user_code":                 "WDJB-MJHT",
		"verification_uri":          c.base() + "/connect",
		"verification_uri_complete": c.base() + "/connect?user_code=WDJB-MJHT",
		"token_uri":                 c.base() + "/oauth/agent_connection_tokens",
		"expires_in":                600,
		"interval":                  1,
	})
}

// agentConnectionTokens is the poll. The operator has always approved by
// the time it arrives: it hands over the client World.Connection names,
// provisioned with the scope approved, under a newly minted secret. The
// code is spent by the handover, as Basecamp spends it.
func (c *call) agentConnectionTokens() answer {
	code := c.req.PostForm.Get("device_code")
	if !c.s.devices[code] {
		return oauthError(http.StatusBadRequest, "invalid_grant")
	}
	conn := c.s.world.Connection
	client, ok := c.s.world.Agents[conn.ClientID]
	if !ok {
		c.s.r.Errorf("fakebasecamp: the connection hands over client %q, which the world does not hold", conn.ClientID)
		return oauthError(http.StatusBadRequest, "invalid_grant")
	}
	delete(c.s.devices, code)
	client.Scope = conn.Scope
	if client.Scope == "" {
		client.Scope = ScopeFull
	}
	client.rotations++
	client.Secret = fmt.Sprintf("%s-secret-%d", client.ID, client.rotations)
	return c.jsonAnswer(http.StatusOK, map[string]any{
		"client_id":     client.ID,
		"client_secret": client.Secret,
		"account_id":    c.s.world.Account.ID,
		"scope":         client.Scope,
	})
}

// token mints a client_credentials token for an agent's client. A secret
// the client does not hold now, rotated away or never issued, is
// invalid_client.
func (c *call) token() answer {
	form := c.req.PostForm
	if form.Get("grant_type") != "client_credentials" {
		return oauthError(http.StatusBadRequest, "unsupported_grant_type")
	}
	client, ok := c.s.world.Agents[form.Get("client_id")]
	if !ok || client.Secret != form.Get("client_secret") {
		return oauthError(http.StatusUnauthorized, "invalid_client")
	}
	token := fmt.Sprintf("fake-token-%d", c.s.nextSerialLocked())
	c.s.world.Tokens[token] = &Token{PersonID: client.PersonID, Scope: client.Scope}
	return c.jsonAnswer(http.StatusOK, map[string]any{
		"access_token": token,
		"token_type":   "bearer",
		"expires_in":   3600,
		"resource":     fmt.Sprintf("urn:bc:agent:%d", client.PersonID),
		"scope":        client.Scope,
	})
}
