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
3. Runs `make release-check` (quality checks, vuln scan, replace-check, race-test, surface compat)
4. Creates annotated tag `v$VERSION` and pushes to origin
5. GitHub Actions [release workflow](.github/workflows/release.yml) runs:
   - Security scan + full test suite + CLI surface compatibility check
   - Collects PGO profile from benchmarks
   - Generates AI changelog from commit history
   - Builds binaries for all platforms (darwin, linux, windows, freebsd, openbsd × amd64/arm64)
   - Signs checksums with cosign (keyless via Sigstore OIDC)
   - Generates SBOM for supply chain transparency
   - Updates Homebrew cask in `basecamp/homebrew-tap`
   - Updates Scoop manifest in `basecamp/homebrew-tap`
   - Updates AUR `basecamp-bin` package (when `AUR_KEY` is configured)

## Versioning

Pre-1.0: minor bumps for features, patch bumps for fixes. Prerelease tags
(e.g. `0.2.0-rc.1`) are marked as prereleases automatically by GoReleaser.

## Requirements

- On `main` branch with clean, synced working tree
- No `replace` directives in go.mod
- `make release-check` passes (includes check, replace-check, vuln scan, race-test, surface compat)

## CI secrets

| Secret | Purpose |
|--------|---------|
| `RELEASE_CLIENT_ID` (var) | GitHub App ID for `bcq-release-bot` |
| `RELEASE_APP_PRIVATE_KEY` | GitHub App private key |
| `AUR_KEY` | SSH private key for AUR push (optional) |

## AUR setup (one-time)

1. Create an account at https://aur.archlinux.org
2. Register the `basecamp-bin` package
3. Generate an SSH keypair: `ssh-keygen -t ed25519 -f aur_key -C "basecamp-cli AUR"`
4. Add the public key to your AUR profile
5. Add the private key as `AUR_KEY` in GitHub Actions secrets

## Distribution channels

| Channel | Location | Updated by |
|---------|----------|------------|
| GitHub Releases | [basecamp/basecamp-cli](https://github.com/basecamp/basecamp-cli/releases) | GoReleaser |
| Homebrew cask | `basecamp/homebrew-tap` Casks/ | GoReleaser |
| Scoop | `basecamp/homebrew-tap` root | GoReleaser |
| AUR | `basecamp-bin` | GoReleaser |
| go install | `go install github.com/basecamp/basecamp-cli/cmd/basecamp@latest` | Go module proxy |
