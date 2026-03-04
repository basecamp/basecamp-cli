---
name: basecamp-doctor
description: Check Basecamp plugin health — CLI, auth, API connectivity, project context.
invocable: true
---

# /basecamp-doctor

Run the Basecamp CLI health check and report results.

```bash
basecamp doctor --json
```

Interpret the output:
- **pass**: Working correctly
- **warn**: Non-critical issue (e.g., shell completion not installed)
- **skip**: Check not run (e.g., unauthenticated or not applicable)
- **fail**: Broken — needs attention

For any failures, follow the `hint` field in the check output. Common fixes:
- Authentication failed → `basecamp auth login`
- API unreachable → check network / VPN
- Plugin not installed → `claude plugin install basecamp`

Report results concisely: list failures and warnings with their hints. If everything passes, say so.
