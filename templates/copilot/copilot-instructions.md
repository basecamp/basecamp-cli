# Basecamp Integration

You have access to `bcq` CLI for Basecamp project management.

## Quick Reference

```bash
# Read
bcq projects                      # List projects
bcq todos --in <project_id>       # List todos
bcq search "query"                # Search

# Write
bcq todo "Task" --in <project_id> # Create todo
bcq done <todo_id>                # Complete
bcq comment "Text" --on <id>      # Comment
```

## Linking Code to Basecamp

When completing work related to a todo:

```bash
# Link commit to todo
bcq comment "Commit $(git rev-parse --short HEAD): $(git log -1 --format=%s)" --on <todo_id>

# Complete the todo
bcq done <todo_id>
```

## Skills

For detailed command reference, see:
- `~/.local/share/bcq/skills/basecamp/SKILL.md`
- `~/.local/share/bcq/skills/basecamp-api-reference/SKILL.md`
