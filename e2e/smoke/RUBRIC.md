# QA Rubric

Quality dimensions applied to smoke test traces.

## v0: Automatable

These checks are evaluated programmatically from trace data.

| Dimension | Pass criteria |
|-----------|---------------|
| **Functional** | Exit code 0, `ok: true` in JSON envelope |
| **Non-empty** | `.data` is non-null and non-empty |
| **Correct types** | Field types match expected schema (id = number, name = string) |
| **Summary present** | `.summary` is a non-empty string |
| **Scriptable** | `--json` parses, `--ids` extracts numeric IDs |

## v1: Critic-evaluated

These require human or LLM judgment against the trace output.

| Dimension | What to look for |
|-----------|------------------|
| **Readable** | Human output is scannable, not a wall of JSON |
| **Discoverable** | Breadcrumbs suggest logical next actions |
| **Consistent** | Similar commands produce similar output shapes |
| **Helpful errors** | Error messages explain what went wrong and how to fix it |
| **Complete** | All relevant fields from the API are surfaced |
