package connector

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The one release point, as a property of the source rather than of a
// reviewer's attention: settling an attempt, releasing a working directory
// and reporting an end happen in Dispatcher.release and nowhere else, so no
// later card can add a path that releases a directory while a worker may
// still be in it.
func TestOnlyTheReleasePointSettlesAnAttemptOrReleasesItsDirectory(t *testing.T) {
	source, err := os.ReadFile("dispatcher.go")
	require.NoError(t, err)
	functions := splitFunctions(string(source))
	require.NotEmpty(t, functions)

	for _, call := range []string{"EndAttempt(", "finishWorkspace(", "d.settle(", "d.adopt("} {
		for name, body := range functions {
			if name == "release" || name == call[:len(call)-1] || (name == "settle" && call == "EndAttempt(") {
				continue
			}
			assert.NotContains(t, body, call, "%s calls %s outside the release point", name, call)
		}
	}
	// The only other way to release a directory is one no task ever owned.
	for name, body := range functions {
		switch name {
		case "finishWorkspace", "discardPreparedWorkspace", "workspaceFinished":
			continue
		}
		assert.NotContains(t, body, "Workspaces.Finish(", "%s releases a working directory of its own accord", name)
	}
	for name, body := range functions {
		if name == "release" {
			continue
		}
		assert.NotContains(t, body, "State: string(AttemptEnded)", "%s reports an attempt ended outside the release point", name)
	}
}

// splitFunctions maps each top-level function or method name in a Go file to
// its body text.
func splitFunctions(source string) map[string]string {
	header := regexp.MustCompile(`(?m)^func (?:\([^)]*\) )?(\w+)\(`)
	matches := header.FindAllStringSubmatchIndex(source, -1)
	out := make(map[string]string, len(matches))
	for i, m := range matches {
		end := len(source)
		if i+1 < len(matches) {
			end = matches[i+1][0]
		}
		name := source[m[2]:m[3]]
		out[name] = strings.TrimSpace(source[m[0]:end])
	}
	return out
}
