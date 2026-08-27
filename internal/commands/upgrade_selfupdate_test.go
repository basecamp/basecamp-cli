package commands

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gofrs/flock"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/output"
	"github.com/basecamp/basecamp-cli/internal/version"
)

// --- seam stubs ---

func stubGoInstallChecker(t *testing.T, isGoInstall bool) {
	t.Helper()
	orig := goInstallChecker
	goInstallChecker = func() bool { return isGoInstall }
	t.Cleanup(func() { goInstallChecker = orig })
}

func stubSelfUpdateTarget(t *testing.T, path string, err error) {
	t.Helper()
	orig := selfUpdateTargetResolver
	selfUpdateTargetResolver = func() (string, error) { return path, err }
	t.Cleanup(func() { selfUpdateTargetResolver = orig })
}

func stubBinaryVersionProber(t *testing.T, probe func(context.Context, string) (string, error)) {
	t.Helper()
	orig := binaryVersionProber
	binaryVersionProber = probe
	t.Cleanup(func() { binaryVersionProber = orig })
}

func stubBundleVerifier(t *testing.T, verify func(checksums, bundleBytes []byte, ver string) error) {
	t.Helper()
	orig := bundleVerifier
	bundleVerifier = verify
	t.Cleanup(func() { bundleVerifier = orig })
}

func stubEuid(t *testing.T, uid int) {
	t.Helper()
	orig := euidResolver
	euidResolver = func() int { return uid }
	t.Cleanup(func() { euidResolver = orig })
}

func stubHomeDir(t *testing.T, home string) {
	t.Helper()
	orig := homeDirResolver
	homeDirResolver = func() (string, error) { return home, nil }
	t.Cleanup(func() { homeDirResolver = orig })
}

func stubLinkFile(t *testing.T, link func(oldname, newname string) error) {
	t.Helper()
	orig := linkFile
	linkFile = link
	t.Cleanup(func() { linkFile = orig })
}

func stubRenameFile(t *testing.T, rename func(oldpath, newpath string) error) {
	t.Helper()
	orig := renameFile
	renameFile = rename
	t.Cleanup(func() { renameFile = orig })
}

func stubBrewPrefixResolver(t *testing.T, resolve func(context.Context) (string, error)) {
	t.Helper()
	orig := brewPrefixResolver
	brewPrefixResolver = resolve
	t.Cleanup(func() { brewPrefixResolver = orig })
}

func stubVersion(t *testing.T, v string) {
	t.Helper()
	orig := version.Version
	version.Version = v
	t.Cleanup(func() { version.Version = orig })
}

// requireUpgradeError asserts the command failed the success/exit contract
// way: structured error with the expected code and a nonzero exit code.
func requireUpgradeError(t *testing.T, err error, code string) *output.Error {
	t.Helper()
	require.Error(t, err)
	apiErr := output.AsError(err)
	assert.Equal(t, code, apiErr.Code)
	assert.NotZero(t, apiErr.ExitCode())
	return apiErr
}

// --- archive builders ---

type tarEntry struct {
	name     string
	body     []byte
	typeflag byte
	linkname string
}

func buildTarGz(t *testing.T, entries []tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		typeflag := e.typeflag
		if typeflag == 0 {
			typeflag = tar.TypeReg
		}
		hdr := &tar.Header{
			Name:     e.name,
			Mode:     0o755,
			Size:     int64(len(e.body)),
			Typeflag: typeflag,
			Linkname: e.linkname,
		}
		require.NoError(t, tw.WriteHeader(hdr))
		if typeflag == tar.TypeReg {
			_, err := tw.Write(e.body)
			require.NoError(t, err)
		}
	}
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())
	return buf.Bytes()
}

type zipEntry struct {
	name    string
	body    []byte
	symlink bool
}

func buildZip(t *testing.T, entries []zipEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		hdr := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		if e.symlink {
			hdr.SetMode(os.ModeSymlink | 0o777)
		} else {
			hdr.SetMode(0o755)
		}
		w, err := zw.CreateHeader(hdr)
		require.NoError(t, err)
		_, err = w.Write(e.body)
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

func writeArchiveFile(t *testing.T, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "archive")
	require.NoError(t, os.WriteFile(path, data, 0o600))
	return path
}

// --- flow fixture ---

// nativeFlowFixture wires every seam for an end-to-end runUpgrade exercise of
// the native self-update path against an httptest release server.
type nativeFlowFixture struct {
	target     string // installed fake binary
	oldContent []byte
	newContent []byte
	latest     string
	server     *httptest.Server
	hits       atomic.Int32
	release    releaseInfo
}

func setupNativeFlow(t *testing.T, current, latest string) *nativeFlowFixture {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("native flow fixture builds tar.gz archives (unix asset shape)")
	}

	f := &nativeFlowFixture{
		latest:     latest,
		oldContent: []byte("old-binary-" + current),
		newContent: []byte("new-binary-" + latest),
	}

	stubVersion(t, current)
	stubGoInstallChecker(t, false)
	stubEuid(t, 1000)

	home := t.TempDir()
	binDir := filepath.Join(home, "bin")
	require.NoError(t, os.MkdirAll(binDir, 0o755))
	f.target = filepath.Join(binDir, "basecamp")
	require.NoError(t, os.WriteFile(f.target, f.oldContent, 0o755))
	stubHomeDir(t, home)
	stubSelfUpdateTarget(t, f.target, nil)

	archiveName := fmt.Sprintf("basecamp_%s_%s_%s.tar.gz", latest, runtime.GOOS, runtime.GOARCH)
	archive := buildTarGz(t, []tarEntry{{name: "basecamp", body: f.newContent}})
	sum := sha256.Sum256(archive)
	checksums := []byte(hex.EncodeToString(sum[:]) + "  " + archiveName + "\n")
	bundleBytes := []byte("stub-bundle")

	mux := http.NewServeMux()
	serve := func(name string, data []byte) {
		mux.HandleFunc("/"+name, func(w http.ResponseWriter, r *http.Request) {
			f.hits.Add(1)
			_, _ = w.Write(data)
		})
	}
	serve(archiveName, archive)
	serve("checksums.txt", checksums)
	serve("checksums.txt.bundle", bundleBytes)
	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)

	f.release = releaseInfo{
		Version: latest,
		Assets: []releaseAsset{
			{Name: archiveName, DownloadURL: f.server.URL + "/" + archiveName},
			{Name: "checksums.txt", DownloadURL: f.server.URL + "/checksums.txt"},
			{Name: "checksums.txt.bundle", DownloadURL: f.server.URL + "/checksums.txt.bundle"},
		},
	}
	stubUpgradeCheckers(t, upgradeCheckersStub{release: &f.release})

	stubBundleVerifier(t, func(_, _ []byte, _ string) error { return nil })

	// Staged and installed binaries both report the new version by default.
	stubBinaryVersionProber(t, func(_ context.Context, path string) (string, error) {
		return latest, nil
	})

	// Confine the flow's MkdirTemp so leftover temp dirs are detectable.
	t.Setenv("TMPDIR", t.TempDir())

	return f
}

