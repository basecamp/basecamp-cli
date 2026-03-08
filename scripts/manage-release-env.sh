#!/usr/bin/env bash
#
# manage-release-env.sh — Audit and enforce release environment protection
#
# Usage:
#   ./manage-release-env.sh audit           # Print current state per repo
#   ./manage-release-env.sh apply           # Converge to desired state
#   ./manage-release-env.sh migrate-secrets # Move secrets from repo to env scope
#     --op-vault VAULT                      # 1Password vault name
#     [--delete-old]                        # Delete repo-scoped copies after env write
#   ./manage-release-env.sh migrate-vars    # Move vars from repo to env scope
#     [--delete-old]                        # Delete repo-scoped copies after env write
#
# Requires: gh (authenticated), jq, op (for migrate-secrets)
#
set -euo pipefail

# ── Repo manifest ────────────────────────────────────────────────────────────
# Each entry: repo|env_name|deploy_ref_pattern|reviewer_team|admin_bypass
MANIFEST=(
  "basecamp/basecamp-cli|release|v*|basecamp/sip|false"
  "basecamp/hey-cli|release|v*|basecamp/sip|false"
  "basecamp/fizzy-cli|release|v*|basecamp/sip|false"
  "basecamp/cli|release|v*|basecamp/sip|false"
)

# Secrets to migrate per repo (repo|secret_name|op_reference)
# op_reference is "vault/item/field" — resolved via: op read "op://VAULT/item/field"
SECRETS_MANIFEST=(
  "basecamp/basecamp-cli|RELEASE_APP_PRIVATE_KEY|Release GitHub App/private-key"
  "basecamp/basecamp-cli|MACOS_SIGN_P12|macOS Code Signing/p12-base64"
  "basecamp/basecamp-cli|MACOS_SIGN_PASSWORD|macOS Code Signing/password"
  "basecamp/basecamp-cli|MACOS_NOTARY_KEY|macOS Notarization/api-key"
  "basecamp/basecamp-cli|MACOS_NOTARY_KEY_ID|macOS Notarization/key-id"
  "basecamp/basecamp-cli|MACOS_NOTARY_ISSUER_ID|macOS Notarization/issuer-id"
  "basecamp/basecamp-cli|AUR_KEY|AUR SSH Key/private-key"
  "basecamp/hey-cli|RELEASE_APP_PRIVATE_KEY|Release GitHub App/private-key"
  "basecamp/hey-cli|MACOS_SIGN_P12|macOS Code Signing/p12-base64"
  "basecamp/hey-cli|MACOS_SIGN_PASSWORD|macOS Code Signing/password"
  "basecamp/hey-cli|MACOS_NOTARY_KEY|macOS Notarization/api-key"
  "basecamp/hey-cli|MACOS_NOTARY_KEY_ID|macOS Notarization/key-id"
  "basecamp/hey-cli|MACOS_NOTARY_ISSUER_ID|macOS Notarization/issuer-id"
  "basecamp/hey-cli|AUR_KEY|AUR SSH Key/private-key"
  "basecamp/fizzy-cli|RELEASE_APP_PRIVATE_KEY|Release GitHub App/private-key"
  "basecamp/fizzy-cli|MACOS_SIGN_P12|macOS Code Signing/p12-base64"
  "basecamp/fizzy-cli|MACOS_SIGN_PASSWORD|macOS Code Signing/password"
  "basecamp/fizzy-cli|MACOS_NOTARY_KEY|macOS Notarization/api-key"
  "basecamp/fizzy-cli|MACOS_NOTARY_KEY_ID|macOS Notarization/key-id"
  "basecamp/fizzy-cli|MACOS_NOTARY_ISSUER_ID|macOS Notarization/issuer-id"
  "basecamp/fizzy-cli|AUR_KEY|AUR SSH Key/private-key"
  "basecamp/cli|SKILLS_APP_PRIVATE_KEY|Skills GitHub App/private-key"
)

# Vars to migrate per repo (repo|var_name)
VARS_MANIFEST=(
  "basecamp/basecamp-cli|RELEASE_CLIENT_ID"
  "basecamp/hey-cli|RELEASE_APP_ID"
  "basecamp/fizzy-cli|RELEASE_CLIENT_ID"
  "basecamp/cli|SKILLS_APP_ID"
)

