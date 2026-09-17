//go:build unix

package drivertest

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
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

// Card 22: SQLite's locks are the process's, and closing any descriptor to a
// database drops them. A scan of a state directory must not open the ledger
// this process holds, or another process may reset its WAL underneath it.
func TestTheScanLeavesADatabaseThisProcessHoldsLocked(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 checks the lock from another process")
	}
	dir := t.TempDir()
	for _, name := range []string{"ledger.db", "ledger.db-wal", "ledger.db-shm"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("test-token-not-real"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	db, err := os.OpenFile(filepath.Join(dir, "ledger.db"), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	lock := syscall.Flock_t{Type: syscall.F_WRLCK, Whence: 0, Start: 0, Len: 0}
	if err := syscall.FcntlFlock(db.Fd(), syscall.F_SETLK, &lock); err != nil {
		t.Fatal(err)
	}

	RequireNoSecret(t, "test-token-not-real", Places{Dirs: []string{dir}})
	if found := WatchForSecretFiles("test-token-not-real", dir); len(found()) != 0 {
		t.Error("a database file was read")
	}

	probe := exec.Command(python, "-c", "import fcntl,sys\nf=open(sys.argv[1],'r+')\ntry:\n  fcntl.lockf(f, fcntl.LOCK_EX|fcntl.LOCK_NB)\nexcept OSError:\n  sys.exit(3)\n", filepath.Join(dir, "ledger.db"))
	err = probe.Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 3 {
		t.Fatalf("another process could lock the database this one holds: the scan dropped its lock (%v)", err)
	}
}

// Files that are not databases are still read.
func TestTheScanStillReadsFilesThatAreNotDatabases(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ledger.db.json")
	if err := os.WriteFile(path, []byte("test-token-not-real"), 0o600); err != nil {
		t.Fatal(err)
	}
	if found := filesContaining([]string{dir}, "test-token-not-real"); len(found) != 1 || found[0] != path {
		t.Fatalf("a file that is not a database was skipped: %v", found)
	}
}
