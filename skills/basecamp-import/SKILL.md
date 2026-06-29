---
name: basecamp-import
description: |
  Import task and project tracking CSVs into Basecamp using deterministic Basecamp CLI import artifacts.
  Use for CSV imports, task migrations, spreadsheet-to-Basecamp imports, and validated import dry-runs.
triggers:
  - basecamp import
  - import csv to basecamp
  - import tasks to basecamp
  - migrate tasks to basecamp
  - spreadsheet to basecamp
  - csv task import
  - basecamp import artifact
  - basecamp import dry run
invocable: true
argument-hint: "[csv path or import action]"
---

# Basecamp CSV Import

Use this skill to turn CSV exports from spreadsheets, task apps, and internal tools into validated Basecamp todos or cards.

The import pipeline is deterministic:

```text
raw CSV
  → inspect
  → confirmed mapping + destination
  → compile validated Basecamp import artifact
  → plan dry run
  → preflight readiness check
  → execute after explicit approval
```

## Non-negotiable Rules

1. **Never hand-parse CSVs.** Use `basecamp import inspect` for row counts, columns, samples, warnings, and mapping candidates.
2. **Never invent counts or dry-run text.** Use planner output, especially `data.dry_run_markdown`.
3. **Use column indexes from inspection.** Duplicate headers are distinguished by index.
4. **Compile the artifact before planning execution.** The artifact is the import source of truth.
5. **Execute only after explicit user approval.** The execute command requires `--approved`; ask the user before running it.
6. **Preserve unmapped useful data.** Prefer `"custom_fields": "all_unmapped_columns"` unless the user chooses otherwise.
7. **Treat assignees carefully.** Native Basecamp assignment requires Basecamp person IDs. The current artifact preserves assignee emails/names as metadata.

## Commands

| Step | Command |
|------|---------|
| Inspect CSV | `basecamp import inspect <csv-path> --json` |
| Compile artifact | `basecamp import compile --inspection inspection.json --mapping mapping.json --destination destination.json --out basecamp-import/ --json` |
| Plan artifact | `basecamp import plan --artifact basecamp-import/ --json` |
| Show artifact status | `basecamp import status --artifact basecamp-import/ --json` |
| Review repair state | `basecamp import repair --artifact basecamp-import/ --json` |
| Create follow-up artifact | `basecamp import followup --artifact basecamp-import/ --out followup-import/ --reviewed --json` |
| Preflight artifact | `basecamp import preflight --artifact basecamp-import/ --json` |
| Execute approved import | `basecamp import execute --artifact basecamp-import/ --approved --json` |

## Workflow

### 1. Confirm the source CSV

Ask for the path to the CSV export. If the user has multiple CSVs, process one at a time unless a validated multi-file artifact workflow exists.

### 2. Inspect the CSV

```bash
basecamp import inspect ./tasks.csv --json
```

Use the returned JSON as the factual source. Explain:

- `row_count`
- columns by index and name
- duplicate headers
- likely role candidates
- warnings
- returned mapping questions

The inspection can return no obvious title candidate. That is safe: ask the user which non-empty text column should become the todo title.

### 3. Confirm mappings with the user

Ask whether the CSV rows should become Basecamp todos or Basecamp cards. Create `mapping.json` from confirmed answers. At minimum, `title` is required.

Example:

```json
{
  "schema_version": 1,
  "record_id": { "column_index": 0, "column_name": "Task ID" },
  "title": { "column_index": 1, "column_name": "Task Name" },
  "description": { "column_index": 2, "column_name": "Notes" },
  "todolist": { "column_index": 3, "column_name": "Section" },
  "status": { "column_index": 4, "column_name": "Status" },
  "assignees": {
    "column_index": 5,
    "column_name": "Owner",
    "mapping_policy": "leave_unassigned_when_ambiguous"
  },
  "due_on": { "column_index": 6, "column_name": "Due Date" },
  "attachment_urls": [{ "column_index": 7, "column_name": "Link" }],
  "custom_fields": "all_unmapped_columns"
}
```

Mapping guidance:

- `record_id`: stable source ID, if available.
- `title`: required todo or card title.
- `description`: long notes/body/content.
- `todolist`: grouping column for todo imports, such as list, section, phase, stream, room, area, or project.
- `column`: grouping column for card imports. The importer also accepts `todolist` as the grouping mapping for card imports.
- `status`: source status preserved as metadata.
- `assignees`: emails or names preserved as metadata unless Basecamp person IDs are available in a later workflow.
- `due_on`: due/deadline column. Compile normalizes deterministic date values to `YYYY-MM-DD`. For ambiguous slash dates such as `06/01/2026`, add `"date_order": "mdy"` or `"date_order": "dmy"` after confirming the source convention with the user.
- `attachment_urls`: URL-like fields preserved in metadata.
- `custom_fields`: use `all_unmapped_columns` to preserve non-empty unmapped columns.

