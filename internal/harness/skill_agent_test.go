package harness

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Every shared-skill agent is one row of the same table, so every test here
// runs once per row: a behavior one row has and another lacks is a bug in
// the table, not a difference between agents.
func forEachSkillAgent(t *testing.T, test func(t *testing.T, agent SkillAgent)) {
	t.Helper()
	for _, agent := range SkillAgents() {
		t.Run(agent.ID, func(t *testing.T) { test(t, agent) })
	}
}

// isolatedHome points HOME at an empty directory and PATH at another, with the
// agent's home override cleared, so nothing on the developer's machine leaks in.
func isolatedHome(t *testing.T, agent SkillAgent) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", t.TempDir())
	t.Setenv(agent.HomeEnv, "")
	return home
}

// stubBinaryPath is where a stub of the agent's executable goes in dir: the
// bare name on Unix, name.exe on Windows, where exec.LookPath resolves the
// bare name through PATHEXT and the official binary is grok.exe.
func stubBinaryPath(dir string, agent SkillAgent) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(dir, agent.Binary+".exe")
	}
	return filepath.Join(dir, agent.Binary)
}

func writeStubBinary(t *testing.T, path string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755)) //nolint:gosec // G306: test stub must be executable
}

func TestSkillAgentTableHoldsGrok(t *testing.T) {
	assert.Equal(t, []SkillAgent{Grok}, SkillAgents())
	assert.Equal(t, SkillAgent{Name: "Grok", ID: "grok", HomeEnv: "GROK_HOME", HomeDir: ".grok", Binary: "grok"}, Grok)
}

// Sibling tests reset the global registry, so init()'s registration cannot be
// observed here; what can be is the AgentInfo a row registers, the same way
// TestClaudeAgentInfoWiring covers Claude.
func TestSkillAgentInfoWiring(t *testing.T) {
	forEachSkillAgent(t, func(t *testing.T, agent SkillAgent) {
		resetRegistry()
		defer resetRegistry()

		RegisterAgent(agent.agentInfo())

		info := FindAgent(agent.ID)
		require.NotNil(t, info, "%s agent not registered", agent.ID)
		assert.Equal(t, agent.Name, info.Name)
		assert.NotNil(t, info.Detect)
		assert.NotNil(t, info.FindBinary)
		assert.NotNil(t, info.Checks)
		assert.NotNil(t, info.Diagnostics)

		// Checks and Diagnostics are the same one check: skill presence.
		isolatedHome(t, agent)
		checks := info.Checks()
		require.Len(t, checks, 1)
		assert.Equal(t, agent.Name+" Skill", checks[0].Name)
		assert.Equal(t, checks, info.Diagnostics(t.Context()))
	})
}

func TestSkillAgentDetectByHomeDirectory(t *testing.T) {
	forEachSkillAgent(t, func(t *testing.T, agent SkillAgent) {
		home := isolatedHome(t, agent)

		assert.False(t, agent.Detect(), "no ~/%s and no binary", agent.HomeDir)
		require.NoError(t, os.MkdirAll(filepath.Join(home, agent.HomeDir), 0o755))
		assert.True(t, agent.Detect(), "~/%s directory", agent.HomeDir)
	})
}

func TestSkillAgentDetectByBinaryOnPath(t *testing.T) {
	forEachSkillAgent(t, func(t *testing.T, agent SkillAgent) {
		isolatedHome(t, agent)
		bin := t.TempDir()
		stub := stubBinaryPath(bin, agent)
		writeStubBinary(t, stub)
		t.Setenv("PATH", bin)

		assert.True(t, agent.Detect(), "%s on PATH detects %s without a home directory", agent.Binary, agent.Name)
		assert.Equal(t, stub, agent.FindBinary())
	})
}

// Off PATH, the binary is found where an installer leaves it: ~/.local/bin,
// or the bin directory of the agent's own home — a relocated one included.
func TestSkillAgentFindBinaryOffPath(t *testing.T) {
	forEachSkillAgent(t, func(t *testing.T, agent SkillAgent) {
		cases := map[string]func(t *testing.T, home string) string{
			"local bin": func(_ *testing.T, home string) string { return filepath.Join(home, ".local", "bin") },
			"home bin":  func(_ *testing.T, home string) string { return filepath.Join(home, agent.HomeDir, "bin") },
			"env home bin": func(t *testing.T, _ string) string {
				override := t.TempDir()
				t.Setenv(agent.HomeEnv, override)
				return filepath.Join(override, "bin")
			},
		}
		for name, binDir := range cases {
			t.Run(name, func(t *testing.T) {
				home := isolatedHome(t, agent)
				require.Empty(t, agent.FindBinary(), "before any install")

				stub := stubBinaryPath(binDir(t, home), agent)
				writeStubBinary(t, stub)
				assert.Equal(t, stub, agent.FindBinary())
				assert.True(t, agent.Detect())
			})
		}
	})
}

