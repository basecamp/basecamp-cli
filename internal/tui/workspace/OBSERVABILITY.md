# TUI Observability Layer — Design

## Vision

Chrome DevTools-grade observability native to the terminal. Three tiers of
detail, from always-on heartbeat to full network/cache inspector. Built on
the Pool infrastructure — every metric already exists, this phase gives it
a face.

## Tier 1: Status Bar Heartbeat (always on)

Right-aligned in the existing status bar. One line, unobtrusive.

```
 Campfire > Messages                    j/k navigate  enter open  ● 4 fresh · 180ms
```

### What it shows

- **Connectivity indicator**: `●` green (all pools responding), `◐` yellow
  (fetching), `○` red (errors/offline)
- **Pool summary**: `4 fresh` / `2 stale` / `1 fetching`
- **Latency**: rolling average of recent fetches (last 20)

### Color semantics

| State | Color | Meaning |
|-------|-------|---------|
| All pools fresh or idle | Green | Everything current |
| Any pool fetching | Yellow | In-flight request |
| Any pool errored | Red | Degraded |
| All pools errored | Red pulse | Offline/broken |

### Apdex emoticon (optional, rightmost)

Based on time-weighted navigation quality over a sliding window:

| Score | Display | Meaning |
|-------|---------|---------|
| > 0.9 | (green) | Nearly all navigations were instant |
| 0.7-0.9 | (yellow) | Some waits, mostly SWR-covered |
| < 0.7 | (red) | Frequent spinners, poor cache coverage |

Navigation quality is measured at `Init()` time:
- **Satisfied** (1.0): Pool snapshot was Fresh — no fetch needed, no spinner
- **Tolerating** (0.5): Pool was Stale — SWR rendered cached, then refreshed
- **Frustrated** (0.0): Pool was Empty — spinner shown, user waited

This is the number the entire SWR/realm architecture optimizes for.

## Tier 2: Expanded Metrics Bar (toggle, 2-3 lines)

Activated by the `` ` `` key. Expands the status bar area to show per-pool
detail.

```
 ─── pools ────────────────────────────────────────────────────────────────
  hey:activity    Fresh  1.2s ago  poll:30s  hits:4 miss:0  │  avg:820ms
  assignments     Fresh  0.3s ago  poll:—    hits:1 miss:0  │  avg:1.1s
  projects        Stale  45s ago   poll:—    hits:2 miss:1  │  avg:340ms
 ─── navigations ──────────────────────────────────────────────── 87% instant
```

### Per-pool columns

- **Key**: pool identifier
- **State**: Fresh / Stale / Loading / Error / Empty
- **Age**: time since FetchedAt
- **Poll**: current adaptive interval (or `—` if not polling)
- **Hits/Misses**: polling effectiveness (resets on focus)
- **avg**: mean fetch duration for this pool

### Navigation quality line

- Percentage of navigations (last N) that were instant (pool Fresh at Init)
- This is the jank meter — flags views that consistently show spinners

## Tier 3: Activity Panel (overlay)

Full-screen overlay (toggle with `~` or `ctrl+shift+d`). Chrome Network
tab equivalent.

### Request log (scrollable)

```
 ┌─ Network ──────────────────────────────────────────────────────────────┐
 │ 10:23:41  GET /3185632/projects.json           200  229ms  [projects] │
 │ 10:23:41  GET /3185632/recordings.json?type=m   200  168ms  [hey]     │
 │ 10:23:41  GET /3185632/recordings.json?type=t   200  157ms  [hey]     │
 │ 10:23:41  GET /2919105/recordings.json?type=m   200  113ms  [hey]     │
 │ 10:23:42  GET /2914079/projects.json            200  277ms  [projects]│
 │ 10:23:42  GET /2919105/recordings.json?type=d   200   84ms  [hey]     │
 │                                                                        │
 │ 6 requests · 1.2s total · 3 accounts · avg: 163ms                     │
 └────────────────────────────────────────────────────────────────────────┘
