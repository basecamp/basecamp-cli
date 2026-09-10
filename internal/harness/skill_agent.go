package harness

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// SkillAgent describes a coding agent whose whole Basecamp integration is the
// shared ~/.agents skill: basecamp-cli ships no plugin for it, so setup is
// confirming the shared skill and health is skill presence only. Each such
// agent differs only in the five fields below, which is why they are rows in
// a table rather than a file apiece.
//
// Codex is not a row. It has a native plugin here (.codex-plugin, installed
// through the 37signals marketplace), so its detection, setup and health are
// its own — see codex.go. An agent leaves this table the day it grows one.
type SkillAgent struct {
	Name    string // "Grok"
	ID      string // "grok"; the `basecamp setup <id>` subcommand and BASECAMP_SETUP_AGENT value
	HomeEnv string // "GROK_HOME"; overrides HomeDir when set
	HomeDir string // ".grok"; under the user's home directory
	Binary  string // "grok"; the executable's name
}

// Grok is xAI's Grok Build CLI. It reads user skills from ~/.grok/skills and
// from the cross-agent ~/.agents/skills, so the shared skill is all it needs.
var Grok = SkillAgent{Name: "Grok", ID: "grok", HomeEnv: "GROK_HOME", HomeDir: ".grok", Binary: "grok"}

// skillAgents is the registration table: every agent that reads the shared
// skill, in the order they register.
var skillAgents = []SkillAgent{Grok}

func init() {
	for _, agent := range skillAgents {
		RegisterAgent(agent.agentInfo())
	}
}

// SkillAgents returns every shared-skill agent, in registration order.
func SkillAgents() []SkillAgent {
	return append([]SkillAgent(nil), skillAgents...)
}

func (a SkillAgent) agentInfo() AgentInfo {
	checks := func() []*StatusCheck { return []*StatusCheck{a.CheckSkill()} }
	return AgentInfo{
		Name:        a.Name,
		ID:          a.ID,
		Detect:      a.Detect,
		FindBinary:  a.FindBinary,
		Checks:      checks,
		Diagnostics: func(context.Context) []*StatusCheck { return checks() },
	}
}

// Detect reports whether the agent has a home directory or an executable.
func (a SkillAgent) Detect() bool {
	if info, err := os.Stat(a.Home()); err == nil && info.IsDir() {
		return true
	}
	return a.FindBinary() != ""
}

// FindBinary returns the agent's executable path, or an empty string. It
// looks on PATH first, then where an installer puts the binary when the
// shell has not picked up the PATH change yet: ~/.local/bin, and the agent's
// own home's bin (Grok Build's installers write ~/.grok/bin/grok, or
// $GROK_HOME/bin/grok for the npm package). Each fallback goes through
// exec.LookPath too, so it is held to the PATH lookup's standard — an
// executable regular file on Unix, a PATHEXT extension such as .exe on
// Windows — and a stale directory or non-executable file of that name is
// not reported as the binary.
func (a SkillAgent) FindBinary() string {
	if path, err := exec.LookPath(a.Binary); err == nil {
		return path
	}
	var candidates []string
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		candidates = append(candidates, filepath.Join(filepath.Clean(home), ".local", "bin", a.Binary))
	}
	if agentHome := a.Home(); agentHome != "" {
		candidates = append(candidates, filepath.Join(agentHome, "bin", a.Binary))
	}
	for _, candidate := range candidates {
		if path, err := exec.LookPath(candidate); err == nil {
			return path
		}
	}
	return ""
}

// Home returns the agent's home directory: $HomeEnv, or HomeDir under the
// user's home. Empty when neither can be determined.
func (a SkillAgent) Home() string {
	if home := strings.TrimSpace(os.Getenv(a.HomeEnv)); home != "" {
		return home
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(filepath.Clean(home), a.HomeDir)
}

// CheckSkill checks whether the shared Basecamp skill is installed for the
// agent. It answers from the same predicate as BaselineSkillInstalled; the
// extra states only say why it is false.
func (a SkillAgent) CheckSkill() *StatusCheck {
	name := a.Name + " Skill"
	err := statAgentSkill()
	switch {
	case errors.Is(err, errNoHomeDir):
		return &StatusCheck{
			Name:    name,
			Status:  "warn",
			Message: "Cannot determine shared Agent Skills directory",
		}
	case os.IsNotExist(err):
		return &StatusCheck{
			Name:    name,
			Status:  "fail",
			Message: "Skill not installed",
			Hint:    "Run: basecamp setup " + a.ID,
		}
	case err != nil:
		return &StatusCheck{
			Name:    name,
			Status:  "warn",
			Message: "Cannot check " + a.Name + " skill",
			Hint:    "Unable to stat " + AgentSkillPath(),
		}
	}
	return &StatusCheck{
		Name:    name,
		Status:  "pass",
		Message: "Installed",
	}
}
