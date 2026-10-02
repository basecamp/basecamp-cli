//go:build (linux || darwin) && race

package connect

// raceBuild says this test binary was built with -race, so the basecamp
// binary it runs is built with it too.
const raceBuild = true
