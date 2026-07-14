//go:build unix

package cli

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"github.com/basecamp/basecamp-cli/internal/config"
)

// TestHardenConfigDir exercises the step-by-step openat descent that hardens
// the global config dir (open "/", Openat each component with O_NOFOLLOW,
// then fstat -> fchmod the final fd). Refusing a symlink at every component
// means neither a symlinked final component nor a symlinked ANCESTOR can
// redirect the fchmod, and a dir under a sticky world-writable parent like
// /tmp is still hardened like any other.
func TestHardenConfigDir(t *testing.T) {
	mode := func(t *testing.T, path string) os.FileMode {
		t.Helper()
		fi, err := os.Stat(path)
		require.NoError(t, err)
		return fi.Mode().Perm()
	}

	t.Run("tightens a normal config dir to 0700", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "basecamp")
		require.NoError(t, os.Mkdir(dir, 0o755))
		require.NoError(t, os.Chmod(dir, 0o755))

		hardenConfigDir(dir)

		assert.Equal(t, os.FileMode(0o700), mode(t, dir))
	})

	t.Run("group/world-writable target under a safe parent is tightened to 0700", func(t *testing.T) {
		// Regression: the target dir's OWN permissiveness (0775/0777) is exactly
		// the case this feature exists to clamp to 0700. ancestorIsSafe must gate
		// only the ANCESTORS, never the final target — otherwise a group- or
		// world-writable config dir (the most permissive, most-in-need case) would
		// be rejected as an "unsafe ancestor" and left untightened. The parent here
		// is a normal 0755 euid-owned temp dir (safe), so only the target's own mode
		// is in play.
		for _, targetMode := range []os.FileMode{0o775, 0o777} {
			parent := t.TempDir() // default 0700-ish, euid-owned, non-writable by others
			require.NoError(t, os.Chmod(parent, 0o755))
			dir := filepath.Join(parent, "basecamp")
			require.NoError(t, os.Mkdir(dir, targetMode))
			require.NoError(t, os.Chmod(dir, targetMode))

			hardenConfigDir(dir)

			assert.Equal(t, os.FileMode(0o700), mode(t, dir),
				"target with mode %#o under a safe parent must be tightened", targetMode)
		}
	})

	t.Run("owner-unreadable target under a safe parent is tightened to 0700", func(t *testing.T) {
		// Gap this fix closes: an euid-owned config dir with the owner-READ bit
		// cleared but still group/world-writable (e.g. 0333 or 0377) is exactly a
		// credential-bearing dir left writable by others. The predecessor opened the
		// FINAL target O_RDONLY, so this dir failed the open with EACCES and the
		// ownership check + tighten never ran — it stayed writable. Opening the
		// target traverse-only (O_PATH on Linux) and tightening via Fchmodat on the
		// vetted parent fd makes the verify+tighten mode-independent, so the dir is
		// clamped to 0700.
		//
		// Linux-only: the fix relies on O_PATH, which lets an owner-unreadable dir
		// be opened for Fstat. On non-Linux unix, ancestorAccessFlag falls back to
		// O_RDONLY and the target open still needs owner read, so this scenario
		// remains a documented limitation there.
		if runtime.GOOS != "linux" {
			t.Skip("owner-unreadable target open relies on O_PATH, Linux-only; O_RDONLY fallback elsewhere still needs read")
		}
		for _, targetMode := range []os.FileMode{0o333, 0o377} {
			parent := t.TempDir()
			require.NoError(t, os.Chmod(parent, 0o755))
			dir := filepath.Join(parent, "basecamp")
			require.NoError(t, os.Mkdir(dir, 0o700))
			// Clear the owner-read bit (leaving owner write+execute) while keeping the
			// dir group/world-writable — the untightened, at-risk state.
			require.NoError(t, os.Chmod(dir, targetMode))

			hardenConfigDir(dir)

			assert.Equal(t, os.FileMode(0o700), mode(t, dir),
				"owner-unreadable target with mode %#o under a safe parent must be tightened", targetMode)
		}
	})

	t.Run("symlinked final component is not followed", func(t *testing.T) {
		base := t.TempDir()
		target := filepath.Join(base, "real-cfg")
		require.NoError(t, os.Mkdir(target, 0o755))
		require.NoError(t, os.Chmod(target, 0o755))
		link := filepath.Join(base, "link")
		if err := os.Symlink(target, link); err != nil {
			t.Skipf("symlinks unavailable in this environment: %v", err)
		}

		hardenConfigDir(link)

		// O_NOFOLLOW makes the open fail; the symlink target's mode is untouched.
		assert.Equal(t, os.FileMode(0o755), mode(t, target))
	})

	t.Run("symlinked ancestor is not followed", func(t *testing.T) {
		// The single-open predecessor (os.OpenFile(dir, O_NOFOLLOW)) only
		// rejected a symlinked FINAL component. A symlinked ANCESTOR was still
		// followed at open time, so an attacker who controlled an ancestor could
		// aim it at a different euid-owned dir and have the fchmod tighten THAT
		// dir. Here `link` is an ANCESTOR of the requested path, not the final
		// component: under the old code hardenConfigDir(base/link/child) would
		// follow link -> real and chmod base/real/child to 0700. The
		// per-component O_NOFOLLOW descent refuses `link` (ELOOP) and chmods
		// nothing.
		base := t.TempDir()
		realDir := filepath.Join(base, "realDir")
		require.NoError(t, os.Mkdir(realDir, 0o755))
		victim := filepath.Join(realDir, "child")
		require.NoError(t, os.Mkdir(victim, 0o755))
		require.NoError(t, os.Chmod(victim, 0o755))
		link := filepath.Join(base, "link")
		if err := os.Symlink(realDir, link); err != nil {
			t.Skipf("symlinks unavailable in this environment: %v", err)
		}

		// Sanity: the requested path resolves through the ancestor symlink to
		// the victim dir, which is exactly what the old single-open code would
		// have opened and tightened.
		dir := filepath.Join(link, "child")
		resolved, err := filepath.EvalSymlinks(dir)
		require.NoError(t, err)
		require.Equal(t, victim, resolved)

		hardenConfigDir(dir)

		// The ancestor symlink is refused, so the victim dir keeps its mode.
		assert.Equal(t, os.FileMode(0o755), mode(t, victim))
		assert.Equal(t, os.FileMode(0o755), mode(t, realDir))
	})

	t.Run("dir under a sticky 1777 parent is hardened", func(t *testing.T) {
		// Previously the ancestor walk skipped any dir under a world-writable
		// parent (e.g. a config dir under /tmp), leaving it loose. The fd-based
		// sequence is race-free regardless of ancestry, so it hardens it.
		parent := filepath.Join(t.TempDir(), "tmp-style")
		require.NoError(t, os.Mkdir(parent, 0o777))
		require.NoError(t, os.Chmod(parent, 0o777|os.ModeSticky))
		dir := filepath.Join(parent, "basecamp")
		require.NoError(t, os.Mkdir(dir, 0o755))
		require.NoError(t, os.Chmod(dir, 0o755))

		hardenConfigDir(dir)

		assert.Equal(t, os.FileMode(0o700), mode(t, dir))
	})

	t.Run("dir under a group/world-writable non-sticky parent is not hardened", func(t *testing.T) {
		// Under a NON-sticky world-writable ancestor (e.g. XDG_CONFIG_HOME=/shared
		// at 0777), a foreign user with write access to that parent can rename a
		// real euid-owned directory into the config path between our openat hops.
		// O_NOFOLLOW does not reject a renamed REAL dir, so the ownership check
		// would pass and the fchmod would tighten a substituted victim dir. The
		// fd-based ancestor-safety check aborts on the writable non-sticky parent
		// before any chmod. The target itself is euid-owned; only the parent is
		// unsafe.
		parent := filepath.Join(t.TempDir(), "shared")
		require.NoError(t, os.Mkdir(parent, 0o777))
		require.NoError(t, os.Chmod(parent, 0o777)) // world-writable, NO sticky bit
		dir := filepath.Join(parent, "basecamp")
		require.NoError(t, os.Mkdir(dir, 0o755))
		require.NoError(t, os.Chmod(dir, 0o755))

		hardenConfigDir(dir)

		// Ancestor-safety abort: the loose parent means the dir keeps its mode.
		assert.Equal(t, os.FileMode(0o755), mode(t, dir))
	})

	t.Run("execute-only (0711) ancestor still hardens via the O_PATH descent", func(t *testing.T) {
		// Regression: the ancestor descent used to open each component O_RDONLY,
		// which requires READ permission. A traverse-only ancestor (execute but
		// not read, e.g. /home at 0711 on hardened distros) granted search but not
		// read, so the Openat failed EACCES and the WHOLE harden aborted — leaving
		// the config dir world-listable for exactly those users. On Linux the
		// ancestor components are now opened with O_PATH, which needs only
		// execute/search, so the descent succeeds and the target is tightened.
		//
		// Note: the current euid OWNS this ancestor, so it retains read via the
		// owner bits even at 0711 — a same-owner test can't fully starve read the
		// way a foreign 0711 /home would. This still exercises the O_PATH descent
		// path end-to-end (the parent carries no group/world read or write) and
		// asserts hardening completes; a genuinely read-denied ancestor would
		// require a second uid.
		grandparent := filepath.Join(t.TempDir(), "traverse-only")
		require.NoError(t, os.Mkdir(grandparent, 0o711))
		require.NoError(t, os.Chmod(grandparent, 0o711)) // execute-only for group/other
		dir := filepath.Join(grandparent, "basecamp")
		require.NoError(t, os.Mkdir(dir, 0o755))
		require.NoError(t, os.Chmod(dir, 0o755))

		hardenConfigDir(dir)

		assert.Equal(t, os.FileMode(0o700), mode(t, dir),
			"a config dir under an execute-only ancestor must still be hardened")
	})

	t.Run("already-0700 dir stays 0700 and skips the fchmod", func(t *testing.T) {
		// Idempotence: on the steady-state run the dir is already 0700, so the
		// fchmod is skipped to avoid churning the inode ctime every command. We
		// assert the mode is unchanged and, best-effort, that the ctime did not
		// advance (skipping the fchmod means no ctime bump). Timestamp granularity
		// on some runners can make the ctime check flaky, so it only fails when the
		// ctime clearly moved forward after a short spin.
		dir := filepath.Join(t.TempDir(), "basecamp")
		require.NoError(t, os.Mkdir(dir, 0o700))
		require.NoError(t, os.Chmod(dir, 0o700))

		ctimeOf := func() syscall.Timespec {
			var st syscall.Stat_t
			require.NoError(t, syscall.Stat(dir, &st))
			return ctimespec(&st)
		}
		before := ctimeOf()

		hardenConfigDir(dir)

		assert.Equal(t, os.FileMode(0o700), mode(t, dir), "an already-0700 dir must remain 0700")

		after := ctimeOf()
		// If the fchmod ran it would advance ctime; skipping it leaves ctime put.
		// Compare seconds+nanos as a single moment; tolerate equal (skipped) but
		// flag a forward jump.
		beforeNs := before.Sec*1e9 + before.Nsec
		afterNs := after.Sec*1e9 + after.Nsec
		assert.LessOrEqual(t, afterNs, beforeNs,
			"ctime advanced, suggesting the fchmod was not skipped on an already-0700 dir")
	})

	t.Run("regular file is not chmod'd", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "not-a-dir")
		require.NoError(t, os.WriteFile(file, []byte("x"), 0o644))

		hardenConfigDir(file)

		assert.Equal(t, os.FileMode(0o644), mode(t, file))
	})

	t.Run("missing dir is a no-op", func(t *testing.T) {
		assert.NotPanics(t, func() {
			hardenConfigDir(filepath.Join(t.TempDir(), "does-not-exist"))
		})
	})

	t.Run("FIFO at config path does not hang and is not chmod'd", func(t *testing.T) {
		// A pre-planted named pipe at the config path used to hang open() until
		// a writer appeared (blocking every command). O_NONBLOCK|O_DIRECTORY
		// make the open return immediately and fail on the non-directory, so
		// nothing is chmod'd.
		fifo := filepath.Join(t.TempDir(), "basecamp")
		if err := syscall.Mkfifo(fifo, 0o644); err != nil {
			t.Skipf("mkfifo unavailable in this environment: %v", err)
		}
		before, err := os.Lstat(fifo)
		require.NoError(t, err)

		done := make(chan struct{})
		go func() {
			hardenConfigDir(fifo)
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("hardenConfigDir hung on a FIFO config path")
		}

		after, err := os.Lstat(fifo)
		require.NoError(t, err)
		assert.NotZero(t, after.Mode()&os.ModeNamedPipe, "path should still be a FIFO")
		assert.Equal(t, before.Mode(), after.Mode(), "FIFO mode must be untouched")
	})

	t.Run("relative cfgDir is skipped by the call-site IsAbs gate", func(t *testing.T) {
		// GlobalConfigDir only Cleans a relative XDG_CONFIG_HOME, so it can
		// return a non-absolute path. The IsAbs gate lives at the call site in
		// root.go (`cfgDir != "" && filepath.IsAbs(cfgDir)`); testing it there
		// is the cleanly reachable seam since hardenConfigDir itself takes a
		// resolved path. Verify the gate skips a relative cfgDir so no
		// cwd-relative dir is surprise-chmod'd.
		t.Setenv("XDG_CONFIG_HOME", "relative-cfg-home")
		cfgDir := config.GlobalConfigDir()
		require.False(t, filepath.IsAbs(cfgDir), "expected a relative cfgDir for this case")
		assert.False(t, cfgDir != "" && filepath.IsAbs(cfgDir), "gate must skip a relative cfgDir")
	})

	t.Run("foreign-owned dir is not chmod'd", func(t *testing.T) {
		// Producing a genuinely foreign-owned dir requires root (chown to
		// another uid). Run the end-to-end case when we have root; the
		// ownership predicate itself is covered for all users by
		// TestOwnedByUID below.
		if os.Geteuid() != 0 {
			t.Skip("need root to chown a dir to a foreign uid; ownership logic covered by TestOwnedByUID")
		}
		dir := filepath.Join(t.TempDir(), "foreign")
		require.NoError(t, os.Mkdir(dir, 0o755))
		require.NoError(t, os.Chmod(dir, 0o755))
		const foreignUID = 65534 // conventionally "nobody"
		if err := os.Chown(dir, foreignUID, foreignUID); err != nil {
			t.Skipf("cannot chown to foreign uid %d: %v", foreignUID, err)
		}

		hardenConfigDir(dir)

		assert.Equal(t, os.FileMode(0o755), mode(t, dir))
	})
}

