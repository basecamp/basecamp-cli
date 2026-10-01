//go:build !linux && !darwin

package cli

// sealInheritedDescriptors has nothing to seal where the handover it protects
// cannot happen: a task token arrives on an inherited descriptor only on
// Linux and macOS, which is what internal/commands/mcp_token_sealed.go accepts
// and internal/commands/mcp_token_other.go refuses. Elsewhere this keeps the
// program building and starting the same way.
func sealInheritedDescriptors() error { return nil }
