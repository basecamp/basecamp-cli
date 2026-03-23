#!/usr/bin/env bash
set -euo pipefail

# Stamps the CLI release version into .claude-plugin/plugin.json so that
# Claude Code can detect updates. Called by GoReleaser before the build.

VERSION="${1:?Usage: stamp-plugin-version.sh VERSION}"
PLUGIN_JSON=".claude-plugin/plugin.json"

jq --arg v "$VERSION" '.version = $v' "$PLUGIN_JSON" > "${PLUGIN_JSON}.tmp"
mv "${PLUGIN_JSON}.tmp" "$PLUGIN_JSON"
