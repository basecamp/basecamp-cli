# Basecamp Import Artifact v1

`basecamp-import-csv-v1` is the stable artifact format produced by `basecamp import compile` for CSV-to-Basecamp todo and card imports. The artifact is a durable checkpoint between CSV inspection and approved Basecamp writes.

A compiled artifact contains:

```text
basecamp-import/
├── import.json
└── todos.csv     # todo imports

basecamp-import/
├── import.json
└── cards.csv     # card imports
```

Execution adds a local ledger:

```text
basecamp-import/
├── import.json
├── todos.csv
└── execution.json
```

The artifact schema is the same for every CSV import that uses `basecamp-import-csv-v1`. Import-specific values such as source fingerprints, destinations, todo or card titles, due dates, and preserved metadata live inside the fixed schema.

## Lifecycle

```bash
basecamp import inspect ./tasks.csv --json > inspection.json
basecamp import compile \
  --inspection inspection.json \
  --mapping mapping.json \
  --destination destination.json \
  --out basecamp-import/ \
  --json
basecamp import plan --artifact basecamp-import/ --json
basecamp import status --artifact basecamp-import/ --json
basecamp import repair --artifact basecamp-import/ --json
basecamp import followup --artifact basecamp-import/ --out followup-import/ --reviewed --json
basecamp import preflight --artifact basecamp-import/ --json
basecamp import execute --artifact basecamp-import/ --approved --json
```

`compile` validates the source CSV fingerprint, confirmed mapping, destination, titles, due dates, and artifact shape before writing the artifact. `plan --artifact`, `status --artifact`, `repair --artifact`, `followup --artifact`, `preflight --artifact`, and `execute --artifact` read the artifact as their source of truth.

## `import.json`

`import.json` is the artifact manifest.

Example:

```json
{
  "schema_version": 1,
  "status": "compiled",
  "artifact_format": "basecamp-import-csv-v1",
  "source_path": "./tasks.csv",
  "source_fingerprint": {
    "algorithm": "sha256-file-v1",
    "value": "..."
  },
  "destination": {
    "schema_version": 1,
    "mode": "existing_project",
    "project_id": "12345",
    "todolist_strategy": "create_from_column"
  },
  "counts": {
    "projects": 0,
    "todolists": 2,
    "todos": 42
  },
  "files": {
    "todos": "todos.csv"
  }
}
```

Fields:

| Field | Type | Description |
|---|---:|---|
| `schema_version` | integer | Manifest schema version. v1 uses `1`. |
| `status` | string | Artifact status. Compiled artifacts use `compiled`. |
| `artifact_format` | string | Artifact format identifier. v1 uses `basecamp-import-csv-v1`. |
| `source_path` | string | Source CSV path inspected and compiled into the artifact. |
| `source_fingerprint.algorithm` | string | Fingerprint algorithm. v1 uses `sha256-file-v1`. |
| `source_fingerprint.value` | string | SHA-256 fingerprint of the inspected source CSV. |
| `destination` | object | Basecamp destination selected for execution. |
| `counts.projects` | integer | Number of projects execution creates. |
| `counts.todolists` | integer | Number of todolists execution creates. |
| `counts.todos` | integer | Number of todos execution creates. |
| `counts.card_columns` | integer | Number of card table columns execution creates for card imports. |
| `counts.cards` | integer | Number of cards execution creates for card imports. |
| `files.todos` | string | Relative path to the canonical todo CSV. v1 uses `todos.csv` for todo imports. |
| `files.cards` | string | Relative path to the canonical card CSV. v1 uses `cards.csv` for card imports. |

## Resource type

`destination.resource_type` selects the Basecamp resource type. A blank value means `todos` for compatibility with existing v1 todo artifacts.

| Resource type | Behavior |
|---|---|
| `todos` | Creates todolists and todos. |
| `cards` | Creates card table columns and cards. |

## Destination modes

### Existing project

Creates todolists and todos, or card table columns and cards, inside an existing Basecamp project.

```json
{
  "schema_version": 1,
  "mode": "existing_project",
  "project_id": "12345",
  "todolist_strategy": "create_from_column"
}
```

`project_id` identifies the destination project. `project_name` may be present as display context.

### New project

Creates a Basecamp project, then creates todolists and todos, or card table columns and cards, inside it.

```json
{
  "schema_version": 1,
  "mode": "new_project",
  "project_name": "Imported launch tasks",
  "todolist_strategy": "create_from_column"
}
```

`project_name` provides the project name created during execution.

## Todolist strategies

| Strategy | Behavior |
|---|---|
| `create_from_column` | Creates one todolist for each distinct mapped todolist value. Blank todolist values use `Imported todos`. |
| `single_todolist` | Creates one todolist named by `todolist_name`, or `Imported todos` when the name is blank. |
| `existing_todolist` | Creates todos in the todolist identified by `todolist_id`. |

## Card column strategies

Card imports use `card_table_id` and `column_strategy` in the destination.

| Strategy | Behavior |
|---|---|
| `create_from_column` | Creates one card table column for each distinct mapped column value. Blank column values use `Imported cards`. |
| `single_column` | Creates one card table column named by `column_name`, or `Imported cards` when the name is blank. |
| `existing_column` | Creates cards in the card table column identified by `column_id`. |

