#!/usr/bin/env bash
# session-start.sh - Basecamp plugin liveness check
#
# Lightweight: one subprocess call. Confirms the CLI is available and,
# when jq is installed, whether it is authenticated. Context priming
# (project IDs, etc.) happens on first use via the /basecamp skill,
# not here.

set -euo pipefail

if ! command -v basecamp &>/dev/null; then
  cat << 'EOF'
<hook-output>
Basecamp plugin active — CLI not found on PATH.
Install: https://github.com/basecamp/basecamp-cli#installation
</hook-output>
EOF
  exit 0
fi

# Single subprocess: auth status tells us if we're good to go
auth_json=$(basecamp auth status --json 2>/dev/null || echo '{}')

if ! command -v jq &>/dev/null; then
  # No jq — can't parse, just confirm presence
  cat << 'EOF'
<hook-output>
Basecamp plugin active.
</hook-output>
EOF
  exit 0
fi

is_auth=false
if parsed_auth=$(echo "$auth_json" | jq -er '.data.authenticated' 2>/dev/null); then
  is_auth="$parsed_auth"
fi

if [[ "$is_auth" == "true" ]]; then
  cat << 'EOF'
<hook-output>
Basecamp plugin active.
</hook-output>
EOF
else
  cat << 'EOF'
<hook-output>
Basecamp plugin active — not authenticated.
Run: basecamp auth login
</hook-output>
EOF
fi
