package resilience

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestNewStoreExplicitDir(t *testing.T) {
	s := NewStore("/custom/path")
	if s.Dir() != "/custom/path" {
		t.Errorf("Dir() = %q, want /custom/path", s.Dir())
	}
}

func TestNewStoreEmptyUsesDefault(t *testing.T) {
	s := NewStore("")
	if s.Dir() == "" {
		t.Error("Dir() should not be empty when created with empty string")
	}
	if !strings.Contains(s.Dir(), "basecamp") {
		t.Errorf("Dir() = %q, should contain 'basecamp'", s.Dir())
	}
}

func TestDefaultStateDirXDGStateHome(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "freebsd" && runtime.GOOS != "openbsd" {
		t.Skip("XDG_STATE_HOME only applies on Linux/BSD")
	}
	t.Setenv("XDG_STATE_HOME", "/tmp/xdg-test-state")
	got := defaultStateDir()
	want := filepath.Join("/tmp/xdg-test-state", "basecamp", DefaultDirName)
	if got != want {
		t.Errorf("defaultStateDir() = %q, want %q", got, want)
	}
}

func TestDefaultStateDirLinuxFallback(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "freebsd" && runtime.GOOS != "openbsd" {
		t.Skip("Linux fallback only applies on Linux/BSD")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", "")
	got := defaultStateDir()
	want := filepath.Join(home, ".local", "state", "basecamp", DefaultDirName)
	if got != want {
		t.Errorf("defaultStateDir() = %q, want %q", got, want)
	}
}
