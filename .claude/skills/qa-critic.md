---
description: Grade smoke test traces against the QA rubric and produce prioritized findings
user_invocable: true
---

# QA Critic

Analyze smoke test traces and grade CLI output quality against the rubric.

## Prerequisites

Smoke traces must exist. Run `make smoke` first to generate them:

```bash
BASECAMP_PROFILE=dev make smoke
# or: BASECAMP_TOKEN=<token> make smoke
```

Traces land in `tmp/qa-traces/traces.jsonl` (or `$QA_TRACE_DIR`).

## Steps

1. **Read the rubric**: Read `e2e/smoke/RUBRIC.md` for the grading dimensions.

2. **Read results**: Two sources, each authoritative for different things:
   - **BATS TAP output** (stdout from `make smoke`): Parse TAP lines to count pass (`ok ...`), fail (`not ok ...`), and skip (`ok ... # skip ...`). These are the ground truth for pass/fail.
   - **Trace file** (`tmp/qa-traces/traces.jsonl`): Each line is a JSON object with fields: `test`, `command`, `exit_code`, `status`, `reason`. Traces record only gap/exclusion metadata — `unverifiable` (test could not verify due to missing data) and `out-of-scope` (intentionally excluded). Traces say nothing about pass/fail; use them only for coverage-gap analysis.

3. **Identify coverage gaps**: List all commands from the `.surface` file (lines starting with `CMD`). Cross-reference against the BATS test inventory (grep `@test` lines across `e2e/smoke/*.bats` and match the command name in each `run_smoke basecamp <command>` call). A command is covered if at least one `@test` exercises it. Traces are not useful here — passing tests leave no trace entry, so a pure-pass command group would be misclassified as uncovered.

4. **Run sample commands**: For each covered command group, run 2-3 representative commands with `--json` and without `--json` to capture both machine and human output. Evaluate against both v0 and v1 rubric dimensions.

5. **Grade v0 (automatable)**: For each command tested:
   - **Functional**: Did it exit 0 with `ok: true`?
   - **Non-empty**: Is `.data` present and non-null?
   - **Correct types**: Are IDs numbers, names strings?
   - **Summary present**: Is `.summary` a non-empty string?
   - **Scriptable**: Does `--json` parse cleanly? Does `--ids-only` work where applicable?

6. **Grade v1 (critic-evaluated)**: For each command tested:
   - **Readable**: Is the human output scannable, not a wall of text?
   - **Discoverable**: Do breadcrumbs suggest logical next actions?
   - **Consistent**: Do similar commands (e.g., all `list` commands) produce similar output shapes?
   - **Helpful errors**: Run with bad input — does the error explain what's wrong and how to fix it?
   - **Complete**: Are all relevant API fields surfaced?

7. **Produce findings**: Output a prioritized list of issues, grouped by severity:
   - **Critical**: Command exits non-zero, crashes, or returns malformed JSON
   - **High**: Missing `.summary`, empty `.data` when data exists, no breadcrumbs
   - **Medium**: Inconsistent output shapes, missing fields vs API, poor error messages
   - **Low**: Style/readability nits, missing `--ids-only` support

Format each finding as:
```
[SEVERITY] command: description
  Evidence: <what you observed>
  Expected: <what the rubric requires>
```

8. **Summary table**: End with a coverage matrix showing each command group, its test count, and a letter grade (A-F) based on v0+v1 scores.