func (f *nativeFlowFixture) upgradeTempLeftovers(t *testing.T) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(os.TempDir(), "basecamp-upgrade-*"))
	require.NoError(t, err)
	return matches
}

// --- native flow tests ---

func TestNativeSelfUpdateSuccess(t *testing.T) {
	app, appBuf := setupPeopleTestApp(t)
	f := setupNativeFlow(t, "1.0.0", "1.1.0")

	// Distinctive mode to prove preservation, and a stale sidecar to prove
	// cleanup-adjacent behavior (backup removed on success).
	require.NoError(t, os.Chmod(f.target, 0o700))

	cmdOut, err := executeUpgradeCommand(t, app)
	require.NoError(t, err)

	assert.Contains(t, cmdOut, "update available: 1.1.0")
	assert.Contains(t, cmdOut, "Upgraded 1.0.0 → 1.1.0")
	assert.Contains(t, appBuf.String(), `"upgraded"`)
	assert.Contains(t, appBuf.String(), `"native"`)

	installed, readErr := os.ReadFile(f.target)
	require.NoError(t, readErr)
	assert.Equal(t, f.newContent, installed)

	info, statErr := os.Stat(f.target)
	require.NoError(t, statErr)
	assert.Equal(t, os.FileMode(0o700), info.Mode().Perm())

	// No staging or backup sidecars remain next to the binary.
	leftovers, globErr := filepath.Glob(filepath.Join(filepath.Dir(f.target), ".basecamp*"))
	require.NoError(t, globErr)
	assert.Empty(t, leftovers)
	assert.Empty(t, f.upgradeTempLeftovers(t))
}

func TestNativeSelfUpdateChecksumMismatch(t *testing.T) {
	app, _ := setupPeopleTestApp(t)
	f := setupNativeFlow(t, "1.0.0", "1.1.0")

	// Corrupt the served checksums: right shape, wrong hash.
	archiveName := f.release.Assets[0].Name
	bad := strings.Repeat("ab", 32) + "  " + archiveName + "\n"
	f.release.Assets[1].DownloadURL = serveBytes(t, []byte(bad))

	_, err := executeUpgradeCommand(t, app)
	apiErr := requireUpgradeError(t, err, "upgrade_failed")
	assert.Contains(t, apiErr.Message, "checksum mismatch")

	assertTargetUntouched(t, f)
}

func TestNativeSelfUpdateChecksumEntryMissing(t *testing.T) {
	app, _ := setupPeopleTestApp(t)
	f := setupNativeFlow(t, "1.0.0", "1.1.0")

	f.release.Assets[1].DownloadURL = serveBytes(t, []byte("deadbeef  some_other_file.tar.gz\n"))

	_, err := executeUpgradeCommand(t, app)
	apiErr := requireUpgradeError(t, err, "upgrade_failed")
	assert.Contains(t, apiErr.Message, "no entry")

	assertTargetUntouched(t, f)
}

func TestNativeSelfUpdateMissingBundleAsset(t *testing.T) {
	app, _ := setupPeopleTestApp(t)
	f := setupNativeFlow(t, "1.0.0", "1.1.0")

	f.release.Assets = f.release.Assets[:2] // drop checksums.txt.bundle

	_, err := executeUpgradeCommand(t, app)
	apiErr := requireUpgradeError(t, err, "upgrade_failed")
	assert.Contains(t, apiErr.Message, "checksums.txt.bundle")

	assertTargetUntouched(t, f)
}

func TestNativeSelfUpdateBundleVerificationFailure(t *testing.T) {
	app, _ := setupPeopleTestApp(t)
	f := setupNativeFlow(t, "1.0.0", "1.1.0")

	stubBundleVerifier(t, func(_, _ []byte, _ string) error {
		return errors.New("no matching certificate identity")
	})

	_, err := executeUpgradeCommand(t, app)
	apiErr := requireUpgradeError(t, err, "upgrade_failed")
	assert.Contains(t, apiErr.Message, "signature verification failed")

	assertTargetUntouched(t, f)
}

func TestNativeSelfUpdateNoPlatformAsset(t *testing.T) {
	app, _ := setupPeopleTestApp(t)
	f := setupNativeFlow(t, "1.0.0", "1.1.0")

	f.release.Assets = f.release.Assets[1:] // drop the platform archive

	cmdOut, err := executeUpgradeCommand(t, app)
	apiErr := requireUpgradeError(t, err, "upgrade_required")
	assert.Contains(t, apiErr.Message, "no prebuilt binary")
	assert.Contains(t, apiErr.Hint, "releases/tag/v1.1.0")
	assert.Contains(t, cmdOut, "releases/tag/v1.1.0")

	assertTargetUntouched(t, f)
}

func TestNativeSelfUpdatePostVerifyMismatchRestoresOldBinary(t *testing.T) {
	app, _ := setupPeopleTestApp(t)
	f := setupNativeFlow(t, "1.0.0", "1.1.0")

	// The staged pre-install probe passes; the installed binary then reports
	// the old version — the swap must be rolled back.
	stubBinaryVersionProber(t, func(_ context.Context, path string) (string, error) {
		if strings.Contains(filepath.Base(path), upgradeStagePrefix) {
			return f.latest, nil
		}
		return "1.0.0", nil
	})

	_, err := executeUpgradeCommand(t, app)
	apiErr := requireUpgradeError(t, err, "upgrade_failed")
	assert.Contains(t, apiErr.Message, "was restored")

	installed, readErr := os.ReadFile(f.target)
	require.NoError(t, readErr)
	assert.Equal(t, f.oldContent, installed)
}

func TestNativeSelfUpdatePreProbeFailureLeavesTarget(t *testing.T) {
	app, _ := setupPeopleTestApp(t)
	f := setupNativeFlow(t, "1.0.0", "1.1.0")

	stubBinaryVersionProber(t, func(_ context.Context, path string) (string, error) {
		if strings.Contains(filepath.Base(path), upgradeStagePrefix) {
			return "", errors.New("exec format error")
		}
		return f.latest, nil
	})

	_, err := executeUpgradeCommand(t, app)
	apiErr := requireUpgradeError(t, err, "upgrade_failed")
	assert.Contains(t, apiErr.Message, "pre-install check")

	assertTargetUntouched(t, f)
}