# ── Helpers ──────────────────────────────────────────────────────────────────

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[0;33m'
CYAN='\033[0;36m'
NC='\033[0m'

ok()   { printf "${GREEN}✓${NC} %s\n" "$*"; }
warn() { printf "${YELLOW}!${NC} %s\n" "$*"; }
fail() { printf "${RED}✗${NC} %s\n" "$*"; }
info() { printf "${CYAN}→${NC} %s\n" "$*"; }

die() { fail "$@"; exit 1; }

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || die "Required command not found: $1"
}

# ── Environment operations ───────────────────────────────────────────────────

get_env() {
  local repo=$1 env_name=$2
  gh api "repos/$repo/environments/$env_name" 2>/dev/null || echo ""
}

env_exists() {
  local repo=$1 env_name=$2
  gh api "repos/$repo/environments/$env_name" --silent 2>/dev/null
}

# ── Audit ────────────────────────────────────────────────────────────────────

audit_repo() {
  local repo=$1 env_name=$2 deploy_ref=$3 reviewer=$4 admin_bypass=$5
  local drift=0

  printf "\n${CYAN}═══ %s ═══${NC}\n" "$repo"

  local env_json
  env_json=$(get_env "$repo" "$env_name")

  if [ -z "$env_json" ]; then
    fail "Environment '$env_name' does not exist"
    return 1
  fi
  ok "Environment '$env_name' exists"

  # Reviewers
  local reviewer_count
  reviewer_count=$(echo "$env_json" | jq '[.protection_rules // [] | .[] | select(.type == "required_reviewers")] | .[0].reviewers | length // 0')
  if [ "$reviewer_count" -gt 0 ]; then
    local reviewer_names
    reviewer_names=$(echo "$env_json" | jq -r '[.protection_rules // [] | .[] | select(.type == "required_reviewers")] | .[0].reviewers // [] | [.[].reviewer | .slug // .login] | join(", ")' 2>/dev/null || echo "")
    ok "Required reviewers ($reviewer_count): $reviewer_names"

    # Verify expected team is a reviewer
    local expected_slug
    expected_slug=$(echo "$reviewer" | cut -d/ -f2)
    if ! echo "$reviewer_names" | grep -q "$expected_slug"; then
      fail "Expected reviewer '$reviewer' not found (have: $reviewer_names)"
      drift=1
    fi
  else
    fail "No required reviewers configured (want: $reviewer)"
    drift=1
  fi

  # Deployment branch policy
  local branch_policy
  branch_policy=$(echo "$env_json" | jq '.deployment_branch_policy // empty')
  if [ -n "$branch_policy" ]; then
    local custom
    custom=$(echo "$branch_policy" | jq '.custom_branch_policies')
    if [ "$custom" = "true" ]; then
      local policies
      policies=$(gh api "repos/$repo/environments/$env_name/deployment-branch-policies" \
        --jq '[.branch_policies[] | "\(.name) (\(.type))"] | join(", ")' 2>/dev/null || echo "")
      if [ -n "$policies" ]; then
        ok "Deployment policy: $policies"
      else
        warn "Custom branch policies enabled but none defined"
      fi
    else
      ok "Deployment policy: protected branches only"
    fi
  else
    warn "No deployment branch policy (any branch/tag can deploy)"
  fi

  # Admin bypass
  local prevent_self_review
  prevent_self_review=$(echo "$env_json" | jq '[.protection_rules // [] | .[] | select(.type == "required_reviewers")] | .[0].prevent_self_review // false' 2>/dev/null)
  local want_prevent="true"
  if [ "$admin_bypass" = "true" ]; then
    want_prevent="false"
  fi
  if [ "$prevent_self_review" = "$want_prevent" ]; then
    ok "Admin bypass: $([ "$admin_bypass" = "true" ] && echo "allowed (intended)" || echo "blocked")"
  else
    fail "Admin bypass: $([ "$prevent_self_review" = "true" ] && echo "blocked" || echo "allowed") (want: $([ "$admin_bypass" = "true" ] && echo "allowed" || echo "blocked"))"
    drift=1
  fi

  # Secrets at env scope
  local env_secrets_json
  env_secrets_json=$(gh api "repos/$repo/environments/$env_name/secrets" 2>/dev/null || echo '{"secrets":[]}')
  local env_secrets
  env_secrets=$(echo "$env_secrets_json" | jq -r '[.secrets // [] | .[].name] | join(", ")' 2>/dev/null || echo "")
  if [ -n "$env_secrets" ]; then
    ok "Env-scoped secrets: $env_secrets"
  else
    warn "No secrets in environment scope"
  fi

  # Secrets at repo scope (should be empty after migration)
  local repo_secrets
  repo_secrets=$(gh secret list --repo "$repo" --json name --jq '[.[].name] | join(", ")' 2>/dev/null || echo "")
  if [ -n "$repo_secrets" ]; then
    warn "Repo-scoped secrets still present: $repo_secrets"
  else
    ok "No repo-scoped secrets"
  fi

  # Vars
  local env_vars_json
  env_vars_json=$(gh api "repos/$repo/environments/$env_name/variables" 2>/dev/null || echo '{"variables":[]}')
  local env_vars
  env_vars=$(echo "$env_vars_json" | jq -r '[.variables // [] | .[].name] | join(", ")' 2>/dev/null || echo "")
  if [ -n "$env_vars" ]; then
    ok "Env-scoped vars: $env_vars"
  else
    info "No vars in environment scope"
  fi

  return "$drift"
}

