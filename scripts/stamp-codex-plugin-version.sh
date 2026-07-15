#!/usr/bin/env bash
set -euo pipefail

if ! command -v jq >/dev/null 2>&1; then
  echo "Error: jq is required but not found. Install with your package manager." >&2
  exit 1
fi

# Stamps the CLI release version into the Codex plugin manifest.
# Kept separate from the Claude stamper so each integration remains isolated.

VERSION="${1:?Usage: stamp-codex-plugin-version.sh VERSION}"
PLUGIN_JSON=".codex-plugin/plugin.json"

jq --arg v "$VERSION" '.version = $v' "$PLUGIN_JSON" > "${PLUGIN_JSON}.tmp"
mv "${PLUGIN_JSON}.tmp" "$PLUGIN_JSON"