// assertTargetUntouched verifies the installed binary and its directory
// survived a failed upgrade unchanged, and no temp dirs leaked.
func assertTargetUntouched(t *testing.T, f *nativeFlowFixture) {
	t.Helper()
	installed, err := os.ReadFile(f.target)
	require.NoError(t, err)
	assert.Equal(t, f.oldContent, installed)

	leftovers, err := filepath.Glob(filepath.Join(filepath.Dir(f.target), ".basecamp*"))
	require.NoError(t, err)
	assert.Empty(t, leftovers)
	assert.Empty(t, f.upgradeTempLeftovers(t))
}

// serveBytes spins up a one-asset server and returns its URL.
func serveBytes(t *testing.T, data []byte) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(data)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// --- real-bundle authenticity (hermetic: vendored fixtures, no network) ---

func loadSelfUpdateFixtures(t *testing.T) (trusted root.TrustedMaterial, checksums, bundleBytes []byte) {
	t.Helper()
	trustedRoot, err := root.NewTrustedRootFromPath(filepath.Join("testdata", "selfupdate", "trusted_root.json"))
	require.NoError(t, err)
	checksums, err = os.ReadFile(filepath.Join("testdata", "selfupdate", "checksums.txt"))
	require.NoError(t, err)
	bundleBytes, err = os.ReadFile(filepath.Join("testdata", "selfupdate", "checksums.txt.bundle"))
	require.NoError(t, err)
	return trustedRoot, checksums, bundleBytes
}

func TestVerifyBundleRealReleaseFixtures(t *testing.T) {
	trusted, checksums, bundleBytes := loadSelfUpdateFixtures(t)

	// The published v0.8.1 bundle verifies under the release-tag identity.
	require.NoError(t, verifyBundleWithRoot(trusted, checksums, bundleBytes, "0.8.1"))
}

func TestVerifyBundleRejectsWrongIdentity(t *testing.T) {
	trusted, checksums, bundleBytes := loadSelfUpdateFixtures(t)

	err := verifyBundleWithRoot(trusted, checksums, bundleBytes, "9.9.9")
	require.Error(t, err)
}

func TestVerifyBundleRejectsTamperedArtifact(t *testing.T) {
	trusted, checksums, bundleBytes := loadSelfUpdateFixtures(t)

	tampered := append([]byte("tampered\n"), checksums...)
	err := verifyBundleWithRoot(trusted, tampered, bundleBytes, "0.8.1")
	require.Error(t, err)
}

func TestVerifyBundleRejectsGarbageBundle(t *testing.T) {
	trusted, checksums, _ := loadSelfUpdateFixtures(t)

	err := verifyBundleWithRoot(trusted, checksums, []byte("not a bundle"), "0.8.1")
	require.Error(t, err)
}

// --- path policy ---

func TestSelfUpdateIneligibleAsRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("root check is unix-only")
	}
	stubEuid(t, 0)
	reason, hint := selfUpdateIneligibility("/home/user/bin/basecamp")
	assert.Equal(t, "running_as_root", reason)
	assert.NotEmpty(t, hint)
}

func TestSelfUpdateIneligibleNixStore(t *testing.T) {
	stubEuid(t, 1000)
	reason, hint := selfUpdateIneligibility("/nix/store/abc123-basecamp-cli/bin/basecamp")
	assert.Equal(t, "nix_store", reason)
	assert.Contains(t, hint, "Nix")
}

func TestSelfUpdateIneligibleMiseInstall(t *testing.T) {
	for _, euid := range []int{1000, 0} {
		stubEuid(t, euid)
		reason, hint := selfUpdateIneligibility("/home/user/.local/share/mise/installs/github-basecamp-basecamp-cli/0.9.1/basecamp")
		assert.Equal(t, "mise_install", reason)
		assert.Contains(t, hint, "mise use --global github:basecamp/basecamp-cli@latest")
	}
}

func TestSelfUpdateIneligibleSiblingPrefixHomeEscape(t *testing.T) {
	stubEuid(t, 1000)
	base := t.TempDir()
	home := filepath.Join(base, "jeremy")
	sibling := filepath.Join(base, "jeremyx", "bin")
	require.NoError(t, os.MkdirAll(home, 0o755))
	require.NoError(t, os.MkdirAll(sibling, 0o755))
	target := filepath.Join(sibling, "basecamp")
	require.NoError(t, os.WriteFile(target, []byte("x"), 0o755))
	stubHomeDir(t, home)

	reason, _ := selfUpdateIneligibility(target)
	assert.Equal(t, "system_path", reason)
}

func TestSelfUpdateIneligibleSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation not reliable on windows CI")
	}
	stubEuid(t, 1000)
	base := t.TempDir()
	home := filepath.Join(base, "home")
	outside := filepath.Join(base, "outside")
	require.NoError(t, os.MkdirAll(home, 0o755))
	require.NoError(t, os.MkdirAll(outside, 0o755))
	// ~/bin is a symlink pointing outside the home directory.
	require.NoError(t, os.Symlink(outside, filepath.Join(home, "bin")))
	target := filepath.Join(home, "bin", "basecamp")
	require.NoError(t, os.WriteFile(target, []byte("x"), 0o755))
	stubHomeDir(t, home)

	reason, _ := selfUpdateIneligibility(target)
	assert.Equal(t, "system_path", reason)
}

func TestSelfUpdateIneligibleUnwritableDir(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("permission bits not enforceable here")
	}
	stubEuid(t, 1000)
	home := t.TempDir()
	binDir := filepath.Join(home, "bin")
	require.NoError(t, os.MkdirAll(binDir, 0o755))
	target := filepath.Join(binDir, "basecamp")
	require.NoError(t, os.WriteFile(target, []byte("x"), 0o755))
	require.NoError(t, os.Chmod(binDir, 0o555))
	t.Cleanup(func() { _ = os.Chmod(binDir, 0o755) })
	stubHomeDir(t, home)

	reason, _ := selfUpdateIneligibility(target)
	assert.Equal(t, "not_writable", reason)
}

func TestSelfUpdateEligibleUnderHome(t *testing.T) {
	stubEuid(t, 1000)
	home := t.TempDir()
	binDir := filepath.Join(home, ".local", "bin")
	require.NoError(t, os.MkdirAll(binDir, 0o755))
	target := filepath.Join(binDir, "basecamp")
	require.NoError(t, os.WriteFile(target, []byte("x"), 0o755))
	stubHomeDir(t, home)

	reason, hint := selfUpdateIneligibility(target)
	assert.Empty(t, reason)
	assert.Empty(t, hint)
}

