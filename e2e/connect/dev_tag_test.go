//go:build (linux || darwin) && dev

package connect

// devBuild says this test binary was built with -tags dev, so the basecamp
// binary it runs is built with it too, as internal/commands/dev_tag.go
// would see it.
const devBuild = true
