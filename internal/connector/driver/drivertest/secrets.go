//go:build unix

package drivertest

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Places are where a secret must not be found. The credential rule (written
// out beside "One owner, one release point" in driver/worker.go) forbids a
// token in a worker's environment, in any argv, in any log, and in any file
// under a working directory or the connector's state directory.
type Places struct {
	// Env is an environment, as KEY=VALUE.
	Env []string
	// Args are a command line.
	Args []string
	// Texts are logs, output lines, anything written.
	Texts []string
	// Dirs are walked, and every regular file in them read, except SQLite
	// databases and their journals (see isDatabaseFile).
	//
	// A directory holding a database this process has open must not be
	// scanned from this process at all: SQLite's POSIX locks belong to the
	// process, and closing any descriptor to the database, its -wal or its
	// -shm drops every one of them, so another process may checkpoint and
	// reset the WAL under the open handle, which then reads stale data or
	// fails with SQLITE_IOERR_SHORT_READ. Skipping those files by name keeps
	// this walk from opening them; a database under another name cannot be
	// recognized without opening it, so a caller that keeps one open under a
	// name of its own runs the scan from a subprocess of its own (card 22
	// does; this package ships no helper for it).
	Dirs []string
}

// RequireNoSecret fails the test wherever secret appears in places.
func RequireNoSecret(t *testing.T, secret string, places Places) {
	t.Helper()
	if secret == "" {
		t.Fatal("RequireNoSecret needs the secret to look for")
	}
	for _, kv := range places.Env {
		if strings.Contains(kv, secret) {
			name, _, _ := strings.Cut(kv, "=")
			t.Errorf("the secret is in the environment, as %s", name)
		}
	}
	for i, arg := range places.Args {
		if strings.Contains(arg, secret) {
			t.Errorf("the secret is in argv[%d]", i)
		}
	}
	for i, text := range places.Texts {
		if strings.Contains(text, secret) {
			t.Errorf("the secret is in written text #%d", i)
		}
	}
	for _, found := range filesContaining(places.Dirs, secret) {
		t.Errorf("the secret is in a file: %s", found)
	}
}

// WatchForSecretFiles watches dirs for any file that carries secret, however
// briefly, from now until the returned stop is called, and stop returns every
// such file it saw. It is the check for a token file that exists for less
// than a second — an owner-only environment file a wrapper deletes once the
// child has read it — which a check made afterwards cannot see. Most tests
// want RequireNoSecretFilesDuring.
func WatchForSecretFiles(secret string, dirs ...string) (stop func() []string) {
	var (
		mu    sync.Mutex
		seen  = map[string]bool{}
		done  = make(chan struct{})
		ended = make(chan struct{})
	)
	go func() {
		defer close(ended)
		ticker := time.NewTicker(5 * time.Millisecond)
		defer ticker.Stop()
		for {
			for _, found := range filesContaining(dirs, secret) {
				mu.Lock()
				seen[found] = true
				mu.Unlock()
			}
			select {
			case <-done:
				return
			case <-ticker.C:
			}
		}
	}()
	var once sync.Once
	var result []string
	return func() []string {
		once.Do(func() {
			close(done)
			<-ended
			mu.Lock()
			defer mu.Unlock()
			for found := range seen {
				result = append(result, found)
			}
		})
		return result
	}
}

// RequireNoSecretFilesDuring fails the test for every file under dirs that
// carried secret at any moment while during ran.
func RequireNoSecretFilesDuring(t *testing.T, secret string, dirs []string, during func()) {
	t.Helper()
	stop := WatchForSecretFiles(secret, dirs...)
	during()
	for _, found := range stop() {
		t.Errorf("a file carried the secret while it was watched: %s", found)
	}
}

func filesContaining(dirs []string, secret string) []string {
	var found []string
	for _, dir := range dirs {
		root, err := os.OpenRoot(dir)
		if err != nil {
			continue
		}
		_ = fs.WalkDir(root.FS(), ".", func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				// A directory that vanished while it was walked holds nothing
				// to find; the watch looks again.
				return nil //nolint:nilerr // a file gone mid-walk is not a finding
			}
			if !entry.Type().IsRegular() || isDatabaseFile(entry.Name()) {
				return nil
			}
			data, readErr := root.ReadFile(path)
			if readErr == nil && len(data) <= 4<<20 && strings.Contains(string(data), secret) {
				found = append(found, filepath.Join(dir, path))
			}
			return nil
		})
		_ = root.Close()
	}
	return found
}

// isDatabaseFile reports a SQLite database or journal by its name. It is told
// by name, never by reading its header: opening and closing a descriptor to a
// database another handle in this process holds drops that handle's locks.
func isDatabaseFile(name string) bool {
	for _, suffix := range []string{".db", ".db-wal", ".db-shm", ".db-journal", ".sqlite", ".sqlite-wal", ".sqlite-shm", ".sqlite-journal", ".sqlite3", ".sqlite3-wal", ".sqlite3-shm", ".sqlite3-journal"} {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}
	return false
}