func TestUpgradePolicyFailureSkipsDownload(t *testing.T) {
	app, _ := setupPeopleTestApp(t)
	f := setupNativeFlow(t, "1.0.0", "1.1.0")

	stubEuid(t, 0) // policy fails before any download

	_, err := executeUpgradeCommand(t, app)
	apiErr := requireUpgradeError(t, err, "upgrade_required")
	assert.Contains(t, apiErr.Message, "running_as_root")
	assert.Zero(t, f.hits.Load())
}

func TestUpgradeGoInstallProvenance(t *testing.T) {
	for _, current := range []string{"1.0.0", "0.4.1-0.20260313174735-243815fa23b2"} {
		t.Run(current, func(t *testing.T) {
			app, _ := setupPeopleTestApp(t)
			stubVersion(t, current)
			stubUpgradeCheckers(t, upgradeCheckersStub{latestVersion: "1.1.0"})
			stubGoInstallChecker(t, true)

			_, err := executeUpgradeCommand(t, app)
			apiErr := requireUpgradeError(t, err, "upgrade_required")
			assert.Contains(t, apiErr.Message, "go install")
			assert.Contains(t, apiErr.Hint, "go install github.com/basecamp/basecamp-cli/cmd/basecamp@latest")
		})
	}
}

func TestUpgradeUnresolvableExecutable(t *testing.T) {
	app, _ := setupPeopleTestApp(t)
	stubVersion(t, "1.0.0")
	stubUpgradeCheckers(t, upgradeCheckersStub{latestVersion: "1.1.0"})
	stubGoInstallChecker(t, false)
	stubSelfUpdateTarget(t, "", errors.New("no exe"))

	cmdOut, err := executeUpgradeCommand(t, app)
	apiErr := requireUpgradeError(t, err, "upgrade_required")
	assert.Contains(t, apiErr.Message, "could not be resolved")
	assert.Contains(t, cmdOut, "releases/tag/v1.1.0")
}

// --- swap contract ---

func TestReplaceExecutableUnixPreservesInodeViaHardLink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix swap semantics")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "basecamp")
	staged := filepath.Join(dir, ".basecamp-upgrade-test")
	require.NoError(t, os.WriteFile(target, []byte("old"), 0o755))
	require.NoError(t, os.WriteFile(staged, []byte("new"), 0o755))

	backup, err := replaceExecutable(runtime.GOOS, target, staged)
	require.NoError(t, err)

	installed, _ := os.ReadFile(target)
	assert.Equal(t, []byte("new"), installed)
	preserved, readErr := os.ReadFile(backup)
	require.NoError(t, readErr)
	assert.Equal(t, []byte("old"), preserved)

	// Rollback restores the preserved inode via a single rename-over.
	require.NoError(t, restoreBackup(runtime.GOOS, target, backup))
	restored, _ := os.ReadFile(target)
	assert.Equal(t, []byte("old"), restored)
}

func TestReplaceExecutableUnixHardLinkFallbackCopies(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix swap semantics")
	}
	stubLinkFile(t, func(_, _ string) error { return errors.New("EPERM: links unsupported") })

	dir := t.TempDir()
	target := filepath.Join(dir, "basecamp")
	staged := filepath.Join(dir, ".basecamp-upgrade-test")
	require.NoError(t, os.WriteFile(target, []byte("old"), 0o755))
	require.NoError(t, os.WriteFile(staged, []byte("new"), 0o755))

	backup, err := replaceExecutable(runtime.GOOS, target, staged)
	require.NoError(t, err)

	preserved, readErr := os.ReadFile(backup)
	require.NoError(t, readErr)
	assert.Equal(t, []byte("old"), preserved)
	installed, _ := os.ReadFile(target)
	assert.Equal(t, []byte("new"), installed)
}

func TestReplaceExecutableUnixBackupFailureLeavesTarget(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("permission bits not enforceable here")
	}
	stubLinkFile(t, func(_, _ string) error { return errors.New("EPERM") })

	dir := t.TempDir()
	target := filepath.Join(dir, "basecamp")
	staged := filepath.Join(dir, ".basecamp-upgrade-test")
	require.NoError(t, os.WriteFile(target, []byte("old"), 0o755))
	require.NoError(t, os.WriteFile(staged, []byte("new"), 0o755))
	require.NoError(t, os.Chmod(dir, 0o555)) // backup copy creation fails
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	_, err := replaceExecutable(runtime.GOOS, target, staged)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "preserve current binary")

	installed, _ := os.ReadFile(target)
	assert.Equal(t, []byte("old"), installed)
}

func TestReplaceExecutableUnixRenameFailureKeepsOldBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix swap semantics")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "basecamp")
	staged := filepath.Join(dir, ".basecamp-upgrade-missing") // never created
	require.NoError(t, os.WriteFile(target, []byte("old"), 0o755))

	_, err := replaceExecutable(runtime.GOOS, target, staged)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "install new binary")

	installed, _ := os.ReadFile(target)
	assert.Equal(t, []byte("old"), installed)

	// The failed attempt must not strand its backup sidecar.
	leftovers, _ := filepath.Glob(target + ".old-*")
	assert.Empty(t, leftovers)
}

func TestReplaceExecutableWindowsShuffle(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "basecamp.exe")
	staged := filepath.Join(dir, ".basecamp-upgrade-test.exe")
	require.NoError(t, os.WriteFile(target, []byte("old"), 0o755))
	require.NoError(t, os.WriteFile(staged, []byte("new"), 0o755))

	backup, err := replaceExecutable("windows", target, staged)
	require.NoError(t, err)
	assert.Equal(t, target+".old", backup)

	installed, _ := os.ReadFile(target)
	assert.Equal(t, []byte("new"), installed)
	preserved, _ := os.ReadFile(backup)
	assert.Equal(t, []byte("old"), preserved)

	// Post-probe restore: new moved aside, .old back in place.
	require.NoError(t, restoreBackup("windows", target, backup))
	restored, _ := os.ReadFile(target)
	assert.Equal(t, []byte("old"), restored)
}