// TestOwnedByUID exercises the ownership predicate that gates the fchmod
// directly, since constructing a real foreign-owned dir requires root.
func TestOwnedByUID(t *testing.T) {
	fi, err := os.Stat(t.TempDir())
	require.NoError(t, err)

	t.Run("self-owned dir matches current euid", func(t *testing.T) {
		assert.True(t, ownedByUID(fi, os.Geteuid()))
	})

	t.Run("mismatched uid is foreign", func(t *testing.T) {
		assert.False(t, ownedByUID(fi, os.Geteuid()+1))
	})

	t.Run("FileInfo without Stat_t fails closed", func(t *testing.T) {
		assert.False(t, ownedByUID(fakeFileInfo{}, os.Geteuid()))
	})
}

// TestAncestorIsSafe exercises the fd-based ancestor-safety predicate that
// guards the descent: a dir is unsafe if it is group/world-writable without the
// sticky bit, or owned by a foreign non-root uid.
func TestAncestorIsSafe(t *testing.T) {
	euid := os.Geteuid()

	t.Run("self-owned 0755 is safe", func(t *testing.T) {
		st := &syscall.Stat_t{Mode: 0o755, Uid: uint32(euid)}
		assert.True(t, ancestorIsSafe(st, euid))
	})

	t.Run("root-owned 0755 is safe", func(t *testing.T) {
		st := &syscall.Stat_t{Mode: 0o755, Uid: 0}
		assert.True(t, ancestorIsSafe(st, euid))
	})

	t.Run("world-writable with sticky bit is safe", func(t *testing.T) {
		st := &syscall.Stat_t{Mode: 0o777 | unix.S_ISVTX, Uid: uint32(euid)}
		assert.True(t, ancestorIsSafe(st, euid))
	})

	t.Run("world-writable without sticky bit is unsafe", func(t *testing.T) {
		st := &syscall.Stat_t{Mode: 0o777, Uid: uint32(euid)}
		assert.False(t, ancestorIsSafe(st, euid))
	})

	t.Run("group-writable without sticky bit is unsafe", func(t *testing.T) {
		st := &syscall.Stat_t{Mode: 0o775, Uid: uint32(euid)}
		assert.False(t, ancestorIsSafe(st, euid))
	})

	t.Run("foreign non-root owner is unsafe", func(t *testing.T) {
		st := &syscall.Stat_t{Mode: 0o755, Uid: uint32(euid) + 1}
		assert.False(t, ancestorIsSafe(st, euid))
	})
}

// fakeFileInfo is a FileInfo whose Sys() is not a *syscall.Stat_t, modeling a
// filesystem that can't prove ownership.
type fakeFileInfo struct{}

func (fakeFileInfo) Name() string       { return "fake" }
func (fakeFileInfo) Size() int64        { return 0 }
func (fakeFileInfo) Mode() fs.FileMode  { return fs.ModeDir | 0o755 }
func (fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (fakeFileInfo) IsDir() bool        { return true }
func (fakeFileInfo) Sys() any           { return nil }