### 4. Confirm destination

Create `destination.json` from the user's Basecamp destination choice.

Existing project with todolists created from a CSV column:

```json
{
  "schema_version": 1,
  "mode": "existing_project",
  "project_id": "12345",
  "todolist_strategy": "create_from_column"
}
```

Existing project and existing todolist:

```json
{
  "schema_version": 1,
  "mode": "existing_project",
  "project_id": "12345",
  "todolist_strategy": "existing_todolist",
  "todolist_id": "67890",
  "todolist_name": "Imported todos"
}
```

Existing project with cards created in card table columns from a CSV column:

```json
{
  "schema_version": 1,
  "resource_type": "cards",
  "mode": "existing_project",
  "project_id": "12345",
  "card_table_id": "67890",
  "column_strategy": "create_from_column"
}
```

Existing project and existing card table column:

```json
{
  "schema_version": 1,
  "resource_type": "cards",
  "mode": "existing_project",
  "project_id": "12345",
  "card_table_id": "67890",
  "column_strategy": "existing_column",
  "column_id": "24680",
  "column_name": "To do"
}
```

New project:

```json
{
  "schema_version": 1,
  "mode": "new_project",
  "project_name": "Imported tasks",
  "todolist_strategy": "create_from_column"
}
```

### 5. Compile the validated artifact

```bash
basecamp import compile \
  --inspection inspection.json \
  --mapping mapping.json \
  --destination destination.json \
  --out basecamp-import/ \
  --json
```

The artifact contains:

```text
basecamp-import/
├── import.json
└── todos.csv     # todo imports

basecamp-import/
├── import.json
└── cards.csv     # card imports
```

The artifact format is `basecamp-import-csv-v1`. Treat it as the durable checkpoint for the import.

If compile fails, fix the mapping or destination with the user. Date errors name the source row and can be resolved by correcting the source date or by adding `date_order` to the `due_on` mapping when slash dates are ambiguous. Do not proceed to plan or execute.

### 6. Plan from the artifact

```bash
basecamp import plan --artifact basecamp-import/ --json
```

Present `data.dry_run_markdown` verbatim.

### 7. Check local artifact status

```bash
basecamp import status --artifact basecamp-import/ --json
```

If status reports `completed`, `failed`, or `started`, explain the execution ledger and do not execute the artifact again.

For failed or partial executions, run the local repair review:

```bash
basecamp import repair --artifact basecamp-import/ --json
```

Use `completed_operations`, `failed_operations`, `pending_todos`, and `pending_cards` to explain what needs manual review before a fresh follow-up artifact is created.

After the user confirms they reviewed Basecamp state and the repair summary, create a fresh follow-up artifact for pending rows:

```bash
basecamp import followup --artifact basecamp-import/ --out followup-import/ --reviewed --json
```

Plan and preflight the follow-up artifact before execution. Do not remove `execution.json` from the source artifact.

### 8. Preflight the artifact

```bash
basecamp import preflight --artifact basecamp-import/ --json
```

If preflight returns `status: "blocked"`, resolve the reported blocker before execution. Todolist or card column name collisions mean the destination project already has a group with a name the artifact plans to create. Todo or card title collisions mean an existing destination group already contains a record with a title the artifact plans to import.

Then ask:

```text
Do you approve executing this import into Basecamp?
```

Do not execute unless the user clearly approves.

### 9. Execute after approval

```bash
basecamp import execute --artifact basecamp-import/ --approved --json
```

Summarize:

- `created.projects`
- `created.todolists`
- `created.todos`
- `created.card_columns`
- `created.cards`
- `skipped`

Skipped assignees mean the source assignee values were preserved as metadata but not assigned natively.

## Safety and Failure Handling

- If inspection warnings mention duplicate headers, use column indexes in all mapping references.
- When including `column_name`, copy the inspected column name for that exact `column_index`; compile validates that they match.
- If planning or compile says user input is required, ask only the returned questions.
- If the CSV changed after inspection, compile rejects the fingerprint; inspect the current CSV again.
- Use `basecamp import status --artifact` to read local artifact and execution ledger state without Basecamp access.
- Execution writes `execution.json` in the artifact directory and refuses to run again when that ledger exists.
- If execution fails on a row, report the source row from the error and stop. Treat a failed `execution.json` as evidence of possible partial Basecamp writes, and use its operation records to explain what was created before the failure.
- If the user asks for a dry run only, stop after `basecamp import plan --artifact`.

## Output Discipline

Use `--json` for every command. For planning, present the deterministic `dry_run_markdown`; do not rewrite counts or operations from memory.