// Both the install rename AND the rollback rename fail — the worst case,
// where the target path is left with no executable. The error must be the
// distinct catastrophe carrying the backup path, never the ordinary
// "install failed, original still in place" shape.
func TestReplaceExecutableWindowsDoubleRenameFailureIsCatastrophic(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "basecamp.exe")
	staged := filepath.Join(dir, ".basecamp-upgrade-test.exe")
	require.NoError(t, os.WriteFile(target, []byte("old"), 0o755))
	require.NoError(t, os.WriteFile(staged, []byte("new"), 0o755))

	// First rename (running exe → .old) succeeds; every later rename fails.
	calls := 0
	stubRenameFile(t, func(oldpath, newpath string) error {
		calls++
		if calls == 1 {
			return os.Rename(oldpath, newpath)
		}
		return errors.New("access denied")
	})

	_, err := replaceExecutable("windows", target, staged)
	var cat *swapCatastropheError
	require.ErrorAs(t, err, &cat)
	assert.Equal(t, target+".old", cat.backup)

	// The reported condition is real: the target path has no executable.
	_, statErr := os.Stat(target)
	assert.True(t, os.IsNotExist(statErr))

	// And the caller-facing mapping surfaces the backup path and never claims
	// the previous binary is still installed.
	apiErr := swapFailureError(target, err)
	assert.Equal(t, "upgrade_failed", apiErr.Code)
	assert.Contains(t, apiErr.Message, "may now be missing")
	assert.Contains(t, apiErr.Hint, cat.backup)
	assert.NotContains(t, apiErr.Hint, "left in place")
}

func TestSwapFailureErrorOrdinaryCaseKeepsExistingInstallClaim(t *testing.T) {
	apiErr := swapFailureError("/home/u/bin/basecamp", errors.New("install new binary: disk full"))
	assert.Equal(t, "upgrade_failed", apiErr.Code)
	assert.Contains(t, apiErr.Hint, "left in place")
}

func TestReplaceExecutableWindowsSecondRenameFailureRollsBack(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "basecamp.exe")
	staged := filepath.Join(dir, ".basecamp-upgrade-missing.exe") // never created
	require.NoError(t, os.WriteFile(target, []byte("old"), 0o755))

	_, err := replaceExecutable("windows", target, staged)
	require.Error(t, err)

	// First rename is rolled back: target holds the old binary, no .old left.
	installed, readErr := os.ReadFile(target)
	require.NoError(t, readErr)
	assert.Equal(t, []byte("old"), installed)
	_, statErr := os.Stat(target + ".old")
	assert.True(t, os.IsNotExist(statErr))
}

// A failed update check must honor the structured upgrade contract — a plain
// api_error would read as a generic API failure, not an upgrade outcome.
func TestUpgradeCheckFailureIsStructured(t *testing.T) {
	app, _ := setupPeopleTestApp(t)
	stubVersion(t, "1.0.0")

	orig := releaseFetcher
	releaseFetcher = func() (releaseInfo, error) { return releaseInfo{}, errors.New("unexpected status: 403") }
	t.Cleanup(func() { releaseFetcher = orig })

	_, err := executeUpgradeCommand(t, app)
	apiErr := requireUpgradeError(t, err, "upgrade_failed")
	assert.Contains(t, apiErr.Message, "could not check for updates")
	assert.Contains(t, apiErr.Hint, "GITHUB_TOKEN")
}

// When rollback fails, the backup is the user's only good binary. It must be
// moved out of the sidecar-reap namespace (`.recovered`), referenced by the
// failure hint, and survive a subsequent startup cleanup.
func TestPostProbeRestoreFailurePreservesRecoveryArtifact(t *testing.T) {
	app, _ := setupPeopleTestApp(t)
	f := setupNativeFlow(t, "1.0.0", "1.1.0")

	stubBinaryVersionProber(t, func(_ context.Context, path string) (string, error) {
		if strings.Contains(filepath.Base(path), upgradeStagePrefix) {
			return f.latest, nil
		}
		return "1.0.0", nil
	})
	// The rollback rename (backup → target) fails; every other rename is real.
	stubRenameFile(t, func(oldpath, newpath string) error {
		if strings.Contains(oldpath, ".old-") && newpath == f.target {
			return errors.New("permission denied")
		}
		return os.Rename(oldpath, newpath)
	})

	_, err := executeUpgradeCommand(t, app)
	apiErr := requireUpgradeError(t, err, "upgrade_failed")

	matches, globErr := filepath.Glob(f.target + ".recovered-*")
	require.NoError(t, globErr)
	require.Len(t, matches, 1)
	recovered := matches[0]
	assert.Contains(t, apiErr.Hint, recovered)
	data, readErr := os.ReadFile(recovered)
	require.NoError(t, readErr)
	assert.Equal(t, f.oldContent, data)

	cleanupUpgradeSidecarsFor(f.target)
	_, statErr := os.Stat(recovered)
	assert.NoError(t, statErr, "recovery artifact must survive startup sidecar cleanup")
}

// Worst case within the worst case: the rollback rename fails AND the
// preservation rename fails, leaving the backup at its `.old-*` name. The
// hint must reference that surviving path, and cleanup must leave it alone.
func TestPostProbeRestoreAndPreserveFailureKeepsOldBackupSafe(t *testing.T) {
	app, _ := setupPeopleTestApp(t)
	f := setupNativeFlow(t, "1.0.0", "1.1.0")

	stubBinaryVersionProber(t, func(_ context.Context, path string) (string, error) {
		if strings.Contains(filepath.Base(path), upgradeStagePrefix) {
			return f.latest, nil
		}
		return "1.0.0", nil
	})
	// Every rename OF the backup fails: rollback (backup → target) and
	// preservation (backup → .recovered-*) alike.
	stubRenameFile(t, func(oldpath, newpath string) error {
		if strings.Contains(oldpath, ".old-") {
			return errors.New("permission denied")
		}
		return os.Rename(oldpath, newpath)
	})

	_, err := executeUpgradeCommand(t, app)
	apiErr := requireUpgradeError(t, err, "upgrade_failed")

	backups, globErr := filepath.Glob(f.target + ".old-*")
	require.NoError(t, globErr)
	require.Len(t, backups, 1)
	assert.Contains(t, apiErr.Hint, backups[0])

	cleanupUpgradeSidecarsFor(f.target)
	data, readErr := os.ReadFile(backups[0])
	require.NoError(t, readErr)
	assert.Equal(t, f.oldContent, data, ".old-* backup must survive cleanup — it is the only good binary")
}

// After a catastrophic swap the `.old` sidecar may be the only binary left —
// cleanup must not reap anything when the executable itself is missing.
func TestCleanupSkipsWhenExecutableMissing(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "basecamp")
	backup := exe + ".old"
	require.NoError(t, os.WriteFile(backup, []byte("only-copy"), 0o755))

	cleanupUpgradeSidecarsFor(exe)

	_, err := os.Stat(backup)
	assert.NoError(t, err)
}

