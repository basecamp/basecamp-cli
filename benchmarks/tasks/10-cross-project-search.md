# Task 10: Search with Pagination Parameter

## Objective

Search for items containing the benchmark search marker with `per_page=10` and return the results.

## Context

- Search marker: `$BCQ_BENCH_SEARCH_MARKER` (set from spec.yaml fixtures.search_marker)
- Pagination: `per_page=10`
- Expected: Between 1 and 10 results

## Success Criteria

Return search results with 1-10 items containing the marker. The validation checks that results are properly limited and contain the seeded marker.

## Instructions

1. Perform a search for `$BCQ_BENCH_SEARCH_MARKER` across the account
2. Use `per_page=10` to limit results
3. Return the results

## Notes

- Search endpoint: `GET /search.json?q=$BCQ_BENCH_SEARCH_MARKER&per_page=10`
- The seeded fixtures include at least one item with this unique marker
- This tests that pagination parameters are correctly passed to the API
