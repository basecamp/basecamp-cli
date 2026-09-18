package cli

import (
	"go/build"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sealingFile is the only implementation that keeps an inherited descriptor
// from the processes this one starts; refusalFile is the readTaskToken that
// refuses a task token outright. Named rather than inferred, so this test
// asks about the code rather than about a build tag it repeats.
const (
	sealingDir  = "."
	sealingFile = "inherited_fds_linux.go"
	handoverDir = "../commands"
	refusalFile = "mcp_token_other.go"
)

// releasePlatforms are the targets `make build-all` produces, plus the other
// Unix ones a `go build` is expected to work on.
var releasePlatforms = []struct{ goos, goarch string }{
	{"linux", "amd64"},
	{"linux", "arm64"},
	{"darwin", "arm64"},
	{"darwin", "amd64"},
	{"freebsd", "amd64"},
	{"openbsd", "amd64"},
	{"netbsd", "amd64"},
	{"windows", "amd64"},
}

// A platform reads a task token off an inherited descriptor exactly where it
// seals inherited descriptors.
//
// These are two build constraints in two packages answering one question, and
// they are only ever right together: a platform that accepts the handover
// without the seal leaves the token descriptor inheritable through every hook
// that runs before the command reads it, which is the guarantee the handover
// exists for. The pair drifted once already — the reader was built for every
// Unix while the seal was Linux-only — so it is checked rather than
// remembered.
func TestTheTokenIsOnlyReadWhereItIsSealed(t *testing.T) {
	for _, platform := range releasePlatforms {
		t.Run(platform.goos+"/"+platform.goarch, func(t *testing.T) {
			seals := compilesFile(t, sealingDir, platform.goos, platform.goarch, sealingFile)
			refuses := compilesFile(t, handoverDir, platform.goos, platform.goarch, refusalFile)

			assert.Equal(t, seals, !refuses,
				"%s/%s compiles %s (seals inherited descriptors) = %v, and compiles %s (refuses the token handover) = %v; it must do exactly one",
				platform.goos, platform.goarch, sealingFile, seals, refusalFile, refuses)
		})
	}
}

// compilesFile reports whether a build for this platform includes the file.
func compilesFile(t *testing.T, dir, goos, goarch, file string) bool {
	t.Helper()
	context := build.Default
	context.GOOS = goos
	context.GOARCH = goarch
	context.CgoEnabled = false
	pkg, err := context.ImportDir(dir, 0)
	require.NoError(t, err)
	return slices.Contains(pkg.GoFiles, file)
}