// A second upgrade must refuse before any asset download or filesystem
// mutation while another upgrade holds the lock.
func TestUpgradeRefusesWhenLockHeld(t *testing.T) {
	app, _ := setupPeopleTestApp(t)
	f := setupNativeFlow(t, "1.0.0", "1.1.0")

	lock := flock.New(upgradeLockPath(f.target))
	locked, err := lock.TryLock()
	require.NoError(t, err)
	require.True(t, locked)
	t.Cleanup(func() { _ = lock.Unlock() })

	_, execErr := executeUpgradeCommand(t, app)
	apiErr := requireUpgradeError(t, execErr, "upgrade_failed")
	assert.Contains(t, apiErr.Message, "another basecamp upgrade")

	assert.Zero(t, f.hits.Load(), "no downloads may start while the lock is held")
	installed, readErr := os.ReadFile(f.target)
	require.NoError(t, readErr)
	assert.Equal(t, f.oldContent, installed)
}

// Startup sidecar cleanup must skip entirely while an upgrade is in flight —
// the glob patterns match that upgrade's live staging and backup files.
func TestCleanupSkipsWhileUpgradeLockHeld(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "basecamp")
	require.NoError(t, os.WriteFile(exe, []byte("bin"), 0o755))
	sidecars := []string{
		filepath.Join(dir, ".basecamp-upgrade-live"),
		exe + ".old-aaaa",
	}
	for _, s := range sidecars {
		require.NoError(t, os.WriteFile(s, []byte("live"), 0o644))
	}

	lock := flock.New(upgradeLockPath(exe))
	locked, err := lock.TryLock()
	require.NoError(t, err)
	require.True(t, locked)

	cleanupUpgradeSidecarsFor(exe)
	for _, s := range sidecars {
		_, statErr := os.Stat(s)
		assert.NoError(t, statErr, "%s must survive cleanup while the upgrade lock is held", s)
	}

	require.NoError(t, lock.Unlock())
	cleanupUpgradeSidecarsFor(exe)
	_, statErr := os.Stat(sidecars[0])
	assert.True(t, os.IsNotExist(statErr), "staging file is reaped after the lock is released")
	_, statErr = os.Stat(sidecars[1])
	assert.NoError(t, statErr, ".old-* backups are never reaped by cleanup")
	_, statErr = os.Stat(upgradeLockPath(exe))
	assert.True(t, os.IsNotExist(statErr), "lock file itself is reaped once no upgrade holds it")
}

func TestCleanupUpgradeSidecars(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "basecamp")
	require.NoError(t, os.WriteFile(exe, []byte("bin"), 0o755))

	// Only the staging namespace is reaped — a positive safe-to-reap
	// contract (discardBackup moves verified-garbage backups into it).
	reaped := []string{
		filepath.Join(dir, ".basecamp-upgrade-abc123"),
		filepath.Join(dir, ".basecamp-upgrade-failed-def.exe"),
		filepath.Join(dir, ".basecamp-upgrade-probe-42"),
		filepath.Join(dir, ".basecamp-upgrade-reap-99"),
	}
	// `.old*` and `.recovered-*` can be the only good binary after a failed
	// rollback — never reaped, along with unrelated bystanders.
	kept := []string{
		exe + ".old",
		exe + ".old-1a2b3c4d",
		exe + ".recovered-cafe",
		filepath.Join(dir, "basecamp.bak"),
	}
	for _, s := range append(append([]string{}, reaped...), kept...) {
		require.NoError(t, os.WriteFile(s, []byte("leftover"), 0o644))
	}

	cleanupUpgradeSidecarsFor(exe)

	for _, s := range reaped {
		_, err := os.Stat(s)
		assert.True(t, os.IsNotExist(err), "expected %s to be reaped", s)
	}
	for _, s := range append(kept, exe) {
		_, err := os.Stat(s)
		assert.NoError(t, err, "expected %s to survive", s)
	}
}

// A verified upgrade discards its backup; when deletion is impossible (the
// Windows-locked old exe), the backup is renamed into the staging namespace
// so cleanup reaps it later — the positive safe-to-reap marker.
func TestDiscardBackupFallsBackToReapNamespace(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "basecamp")
	backup := target + ".old"
	require.NoError(t, os.WriteFile(target, []byte("new"), 0o755))
	require.NoError(t, os.WriteFile(backup, []byte("old"), 0o755))

	orig := removeFile
	removeFile = func(string) error { return errors.New("locked") }
	t.Cleanup(func() { removeFile = orig })

	discardBackup(target, backup)

	_, err := os.Stat(backup)
	assert.True(t, os.IsNotExist(err), "backup must be moved out of the .old name")
	marked, globErr := filepath.Glob(filepath.Join(dir, ".basecamp-upgrade-reap-*"))
	require.NoError(t, globErr)
	require.Len(t, marked, 1)

	cleanupUpgradeSidecarsFor(target)
	_, err = os.Stat(marked[0])
	assert.True(t, os.IsNotExist(err), "reap-marked backup is cleaned up")
}

// Recovery artifacts get unique names: a pre-existing artifact from an
// earlier failure is never clobbered, and repeated failures all survive.
func TestPreserveRecoveryArtifactIsUniqueAndNonClobbering(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "basecamp")
	preexisting := target + ".recovered-aaaa"
	require.NoError(t, os.WriteFile(preexisting, []byte("earlier-recovery"), 0o755))

	backup1 := target + ".old-1111"
	backup2 := target + ".old-2222"
	require.NoError(t, os.WriteFile(backup1, []byte("first"), 0o755))
	require.NoError(t, os.WriteFile(backup2, []byte("second"), 0o755))

	kept1 := preserveRecoveryArtifact(target, backup1)
	kept2 := preserveRecoveryArtifact(target, backup2)

	assert.NotEqual(t, kept1, kept2)
	assert.NotEqual(t, preexisting, kept1)
	assert.NotEqual(t, preexisting, kept2)

	for path, want := range map[string]string{preexisting: "earlier-recovery", kept1: "first", kept2: "second"} {
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Equal(t, want, string(data))
	}
}

// --- parseChecksums / extraction ---

func TestParseChecksums(t *testing.T) {
	data := "abc123  basecamp_1.0.0_linux_amd64.tar.gz\r\n" +
		"DEF456 *basecamp_1.0.0_windows_amd64.zip\r\n" +
		"malformed line with too many fields here\n" +
		"loner\n" +
		"\n" +
		"789abc  basecamp_1.0.0_darwin_arm64.tar.gz"

	sums := parseChecksums([]byte(data))
	assert.Equal(t, "abc123", sums["basecamp_1.0.0_linux_amd64.tar.gz"])
	assert.Equal(t, "def456", sums["basecamp_1.0.0_windows_amd64.zip"], "star prefix stripped, hash lowercased")
	assert.Equal(t, "789abc", sums["basecamp_1.0.0_darwin_arm64.tar.gz"], "no trailing newline")
	assert.Len(t, sums, 3)
}

