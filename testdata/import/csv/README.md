# Import CSV testdata

CSV fixtures for the generic `basecamp import inspect` profiler and later import planning tests.

## Layout

- `canonical/` — redacted, real-shape CSV exports collected from public examples. These are broad profiler regression fixtures and are not source-specific parser contracts.
- `synthetic/` — deterministic small CSVs used for planning, safety, and LLM eval scenarios.
- `synthetic/adversarial/` — deterministic stress fixtures for inspect → compile → plan artifact round-trips across wide rows, ragged rows, alternate delimiters, duplicate headers, multiline text, cards, todos, and fallback groups.

## Canonical fixture counts

- `canonical/asana/` — 4 CSVs
- `canonical/clickup/` — 4 CSVs
- `canonical/jira/` — 6 CSVs
- `canonical/linear/` — 4 CSVs
- `canonical/trello/` — 1 CSV

## Synthetic fixture counts

- `synthetic/adversarial/` — 5 CSVs
- `synthetic/random/` — 30 CSVs

## Privacy and provenance

Local copies are redacted. Emails have been mapped to `@example.com`, obvious person/account identifiers have been replaced, and source URLs/source-derived filenames have been removed. The fixtures preserve CSV shape: headers, duplicate headers, row/column structure, quoting, multiline fields, and representative values.

## Intended use

The import engine should treat these as arbitrary CSVs and produce factual profiles:

- dialect/header information
- row and column counts
- duplicate headers
- per-column value statistics
- likely role candidates such as title, description, status, assignee, date, URL, and parent-reference columns
- safe sample rows

Do not implement vendor-specific detection or presets for the initial generic profiler.
