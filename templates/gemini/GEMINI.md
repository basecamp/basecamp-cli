# Basecamp Integration for Gemini

You have access to `bcq` CLI for Basecamp project management.

**Always pass `--json` for structured output.**

## Skills

Load these skill files for detailed command reference:

- `~/.local/share/bcq/skills/basecamp/SKILL.md` - Workflow commands
- `~/.local/share/bcq/skills/basecamp-api-reference/SKILL.md` - API reference

## Quick Reference

### Read Operations
```bash
bcq projects --json                      # List all projects
bcq todos --in <project_id> --json       # List todos in project
bcq todos --assignee me --json           # My assigned todos
bcq search "query" --json                # Search across projects
```

### Write Operations
```bash
bcq todo "Task" --in <project_id>           # Create todo
bcq done <todo_id>                          # Complete todo
bcq comment "Text" --on <recording_id>      # Add comment
```

## Output

Always pass `--json` for predictable structured output:

```bash
bcq todos --in 123 --json # JSON envelope (recommended)
bcq todos --in 123 -q     # Raw JSON data only
```

## Best Practices

1. **Search before create**: Check existing todos before creating duplicates
2. **Link code to work**: Comment on todos with commit/PR references
3. **Use natural dates**: `--due tomorrow`, `--due friday`, `--due +3`
