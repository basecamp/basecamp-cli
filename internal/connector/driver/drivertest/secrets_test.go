//go:build unix

package drivertest

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The watcher sees a token file that exists for a few milliseconds — card
// 19's case, an env file a wrapper deletes as soon as its child reads it.
func TestTheWatcherSeesATokenFileThatLivesMilliseconds(t *testing.T) {
	dir := t.TempDir()
	stop := WatchForSecretFiles("test-token-not-real", dir)
	path := filepath.Join(dir, "env")
	if err := os.WriteFile(path, []byte("BASECAMP_CONNECT_TASK_TOKEN=test-token-not-real\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	_ = os.Remove(path)
	if found := stop(); len(found) != 1 || found[0] != path {
		t.Fatalf("a token file that lived 50ms was not seen: %v", found)
	}
}
