package harness

import "sync"

// AgentInfo describes a coding agent integration.
type AgentInfo struct {
	Name   string                // "Claude Code"
	ID     string                // "claude"
	Detect func() bool           // returns true if the agent is installed
	Checks func() []*StatusCheck // health checks for doctor
}

var (
	registryMu sync.RWMutex
	registry   []AgentInfo
)

// RegisterAgent adds an agent to the global registry.
// Typically called from init() in agent-specific files.
// Panics on empty or duplicate IDs to keep registry state well-defined.
func RegisterAgent(info AgentInfo) {
	registryMu.Lock()
	defer registryMu.Unlock()
	if info.ID == "" {
		panic("harness: RegisterAgent called with empty agent ID")
	}
	for i := range registry {
		if registry[i].ID == info.ID {
			panic("harness: RegisterAgent called with duplicate agent ID: " + info.ID)
		}
	}
	registry = append(registry, info)
}

// DetectedAgents returns all agents whose Detect function returns true.
// Copies the registry under the lock before calling Detect callbacks
// to avoid holding the lock during potentially slow I/O.
func DetectedAgents() []AgentInfo {
	registryMu.RLock()
	snapshot := make([]AgentInfo, len(registry))
	copy(snapshot, registry)
	registryMu.RUnlock()

	var detected []AgentInfo
	for _, a := range snapshot {
		if a.Detect != nil && a.Detect() {
			detected = append(detected, a)
		}
	}
	return detected
}

// AllAgents returns every registered agent.
func AllAgents() []AgentInfo {
	registryMu.RLock()
	defer registryMu.RUnlock()
	out := make([]AgentInfo, len(registry))
	copy(out, registry)
	return out
}

// FindAgent returns the agent with the given ID, or nil.
func FindAgent(id string) *AgentInfo {
	registryMu.RLock()
	defer registryMu.RUnlock()
	for i := range registry {
		if registry[i].ID == id {
			info := registry[i]
			return &info
		}
	}
	return nil
}

// resetRegistry clears the registry (for testing only).
func resetRegistry() {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry = nil
}
