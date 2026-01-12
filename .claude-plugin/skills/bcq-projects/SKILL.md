---
name: bcq-projects
description: |
  Navigate Basecamp projects with bcq. List projects, view details, explore
  todolists and team members. Use to discover project structure and IDs.
triggers:
  - list projects
  - show project
  - project details
  - find project
  - project members
  - todolists
---

# bcq Project Navigation

Discover and explore Basecamp projects via the `bcq` CLI.

## List All Projects

```bash
bcq projects
bcq projects list
```

**Output:**
```
## Projects (5 projects)

| # | Name | Updated |
|---|------|---------|
| 12345 | Security Triage | 2025-01-09T14:30:00Z |
| 12346 | Product Launch | 2025-01-09T10:00:00Z |
```

### Filter by Status

```bash
bcq projects --status active    # Active projects only
bcq projects --status archived  # Archived projects
bcq projects --status trashed   # Trashed projects
```

---

## Show Project Details

```bash
bcq projects <id>
bcq projects show <id>
```

**Output:**
```
## Security Triage

| Field | Value |
|-------|-------|
| **Description** | Bug bounty and security report handling |
| **Created** | 2024-06-15T... |
| **Updated** | 2025-01-09T... |

### Tools
- Message Board
- To-dos
- Schedule
- Campfire
```

Shows enabled tools (dock items) for the project.

---

## Explore Project Contents

After finding a project ID:

```bash
# List todolists in project
bcq todolists --project <id>

# List todos
bcq todos --in <id>

# List team members
bcq people --project <id>
```

---

## Find Project by Name

```bash
# Search in output
bcq projects -q | jq '.[] | select(.name | test("Security"; "i"))'

# Get ID for a project name
bcq projects -q | jq -r '.[] | select(.name == "Security Triage") | .id'
```

---

## Project Context Setup

Store default project in `.basecamp/config.json`:

```json
{
  "project_id": 12345
}
```

Check current context:
```bash
bcq config show
```

Set context:
```bash
# Interactive (future)
bcq config set-project

# Manual: create .basecamp/config.json in your repo
```

With context set, `--in <project>` becomes optional for most commands.

---

## Workflow: Discover Project Structure

```bash
# 1. List all projects
bcq projects

# 2. Get project details (note the ID from step 1)
bcq projects 12345

# 3. List todolists
bcq todolists --project 12345

# 4. List todos in a specific todolist
bcq todos --in 12345 --list 67890

# 5. Set as default for this repo
echo '{"project_id": 12345, "todolist_id": 67890}' > .basecamp/config.json
```

---

## JSON Output

```bash
# Get all project IDs
bcq projects -q | jq '.[].id'

# Get project names and IDs
bcq projects -q | jq '.[] | {id, name}'

# Find projects updated recently
bcq projects -q | jq '.[] | select(.updated_at > "2025-01-01")'

# Get dock items (enabled tools) for a project
bcq projects 12345 -q | jq '.dock[] | .name'
```

---

## Related Commands

| Command | Description |
|---------|-------------|
| `bcq me` | Current user info |
| `bcq todos --in <id>` | Todos in project |
| `bcq todolists --project <id>` | Todolists in project |
| `bcq people --project <id>` | Project members |
| `bcq auth status` | Authentication status |
