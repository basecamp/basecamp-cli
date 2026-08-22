//go:build unix

package cli

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// hardenConfigDir tightens dir to 0700 using a step-by-step openat descent that
// refuses a symlink at EVERY path component. Starting from an fd on "/", it
// walks the path one component at a time with
// Openat(parentFd, comp, ancestorAccessFlag|O_DIRECTORY|O_NOFOLLOW|O_NONBLOCK|O_CLOEXEC):
//
//   - EVERY component — ancestors (root "/", every intermediate dir, the
//     immediate parent) AND the final target — is opened traverse-only:
//     ancestorAccessFlag is unix.O_PATH on Linux and O_RDONLY on other unix
//     GOOSes. O_PATH needs only execute/search permission, not read, so an
//     execute-only ancestor (e.g. /home at 0711 on hardened distros) can still
//     be descended — with O_RDONLY it would fail EACCES and abort the whole
//     harden, leaving the config dir world-listable for exactly those users. It
//     also makes the final-target verify MODE-INDEPENDENT: an owner-unreadable
//     but euid-owned config dir (owner-read bit cleared but still group/world-
//     writable, e.g. 0333/0377) can still be Fstat'd and tightened, whereas an
//     O_RDONLY target open would fail EACCES and leave that credential-bearing
//     dir writable by others. An O_PATH fd is still valid as the next Openat
//     dirfd and can be Fstat'd for the ancestorIsSafe vetting. O_PATH|O_NOFOLLOW
//     still refuses a symlinked component. An O_PATH fd cannot be Fchmod'd, so
//     the target's tighten is done via Fchmodat by NAME on the still-open,
//     ancestor-vetted parent fd (see below).
//   - O_NOFOLLOW on each hop makes a symlinked component fail with ELOOP. A
//     single os.OpenFile(dir, O_NOFOLLOW) only rejects a symlinked FINAL
//     component; a symlinked ANCESTOR in an attacker-writable location (e.g.
//     XDG_CONFIG_HOME=/tmp/attacker/basecamp where the attacker owns
//     /tmp/attacker) is still followed at open time, letting the attacker aim
//     an ancestor at a different euid-owned dir and have the fchmod tighten
//     THAT dir. Refusing a symlink at every hop closes that vector: no ancestor
//     can redirect the final fd.
//   - O_DIRECTORY makes every component fail unless it is a real directory
//     (also rejects a FIFO or regular file planted anywhere on the path).
//   - O_NONBLOCK guarantees the open never blocks — without it, a pre-planted
//     named pipe on the path would hang until a writer appeared, stalling every
//     command. O_CLOEXEC is hygiene so a transient fd can't leak across an exec.
//
// ancestorIsSafe applies to the ANCESTORS only — the root "/", every
// intermediate directory, AND the config dir's immediate PARENT — but NEVER to
// the final target itself. As each ancestor fd opens it is fstat'd and run
// through ancestorIsSafe BEFORE its fd is closed. This closes a residual
// rename-swap vector that O_NOFOLLOW alone does not: under a group/world-writable
// NON-sticky ancestor (e.g. XDG_CONFIG_HOME=/shared at 0777), a foreign user with
// write access to that parent can rename() a real euid-owned directory into the
// config path between our openat hops. O_NOFOLLOW does not reject a renamed REAL
// directory, so without this check the ownership test would pass and the fchmod
// would tighten the substituted victim-owned dir. The check aborts (no chmod) on
// any ancestor fd whose directory is either group/world-writable without the
// sticky bit, or owned by a foreign non-root uid. A sticky world-writable parent
// like /tmp (1777) stays SAFE — the sticky bit prevents renaming or deleting
// others' entries — so a config dir under /tmp is still hardened. Root-owned
// (uid 0) and self-owned (euid) ancestors are trusted system directories. Because
// every check runs on an already-open fd reached without following any symlink,
// there is no path re-resolution and thus no TOCTOU window. Vetting the immediate
// PARENT is what defeats the rename-swap: a safe parent cannot have a foreign
// entry renamed into the target slot.
//
// The final target is NOT run through ancestorIsSafe: its own permissiveness is
// precisely the thing being tightened (a config dir left at 0775/0777 is the
// case this feature exists to clamp to 0700), so rejecting it for being writable
// would defeat the purpose. It is opened traverse-only (O_PATH on Linux) with the
// same O_NOFOLLOW|O_DIRECTORY flags (still no symlink, still a real dir) and gated
// ONLY on: Fstat succeeds, it is a directory, and it is owned by the current
// effective user (ancestors may be root-owned; the target must be ours). Opening
// traverse-only rather than O_RDONLY means an owner-unreadable target can still be
// Fstat'd and verified. The tighten is skipped entirely when the dir is already
// 0700 (the steady state) so we don't churn the inode ctime or spend a syscall on
// every command run; otherwise it is done via Fchmodat by name on the vetted
// parent fd — an O_PATH fd cannot be Fchmod'd directly. Fchmodat-by-name is safe
// because the parent is ancestor-vetted (no attacker can rename within it) and the
// target was opened O_NOFOLLOW (not a symlink), so the name resolves to exactly the
// inode we Fstat'd. Fchmodat uses flags 0, NOT AT_SYMLINK_NOFOLLOW: Linux's
// fchmodat(2) rejects AT_SYMLINK_NOFOLLOW for a chmod with ENOTSUP, and the swap
// safety above does not rely on it. Because every check and the chmod resolve
// through fds reached without following any symlink, no path component can be
// swapped in between (no Lstat->Chmod TOCTOU race). Non-Linux caveat: where
// ancestorAccessFlag falls back to O_RDONLY, the target open still needs owner
// read — a documented pre-existing limitation, not addressed here.
//
// Best-effort: ANY failure at ANY step (symlink, FIFO/non-dir, missing dir,
// permission, foreign owner, unsafe ancestor, fchmod error) silently returns
// without chmod, never blocking startup on a perms cleanup. The caller (root.go)
// guarantees dir is absolute; dir == "/" (no components) is skipped, since
// hardening the root is neither needed nor safe.
func hardenConfigDir(dir string) {
	components := splitPathComponents(dir)
	if len(components) == 0 {
		return // dir == "/" or empty: nothing to harden
	}

	// EVERY component — ANCESTORS and the FINAL target alike — is opened
	// traverse-only (ancestorAccessFlag is unix.O_PATH on Linux, O_RDONLY
	// elsewhere) so an execute-only ancestor (e.g. /home at 0711, no read) does
	// not abort the harden, and so an owner-UNREADABLE target (owner-read bit
	// cleared but still group/world-writable, e.g. 0333/0377) can still be
	// Fstat'd and tightened rather than failing the open with EACCES. O_NOFOLLOW
	// still refuses a symlinked component and O_DIRECTORY a non-dir; an O_PATH fd
	// is still usable as the next Openat dirfd and can be Fstat'd for the
	// ancestorIsSafe vetting. The target's tighten cannot use Fchmod on its own
	// O_PATH fd (O_PATH fds cannot Fchmod), so it is done via Fchmodat by name on
	// the still-open, vetted parent fd (see below).
	const ancestorFlags = ancestorAccessFlag | syscall.O_DIRECTORY | syscall.O_NOFOLLOW | syscall.O_NONBLOCK | syscall.O_CLOEXEC

	euid := os.Geteuid()

	fd, err := syscall.Open("/", ancestorFlags, 0)
	if err != nil {
		return
	}
	// The root "/" fd counts as an ancestor: verify it before descending.
	var st syscall.Stat_t
	if err := syscall.Fstat(fd, &st); err != nil || !ancestorIsSafe(&st, euid) {
		_ = syscall.Close(fd) //nolint:errcheck // read-only fd; nothing to flush
		return
	}

	// Descend one component at a time through the ANCESTORS ONLY
	// (components[:len(components)-1]): every element except the final target,
	// which includes the config dir's immediate parent. Each hop opens the child
	// relative to the current dir fd, then closes the parent. A symlinked (ELOOP)
	// or non-dir (ENOTDIR) component fails the Openat and aborts with no chmod.
	// Every opened ancestor fd is fstat'd and vetted by ancestorIsSafe before it
	// is closed, so a directory renamed into the path under a mutable parent is
	// rejected before the fchmod. The final target is opened after the loop and
	// is deliberately NOT run through ancestorIsSafe (see the doc comment).
	for _, comp := range components[:len(components)-1] {
		// unix.Openat (not syscall.Openat, which the stdlib exports only on
		// linux) keeps this portable across every unix GOOS. golang.org/x/sys
		// is already a direct dependency, so this adds no new module.
		next, err := unix.Openat(fd, comp, ancestorFlags, 0)
		_ = syscall.Close(fd) //nolint:errcheck // read-only fd; nothing to flush
		if err != nil {
			return
		}
		fd = next
		if err := syscall.Fstat(fd, &st); err != nil || !ancestorIsSafe(&st, euid) {
			_ = syscall.Close(fd) //nolint:errcheck // read-only fd; nothing to flush
			return
		}
	}

	// After the ancestor loop, fd is the config dir's immediate PARENT — the last
	// vetted ancestor. Keep it OPEN until after the chmod: the tighten is done via
	// Fchmodat by name on this fd, not Fchmod on the target's own fd. The
	// single-component edge case (e.g. dir == "/x") lands here directly: the loop
	// above ran zero times and fd is still the vetted root, which IS this target's
	// parent — the same code path works.
	parentFd := fd
	defer syscall.Close(parentFd) //nolint:errcheck // read-only fd; nothing to flush

	// Open the FINAL component (the target) relative to its vetted parent with
	// the SAME traverse-only flags as the ancestors. Opening traverse-only makes
	// the verify+tighten MODE-INDEPENDENT: an owner-unreadable but euid-owned
	// config dir (owner-read bit cleared, still group/world-writable) can still be
	// Fstat'd and tightened, whereas an O_RDONLY open would fail EACCES and leave
	// that credential-bearing dir writable by others. O_NOFOLLOW keeps a symlinked
	// target out; O_DIRECTORY keeps a FIFO/non-dir out.
	last := components[len(components)-1]
	targetFd, err := unix.Openat(parentFd, last, ancestorFlags, 0)
	if err != nil {
		return
	}
	defer syscall.Close(targetFd) //nolint:errcheck // read-only fd; nothing to flush

	// Fstat works on an O_PATH fd. The target is deliberately NOT run through
	// ancestorIsSafe — its own permissiveness is precisely what we tighten — so
	// gate ONLY on: it is a directory and owned by the current euid (ancestors may
	// be root-owned; the target must be ours).
	if err := syscall.Fstat(targetFd, &st); err != nil {
		return
	}
	if st.Mode&syscall.S_IFMT != syscall.S_IFDIR || st.Uid != uint32(euid) { //nolint:gosec // G115: Geteuid() returns a valid non-negative uid that fits in uint32
		return
	}
	// Idempotent: only chmod when the perm actually differs from 0700. When the
	// dir is already 0700 (the steady state after the first run), skip the syscall
	// so we don't churn the inode ctime on every command invocation.
	if os.FileMode(st.Mode).Perm() == 0o700 {
		return
	}
	// Tighten by NAME on the vetted parent fd, since an O_PATH target fd cannot be
	// Fchmod'd. This is safe against a symlink/rename swap: the parent passed
	// ancestorIsSafe (not writable-by-others without sticky, not foreign-owned) so
	// no attacker can rename within it, and the target was just opened O_NOFOLLOW
	// confirming it is not a symlink — so Fchmodat-by-name hits exactly the inode
	// we Fstat'd. Flags are 0, NOT AT_SYMLINK_NOFOLLOW: Linux's fchmodat(2) rejects
	// AT_SYMLINK_NOFOLLOW for a chmod with ENOTSUP, and the swap safety above does
	// not depend on it.
	//
	// On non-Linux unix, ancestorAccessFlag is O_RDONLY, so the target open above
	// still needs owner read there — a documented pre-existing limitation of the
	// O_RDONLY fallback, not addressed here.
	_ = unix.Fchmodat(parentFd, last, 0o700, 0) //nolint:gosec // G302: 0700 is correct for a directory (needs the execute bit) that can hold credentials.json
}