```

### Pool state timeline

Visual swim-lane per pool showing state transitions:

```
 hey:activity  ████░░████████████████████████████████
 assignments   ░░░░████████████████████████████████████
 projects      ░░░░░░░░████████████████████████████████
               └─ empty ─┘└── fresh ──────────────┘└stale
```

### Cache controls

- `c` — clear all pools (Hub.Shutdown + recreate)
- `i` — invalidate all pools (mark stale, next access refetches)
- `f` — force fetch all visible pools
- `r` — reset metrics counters

### Request detail (expand with enter)

```
 Pool:      hey:activity
 Realm:     global
 Account:   37signals (3185632)
 Type:      Message
 Duration:  168ms
 Status:    200
 Gen:       3 (pool generation at fetch start)
 Version:   7 → 8 (data version before/after)
```

## Architecture

### PoolMetrics (new type in data/)

Central observer that records every pool state transition.

```go
type PoolEvent struct {
    Timestamp time.Time
    PoolKey   string
    Realm     string      // "global", "account:123", "project:42"
    EventType PoolEventType // Fetch, FetchComplete, Error, Invalidate, Clear
    Duration  time.Duration // for FetchComplete
    Status    int           // HTTP status if available
    DataSize  int           // len(data) for slice types
}

type PoolMetrics struct {
    mu     sync.RWMutex
    events []PoolEvent        // ring buffer, last N events
    navLog []NavigationEvent  // Init() quality log
    stats  map[string]*PoolStats // per-pool aggregates
}

type NavigationEvent struct {
    Timestamp time.Time
    ViewTitle string
    PoolKey   string
    Quality   float64 // 1.0 = satisfied, 0.5 = tolerating, 0.0 = frustrated
}
```

### Instrumentation points

Pool.Fetch already brackets every SDK call. Add hooks:
- Pre-fetch: record FetchStart event
- Post-fetch: record FetchComplete with duration
- Error: record Error event
- Init() in each view: record NavigationEvent with quality based on snapshot state

### TEA integration

PoolMetrics exposes a `Subscribe() <-chan PoolEvent` for the activity panel.
Status bar reads `PoolMetrics.Summary()` on every render (cheap, just reads
aggregates).

### SDK log capture

For Tier 3 request detail, intercept SDK HTTP transport to capture URL,
status, duration. This could be a `http.RoundTripper` wrapper that feeds
events into PoolMetrics, or the SDK could expose a logger interface that
we implement.

## Phasing

### Phase A: Status bar heartbeat
- Add PoolMetrics to Hub
- Instrument Pool.Fetch pre/post hooks
- Render summary in status bar right section
- Ship: immediate value, zero UX cost

### Phase B: Navigation quality tracking
- Record snapshot state at Init() time
- Compute sliding-window Apdex
- Add to status bar (emoticon or percentage)
- Ship: makes SWR effectiveness visible

### Phase C: Expanded metrics bar
- Toggle key, 2-3 line expansion
- Per-pool state/age/timing display
- Navigation quality percentage
- Ship: developer daily-driver feature

### Phase D: Activity panel
- Full overlay with request log
- Pool state timeline
- Cache controls
- Request detail expansion
- Ship: replaces -vv for TUI debugging

## Open questions

1. Should PoolMetrics live in Hub or as a separate coordinator?
   Hub owns the pools, but metrics might want broader scope (navigation events
   come from workspace, not Hub).

2. SDK log capture: RoundTripper wrapper vs. SDK logger interface?
   RoundTripper is more portable; SDK logger is more precise about which
   pool/account a request serves.

3. Persistence: should navigation quality stats persist across sessions
   (in cache dir) for trend analysis?

4. Should the jank meter have thresholds that trigger warnings?
   E.g., "Schedule view has shown a spinner on 4 of last 5 visits —
   consider adding FreshTTL" (developer-facing diagnostic).