cmd_audit() {
  echo "Release Environment Audit"
  echo "========================="

  local failures=0
  for entry in "${MANIFEST[@]}"; do
    IFS='|' read -r repo env_name deploy_ref reviewer admin_bypass <<< "$entry"
    audit_repo "$repo" "$env_name" "$deploy_ref" "$reviewer" "$admin_bypass" || failures=$((failures + 1))
  done

  echo ""
  if [ "$failures" -gt 0 ]; then
    die "$failures repo(s) have configuration drift"
  fi
}

# ── Apply ────────────────────────────────────────────────────────────────────

apply_repo() {
  local repo=$1 env_name=$2 deploy_ref=$3 reviewer=$4 admin_bypass=$5
  local drift=0

  printf "\n${CYAN}═══ %s ═══${NC}\n" "$repo"

  # Resolve reviewer team to ID
  local org team_slug team_id
  org=$(echo "$reviewer" | cut -d/ -f1)
  team_slug=$(echo "$reviewer" | cut -d/ -f2)
  team_id=$(gh api "orgs/$org/teams/$team_slug" --jq '.id' 2>/dev/null || echo "")
  if [ -z "$team_id" ]; then
    fail "Could not resolve team $reviewer to an ID"
    return 1
  fi

  local prevent_self_review="true"
  if [ "$admin_bypass" = "true" ]; then
    prevent_self_review="false"
  fi

  # Single PUT with all protection settings to avoid overwrite race
  info "Applying environment config (reviewers=$reviewer, deploy=$deploy_ref, prevent_self_review=$prevent_self_review)"
  gh api --method PUT "repos/$repo/environments/$env_name" \
    --input <(jq -n \
      --argjson team_id "$team_id" \
      --argjson prevent "$prevent_self_review" \
      '{
        reviewers: [{ type: "Team", id: $team_id }],
        deployment_branch_policy: {
          protected_branches: false,
          custom_branch_policies: true
        },
        prevent_self_review: $prevent
      }') >/dev/null

  # Set deployment tag policy (must be done after enabling custom_branch_policies)
  # Remove any existing policies first
  local existing
  existing=$(gh api "repos/$repo/environments/$env_name/deployment-branch-policies" \
    --jq '.branch_policies[].id' 2>/dev/null || true)
  for id in $existing; do
    gh api --method DELETE \
      "repos/$repo/environments/$env_name/deployment-branch-policies/$id" \
      >/dev/null 2>/dev/null || true
  done

  # Add tag policy
  gh api --method POST \
    "repos/$repo/environments/$env_name/deployment-branch-policies" \
    --field "name=$deploy_ref" \
    --field "type=tag" >/dev/null
  ok "Deployment tag policy set: $deploy_ref"

  # Read back and verify
  info "Verifying applied state..."
  local env_json
  env_json=$(get_env "$repo" "$env_name")

  if [ -z "$env_json" ]; then
    fail "DRIFT: Environment '$env_name' not found after creation"
    return 1
  fi

  # Verify reviewers
  local reviewer_count
  reviewer_count=$(echo "$env_json" | jq '[.protection_rules // [] | .[] | select(.type == "required_reviewers")] | .[0].reviewers | length // 0')
  if [ "$reviewer_count" -eq 0 ]; then
    fail "DRIFT: Reviewers not set after apply"
    drift=1
  else
    ok "Reviewers verified"
  fi

  # Verify deployment policy
  local custom
  custom=$(echo "$env_json" | jq '.deployment_branch_policy.custom_branch_policies // false')
  if [ "$custom" != "true" ]; then
    fail "DRIFT: Custom branch policy not enabled after apply"
    drift=1
  else
    local policy_count
    policy_count=$(gh api "repos/$repo/environments/$env_name/deployment-branch-policies" \
      --jq '.branch_policies | length' 2>/dev/null || echo "0")
    if [ "$policy_count" -eq 0 ]; then
      fail "DRIFT: No deployment policies after apply"
      drift=1
    else
      ok "Deployment policy verified ($policy_count rules)"
    fi
  fi

  # Verify admin bypass
  local prevent_self_review
  prevent_self_review=$(echo "$env_json" | jq '[.protection_rules // [] | .[] | select(.type == "required_reviewers")] | .[0].prevent_self_review // false' 2>/dev/null)
  local want_prevent="true"
  if [ "$admin_bypass" = "true" ]; then
    want_prevent="false"
  fi
  if [ "$prevent_self_review" = "$want_prevent" ]; then
    ok "Admin bypass: $([ "$admin_bypass" = "true" ] && echo "allowed (intended)" || echo "blocked")"
  else
    fail "DRIFT: Admin bypass is $([ "$prevent_self_review" = "true" ] && echo "blocked" || echo "allowed"), want $([ "$admin_bypass" = "true" ] && echo "allowed" || echo "blocked")"
    drift=1
  fi

  # Print exact post-apply state
  local reviewer_names
  reviewer_names=$(echo "$env_json" | jq -r '[.protection_rules // [] | .[] | select(.type == "required_reviewers")] | .[0].reviewers // [] | [.[].reviewer | .slug // .login] | join(", ")' 2>/dev/null || echo "")
  local policies
  policies=$(gh api "repos/$repo/environments/$env_name/deployment-branch-policies" \
    --jq '[.branch_policies[] | "\(.name) (\(.type))"] | join(", ")' 2>/dev/null || echo "none")
  info "Post-apply: reviewers=[$reviewer_names] deploy=[$policies] admin_bypass=$admin_bypass self_review_prevention=$prevent_self_review"

  if [ "$drift" -ne 0 ]; then
    fail "State drift detected on $repo — manual investigation required"
    return 1
  fi
  ok "All protections converged for $repo"
}

