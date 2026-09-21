// Package cable connects to Basecamp's Action Cable server, so a command can be
// told when something changed instead of asking over and over.
package cable

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	actioncable "github.com/basecamp/actioncable-go"

	"github.com/basecamp/basecamp-cli/internal/hostutil"
	"github.com/basecamp/basecamp-cli/internal/version"
)

// Tokens hands out the access token the handshake is authorized with.
// auth.Manager satisfies it.
type Tokens interface {
	AccessToken(ctx context.Context) (string, error)
}

// Dial connects to the cable server, authorizing the upgrade request with the
// same bearer token the SDK sends on an API request.
//
// The token is asked for again on every dial, not only the first: the client
// reconnects on its own for as long as `basecamp watch` runs, which is longer
// than an access token lives, and a reconnect carrying the first dial's token
// would be turned down for good while a working refresh token sat on disk.
func Dial(ctx context.Context, cableURL string, tokens Tokens, options ...actioncable.Option) (*actioncable.Client, error) {
	// The first header is built here so credentials the server won't take are
	// reported now, rather than becoming a reconnect loop inside the client.
	if _, err := authHeader(ctx, tokens); err != nil {
		return nil, err
	}

	settings := make([]actioncable.Option, 0, 1+len(options))
	settings = append(settings, actioncable.WithHeaderFunc(func(ctx context.Context) (http.Header, error) {
		return authHeader(ctx, tokens)
	}))
	settings = append(settings, options...)

	client := actioncable.New(cableURL, settings...)
	if err := client.Connect(ctx); err != nil {
		// Connect's context bounds the caller's wait, not the client's lifetime.
		// A dial that did not complete has no owner to close it, so stop its
		// retry loop before handing back the error.
		_ = client.Close()
		return nil, err
	}

	return client, nil
}

// CableURLEnv names the variable that overrides the endpoint outright.
const CableURLEnv = "BASECAMP_CABLE_URL"

// URL is the cable endpoint for an account, given where that account's web app
// lives.
//
// Deployed, Basecamp serves Action Cable from a host of its own rather than the
// web host or the API's: app.basecamp.com's cable is at chat.app.basecamp.com,
// and the account's slug is the whole path. Locally there is no separate host —
// the server mounts /cable on the app itself — so the path carries it instead.
// Both shapes are the ones CableHelper builds for the web client, which is the
// only client the cable server has had until now.
//
// BASECAMP_CABLE_URL overrides the result outright.
func URL(appURL, accountID string) (string, error) {
	if override := os.Getenv(CableURLEnv); override != "" {
		return override, nil
	}

	slug, err := Slug(accountID)
	if err != nil {
		return "", err
	}

	parsed, err := url.Parse(strings.TrimSuffix(appURL, "/"))
	if err != nil || parsed.Host == "" {
		return "", fmt.Errorf("could not read the Basecamp web URL %q", appURL)
	}

	local := hostutil.IsLocalhost(parsed.Host)
	switch parsed.Scheme {
	case "https":
		parsed.Scheme = "wss"
	case "http":
		if !local {
			return "", fmt.Errorf("refusing to open an unencrypted websocket to %q", parsed.Host)
		}
		parsed.Scheme = "ws"
	default:
		return "", fmt.Errorf("web URL %q is neither http nor https", appURL)
	}

	if local {
		parsed.Path = "/" + slug + "/cable"
	} else {
		parsed.Host = "chat." + parsed.Host
		parsed.Path = "/" + slug
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""

	return parsed.String(), nil
}

// Slug is the account's id as it appears in a Basecamp path: seven digits, or
// its own length once it outgrows seven. AccountSlug on the server side pads
// the same way, and the path is how the cable server knows which account a
// connection is for.
func Slug(accountID string) (string, error) {
	id, err := strconv.ParseInt(strings.TrimSpace(accountID), 10, 64)
	if err != nil || id <= 0 {
		return "", fmt.Errorf("account %q is not a Basecamp account id", accountID)
	}

	return fmt.Sprintf("%07d", id), nil
}

// AppURLHost is the web host carried by a Basecamp app_url or app_href, or ""
// when there is not one to read. Every recording and notification carries the
// account's own app_url, which is how a command learns the web host without
// being told it.
func AppURLHost(appURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(appURL))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return ""
	}

	return parsed.Scheme + "://" + parsed.Host
}

func authHeader(ctx context.Context, tokens Tokens) (http.Header, error) {
	token, err := tokens.AccessToken(ctx)
	if err != nil {
		return nil, err
	}

	header := http.Header{}
	header.Set("Authorization", "Bearer "+token)
	header.Set("User-Agent", version.UserAgent())

	return header, nil
}
