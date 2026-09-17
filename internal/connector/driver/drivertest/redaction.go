package drivertest

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/basecamp/basecamp-cli/internal/connector/driver"
)

// RedactionPaths are the ways out of a worker a driver's redaction case must
// cover: a start that fails, a handshake that fails, a turn that fails, a
// cancel, and a close. Each is a place a driver builds text out of what the
// agent or the operating system said, which is where a secret gets out.
var RedactionPaths = []string{"start", "handshake", "prompt", "cancel", "close"}

// Crossing is everything one error path handed back to the connector: what a
// person or a file could end up holding.
type Crossing struct {
	// Errors are every error the path returned.
	Errors []error
	// Updates are every update the session emitted.
	Updates []driver.Update
	// Results are every turn result.
	Results []driver.PromptResult
	// Texts are the rest: a stderr tail, a log the driver wrote, a status
	// line.
	Texts []string
}

// RedactionPath is one error path, named from RedactionPaths.
type RedactionPath struct {
	Name string
	Run  func(t *testing.T) Crossing
}

// RequireRedacted is the redaction rule's test (driver's redact.go): a driver
// is fed a secret it must never pass on — in its environment, in its MCP
// server's environment, in what the agent writes back, or in a path under the
// directories the connector named — and every error, update, result and text
// that comes back out of it is checked for that secret.
//
// A driver's case must cover every path in RedactionPaths; one left out fails
// the test, because an unexercised path is exactly where the rule rots.
func RequireRedacted(t *testing.T, secret string, paths []RedactionPath) {
	t.Helper()
	if secret == "" {
		t.Fatal("RequireRedacted needs the secret to look for")
	}
	for _, name := range RedactionPaths {
		if !slices.ContainsFunc(paths, func(p RedactionPath) bool { return p.Name == name }) {
			t.Errorf("the redaction case does not cover the %q path", name)
		}
	}
	for _, path := range paths {
		t.Run(path.Name, func(t *testing.T) {
			crossing := path.Run(t)
			for i, err := range crossing.Errors {
				if err == nil {
					continue
				}
				// The message, and every verbose form of it, since a %+v in
				// a log reaches whatever the error kept.
				for _, text := range []string{err.Error(), fmt.Sprintf("%v", err), fmt.Sprintf("%+v", err), fmt.Sprintf("%#v", err)} {
					if strings.Contains(text, secret) {
						t.Errorf("the secret is in error #%d: %s", i, text)
						break
					}
				}
			}
			for i, u := range crossing.Updates {
				encoded, _ := json.Marshal(u)
				if strings.Contains(string(encoded), secret) {
					t.Errorf("the secret is in update #%d: %s", i, encoded)
				}
			}
			for i, r := range crossing.Results {
				if text := fmt.Sprintf("%+v", r); strings.Contains(text, secret) {
					t.Errorf("the secret is in turn result #%d: %s", i, text)
				}
			}
			for i, text := range crossing.Texts {
				if strings.Contains(text, secret) {
					t.Errorf("the secret is in text #%d: %s", i, text)
				}
			}
		})
	}
}