The mapping can use either `column` or `todolist` to identify the source grouping column for cards. `column` is preferred for card imports.

## `todos.csv`

`todos.csv` contains one normalized todo row per source CSV row selected for import. The header is fixed and validated exactly.

```csv
source_path,source_row,source_record_id,project_id,project_name,todolist_id,todolist_name,title,description,due_on,assignee_emails,assignee_names,status,attachment_urls_json,comments_json,custom_fields_json
```

Columns:

| Column | Type | Description |
|---|---:|---|
| `source_path` | string | Source CSV path for row provenance. |
| `source_row` | integer | One-based source data row number, excluding the header row. |
| `source_record_id` | string | Stable source record ID when mapped. |
| `project_id` | string | Destination project ID for existing-project imports. |
| `project_name` | string | Destination project name for display or new-project imports. |
| `todolist_id` | integer string | Destination todolist ID for existing-todolist imports. Blank means execution creates or resolves the list from the artifact destination. |
| `todolist_name` | string | Destination todolist name. Blank values resolve to `Imported todos` for created lists. |
| `title` | string | Basecamp todo title. Compile requires a non-blank title. |
| `description` | string | Basecamp todo description from the mapped source description column. |
| `due_on` | string | Due date normalized to `YYYY-MM-DD`, or blank. |
| `assignee_emails` | semicolon list | Source assignee emails preserved as metadata. |
| `assignee_names` | semicolon list | Source assignee display names preserved as metadata. |
| `status` | string | Source status preserved as metadata. |
| `attachment_urls_json` | JSON array | Source attachment/link URLs preserved as metadata. |
| `comments_json` | JSON array | Source comments preserved as metadata. |
| `custom_fields_json` | JSON object | Non-empty unmapped source columns preserved as metadata when requested by mapping. |

### JSON columns

The JSON columns contain valid JSON values:

```csv
attachment_urls_json,comments_json,custom_fields_json
"[""https://example.com/a""]","[""Original comment""]","{""priority"":""High""}"
```

Readers validate these columns as JSON arrays or objects before planning or execution.

## `cards.csv`

`cards.csv` contains one normalized card row per source CSV row selected for import. The header is fixed and validated exactly.

```csv
source_path,source_row,source_record_id,project_id,project_name,card_table_id,column_id,column_name,title,content,due_on,assignee_emails,assignee_names,status,attachment_urls_json,comments_json,custom_fields_json
```

Columns follow the same provenance and metadata contract as `todos.csv`. Card-specific columns are:

| Column | Type | Description |
|---|---:|---|
| `card_table_id` | integer string | Destination card table ID. Blank means execution resolves the project's card table. |
| `column_id` | integer string | Destination card table column ID for existing-column imports. Blank means execution creates or resolves the column from the artifact destination. |
| `column_name` | string | Destination card table column name. Blank values resolve to `Imported cards` for created columns. |
| `title` | string | Basecamp card title. Compile requires a non-blank title. |
| `content` | string | Basecamp card content from the mapped source description column plus preserved metadata. |

## Due dates

Artifact due dates use `YYYY-MM-DD`.

Compile accepts and normalizes deterministic date values, including:

- `YYYY-MM-DD`
- RFC3339 timestamps such as `2026-06-01T15:04:05Z`
- `YYYY/MM/DD`
- Month-name dates such as `June 1, 2026` and `1 June 2026`
- Slash dates with an inferred or confirmed order

For slash dates, compile uses the whole mapped due-date column:

- A value with the second component greater than `12` confirms `mdy`, such as `06/18/2026`.
- A value with the first component greater than `12` confirms `dmy`, such as `18/06/2026`.
- A mapping can confirm the convention explicitly:

```json
{
  "due_on": {
    "column_index": 6,
    "column_name": "Due Date",
    "date_order": "mdy"
  }
}
```

`date_order` accepts `mdy` or `dmy`.

Compile reports source-row context for invalid, conflicting, or ambiguous due dates so the mapping or source data can be corrected before planning or execution.

## Mapping contract

`mapping.json` records user-confirmed source column choices. Column indexes are authoritative because CSV files can contain duplicate header names.

Example:

```json
{
  "schema_version": 1,
  "record_id": { "column_index": 0, "column_name": "Task ID" },
  "title": { "column_index": 1, "column_name": "Task Name" },
  "description": { "column_index": 2, "column_name": "Notes" },
  "todolist": { "column_index": 3, "column_name": "List" },
  "due_on": { "column_index": 4, "column_name": "Due Date", "date_order": "mdy" },
  "custom_fields": "all_unmapped_columns"
}
```

When `column_name` is present, compile validates that it matches the inspected column name at `column_index`. This confirms that the mapping still points at the inspected column.

## Validation guarantees

Artifact readers validate:

- supported manifest `schema_version`
- supported `artifact_format`
- required artifact files
- exact `todos.csv` or `cards.csv` header
- manifest todo/card count against CSV row count
- non-blank todo and card titles
- integer fields such as `source_row`, `todolist_id`, `card_table_id`, and `column_id`
- JSON array/object columns
- destination fields required for execution