func TestExtractTarGzMember(t *testing.T) {
	archive := writeArchiveFile(t, buildTarGz(t, []tarEntry{
		{name: "README.md", body: []byte("docs")},
		{name: "completions", typeflag: tar.TypeDir},
		{name: "basecamp", body: []byte("the-binary")},
	}))
	dest := filepath.Join(t.TempDir(), "out")

	require.NoError(t, extractTarGzMember(archive, "basecamp", dest))
	data, err := os.ReadFile(dest)
	require.NoError(t, err)
	assert.Equal(t, []byte("the-binary"), data)
}

func TestExtractTarGzRejectsNestedOnlyMember(t *testing.T) {
	archive := writeArchiveFile(t, buildTarGz(t, []tarEntry{
		{name: "subdir/basecamp", body: []byte("nested")},
	}))
	err := extractTarGzMember(archive, "basecamp", filepath.Join(t.TempDir(), "out"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not contain")
}

func TestExtractTarGzRejectsMissingMember(t *testing.T) {
	archive := writeArchiveFile(t, buildTarGz(t, []tarEntry{
		{name: "README.md", body: []byte("docs")},
	}))
	err := extractTarGzMember(archive, "basecamp", filepath.Join(t.TempDir(), "out"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not contain")
}

func TestExtractTarGzRejectsDuplicateMembers(t *testing.T) {
	archive := writeArchiveFile(t, buildTarGz(t, []tarEntry{
		{name: "basecamp", body: []byte("one")},
		{name: "basecamp", body: []byte("two")},
	}))
	err := extractTarGzMember(archive, "basecamp", filepath.Join(t.TempDir(), "out"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate")
}

func TestExtractTarGzRejectsSymlinkEntries(t *testing.T) {
	archive := writeArchiveFile(t, buildTarGz(t, []tarEntry{
		{name: "evil", typeflag: tar.TypeSymlink, linkname: "/etc/passwd"},
		{name: "basecamp", body: []byte("bin")},
	}))
	err := extractTarGzMember(archive, "basecamp", filepath.Join(t.TempDir(), "out"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "link entry")
}

func TestExtractTarGzRejectsHardLinkEntries(t *testing.T) {
	archive := writeArchiveFile(t, buildTarGz(t, []tarEntry{
		{name: "basecamp", typeflag: tar.TypeLink, linkname: "target"},
	}))
	err := extractTarGzMember(archive, "basecamp", filepath.Join(t.TempDir(), "out"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "link entry")
}

func TestExtractTarGzRejectsOversizedMember(t *testing.T) {
	origMax := maxBinaryBytes
	maxBinaryBytes = 16
	t.Cleanup(func() { maxBinaryBytes = origMax })

	archive := writeArchiveFile(t, buildTarGz(t, []tarEntry{
		{name: "basecamp", body: bytes.Repeat([]byte("A"), 64)},
	}))
	err := extractTarGzMember(archive, "basecamp", filepath.Join(t.TempDir(), "out"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds")
}

func TestExtractZipMember(t *testing.T) {
	archive := writeArchiveFile(t, buildZip(t, []zipEntry{
		{name: "README.md", body: []byte("docs")},
		{name: "basecamp.exe", body: []byte("the-binary")},
	}))
	dest := filepath.Join(t.TempDir(), "out.exe")

	require.NoError(t, extractZipMember(archive, "basecamp.exe", dest))
	data, err := os.ReadFile(dest)
	require.NoError(t, err)
	assert.Equal(t, []byte("the-binary"), data)
}

func TestExtractZipRejectsSymlinkEntries(t *testing.T) {
	archive := writeArchiveFile(t, buildZip(t, []zipEntry{
		{name: "evil", body: []byte("/etc/passwd"), symlink: true},
		{name: "basecamp.exe", body: []byte("bin")},
	}))
	err := extractZipMember(archive, "basecamp.exe", filepath.Join(t.TempDir(), "out.exe"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "symlink entry")
}

func TestExtractZipRejectsDuplicateMembers(t *testing.T) {
	archive := writeArchiveFile(t, buildZip(t, []zipEntry{
		{name: "basecamp.exe", body: []byte("one")},
		{name: "basecamp.exe", body: []byte("two")},
	}))
	err := extractZipMember(archive, "basecamp.exe", filepath.Join(t.TempDir(), "out.exe"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate")
}

func TestExtractZipRejectsMissingMember(t *testing.T) {
	archive := writeArchiveFile(t, buildZip(t, []zipEntry{
		{name: "nested/basecamp.exe", body: []byte("nested")},
	}))
	err := extractZipMember(archive, "basecamp.exe", filepath.Join(t.TempDir(), "out.exe"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not contain")
}

// --- brew/scoop post-verification ---

func TestUpgradeHomebrewStaleProbeIsIncomplete(t *testing.T) {
	app, _ := setupPeopleTestApp(t)
	stubVersion(t, "1.2.3")
	stubUpgradeCheckers(t, upgradeCheckersStub{latestVersion: "1.3.0", isBrew: true})
	stubBrewPrefixResolver(t, func(context.Context) (string, error) { return "/opt/homebrew", nil })
	stubBinaryVersionProber(t, func(_ context.Context, path string) (string, error) {
		assert.Equal(t, filepath.Join("/opt/homebrew", "bin", "basecamp"), path)
		return "1.2.3", nil
	})

	_, err := executeUpgradeCommand(t, app)
	apiErr := requireUpgradeError(t, err, "upgrade_incomplete")
	assert.Contains(t, apiErr.Message, "still reports 1.2.3")
	assert.Contains(t, apiErr.Message, "1.3.0")
	assert.Contains(t, apiErr.Hint, "brew reinstall --cask basecamp/tap/basecamp-cli")
}

func TestUpgradeHomebrewPrefixFailureIsUnverified(t *testing.T) {
	app, _ := setupPeopleTestApp(t)
	stubVersion(t, "1.2.3")
	stubUpgradeCheckers(t, upgradeCheckersStub{latestVersion: "1.3.0", isBrew: true})
	stubBrewPrefixResolver(t, func(context.Context) (string, error) { return "", errors.New("brew not on PATH") })

	_, err := executeUpgradeCommand(t, app)
	apiErr := requireUpgradeError(t, err, "upgrade_unverified")
	assert.Contains(t, apiErr.Hint, "basecamp version")
}

func TestUpgradeHomebrewProbeFailureIsUnverified(t *testing.T) {
	app, _ := setupPeopleTestApp(t)
	stubVersion(t, "1.2.3")
	stubUpgradeCheckers(t, upgradeCheckersStub{latestVersion: "1.3.0", isBrew: true})
	stubBrewPrefixResolver(t, func(context.Context) (string, error) { return "/opt/homebrew", nil })
	stubBinaryVersionProber(t, func(context.Context, string) (string, error) {
		return "", errors.New("exec failed")
	})

	_, err := executeUpgradeCommand(t, app)
	requireUpgradeError(t, err, "upgrade_unverified")
}

// Manager execution failures are upgrade outcomes and must honor the
// structured contract, not surface as generic api_error.
func TestUpgradeHomebrewExecFailureIsStructured(t *testing.T) {
	app, _ := setupPeopleTestApp(t)
	stubVersion(t, "1.2.3")
	stubUpgradeCheckers(t, upgradeCheckersStub{
		latestVersion: "1.3.0",
		isBrew:        true,
		homebrewUpgrade: func(context.Context, io.Writer, io.Writer) error {
			return errors.New("exit status 1")
		},
	})

	_, err := executeUpgradeCommand(t, app)
	apiErr := requireUpgradeError(t, err, "upgrade_failed")
	assert.Contains(t, apiErr.Message, "brew upgrade failed")
	assert.Contains(t, apiErr.Hint, "brew upgrade --cask basecamp/tap/basecamp-cli")
}

func TestUpgradeScoopExecFailureIsStructured(t *testing.T) {
	app, _ := setupPeopleTestApp(t)
	stubVersion(t, "1.2.3")
	stubUpgradeCheckers(t, upgradeCheckersStub{
		latestVersion: "1.3.0",
		isScoop:       true,
		isGlobalScoop: true,
		scoopUpgrade: func(context.Context, bool, io.Writer, io.Writer) error {
			return errors.New("exit status 1")
		},
	})

	_, err := executeUpgradeCommand(t, app)
	apiErr := requireUpgradeError(t, err, "upgrade_failed")
	assert.Contains(t, apiErr.Message, "scoop update failed")
	assert.Contains(t, apiErr.Hint, "scoop update -g basecamp-cli")
}

// A release published while the manager runs can install something newer
// than the snapshot fetched at the start — reported >= latest is success.
func TestUpgradeHomebrewNewerReportedVersionIsSuccess(t *testing.T) {
	app, appBuf := setupPeopleTestApp(t)
	stubVersion(t, "1.2.3")
	stubUpgradeCheckers(t, upgradeCheckersStub{latestVersion: "1.3.0", isBrew: true})
	stubBrewPrefixResolver(t, func(context.Context) (string, error) { return "/opt/homebrew", nil })
	stubBinaryVersionProber(t, func(context.Context, string) (string, error) { return "1.3.1", nil })

	_, err := executeUpgradeCommand(t, app)
	require.NoError(t, err)
	assert.Contains(t, appBuf.String(), `"upgraded"`)
	assert.Contains(t, appBuf.String(), "1.3.1")
}

// An uninterpretable probe result must fail safely as unconfirmed — never
// claim success or completion either way.
func TestUpgradeHomebrewGarbageReportedVersionIsUnverified(t *testing.T) {
	app, _ := setupPeopleTestApp(t)
	stubVersion(t, "1.2.3")
	stubUpgradeCheckers(t, upgradeCheckersStub{latestVersion: "1.3.0", isBrew: true})
	stubBrewPrefixResolver(t, func(context.Context) (string, error) { return "/opt/homebrew", nil })
	stubBinaryVersionProber(t, func(context.Context, string) (string, error) { return "source)", nil })

	_, err := executeUpgradeCommand(t, app)
	apiErr := requireUpgradeError(t, err, "upgrade_unverified")
	assert.Contains(t, apiErr.Message, "could not be interpreted")
}

func TestUpgradeHomebrewConfirmedSuccess(t *testing.T) {
	app, appBuf := setupPeopleTestApp(t)
	stubVersion(t, "1.2.3")
	stubUpgradeCheckers(t, upgradeCheckersStub{latestVersion: "1.3.0", isBrew: true})
	stubBrewPrefixResolver(t, func(context.Context) (string, error) { return "/opt/homebrew", nil })
	stubBinaryVersionProber(t, func(context.Context, string) (string, error) { return "1.3.0", nil })

	_, err := executeUpgradeCommand(t, app)
	require.NoError(t, err)
	assert.Contains(t, appBuf.String(), `"upgraded"`)
	assert.Contains(t, appBuf.String(), `"homebrew"`)
}

func TestUpgradeScoopStaleProbeIsIncomplete(t *testing.T) {
	app, _ := setupPeopleTestApp(t)
	stubVersion(t, "1.2.3")
	stubUpgradeCheckers(t, upgradeCheckersStub{latestVersion: "1.3.0", isScoop: true})
	stubScoopPrefixResolver(t, func(_ context.Context, appName string) (string, bool) {
		return "c:/users/alice/scoop/apps/basecamp-cli/current", true
	})
	stubBinaryVersionProber(t, func(context.Context, string) (string, error) { return "1.2.3", nil })

	_, err := executeUpgradeCommand(t, app)
	requireUpgradeError(t, err, "upgrade_incomplete")
}

func TestUpgradeScoopPrefixFailureIsUnverified(t *testing.T) {
	app, _ := setupPeopleTestApp(t)
	stubVersion(t, "1.2.3")
	stubUpgradeCheckers(t, upgradeCheckersStub{latestVersion: "1.3.0", isScoop: true})
	stubScoopPrefixResolver(t, func(context.Context, string) (string, bool) { return "", false })

	_, err := executeUpgradeCommand(t, app)
	requireUpgradeError(t, err, "upgrade_unverified")
}

func TestUpgradeScoopConfirmedSuccess(t *testing.T) {
	app, appBuf := setupPeopleTestApp(t)
	stubVersion(t, "1.2.3")
	stubUpgradeCheckers(t, upgradeCheckersStub{latestVersion: "1.3.0", isScoop: true})
	stubScoopPrefixResolver(t, func(context.Context, string) (string, bool) {
		return "c:/users/alice/scoop/apps/basecamp-cli/current", true
	})
	stubBinaryVersionProber(t, func(context.Context, string) (string, error) { return "1.3.0", nil })

	_, err := executeUpgradeCommand(t, app)
	require.NoError(t, err)
	assert.Contains(t, appBuf.String(), `"upgraded"`)
	assert.Contains(t, appBuf.String(), `"scoop"`)
}
