# Task 04: Post Comment on Seeded Message

## Objective

Add a comment with text "Benchmark comment" to the seeded message.

## Context

- Project ID: `$BCQ_BENCH_PROJECT_ID`
- Message ID: `$BCQ_BENCH_MESSAGE_ID` (the seeded "Benchmark Seeded Message")

## Success Criteria

A comment containing "Benchmark comment" exists on the seeded message.

## Instructions

1. Post a comment to the message with ID `$BCQ_BENCH_MESSAGE_ID`
2. The comment content should be "Benchmark comment"

## Notes

- Comments are posted to recordings (messages, todos, etc.)
- The endpoint is `/buckets/{project_id}/recordings/{message_id}/comments.json`
- Content will be HTML-wrapped in the response (e.g., `<div>Benchmark comment</div>`)
