#!/usr/bin/env bats
#
# scripts/release.sh against a real git repo with a local bare origin. main
# takes changes only through pull requests, so the script never pushes it: a
# stable release first stamps its metadata on release/vX.Y.Z and opens a PR,
# and only a run from a main that carries the stamps pushes the tag. What
# matters is what reaches origin and when, so a pre-receive hook on the bare
# origin records every ref it receives, in order.
#
# gh and the Docker-backed update-nix-flake.sh are stubbed; both plugin
# stampers are the real ones. make runs a fixture Makefile whose release-check
# only records that it ran.

setup() {
  REPO_ROOT="$(cd "${BATS_TEST_DIRNAME}/.." && pwd)"
  WORK="$(mktemp -d)"
  STUB_DIR="$WORK/bin"
  export LOG="$WORK/calls.log"
  export GH_LOG="$WORK/gh.log"
  PUSH_LOG="$WORK/pushes.log"
  mkdir -p "$STUB_DIR"
  : > "$LOG"

  git init -q --bare --initial-branch=main "$WORK/origin.git"
  cat > "$WORK/origin.git/hooks/pre-receive" <<HOOK
#!/bin/sh
while read -r _old _new ref; do echo "\$ref" >> "$PUSH_LOG"; done
HOOK
  chmod +x "$WORK/origin.git/hooks/pre-receive"

  git clone -q "$WORK/origin.git" "$WORK/repo" 2>/dev/null
  cd "$WORK/repo"
  git config user.email release@example.com
  git config user.name "Release Bot"
  git config commit.gpgsign false
  git config tag.gpgsign false
  git checkout -q -b main

  mkdir -p scripts nix .claude-plugin .codex-plugin
  cp "$REPO_ROOT/scripts/release.sh" \
     "$REPO_ROOT/scripts/stamp-plugin-version.sh" \
     "$REPO_ROOT/scripts/stamp-codex-plugin-version.sh" scripts/
  cat > scripts/update-nix-flake.sh <<'STUB'
#!/usr/bin/env bash
echo "update-nix-flake $1" >> "$LOG"
grep -q "version = \"$1\"" nix/package.nix && exit 2
sed -i.bak "s/version = \"[^\"]*\"/version = \"$1\"/" nix/package.nix
rm -f nix/package.nix.bak
STUB
  chmod +x scripts/update-nix-flake.sh
  printf '{\n  version = "0.1.0";\n}\n' > nix/package.nix
  printf '{\n  "name": "basecamp",\n  "version": "0.1.0"\n}\n' > .claude-plugin/plugin.json
  printf '{\n  "name": "basecamp",\n  "version": "0.1.0"\n}\n' > .codex-plugin/plugin.json
  printf 'module example.com/basecamp\n\ngo 1.24\n' > go.mod
  cat > Makefile <<'MAKEFILE'
release-check:
	@if [ -n "$$DRY_RUN" ]; then echo "release DRY_RUN leaked into release-check: $$DRY_RUN" >&2; exit 1; fi
	@echo "release-check" >> "$$LOG"
MAKEFILE

  git add -A && git commit -qm "Initial import"
  git tag -a v0.1.0 -m "Release v0.1.0"
  printf 'package main\n' > main.go
  git add -A && git commit -qm "Add main"
  git push -q origin main --tags
  : > "$PUSH_LOG"
  BASE=$(git rev-parse HEAD)

  # gh answers the two calls the release PR needs. GH_OPEN_PR stands for a
  # release PR that is already open. Anything else fails, so a test never
  # reaches the real gh.
  cat > "$STUB_DIR/gh" <<'STUB'
#!/usr/bin/env bash
printf 'gh' >> "$GH_LOG"; printf ' %q' "$@" >> "$GH_LOG"; echo >> "$GH_LOG"
case "$1 $2" in
  "pr list") [[ -z "${GH_OPEN_PR:-}" ]] || echo "$GH_OPEN_PR"; exit 0 ;;
  "pr create") echo "https://github.com/basecamp/basecamp-cli/pull/999"; exit 0 ;;
esac
echo "unexpected gh call: $*" >&2
exit 1
STUB
  chmod +x "$STUB_DIR/gh"
  PATH="$STUB_DIR:$PATH"
}

teardown() {
  rm -rf "$WORK"
}

