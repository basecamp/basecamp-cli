package appctx

import (
	"testing"

	"github.com/basecamp/basecamp-cli/internal/config"
	"github.com/basecamp/basecamp-cli/internal/resilience"
)

func TestResolveResilienceDirDefault(t *testing.T) {
	cfg := &config.Config{
		CacheDir: "/home/user/.cache/basecamp",
		Sources:  map[string]string{},
	}
	got := resolveResilienceDir(cfg)
	if got != "" {
		t.Errorf("resolveResilienceDir() = %q, want empty (delegate to defaultStateDir)", got)
	}
}

func TestResolveResilienceDirExplicit(t *testing.T) {
	cfg := &config.Config{
		CacheDir: "/custom/cache",
		Sources:  map[string]string{"cache_dir": "flag"},
	}
	got := resolveResilienceDir(cfg)
	want := "/custom/cache/" + resilience.DefaultDirName
	if got != want {
		t.Errorf("resolveResilienceDir() = %q, want %q", got, want)
	}
}
