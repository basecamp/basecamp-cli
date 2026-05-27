# Basecamp Import Demo

This directory contains demo inputs for the deterministic Basecamp import pipeline.

## Files

Simple spreadsheet-style demo:

- `tasks.csv` — small source CSV with three tasks
- `mapping.json` — confirmed column mapping for `tasks.csv`
- `destination.example.json` — example destination config for an existing project
- `destination-new-project.example.json` — destination config that creates a new demo project

Board-export-shaped demo:

- `board-export.csv` — realistic Trello-shaped board export fixture
- `board-export.mapping.json` — confirmed column mapping for `board-export.csv`
- `board-export.destination.example.json` — example destination config for an existing project
- `board-export.destination-new-project.example.json` — destination config that creates a new demo project

The board-export demo shows the importer handling a service-style export generically. It does not rely on a Trello-specific parser or source mode.

## Simple dry-run demo

```bash
cp demos/import/destination.example.json /tmp/destination.json
# Edit /tmp/destination.json and set project_id to your demo project.
# Or use demos/import/destination-new-project.example.json to create a new demo project during execution.

scripts/demo-import.sh \
  --csv demos/import/tasks.csv \
  --mapping demos/import/mapping.json \
  --destination /tmp/destination.json \
  --out /tmp/basecamp-import-demo
```

## Board-export dry-run demo

```bash
cp demos/import/board-export.destination.example.json /tmp/board-destination.json
# Edit /tmp/board-destination.json and set project_id to your demo project.
# Or use demos/import/board-export.destination-new-project.example.json to create a new demo project during execution.

scripts/demo-import.sh \
  --csv demos/import/board-export.csv \
  --mapping demos/import/board-export.mapping.json \
  --destination /tmp/board-destination.json \
  --out /tmp/basecamp-board-import-demo
```

The script prints the deterministic dry run, checks local artifact status, and stops before writes.

## Preflight and execute demo

After reviewing the dry run, execute against the selected project:

```bash
scripts/demo-import.sh \
  --csv demos/import/board-export.csv \
  --mapping demos/import/board-export.mapping.json \
  --destination /tmp/board-destination.json \
  --out /tmp/basecamp-board-import-demo \
  --execute
```

The script runs `basecamp import preflight --artifact` before asking for approval. If preflight passes, it prompts for approval before running `basecamp import execute --approved`, then prints post-execution status.

## Recovery review demo

If execution fails, the artifact contains `execution.json`. Review the local recovery state without Basecamp reads or writes:

```bash
scripts/demo-import.sh \
  --repair-artifact /tmp/basecamp-board-import-demo
```

The repair review summarizes completed operations, failed operations, pending todos, and guidance.

After reviewing Basecamp state and the repair summary, create a fresh follow-up artifact for pending rows:

```bash
scripts/demo-import.sh \
  --repair-artifact /tmp/basecamp-board-import-demo \
  --followup-out /tmp/basecamp-board-import-followup \
  --reviewed
```

The script creates the follow-up artifact and prints its dry run. Plan, preflight, and execute the follow-up artifact as a separate import.