# Bash's errexit ignores a command negated with `!`, so `! cmd` on its own line
# never fails a bats test. A function returning non-zero does.
not() { if "$@"; then return 1; fi; }
origin() { git --git-dir="$WORK/origin.git" "$@"; }
versions() {
  echo "$(sed -n 's/.*version = "\([^"]*\)".*/\1/p' nix/package.nix)" \
    "$(jq -r .version .claude-plugin/plugin.json)" \
    "$(jq -r .version .codex-plugin/plugin.json)"
}
release_pushes() { grep -E '^refs/(heads/|tags/v)' "$PUSH_LOG" || true; }

# What squash-merging the release PR leaves on origin's main.
merge_release_pr() {
  git fetch -q origin
  git merge -q --squash "origin/release/v$1" >/dev/null
  git commit -qm "Prepare v$1 release (#999)"
  git push -q origin main
  : > "$PUSH_LOG"
  : > "$GH_LOG"
}

@test "an unstamped stable release opens the release PR and pushes neither main nor a tag" {
  run scripts/release.sh 0.2.0
  [ "$status" -eq 0 ]
  [[ "$output" == *"Opened the release PR: https://github.com/basecamp/basecamp-cli/pull/999"* ]]
  [[ "$output" == *"make release VERSION=0.2.0"* ]]

  [ "$(release_pushes)" = "refs/heads/release/v0.2.0" ]
  [ "$(origin rev-parse main)" = "$BASE" ]
  not origin rev-parse -q --verify refs/tags/v0.2.0 >/dev/null
  [ "$(origin log -1 --format=%s release/v0.2.0)" = "Update nix flake and plugin version for v0.2.0" ]
  [ "$(origin rev-parse release/v0.2.0^)" = "$BASE" ]
  origin show release/v0.2.0:nix/package.nix | grep -q 'version = "0.2.0"'
  [ "$(origin show release/v0.2.0:.claude-plugin/plugin.json | jq -r .version)" = "0.2.0" ]
  [ "$(origin show release/v0.2.0:.codex-plugin/plugin.json | jq -r .version)" = "0.2.0" ]
  grep -q -- '^gh pr create --base main --head release/v0.2.0 --label release --title Prepare\\ v0.2.0\\ release' "$GH_LOG"
  not grep -qE -- '^gh pr merge|--admin|--auto' "$GH_LOG"
  grep -q 'nix/package.nix' "$GH_LOG"
  grep -q 'codex-plugin/plugin.json' "$GH_LOG"

  # The clone is back on main, untouched, ready for the run after the merge.
  [ "$(git rev-parse --abbrev-ref HEAD)" = "main" ]
  [ "$(git rev-parse HEAD)" = "$BASE" ]
  [ -z "$(git status --porcelain)" ]
  grep -q '^release-check$' "$LOG"
}

@test "once the release PR is merged the release pushes only the tag, at the stamped commit" {
  scripts/release.sh 0.2.0 >/dev/null
  merge_release_pr 0.2.0
  MERGED=$(git rev-parse HEAD)

  run scripts/release.sh 0.2.0
  [ "$status" -eq 0 ]
  [[ "$output" == *"Release metadata is stamped for 0.2.0"* ]]

  [ "$(release_pushes)" = "refs/tags/v0.2.0" ]
  [ "$(origin rev-parse v0.2.0^{commit})" = "$MERGED" ]
  [ "$(origin cat-file -t v0.2.0)" = "tag" ]
  [ "$(origin rev-parse main)" = "$MERGED" ]
  [ ! -s "$GH_LOG" ]
}

@test "a main stamped for another version opens a PR rather than tagging" {
  scripts/release.sh 0.2.0 >/dev/null
  merge_release_pr 0.2.0

  run scripts/release.sh 0.3.0
  [ "$status" -eq 0 ]
  [[ "$output" == *"Opened the release PR"* ]]
  [ "$(release_pushes)" = "refs/heads/release/v0.3.0" ]
  not origin rev-parse -q --verify refs/tags/v0.3.0 >/dev/null
}

@test "partly stamped metadata is not taken for a stamped release" {
  # Only Nix: release-check holds the two plugin manifests in lockstep.
  sed -i.bak 's/version = "0.1.0"/version = "0.2.0"/' nix/package.nix && rm nix/package.nix.bak
  git commit -qam "Stamp Nix only"
  git push -q origin main
  : > "$PUSH_LOG"

  run scripts/release.sh 0.2.0
  [ "$status" -eq 0 ]
  [ "$(release_pushes)" = "refs/heads/release/v0.2.0" ]
  [ "$(origin log -1 --format=%s release/v0.2.0)" = "Update plugin version for v0.2.0" ]
  not origin rev-parse -q --verify refs/tags/v0.2.0 >/dev/null
}

@test "a prerelease tags HEAD and leaves the stable metadata alone" {
  run scripts/release.sh 0.2.0-rc.1
  [ "$status" -eq 0 ]

  [ "$(release_pushes)" = "refs/tags/v0.2.0-rc.1" ]
  [ "$(origin rev-parse v0.2.0-rc.1^{commit})" = "$BASE" ]
  [ "$(origin rev-parse main)" = "$BASE" ]
  [ "$(versions)" = "0.1.0 0.1.0 0.1.0" ]
  [ ! -f "$GH_LOG" ]
  not grep -q update-nix-flake "$LOG"
}

@test "a dry run of an unstamped release checks everything and creates nothing" {
  run env DRY_RUN=1 scripts/release.sh 0.2.0
  [ "$status" -eq 0 ]
  [[ "$output" == *"opens the release PR"* ]]
  [[ "$output" == *"Dry run complete"* ]]

  [ -z "$(release_pushes)" ]
  [ ! -f "$GH_LOG" ]
  not git rev-parse -q --verify refs/heads/release/v0.2.0 >/dev/null
  [ "$(git rev-parse HEAD)" = "$BASE" ]
  [ "$(versions)" = "0.1.0 0.1.0 0.1.0" ]
  grep -q '^release-check$' "$LOG"
  not grep -q update-nix-flake "$LOG"
}

@test "a dry run of a stamped release creates no tag" {
  scripts/release.sh 0.2.0 >/dev/null
  merge_release_pr 0.2.0

  run scripts/release.sh 0.2.0 --dry-run
  [ "$status" -eq 0 ]
  [ -z "$(release_pushes)" ]
  not git rev-parse -q --verify refs/tags/v0.2.0 >/dev/null
}

@test "the Makefile's dry run stays dry" {
  run make -f "$REPO_ROOT/Makefile" release VERSION=0.2.0 DRY_RUN=1
  [ "$status" -eq 0 ]
  [[ "$output" == *"Dry run complete"* ]]
  [ -z "$(release_pushes)" ]
  [ ! -f "$GH_LOG" ]
}

@test "a release PR that is already open is pointed at, not opened again" {
  GH_OPEN_PR=https://github.com/basecamp/basecamp-cli/pull/998 run scripts/release.sh 0.2.0
  [ "$status" -eq 0 ]
  [[ "$output" == *"already open: https://github.com/basecamp/basecamp-cli/pull/998"* ]]
  [ -z "$(release_pushes)" ]
  not grep -q 'pr create' "$GH_LOG"
  [ "$(git rev-parse --abbrev-ref HEAD)" = "main" ]
}

@test "a release branch left on origin without a PR is not pushed over" {
  git push -q origin "$BASE:refs/heads/release/v0.2.0"
  : > "$PUSH_LOG"

  run scripts/release.sh 0.2.0
  [ "$status" -eq 1 ]
  [[ "$output" == *"origin already has release/v0.2.0 but no open PR"* ]]
  [ -z "$(release_pushes)" ]
  not grep -q 'pr create' "$GH_LOG"
  [ "$(git rev-parse --abbrev-ref HEAD)" = "main" ]
  [ -z "$(git status --porcelain)" ]
}

@test "a failed nix update restores the metadata and returns to main" {
  cat > scripts/update-nix-flake.sh <<'STUB'
#!/usr/bin/env bash
sed -i.bak "s/version = \"[^\"]*\"/version = \"$1\"/" nix/package.nix
rm -f nix/package.nix.bak
echo "ERROR: nix build failed" >&2
exit 1
STUB
  git commit -qam "Break the nix update"
  git push -q origin main
  : > "$PUSH_LOG"
  BASE=$(git rev-parse HEAD)

  run scripts/release.sh 0.2.0
  [ "$status" -eq 1 ]
  [[ "$output" == *"update-nix-flake.sh failed"* ]]
  [ -z "$(git status --porcelain)" ]
  [ "$(versions)" = "0.1.0 0.1.0 0.1.0" ]
  [ "$(git rev-parse --abbrev-ref HEAD)" = "main" ]
  [ "$(git rev-parse HEAD)" = "$BASE" ]
  [ -z "$(release_pushes)" ]
}

@test "a failed release commit restores the metadata, index included, and the next attempt is not blocked" {
  cat > .git/hooks/pre-commit <<'HOOK'
#!/bin/sh
echo "pre-commit: signing failed" >&2
exit 1
HOOK
  chmod +x .git/hooks/pre-commit

  run scripts/release.sh 0.2.0
  [ "$status" -eq 1 ]
  [ -z "$(git status --porcelain)" ]
  [ "$(versions)" = "0.1.0 0.1.0 0.1.0" ]
  [ "$(git rev-parse --abbrev-ref HEAD)" = "main" ]
  [ -z "$(release_pushes)" ]

  rm .git/hooks/pre-commit
  run scripts/release.sh 0.2.0
  [ "$status" -eq 0 ]
  [ "$(release_pushes)" = "refs/heads/release/v0.2.0" ]
}

@test "refuses a tag that exists at another commit before pushing anything" {
  git tag -a v0.2.0 -m "Release v0.2.0" v0.1.0
  git push -q origin v0.2.0
  git tag -d v0.2.0 >/dev/null
  : > "$PUSH_LOG"

  run scripts/release.sh 0.2.0
  [ "$status" -eq 1 ]
  [[ "$output" == *"Tag v0.2.0 already exists at"*"(not HEAD)"* ]]
  [ -z "$(release_pushes)" ]
  [ ! -s "$LOG" ]
}

@test "refuses a hand-pushed stable tag at an unstamped HEAD" {
  git tag -a v0.2.0 -m "Release v0.2.0"
  git push -q origin v0.2.0
  : > "$PUSH_LOG"

  run scripts/release.sh 0.2.0
  [ "$status" -eq 1 ]
  [[ "$output" == *"release metadata is not stamped"* ]]
  [ -z "$(release_pushes)" ]
  [ "$(origin rev-parse main)" = "$BASE" ]
}

@test "re-running a stable release whose tag is already at HEAD pushes nothing new" {
  scripts/release.sh 0.2.0 >/dev/null
  merge_release_pr 0.2.0
  scripts/release.sh 0.2.0 >/dev/null
  : > "$PUSH_LOG"

  run scripts/release.sh 0.2.0
  [ "$status" -eq 0 ]
  [[ "$output" == *"Tag v0.2.0 already exists at HEAD"* ]]
  [ -z "$(release_pushes)" ]
}

@test "a rejected tag push removes the local tag" {
  scripts/release.sh 0.2.0 >/dev/null
  merge_release_pr 0.2.0
  cat > "$WORK/origin.git/hooks/pre-receive" <<'HOOK'
#!/bin/sh
echo "error: GH013: Repository rule violations found for refs/tags/v0.2.0." >&2
exit 1
HOOK

  run scripts/release.sh 0.2.0
  [ "$status" -eq 1 ]
  [[ "$output" == *"was rejected"* ]]
  not origin rev-parse -q --verify refs/tags/v0.2.0 >/dev/null
  not git rev-parse -q --verify refs/tags/v0.2.0 >/dev/null
}

@test "refuses to release from a branch other than main" {
  git checkout -q -b topic
  run scripts/release.sh 0.2.0
  [ "$status" -eq 1 ]
  [[ "$output" == *"Must be on main"* ]]
  [ -z "$(release_pushes)" ]
}

@test "a stable tag waits for a Nix flake that still needs an update at the merged commit" {
  scripts/release.sh 0.2.0 >/dev/null
  merge_release_pr 0.2.0
  MERGED=$(git rev-parse HEAD)
  cat > scripts/update-nix-flake.sh <<'STUB'
#!/usr/bin/env bash
printf '{\n  version = "%s";\n  vendorHash = "new";\n}\n' "$1" > nix/package.nix
echo "  vendorHash: updated"
STUB
  git commit -qam "Dependencies moved after the release PR"
  git push -q origin main
  : > "$PUSH_LOG"
  MERGED=$(git rev-parse HEAD)

  run scripts/release.sh 0.2.0
  [ "$status" -eq 1 ]
  [[ "$output" == *"nix/package.nix needs an update"* ]]
  [ -z "$(release_pushes)" ]
  not git rev-parse -q --verify refs/tags/v0.2.0
  [ -z "$(git status --porcelain)" ]
  [ "$(git rev-parse HEAD)" = "$MERGED" ]
}

@test "a stable tag verifies the Nix flake at the commit it tags" {
  scripts/release.sh 0.2.0 >/dev/null
  merge_release_pr 0.2.0
  : > "$LOG"

  run scripts/release.sh 0.2.0
  [ "$status" -eq 0 ]
  grep -q '^update-nix-flake 0.2.0$' "$LOG"
}

@test "a local release branch from an earlier attempt is left alone" {
  git switch -q -c release/v0.2.0
  git commit -q --allow-empty -m "Local work"
  LOCAL_WORK=$(git rev-parse HEAD)
  git switch -q main

  run scripts/release.sh 0.2.0
  [ "$status" -eq 0 ]
  [ "$(git rev-parse release/v0.2.0)" = "$LOCAL_WORK" ]
  [ "$(release_pushes)" = "refs/heads/release/v0.2.0" ]
  [ "$(origin rev-parse release/v0.2.0^)" = "$BASE" ]
  [ "$(git rev-parse --abbrev-ref HEAD)" = "main" ]
}