cmd_apply() {
  echo "Release Environment Apply"
  echo "========================="

  local failures=0
  for entry in "${MANIFEST[@]}"; do
    IFS='|' read -r repo env_name deploy_ref reviewer admin_bypass <<< "$entry"
    if ! apply_repo "$repo" "$env_name" "$deploy_ref" "$reviewer" "$admin_bypass"; then
      failures=$((failures + 1))
    fi
  done

  echo ""
  if [ "$failures" -gt 0 ]; then
    die "$failures repo(s) failed to converge"
  fi
  ok "All repos converged successfully"
}

# ── Migrate secrets ──────────────────────────────────────────────────────────

cmd_migrate_secrets() {
  require_cmd op

  local op_vault=""
  local delete_old=false

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --op-vault) op_vault="$2"; shift 2 ;;
      --delete-old) delete_old=true; shift ;;
      *) die "Unknown option: $1" ;;
    esac
  done

  if [ -z "$op_vault" ]; then
    die "Required: --op-vault VAULT"
  fi

  echo "Secret Migration (repo → release environment)"
  echo "==============================================="
  echo "1Password vault: $op_vault"
  echo "Delete repo-scoped after write: $delete_old"
  echo ""

  local failures=0
  for entry in "${SECRETS_MANIFEST[@]}"; do
    IFS='|' read -r repo secret_name op_ref <<< "$entry"
    local env_name="release"

    printf "\n${CYAN}%s${NC} / %s\n" "$repo" "$secret_name"

    # Ensure environment exists
    if ! env_exists "$repo" "$env_name"; then
      fail "Environment '$env_name' does not exist on $repo — run 'apply' first"
      failures=$((failures + 1))
      continue
    fi

    # Read from 1Password and write to env scope
    info "Reading from op://$op_vault/$op_ref"
    local value
    if ! value=$(op read "op://$op_vault/$op_ref" 2>/dev/null); then
      fail "Failed to read from 1Password: op://$op_vault/$op_ref"
      failures=$((failures + 1))
      continue
    fi

    info "Writing to env-scoped secret"
    if ! printf '%s' "$value" | gh secret set "$secret_name" --env "$env_name" --repo "$repo"; then
      fail "Failed to set env-scoped secret"
      failures=$((failures + 1))
      continue
    fi
    ok "Env-scoped secret written"

    # Verify it was written
    local exists
    exists=$(gh api "repos/$repo/environments/$env_name/secrets/$secret_name" --jq '.name' 2>/dev/null || echo "")
    if [ "$exists" != "$secret_name" ]; then
      fail "DRIFT: Secret not found at env scope after write"
      failures=$((failures + 1))
      continue
    fi
    ok "Verified at env scope"

    # Delete repo-scoped copy
    if [ "$delete_old" = "true" ]; then
      info "Deleting repo-scoped copy"
      if gh secret delete "$secret_name" --repo "$repo" 2>/dev/null; then
        ok "Repo-scoped copy deleted"
      else
        warn "Repo-scoped copy not found or already deleted"
      fi
    else
      info "Skipping repo-scoped deletion (pass --delete-old to remove)"
    fi
  done

  echo ""
  if [ "$failures" -gt 0 ]; then
    die "$failures secret(s) failed to migrate"
  fi
  ok "All secrets migrated successfully"
}

