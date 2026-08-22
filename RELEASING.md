# Releasing

## Quick release

```bash
make release VERSION=0.2.0
```

## Release candidate

```bash
make release VERSION=0.2.0-rc.1
```

## Dry run

```bash
make release VERSION=0.2.0 DRY_RUN=1
```

## What happens

1. Validates semver format, main branch, clean tree, synced with remote
2. Checks for `replace` directives in go.mod
3. Runs `make release-check` (quality checks, vuln scan, replace-check, race-test, surface compat)
4. Updates stable release metadata (`nix/package.nix`, `.claude-plugin/plugin.json`, and `.codex-plugin/plugin.json`) for stable versions
5. Creates annotated tag `v$VERSION` and pushes to origin
6. GitHub Actions [release workflow](.github/workflows/release.yml) runs:
   - Security scan + full test suite + CLI surface compatibility check
   - Collects PGO profile from benchmarks
   - Builds binaries for all platforms (darwin, linux, windows, freebsd, openbsd × amd64/arm64)
   - Builds `.deb`, `.rpm`, `.apk` Linux packages (amd64 + arm64)
   - Signs and notarizes macOS binaries via GoReleaser's built-in notarize (embedded quill)
   - Signs Windows binaries with Authenticode via jsign and DigiCert KeyLocker
   - Signs a copy of the PowerShell installer and attaches it as `basecamp_installer.ps1`
   - Signs checksums with cosign (keyless via Sigstore OIDC)
   - Generates SBOM for supply chain transparency
   - Updates Homebrew cask (`basecamp-cli`) in `basecamp/homebrew-tap` for stable tags
   - Updates Scoop manifest (`basecamp-cli`) in `basecamp/homebrew-tap` for stable tags
   - Updates AUR `basecamp-cli` package for stable tags when `AUR_KEY` is configured
   - Verifies Nix flake builds successfully for stable tags

## Versioning

Pre-1.0: minor bumps for features, patch bumps for fixes. Use prerelease
versions such as `0.2.0-rc.1` when testers need a release candidate before
the next stable version.

Stable releases publish to every normal distribution channel. Prereleases publish
GitHub Release assets for explicit tester installs while keeping stable package
manager channels and agent distribution metadata on the latest stable version.

| Surface | Stable version `0.2.0` | Prerelease version `0.2.0-rc.1` |
|---------|-------------------------|----------------------------------|
| GitHub Releases | Published as a normal release and eligible for GitHub's latest release. | Published as a GitHub prerelease while GitHub's latest release points at stable. |
| Release assets | Binaries, archives, checksums, SBOMs, and Linux packages are uploaded. | The same downloadable assets are uploaded for explicit tester installs. |
| Homebrew | Updates the `basecamp-cli` cask in `basecamp/homebrew-tap`. | The stable cask remains active. |
| Scoop | Updates the `basecamp-cli` manifest in `basecamp/homebrew-tap`. | The stable manifest remains active. |
| AUR | Updates the `basecamp-cli` package when `AUR_KEY` is configured. | The stable AUR package remains active. |
| Nix flake | Updates `nix/package.nix` and verifies the flake. | Nix metadata remains on the latest stable version. |
| Claude plugin metadata | Updates `.claude-plugin/plugin.json`. | Plugin metadata remains on the latest stable version. |
| Codex plugin metadata | Updates `.codex-plugin/plugin.json`. | Plugin metadata remains on the latest stable version. |
| Skills distribution | Syncs skills to the distribution repo. | Uses the skill embedded in the prerelease binary. |

The prerelease binary embeds the matching `skills/basecamp/SKILL.md`. Testers can
print or install that skill with:

```bash
basecamp skill
basecamp skill install
```

## Requirements

- On `main` branch with clean, synced working tree
- No `replace` directives in go.mod
- `make release-check` passes (includes check, replace-check, vuln scan, race-test, surface compat)

## Release size budget

Stripped release binary ≤ 38 MiB; gzip-compressed binary ≤ 13 MiB (a proxy
for release-archive size), per platform. Baseline set when in-process
Sigstore/TUF verification landed for `basecamp upgrade` (v0.8.1 measured
24.4 MiB stripped / 8.0 MiB gzipped; the sigstore-go tree added ~9.7 MiB
stripped / ~3.2 MiB gzipped — accepted cost of verifying releases without a
cosign dependency). An increase beyond either bound needs explicit review of
what grew, not a budget bump. Enforcement is manual for now.

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
| `SM_API_KEY` | DigiCert ONE API key for KeyLocker |
| `SM_CLIENT_CERT_FILE_B64` | Base64-encoded DigiCert ONE mTLS client certificate (.p12) |
| `SM_CLIENT_CERT_PASSWORD` | Client certificate unlock password |

## Windows signing

Windows binaries and the installer copy are Authenticode-signed from Linux CI
via [jsign](https://ebourg.github.io/jsign/) against DigiCert KeyLocker (cloud
HSM) — no Windows runner or hardware token involved. bc3-desktop's
`docs/windows-signing.md` is the canonical runbook; this repo is a second
consumer of the same certificate.

- Certificate: OV code signing, `CN=37signals LLC`, expires **2027-04-30**.
  The DigiCert ONE certificate ID is pinned once, in
  `.github/workflows/release.yml` (`SIGN_ALIAS`) — keep it in sync with
  bc3-desktop when the certificate is renewed.
- jsign version and jar sha256 are pinned in the release workflow's
  "Prepare Windows signing" step. jsign ≥ 7.5 is required — 7.1–7.3 are
  broken against DigiCert ONE's current API.
- Quota: KeyLocker signatures draw from a budget shared with bc3-desktop.
  Each tag consumes 3 signatures (2 exes + the installer copy); a release
  cycle of rc(s) + stable is ≥ 6, and workflow re-runs after post-signing
  failures consume more. Confirm headroom with the bc3-desktop cert owner
  before a release burst.
- Signing failures abort the release during the build phase — nothing is
  published. Re-run the workflow on the same tag after the outage clears.

## AUR setup (one-time)

1. Create an account at https://aur.archlinux.org
2. Register the `basecamp-cli` package
3. Generate an SSH keypair: `ssh-keygen -t ed25519 -f aur_key -C "basecamp-cli AUR"`
4. Add the public key to your AUR profile
5. Add the private key as `AUR_KEY` in GitHub Actions secrets

## Nix flake maintenance

The `flake.nix` provides `nix profile install github:basecamp/basecamp-cli`. Stable
releases update `nix/package.nix` version and recompute `vendorHash` when `go.mod`
changes (requires Docker).

To manually update the vendorHash (e.g. after an SDK bump):
```bash
make update-nix-hash
```

## Distribution channels

| Channel | Location | Updated by |
|---------|----------|------------|
| GitHub Releases | [basecamp/basecamp-cli](https://github.com/basecamp/basecamp-cli/releases) | GoReleaser |
| Homebrew cask (`basecamp-cli`) | `basecamp/homebrew-tap` Casks/ | GoReleaser (stable tags) |
| Scoop (`basecamp-cli`) | `basecamp/homebrew-tap` root | GoReleaser (stable tags) |
| AUR | `basecamp-cli` | `scripts/publish-aur.sh` (stable tags) |
| deb/rpm/apk packages | GitHub Release assets | GoReleaser (nfpm) |
| Nix flake | `flake.nix` in repo | Self-serve (`nix profile install github:basecamp/basecamp-cli`) |
| go install | `go install github.com/basecamp/basecamp-cli/cmd/basecamp@latest` | Go module proxy |
