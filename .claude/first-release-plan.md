# First Release Plan: basecamp-cli v0.1.0

## Context

Internal-only first release exercising the full release machinery. v0.1.0 — not a prerelease (0.x conveys appropriate maturity). Users install via GitHub Release assets or Docker. Homebrew deferred to a future release when the repo goes public or cask auth is solved.

## Current State

**What exists and is solid:**
- `.goreleaser.yaml` — v2 config with builds (10 os/arch combos), archives, checksums, SBOM (syft), cosign signing, changelog grouping, Docker (`dockers_v2`), Scoop
- `.github/workflows/release.yml` — triggered on `v*` tags: test → PGO profile → goreleaser
- Version injection via ldflags → `internal/version/version.go` (Version, Commit, Date)
- GitHub App for private SDK access
- `Dockerfile.goreleaser` — multi-arch distroless images for `ghcr.io/basecamp/cli`

**No existing version tags** — only a stray `backup-profiles-*` tag.

## Key Decisions

### D1: Skip Homebrew for v0.1.0

Homebrew Casks can't download from private GitHub release assets (the old `GitHubPrivateRepositoryReleaseDownloadStrategy` was removed in Homebrew v2). Rather than hack around this, defer brew to a future release. Users install via `gh release download` or the install script.

**Action:** Set `skip_upload: true` on `homebrew_casks` and `scoops` sections (or remove them). No homebrew-tap repo needed yet.

### D2: v0.1.0 as Normal Release (Not Prerelease)

0.x already signals early maturity. Prerelease tag would be redundant.

### D3: Exercise All Remaining Pipeline Stages

Include Docker, cosign signing, SBOM — the full set minus brew. Better to find failures now.

### D4: Remove `go mod tidy` from Release Hooks

Release build should not mutate source. Tagged source should build as-is.

### D5: Curated Release Notes

First tag = all commits since repo init. The goreleaser `release.header` already has a template — enhance it to serve as the full release note. The auto-generated changelog section will still appear below but the header gives the professional first impression.

**Release notes content** (written into goreleaser `release.header`):
```
## basecamp v{{.Version}}

First internal release of the Basecamp CLI.

### Install

    gh release download v{{.Version}} --repo basecamp/basecamp-cli --pattern '*darwin_arm64*'
    tar xzf basecamp_{{.Version}}_darwin_arm64.tar.gz
    sudo mv basecamp /usr/local/bin/

Or via Docker: `docker pull ghcr.io/basecamp/cli:{{.Version}}`

### What's included

- Full project and resource management (todos, cards, messages, documents, files)
- Campfire messaging with rich text rendering
- Interactive TUI workspace
- Cross-account support with OAuth authentication
- Full-text search across projects
```

**Mechanism:** Edit the `release.header` field in `.goreleaser.yaml`. No `--release-notes` flag needed.

### D6: Forward-Fix Policy

Never retag. If v0.1.0 pipeline fails, diagnose, fix config, ship v0.1.1.

## Changes Required

### 1. goreleaser.yaml

- **Remove `go mod tidy` from before hooks** — reproducibility
- **Set `skip_upload: true` on `homebrew_casks`** — defer brew
- **Set `skip_upload: true` on `scoops`** — no Windows users internally
- **Remove `CHANGELOG*` from archive `files`** — file doesn't exist, just noise
- **Verify `dockers_v2` config is correct** — the Dockerfile.goreleaser already handles TARGETPLATFORM
- **Update `release.header`** with curated first-release notes (see D5 above)

### 2. release.yml

- **Remove homebrew-tap token generation step** — no longer needed for v0.1.0. Keep the SDK token.
- **Strengthen test gate**: Replace bare `go test -v ./...` with `make fmt-check vet test provenance-check check-naming`. Adds formatting, vet, provenance, and naming checks without extra tool installs.
- **Add branch ancestry guard** to the release job: verify the tag commit is reachable from `origin/main` before goreleaser runs. Three lines of shell, prevents accidental release from a feature branch.
  ```yaml
  - name: Verify tag is on main
    run: |
      git fetch origin main
      git merge-base --is-ancestor $GITHUB_SHA origin/main
  ```

### 3. Clean Up Stray Tag

Delete `backup-profiles-1770287811` locally and remotely. Confuses goreleaser's changelog since-last-tag.

### 4. Validate GoReleaser Config

Run `goreleaser check` locally after changes.

### 5. Install Documentation

Document the internal install path:
```bash
# Via gh CLI (recommended)
gh release download v0.1.0 --repo basecamp/basecamp-cli --pattern '*darwin_arm64*'
tar xzf basecamp_0.1.0_darwin_arm64.tar.gz
sudo mv basecamp /usr/local/bin/

# Via Docker
docker pull ghcr.io/basecamp/cli:0.1.0
```

## Implementation Sequence

```
Phase 1: Config changes (goreleaser.yaml)
  1. Remove go mod tidy from before hooks
  2. Set skip_upload: true on homebrew_casks and scoops
  3. Remove CHANGELOG* from archive files
  4. Update release.header with curated v0.1.0 release notes

Phase 1b: Config changes (release.yml)
  5. Remove homebrew-tap token generation step
  6. Strengthen test gate (make fmt-check vet test provenance-check check-naming)
  7. Add branch ancestry guard (tag must be on main)

Phase 2: Local validation
  8. goreleaser check
  9. goreleaser release --snapshot --clean (full local build rehearsal)
  10. Verify GitHub variable RELEASE_CLIENT_ID and secret RELEASE_APP_PRIVATE_KEY
  11. Delete stray backup-profiles tag

Phase 3: Push and tag
  7. Push first-release to main (branches are identical, fast-forward)
  8. git tag -a v0.1.0 -m "First release" on main
  9. git push origin v0.1.0

Phase 4: Monitor and verify
  10. Watch: test job → release job → goreleaser
  11. Verify GitHub Release: archives (10 os/arch), checksums, SBOM, cosign sig
  12. Verify Docker: docker pull ghcr.io/basecamp/cli:0.1.0
  13. Test install: gh release download, extract, basecamp version → 0.1.0
```

## Risks

| Risk | Severity | Mitigation |
|------|----------|------------|
| GitHub App missing required permissions | H | Verify secrets exist before tagging |
| Cosign/Sigstore outage blocks release | M | Re-run workflow; `--skip=sign` escape hatch |
| Docker buildx flaky in CI | M | If fails, disable docker and re-release as v0.1.1 |
| First-tag changelog noisy | M | Curated release notes via release.header |
| `go mod tidy` mutates source in release | M | Remove from before hooks |
| Tag from wrong branch | M | Branch ancestry guard in release job |

## Deferred to Future Release

- Homebrew tap (needs public repo or cask auth solution)
- Scoop manifest
- Shell completions packaging
- Man page generation
- Upgrade notifications
- `workflow_dispatch` snapshot rehearsal mode
- Release workflow concurrency guard