## Status behavior

`basecamp import status --artifact ...` reads local artifact files and reports execution state without Basecamp access.

Status values:

| Status | Meaning |
|---|---|
| `not_executed` | The artifact has no `execution.json` ledger. |
| `completed` | Execution completed and the artifact cannot be executed again. |
| `failed` | Execution failed and Basecamp may contain partial writes. |
| `started` | Execution started and no completion/failure was recorded. |
| `ledger_unreadable` | `execution.json` exists but cannot be parsed. |

The status result includes manifest counts, destination details, execution ledger details when present, and guidance for the next safe action.

## Repair behavior

`basecamp import repair --artifact ...` reads local artifact files and execution operation records without Basecamp access. It summarizes:

- completed operations and created IDs
- failed operations and error text
- pending todo rows with no completed create-todo operation
- guidance for manual review before creating a fresh follow-up artifact

Repair is a review command. It does not resume execution and does not modify the artifact.

## Follow-up behavior

`basecamp import followup --artifact ... --out ... --reviewed` creates a fresh artifact containing pending todo rows from a reviewed failed execution ledger.

Follow-up artifacts:

- preserve source row provenance and metadata for pending todos
- pin pending rows to existing/created Basecamp project and todolist IDs from the source artifact and ledger
- contain no `execution.json`
- require `--reviewed` to confirm the operator reviewed Basecamp state and the repair summary

Plan and preflight the follow-up artifact before approved execution. The source artifact remains closed and must not be rerun.

## Preflight behavior

`basecamp import preflight --artifact ...` checks execution readiness without creating Basecamp records.

Preflight validates:

- the artifact has no `execution.json` ledger
- the destination project can be read when execution will create todolists in an existing project
- planned todolist names do not already exist in the destination project
- planned todo titles do not already exist when importing into an existing todolist

A passed preflight returns `status: "passed"`. A blocked preflight returns `status: "blocked"` with checks, todolist collisions, and todo title collisions that explain what to resolve before execution.

## Execution behavior

`basecamp import execute --artifact ... --approved` runs preflight checks, then performs Basecamp writes described by the artifact:

- creates a project for `new_project` destinations
- creates todolists for `create_from_column` and `single_todolist` strategies
- creates todos with title, description, and due date
- appends preserved source metadata to todo descriptions
- reports created project, todolist, and todo counts
- reports source fields preserved as metadata rather than native Basecamp fields
- writes `execution.json` before creating Basecamp records and updates it when execution completes or fails
- records each completed or failed project, todolist, and todo operation with created Basecamp IDs when available

Execution refuses to run when preflight reports blockers or when `execution.json` already exists. This prevents repeated artifact execution from silently creating duplicate Basecamp records and prevents todolist name collisions from creating duplicate destination lists.

## `execution.json`

`execution.json` records the execution attempt for the artifact directory.

Completed example:

```json
{
  "schema_version": 1,
  "artifact_format": "basecamp-import-csv-v1",
  "status": "completed",
  "source_fingerprint": {
    "algorithm": "sha256-file-v1",
    "value": "..."
  },
  "started_at": "2026-05-27T12:00:00Z",
  "completed_at": "2026-05-27T12:00:03Z",
  "created": {
    "projects": 0,
    "todolists": 2,
    "todos": 42
  },
  "operations": [
    {
      "op": "create_todo",
      "status": "completed",
      "source_row": 1,
      "source_record_id": "T-1",
      "project_id": 12345,
      "todolist_id": 67890,
      "todolist_name": "Imported todos",
      "title": "Buy paint",
      "created_id": 111,
      "at": "2026-05-27T12:00:01Z"
    }
  ]
}
```

Failed example:

```json
{
  "schema_version": 1,
  "artifact_format": "basecamp-import-csv-v1",
  "status": "failed",
  "source_fingerprint": {
    "algorithm": "sha256-file-v1",
    "value": "..."
  },
  "started_at": "2026-05-27T12:00:00Z",
  "failed_at": "2026-05-27T12:00:02Z",
  "created": {
    "projects": 0,
    "todolists": 2,
    "todos": 1
  },
  "operations": [
    {
      "op": "create_todo",
      "status": "failed",
      "source_row": 2,
      "project_id": 12345,
      "todolist_id": 67890,
      "todolist_name": "Imported todos",
      "title": "Book venue",
      "at": "2026-05-27T12:00:02Z",
      "error": "create todo from source row 2: ..."
    }
  ],
  "error": "create todo from source row 2: ..."
}
```

A failed ledger indicates that Basecamp may contain partial writes from that artifact. Review the ledger operations and Basecamp state before creating a fresh artifact for any follow-up execution.

## Versioning

`artifact_format` identifies the artifact contract. `basecamp-import-csv-v1` keeps the v1 manifest fields, `todos.csv` header, and validation behavior stable.

A future artifact shape uses a new format identifier, such as `basecamp-import-csv-v2`, so v1 artifacts remain recognizable and validated by v1-aware readers.
