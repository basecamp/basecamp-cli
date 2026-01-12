---
name: context-linker
description: |
  Automatically link code changes to Basecamp items.
  Use when: committing code, creating PRs, resolving issues.
  Detects todo IDs from branch names, commit messages, and PR descriptions.
---

# Context Linker Agent

Connect code changes to Basecamp todos and discussions.

## Detection Patterns

Look for Basecamp references in:

1. **Branch names**:
   - `feature/todo-12345-description` → todo 12345
   - `fix/BC-12345-auth-bug` → todo 12345
   - `12345-feature-name` → todo 12345

2. **Commit messages**:
   - `[BC-12345] Fix authentication` → todo 12345
   - `[todo:12345] Update docs` → todo 12345
   - `Fixes #12345` (if Basecamp linked) → todo 12345

3. **PR descriptions**:
   - `Closes BC-12345` → todo 12345
   - `Related: https://3.basecamp.com/.../todos/12345` → todo 12345

## Commands

```bash
# Link current commit to a todo
bcq comment "Commit $(git rev-parse --short HEAD): $(git log -1 --format=%s)" --on <todo_id>

# Link a PR
bcq comment "PR: <pr_url>" --on <todo_id>

# Complete a todo
bcq done <todo_id>
```

## Workflow: On Commit

When user commits code:

1. Extract todo ID from branch name:
   ```bash
   BRANCH=$(git branch --show-current)
   TODO_ID=$(echo "$BRANCH" | grep -oE '(BC-|todo-?|^)[0-9]+' | grep -oE '[0-9]+' | head -1)
   ```

2. If found, offer to link:
   ```bash
   COMMIT=$(git rev-parse --short HEAD)
   MSG=$(git log -1 --format=%s)
   bcq comment "Commit $COMMIT: $MSG" --on $TODO_ID
   ```

## Workflow: On PR Creation

When user creates a PR:

1. Check branch name and PR description for todo references
2. For each referenced todo, add PR link as comment
3. Offer to complete todos if PR is merged

## Workflow: On Merge

When a PR is merged:

1. Find all referenced todos
2. Offer to mark them complete:
   ```bash
   bcq done <todo_id>
   ```

## Project Context

Check for `.basecamp/config.json` to get default project:
```bash
PROJECT_ID=$(jq -r '.project_id // empty' .basecamp/config.json 2>/dev/null)
```

This enables todo operations without explicit `--project` flags.

## Example Session

```
User: I just committed the auth fix

Agent: I see you're on branch `fix/BC-42567-auth-bug`.
       The commit is: abc1234 "Fix OAuth token refresh"

       Would you like me to link this commit to todo #42567?

User: yes

Agent: [runs: bcq comment "Commit abc1234: Fix OAuth token refresh" --on 42567]
       Done! Comment added to todo #42567.

       Should I mark this todo as complete?
```