# ── Migrate vars ─────────────────────────────────────────────────────────────

cmd_migrate_vars() {
  local delete_old=false

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --delete-old) delete_old=true; shift ;;
      *) die "Unknown option: $1" ;;
    esac
  done

  echo "Variable Migration (repo → release environment)"
  echo "================================================"
  echo "Delete repo-scoped after write: $delete_old"
  echo ""

  local failures=0
  for entry in "${VARS_MANIFEST[@]}"; do
    IFS='|' read -r repo var_name <<< "$entry"
    local env_name="release"

    printf "\n${CYAN}%s${NC} / %s\n" "$repo" "$var_name"

    # Ensure environment exists
    if ! env_exists "$repo" "$env_name"; then
      fail "Environment '$env_name' does not exist on $repo — run 'apply' first"
      failures=$((failures + 1))
      continue
    fi

    # Read current value
    local value
    if ! value=$(gh variable get "$var_name" --repo "$repo" 2>/dev/null); then
      fail "Could not read repo-scoped var $var_name"
      failures=$((failures + 1))
      continue
    fi
    ok "Read repo value: $value"

    # Write to env scope
    info "Writing to env-scoped variable"
    if ! gh variable set "$var_name" --env "$env_name" --repo "$repo" --body "$value"; then
      fail "Failed to set env-scoped variable"
      failures=$((failures + 1))
      continue
    fi
    ok "Env-scoped variable written"

    # Verify
    local readback
    readback=$(gh api "repos/$repo/environments/$env_name/variables/$var_name" --jq '.value' 2>/dev/null || echo "")
    if [ "$readback" != "$value" ]; then
      fail "DRIFT: Variable value mismatch after write (got '$readback', expected '$value')"
      failures=$((failures + 1))
      continue
    fi
    ok "Verified at env scope"

    # Delete repo-scoped copy
    if [ "$delete_old" = "true" ]; then
      info "Deleting repo-scoped copy"
      if gh variable delete "$var_name" --repo "$repo" 2>/dev/null; then
        ok "Repo-scoped copy deleted"
      else
        warn "Repo-scoped copy not found or already deleted"
      fi
    else
      info "Skipping repo-scoped deletion (pass --delete-old to remove)"
    fi
  done

  echo ""
  if [ "$failures" -gt 0 ]; then
    die "$failures variable(s) failed to migrate"
  fi
  ok "All variables migrated successfully"
}

# ── Main ─────────────────────────────────────────────────────────────────────

require_cmd gh
require_cmd jq

case "${1:-}" in
  audit)           cmd_audit ;;
  apply)           cmd_apply ;;
  migrate-secrets) shift; cmd_migrate_secrets "$@" ;;
  migrate-vars)    shift; cmd_migrate_vars "$@" ;;
  *)
    echo "Usage: $0 {audit|apply|migrate-secrets|migrate-vars}"
    echo ""
    echo "Commands:"
    echo "  audit             Print current environment state per repo"
    echo "  apply             Create environments and enforce protection rules"
    echo "  migrate-secrets   Move secrets from repo to env scope (requires --op-vault)"
    echo "  migrate-vars      Move variables from repo to env scope"
    echo ""
    echo "Options for migrate-secrets:"
    echo "  --op-vault VAULT  1Password vault name (required)"
    echo "  --delete-old      Delete repo-scoped copies after env write succeeds"
    echo ""
    echo "Options for migrate-vars:"
    echo "  --delete-old      Delete repo-scoped copies after env write succeeds"
    exit 1
    ;;
esac