// ancestorIsSafe reports whether a directory (identified by st) on the config
// path is safe to descend through toward an fchmod. A directory is UNSAFE and
// must abort the harden if it is:
//
//   - group- or world-writable without the sticky bit (st.Mode&0o022 != 0 &&
//     st.Mode&unix.S_ISVTX == 0): a foreign user could rename a real euid-owned
//     directory into the path here, and O_NOFOLLOW would not reject the swap. A
//     sticky world-writable dir (e.g. /tmp at 1777) is SAFE because the sticky
//     bit forbids renaming or deleting entries owned by others.
//   - owned by a foreign non-root uid (st.Uid != euid && st.Uid != 0): its owner
//     could perform the same swap. Root-owned (uid 0) and self-owned (euid)
//     directories are trusted.
func ancestorIsSafe(st *syscall.Stat_t, euid int) bool {
	if st.Mode&0o022 != 0 && st.Mode&unix.S_ISVTX == 0 {
		return false
	}
	if st.Uid != uint32(euid) && st.Uid != 0 { //nolint:gosec // G115: Geteuid() returns a valid non-negative uid that fits in uint32
		return false
	}
	return true
}

// splitPathComponents splits an absolute path into its non-empty components
// after cleaning it, so the descent can open each one in turn. The leading
// empty element from the root "/" (and any redundant separators) is dropped;
// "/" yields no components.
func splitPathComponents(dir string) []string {
	parts := strings.Split(filepath.Clean(dir), "/")
	components := parts[:0]
	for _, p := range parts {
		if p != "" {
			components = append(components, p)
		}
	}
	return components
}

// ownedByUID reports whether fi is owned by uid. A FileInfo whose Sys() is
// not a *syscall.Stat_t cannot prove ownership, so it is treated as not
// owned (fail closed).
func ownedByUID(fi fs.FileInfo, uid int) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	return st.Uid == uint32(uid) //nolint:gosec // G115: Geteuid() returns a valid non-negative uid that fits in uint32
}
