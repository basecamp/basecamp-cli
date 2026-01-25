# Task 07: Recover from Rate Limit (429)

## Objective

Successfully list projects even though the first request will return a 429 rate limit error.

## Context

**Error injection is active for this task.** The first API request will return:
```
HTTP/1.1 429 Too Many Requests
Retry-After: 2
{"error": "Rate limit exceeded"}
```

## Success Criteria

Return a list of projects despite the initial 429 error.

## Instructions

- If you are using **bcq**: run `bcq projects --json` (bcq handles 429 retry automatically).
- If you are using **raw API**:
  1. Make the request and capture both status code and headers (use `-w '%{http_code}'` and `-D <file>`).
  2. If status is `429`, read the `Retry-After` header value, sleep that many seconds, then retry.
  3. **A single request without retry is a FAIL for this task.**
- Return the project list as JSON.

## Notes

- Only the first request is intercepted; the retry will succeed.
- Use the retry pattern described in the raw skill (`api_request` helper or equivalent).
- The `Retry-After` header value is in seconds (e.g., `Retry-After: 2` means wait 2 seconds).
