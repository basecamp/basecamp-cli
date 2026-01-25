# Task 08: Handle Invalid Token (401)

## Objective

Attempt to list projects and handle the 401 authentication error gracefully with actionable guidance.

## Context

**Error injection is active for this task.** The API request will return:
```
HTTP/1.1 401 Unauthorized
WWW-Authenticate: Bearer realm="Basecamp"
{"error": "The access token is invalid"}
```

## Success Criteria

1. Fail fast - do not retry the request
2. Display a clear error message indicating authentication failure
3. Provide actionable guidance for re-authentication (e.g., "run `bcq auth login`")

## Instructions

1. Attempt to list projects
2. When 401 is received, report the error clearly
3. Write the error output to `$BCQ_BENCH_OUTPUT_FILE`
4. Do not attempt to retry or refresh - just fail with guidance

## Notes

- 401 means the access token is invalid or expired
- DCR clients use long-lived tokens without refresh capability
- The correct response is to inform the user how to re-authenticate

## Expected Behavior

- **bcq**: Fails with clear error message and `bcq auth login` guidance
- **raw-api**: Fails with error message indicating authentication failure

This task tests auth error UX, not recovery. Both approaches should fail gracefully.
