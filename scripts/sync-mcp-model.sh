#!/bin/sh
# Sync the vendored basecamp-sdk model snapshot that MCP catalog generation reads.
#
# The catalog derives from basecamp-sdk's behavior model (per-operation traits:
# readonly, idempotent, pagination, retry) joined with its exported OpenAPI
# spec (operationId, method, path, tags, docs, parameter schemas). Both files
# are build products of basecamp-sdk's Smithy model, so we vendor a snapshot
# here rather than parse Smithy ourselves: CI stays hermetic and the reviewed
# diff shows exactly which surface changed when the SDK moves. Keep the
# snapshot in lockstep with the basecamp-sdk version pinned in go.mod —
# TestCatalogModelProvenance enforces it.
#
# Tag patch: the SDK export leaves a handful of operations untagged, and the
# toolkit catalog joins operations to domains by tag (exactly one per
# operation, refused otherwise). Until the tags land upstream in the Smithy
# model, this script assigns them here — the patch tables below are the whole
# divergence from the upstream export, applied on copy and recorded in
# PROVENANCE.json so a future SDK release that tags them upstream shows up as
# a sync-time conflict rather than a silent double-tag.
#
# Exclusions: raw-binary upload operations (multipart/octet-stream bodies)
# can't ride the JSON tool-call convention, and the toolkit refuses non-JSON
# bodies at load, so they are dropped from both model files here. Uploads
# stay a CLI affair (basecamp attach / upload).
#
# Usage: scripts/sync-mcp-model.sh [path-to-basecamp-sdk-checkout]
set -eu

sdk="${1:-../basecamp-sdk}"
dest="$(dirname "$0")/../internal/mcpserver/model"

# Resolve provenance before touching the destination, so a checkout that is
# not a git repo (or otherwise broken) can't leave a torn snapshot behind.
# --dirty marks a checkout with uncommitted changes, so modified model files
# are never recorded as a clean release tag.
commit=$(git -C "$sdk" rev-parse HEAD)
ref=$(git -C "$sdk" describe --tags --always --dirty)

for f in behavior-model.json openapi.json; do
  [ -f "$sdk/$f" ] || { echo "missing $sdk/$f (pass a basecamp-sdk checkout path)" >&2; exit 1; }
done

# Copy both model files, assigning tags to the operations the export leaves
# untagged and dropping the raw-binary upload operations. Refuses an
# operation that grew an upstream tag (drop it from the table then) and
# refuses any untagged operation the table doesn't cover, so the patches can
# only shrink as upstream catches up.
python3 - "$sdk" "$dest" <<'PY'
import json, sys

EXCLUDED_OPERATIONS = {
    "CreateAttachment",
    "CreateCampfireUpload",
    "UpdateAccountLogo",
}

PATCHED_TAGS = {
    "GetAnswersByPerson": "Automation",
    "GetQuestionReminders": "Automation",
    "ListQuestionAnswerers": "Automation",
    "PauseQuestion": "Automation",
    "ResumeQuestion": "Automation",
    "UpdateQuestionNotificationSettings": "Automation",
    "RepositionTodo": "Todos",
    "SubscribeToCardColumn": "Card Tables",
    "UnsubscribeFromCardColumn": "Card Tables",
    "GetAssignedTodos": "Reports",
    "GetOverdueTodos": "Reports",
    "GetPersonProgress": "Reports",
    "GetProgressReport": "Reports",
    "GetProjectTimeline": "Reports",
    "GetUpcomingSchedule": "Reports",
    "ListAssignablePeople": "Reports",
}

sdk, dest = sys.argv[1], sys.argv[2]
with open(f"{sdk}/openapi.json") as f:
    doc = json.load(f)

patched = []
excluded = []
for path in list(doc["paths"]):
    methods = doc["paths"][path]
    for method in list(methods):
        op = methods[method]
        if not isinstance(op, dict) or "operationId" not in op:
            continue
        oid = op["operationId"]
        if oid in EXCLUDED_OPERATIONS:
            body = op.get("requestBody", {}).get("content", {})
            if "application/json" in body or not body:
                sys.exit(f"{oid} takes a JSON body now — drop it from EXCLUDED_OPERATIONS")
            del methods[method]
            excluded.append(oid)
            continue
        tags = op.get("tags") or []
        if oid in PATCHED_TAGS:
            if tags:
                sys.exit(f"{oid} is tagged {tags} upstream now — drop it from PATCHED_TAGS")
            op["tags"] = [PATCHED_TAGS[oid]]
            patched.append(oid)
        elif not tags:
            sys.exit(f"{oid} is untagged and not in PATCHED_TAGS — assign it a domain tag")
    if not methods:
        del doc["paths"][path]

missing = sorted(set(PATCHED_TAGS) - set(patched))
if missing:
    sys.exit(f"PATCHED_TAGS entries not found in the export: {missing}")
missing = sorted(set(EXCLUDED_OPERATIONS) - set(excluded))
if missing:
    sys.exit(f"EXCLUDED_OPERATIONS entries not found in the export: {missing}")

with open(f"{dest}/openapi.json", "w") as f:
    json.dump(doc, f, indent=2)
    f.write("\n")

# The catalog join is strict both ways, so the excluded operations leave the
# behavior model too.
with open(f"{sdk}/behavior-model.json") as f:
    bm = json.load(f)
for oid in EXCLUDED_OPERATIONS:
    bm["operations"].pop(oid, None)
with open(f"{dest}/behavior-model.json", "w") as f:
    json.dump(bm, f, indent=2)
    f.write("\n")

print(f"patched tags onto {len(patched)} untagged operations; excluded {len(excluded)} binary-upload operations")
PY

cat > "$dest/PROVENANCE.json" <<JSON
{
  "source": "github.com/basecamp/basecamp-sdk",
  "commit": "$commit",
  "ref": "$ref",
  "files": ["behavior-model.json", "openapi.json"],
  "synced_by": "scripts/sync-mcp-model.sh",
  "patches": "tags assigned to operations the export leaves untagged (PATCHED_TAGS); binary-upload operations dropped (EXCLUDED_OPERATIONS) — see the sync script"
}
JSON

echo "synced model from basecamp-sdk @ $ref ($commit)"
