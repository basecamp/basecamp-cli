#!/usr/bin/env bash
# Usage: scripts/bump-sdk.sh [REF]
#   REF: git ref in SDK repo (default: main)
#
# Updates go.mod and internal/version/sdk-provenance.json atomically.

set -euo pipefail

REF="${1:-main}"
MODULE="github.com/basecamp/basecamp-sdk/go"
PROVENANCE_FILE="internal/version/sdk-provenance.json"

echo "==> Bumping SDK to ${MODULE}@${REF}"

# 1. Update go.mod
go get "${MODULE}@${REF}"
go mod tidy

# 2. Parse the resolved version from go.mod
RESOLVED=$(grep "${MODULE}" go.mod | head -1 | awk '{print $2}')
if [[ -z "${RESOLVED}" ]]; then
  echo "ERROR: Could not find ${MODULE} in go.mod after update"
  exit 1
fi
echo "    Resolved version: ${RESOLVED}"

# 3. Extract commit hash and timestamp from pseudo-version or tag
#    Pseudo-version format: v0.0.0-YYYYMMDDHHMMSS-abcdef123456
COMMIT=""
TIMESTAMP=""

if [[ "${RESOLVED}" =~ -([0-9]{14})-([0-9a-f]{12})$ ]]; then
  # Pseudo-version: extract timestamp and commit hash
  TS_RAW="${BASH_REMATCH[1]}"
  COMMIT="${BASH_REMATCH[2]}"
  # Convert YYYYMMDDHHMMSS to RFC3339
  TIMESTAMP="${TS_RAW:0:4}-${TS_RAW:4:2}-${TS_RAW:6:2}T${TS_RAW:8:2}:${TS_RAW:10:2}:${TS_RAW:12:2}Z"
  echo "    Commit: ${COMMIT}"
  echo "    Timestamp: ${TIMESTAMP}"
elif [[ "${RESOLVED}" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  # Semver tag: try to resolve commit from go module cache
  echo "    Semver release: ${RESOLVED} (no commit hash in version)"
  COMMIT=""
  TIMESTAMP=""
else
  echo "    Non-standard version format: ${RESOLVED}"
fi

# 4. Try to read API provenance from SDK
API_REPO="basecamp/bc3"
API_REVISION=""
API_SYNCED_AT=""

# Try local path first (go.work dev setup)
LOCAL_API_PROVENANCE="../basecamp-sdk/spec/api-provenance.json"
if [[ -f "${LOCAL_API_PROVENANCE}" ]]; then
  echo "    Reading API provenance from local SDK repo"
  API_REVISION=$(jq -r '.bc3.revision // ""' "${LOCAL_API_PROVENANCE}" 2>/dev/null || echo "")
  API_SYNCED_AT=$(jq -r '.bc3.synced_at // ""' "${LOCAL_API_PROVENANCE}" 2>/dev/null || echo "")
elif command -v gh >/dev/null 2>&1 && [[ -n "${COMMIT}" ]]; then
  # Try remote via GitHub API
  echo "    Fetching API provenance from GitHub (ref: ${COMMIT})"
  API_JSON=$(gh api "repos/basecamp/basecamp-sdk/contents/spec/api-provenance.json?ref=${COMMIT}" \
    --jq '.content' 2>/dev/null | base64 -d 2>/dev/null || echo "")
  if [[ -n "${API_JSON}" ]]; then
    API_REVISION=$(echo "${API_JSON}" | jq -r '.bc3.revision // ""' 2>/dev/null || echo "")
    API_SYNCED_AT=$(echo "${API_JSON}" | jq -r '.bc3.synced_at // ""' 2>/dev/null || echo "")
  else
    echo "    Could not fetch API provenance (file may not exist yet)"
  fi
else
  echo "    Skipping API provenance (no local SDK, no gh CLI, or no commit hash)"
fi

# 5. Read existing API provenance as fallback
if [[ -z "${API_REVISION}" && -f "${PROVENANCE_FILE}" ]]; then
  API_REVISION=$(jq -r '.api.revision // ""' "${PROVENANCE_FILE}" 2>/dev/null || echo "")
  API_SYNCED_AT=$(jq -r '.api.synced_at // ""' "${PROVENANCE_FILE}" 2>/dev/null || echo "")
fi

# 6. Write provenance file
cat > "${PROVENANCE_FILE}" <<EOF
{
  "sdk": {
    "module": "${MODULE}",
    "version": "${RESOLVED}",
    "revision": "${COMMIT}",
    "updated_at": "${TIMESTAMP}"
  },
  "api": {
    "repo": "${API_REPO}",
    "revision": "${API_REVISION}",
    "synced_at": "${API_SYNCED_AT}"
  }
}
EOF

echo ""
echo "==> Updated ${PROVENANCE_FILE}"
echo ""
jq . "${PROVENANCE_FILE}"