// A fallback candidate is held to the PATH lookup's standard: something that
// merely has the binary's name is not the binary, and must not make Detect
// report the agent present.
func TestSkillAgentFindBinaryOffPathRequiresExecutable(t *testing.T) {
	forEachSkillAgent(t, func(t *testing.T, agent SkillAgent) {
		t.Run("directory", func(t *testing.T) {
			home := isolatedHome(t, agent)
			require.NoError(t, os.MkdirAll(filepath.Join(home, ".local", "bin", agent.Binary), 0o755))

			assert.Empty(t, agent.FindBinary())
			assert.False(t, agent.Detect())
		})
		t.Run("non-executable file", func(t *testing.T) {
			if runtime.GOOS == "windows" {
				t.Skip("Windows has no execute bit; the extension decides")
			}
			home := isolatedHome(t, agent)
			stub := filepath.Join(home, ".local", "bin", agent.Binary)
			require.NoError(t, os.MkdirAll(filepath.Dir(stub), 0o755))
			require.NoError(t, os.WriteFile(stub, []byte("not a program"), 0o644))

			assert.Empty(t, agent.FindBinary())
			assert.False(t, agent.Detect(), "~/.local/bin/%s without an execute bit", agent.Binary)
		})
	})
}

func TestSkillAgentHomeHonorsEnvOverride(t *testing.T) {
	forEachSkillAgent(t, func(t *testing.T, agent SkillAgent) {
		home := isolatedHome(t, agent)
		assert.Equal(t, filepath.Join(home, agent.HomeDir), agent.Home())

		override := t.TempDir()
		t.Setenv(agent.HomeEnv, override)
		assert.Equal(t, override, agent.Home())
		assert.True(t, agent.Detect(), "an overridden home directory detects the agent")
	})
}

func TestSkillAgentCheckSkill(t *testing.T) {
	forEachSkillAgent(t, func(t *testing.T, agent SkillAgent) {
		home := isolatedHome(t, agent)

		check := agent.CheckSkill()
		assert.Equal(t, agent.Name+" Skill", check.Name)
		assert.Equal(t, "fail", check.Status)
		assert.Equal(t, "Run: basecamp setup "+agent.ID, check.Hint)
		assert.False(t, BaselineSkillInstalled())

		skillDir := filepath.Join(home, ".agents", "skills", "basecamp")
		require.NoError(t, os.MkdirAll(skillDir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# basecamp"), 0o644))

		check = agent.CheckSkill()
		assert.Equal(t, "pass", check.Status, "%+v", check)
		assert.True(t, BaselineSkillInstalled())
	})
}

// The check and the predicate answer from the same stat: a skill the agent
// cannot read is unhealthy for both, in the same direction.
func TestSkillAgentCheckSkillUnreadable(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}
	forEachSkillAgent(t, func(t *testing.T, agent SkillAgent) {
		home := isolatedHome(t, agent)
		skillsDir := filepath.Join(home, ".agents", "skills")
		require.NoError(t, os.MkdirAll(filepath.Join(skillsDir, "basecamp"), 0o755))
		require.NoError(t, os.Chmod(skillsDir, 0o000))
		t.Cleanup(func() { _ = os.Chmod(skillsDir, 0o755) })

		check := agent.CheckSkill()
		assert.Equal(t, "warn", check.Status, "%+v", check)
		assert.Contains(t, check.Hint, AgentSkillPath())
		assert.False(t, BaselineSkillInstalled())
	})
}

func TestSkillAgentCheckSkillReportsMissingHome(t *testing.T) {
	forEachSkillAgent(t, func(t *testing.T, agent SkillAgent) {
		t.Setenv("HOME", "")
		t.Setenv("USERPROFILE", "")

		check := agent.CheckSkill()
		assert.Equal(t, "warn", check.Status)
		assert.Equal(t, "Cannot determine shared Agent Skills directory", check.Message)
		assert.Empty(t, AgentSkillPath())
		assert.False(t, BaselineSkillInstalled())
	})
}
