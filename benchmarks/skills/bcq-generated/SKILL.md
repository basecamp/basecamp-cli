# Basecamp via bcq (Generated)

Use the `bcq` CLI for all Basecamp operations. Run `bcq --help` for commands,
`bcq <command> --help` for details.

## Domain Invariants

These trip up models — internalize them:

- **bucket = project**: In URLs and API responses, `bucket_id` is always the project ID. Basecamp URLs follow `/buckets/{project_id}/...` pattern.
- **todoset vs todolist**: Each project has exactly ONE todoset (the container). Todolists live inside the todoset. To create a todolist, you need the todoset_id, not the project_id.
- **dock defines capabilities**: Project capabilities are listed in the `dock[]` array. Check dock before assuming a feature exists (message_board, todoset, vault, schedule, etc.).
- **recording is generic**: A 'recording' is any content item: todo, message, document, comment, upload, etc. The recordings endpoint queries across types.
- **person vs people**: Singular endpoints use `/people/{id}.json`, plural use `/people.json`. The resource is 'person' not 'user'.
- **completed flag, not status field**: Todos use a boolean `completed` field, not a status string. Filter with `?status=active` or `?status=completed`.
- **assignees is always an array**: Todo assignees is an array of person objects, even for single assignment. Use `assignee_ids: [id]` when creating/updating.
- **parent field for hierarchy**: Items have a `parent` object showing their container. Todo's parent is a todolist. Message's parent is a message_board.

## Preferred Patterns

```bash
bcq todos list --project ID
bcq projects
bcq search "query"
bcq show TYPE/ID
```

## Anti-Patterns

- **Calling Basecamp API directly via curl**: Bypasses auth refresh, rate limiting, and pagination handling
- **Assuming project has a feature without checking dock**: Not all projects have all tools enabled
- **Using todoset_id where todolist_id is expected**: Common confusion; todoset is the container, todolist is the actual list

## Commands Reference

Run `bcq --help` for full command list. Key commands:

| Command | Description |
|---------|-------------|
| `bcq projects` | List projects |
| `bcq todos` | List todos |
| `bcq todo "content"` | Create a todo |
| `bcq done <id>` | Complete a todo |
| `bcq comment "text" <id>` | Add a comment |
| `bcq search "query"` | Search across projects |
| `bcq show TYPE/ID` | Show any recording |

Never call the Basecamp API directly when bcq can do it.
