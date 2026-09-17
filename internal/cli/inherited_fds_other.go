//go:build !linux

package cli

// sealInheritedDescriptors has nothing to seal where the handover it protects
// does not happen: the agent connector runs on Linux, and elsewhere this keeps
// the program building and starting the same way.
func sealInheritedDescriptors() {}
