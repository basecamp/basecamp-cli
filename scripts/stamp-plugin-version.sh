#!/usr/bin/env bash
set -euo pipefail

if ! command -v jq >/dev/null 2>&1; then
  echo "Error: jq is required but not found. Install with your package manager." >&2
  exit 1
fi

# Stamps the CLI release version into .claude-plugin/plugin.json so that
# Claude Code can detect updates. Called by GoReleaser before the build.

VERSION="${1:?Usage: stamp-plugin-version.sh VERSION}"
PLUGIN_JSON=".claude-plugin/plugin.json"

TEMP_FILE=$(mktemp "${PLUGIN_JSON}.tmp.XXXXXX")
trap 'rm -f "${TEMP_FILE}"' EXIT

jq --arg v "$VERSION" '.version = $v' "$PLUGIN_JSON" > "${TEMP_FILE}"
mv "${TEMP_FILE}" "$PLUGIN_JSON"
trap - EXIT
