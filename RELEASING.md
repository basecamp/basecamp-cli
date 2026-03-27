# Releasing

## Quick release

```bash
make release VERSION=0.2.0
```

## Dry run

```bash
make release VERSION=0.2.0 DRY_RUN=1
```

## What happens

1. Validates semver format, main branch, clean tree, synced with remote
2. Checks for `replace` directives in go.mod
3. Updates `nix/package.nix` version and recomputes `vendorHash` via Docker if `go.mod` changed
4. Runs `make release-check` (quality checks, vuln scan, replace-check, race-test, surface compat)
5. Creates annotated tag `v$VERSION` and pushes to origin
6. GitHub Actions [release workflow](.github/workflows/release.yml) runs:
   - Security scan + full test suite + CLI surface compatibility check
   - Collects PGO profile from benchmarks
   - Generates AI changelog from commit history
   - Builds binaries for all platforms (darwin, linux, windows, freebsd, openbsd × amd64/arm64)
   - Builds `.deb`, `.rpm`, `.apk` Linux packages (amd64 + arm64)
   - Signs macOS binaries via build hook (`anchore/quill` CLI), notarizes post-publish (see [tradeoff](#macos-signing-tradeoffs))
   - Signs checksums with cosign (keyless via Sigstore OIDC)
   - Generates SBOM for supply chain transparency
   - Updates Homebrew cask (`basecamp-cli`) in `basecamp/homebrew-tap`
   - Updates Scoop manifest (`basecamp-cli`) in `basecamp/homebrew-tap`
   - Updates AUR `basecamp-cli` package (when `AUR_KEY` is configured)
   - Verifies Nix flake builds successfully

## Versioning

Pre-1.0: minor bumps for features, patch bumps for fixes. Prerelease tags
(e.g. `0.2.0-rc.1`) are marked as prereleases automatically by GoReleaser.

## Requirements

- On `main` branch with clean, synced working tree
- No `replace` directives in go.mod
- `make release-check` passes (includes check, replace-check, vuln scan, race-test, surface compat)

## CI secrets

**Repository secrets** (Settings → Secrets and variables → Actions):

| Secret | Purpose |
|--------|---------|
| `RELEASE_CLIENT_ID` (var) | GitHub App ID for `bcq-release-bot` |
| `RELEASE_APP_PRIVATE_KEY` | GitHub App private key |
| `AUR_KEY` | SSH private key for AUR push (optional) |

**Environment secrets** (`release` environment — Settings → Environments):

| Secret | Purpose |
|--------|---------|
| `MACOS_SIGN_P12` | Base64-encoded Developer ID Application certificate (.p12) |
| `MACOS_SIGN_PASSWORD` | .p12 unlock password |
| `MACOS_NOTARY_KEY` | Base64-encoded App Store Connect API key (.p8) |
| `MACOS_NOTARY_KEY_ID` | App Store Connect API key ID (10 characters) |
| `MACOS_NOTARY_ISSUER_ID` | App Store Connect issuer UUID |

## AUR setup (one-time)

1. Create an account at https://aur.archlinux.org
2. Register the `basecamp-cli` package
3. Generate an SSH keypair: `ssh-keygen -t ed25519 -f aur_key -C "basecamp-cli AUR"`
4. Add the public key to your AUR profile
5. Add the private key as `AUR_KEY` in GitHub Actions secrets

## Nix flake maintenance

The `flake.nix` provides `nix profile install github:basecamp/basecamp-cli`. The release
script automatically updates `nix/package.nix` version and recomputes `vendorHash` when
`go.mod` changes (requires Docker).

To manually update the vendorHash (e.g. after an SDK bump):
```bash
make update-nix-hash
```

## Distribution channels

| Channel | Location | Updated by |
|---------|----------|------------|
| GitHub Releases | [basecamp/basecamp-cli](https://github.com/basecamp/basecamp-cli/releases) | GoReleaser |
| Homebrew cask (`basecamp-cli`) | `basecamp/homebrew-tap` Casks/ | GoReleaser |
| Scoop (`basecamp-cli`) | `basecamp/homebrew-tap` root | GoReleaser |
| AUR | `basecamp-cli` | GoReleaser |
| deb/rpm/apk packages | GitHub Release assets | GoReleaser (nfpm) |
| Nix flake | `flake.nix` in repo | Self-serve (`nix profile install github:basecamp/basecamp-cli`) |
| go install | `go install github.com/basecamp/basecamp-cli/cmd/basecamp@latest` | Go module proxy |

## macOS signing tradeoffs

**Signing** uses `anchore/quill` CLI directly (not GoReleaser's embedded fork)
because the `goreleaser/quill` fork does not populate the `TeamIdentifier` field
in the CodeDirectory ([anchore/quill#147](https://github.com/anchore/quill/issues/147)).
The build hook signs darwin binaries before archiving, so archives, checksums,
and tap manifests all contain correctly signed binaries.

**Notarization** runs after GoReleaser publishes. This means there is a brief
window where the GitHub release and Homebrew/Scoop manifests point at binaries
Apple has not yet accepted. This is accepted release debt, not a bug:
- The binary bytes are final — notarization for bare Mach-O is purely server-side
- GateKeeper checks Apple's servers at runtime, not a stapled ticket
- Bare Mach-O executables cannot be stapled (Apple limitation)
- This matches the previous GoReleaser/quill behavior

The `macos-verify` post-release job runs on a real macOS runner for both amd64
and arm64. TeamIdentifier and hardened runtime are hard assertions that fail
the workflow. The notarization check is best-effort telemetry — ticket
propagation can lag, so it warns rather than gates.

**Reverting**: when `goreleaser/quill` syncs the fix from anchore/quill v0.7.0,
revert to the built-in notarize block: remove the build hook, re-enable
`notarize.macos`, remove the quill install/notarize workflow steps, and delete
`scripts/sign-darwin.sh`.
