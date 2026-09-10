// Package harness detects and checks AI agent integration health.
package harness

import (
	"errors"
	"os"
	"path/filepath"
)

// StatusCheck represents a single agent integration health check result.
type StatusCheck struct {
	Name    string `json:"name"`
	Status  string `json:"status"` // "pass", "warn", "fail"
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`
}

// errNoHomeDir is the shared skill's state when its path cannot be built.
var errNoHomeDir = errors.New("cannot determine home directory")

// AgentSkillPath returns the shared skill's path,
// ~/.agents/skills/basecamp/SKILL.md, or "" when the home directory cannot
// be determined. Every agent that reads the cross-agent skills directory
// finds the skill here.
func AgentSkillPath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(filepath.Clean(home), ".agents", "skills", "basecamp", "SKILL.md")
}

// BaselineSkillInstalled reports whether the shared skill is on disk. It is
// the one health predicate for the shared skill: setup, doctor and the
// shared-skill agents' checks all answer from it.
func BaselineSkillInstalled() bool {
	return statAgentSkill() == nil
}

// statAgentSkill is BaselineSkillInstalled with the reason: errNoHomeDir when
// the path cannot be built, the stat error when it can.
func statAgentSkill() error {
	path := AgentSkillPath()
	if path == "" {
		return errNoHomeDir
	}
	_, err := os.Stat(path)
	return err
}
