# pgbot why — deterministic root-cause chains from baseline history

Approved in-chat 2026-08-23. This is the flagship correlation feature the
README's roadmap promises ("Deeper correlation (`pgbot why`) is still future
work"): *"orders queries 3.2× slower → because seq scans jumped → after the
table grew 18%"* — computed in Go from the local baseline store, never
AI-generated.

## Problem

pgbot already remembers (every inspect saves a full snapshot; schema/config
events are derived and persisted) and already diffs (`internal/diff`, two
snapshots). What it cannot do is answer *why*: connect a regression to the
mechanism behind it and the antecedent that set the mechanism off, across N
snapshots, with timestamps.

## Approach (chosen)

Rule-based causal chains over snapshot history. Rejected: generic statistical
cross-correlation (spurious, unexplainable — off-brand) and LLM-generated
causation (violates "AI never generates findings"; `ask`/`explain` may
narrate this feature's output later, unchanged).

## Architecture

- `internal/why` — the engine. Pure: `Analyze(snaps []store.Snapshot, events
  []model.Event, opts Options) Report`. No database access, no store access —
  fully unit-testable with synthetic histories.
  - **Series builder**: per-object time series from the snapshot Contexts —
    per-query (`queryid`): MeanMS, Calls, CacheHit, WALBytes; per-table
    (`schema.name`): SeqScans (rate), TotalBytes, DeadRatio, IndexScans (rate),
    ModsSinceAnalyze; server: connections, replica lag. Counter series are
    converted to per-second rates between adjacent snapshots; counter
    resets (rate < 0) split the series.
  - **Onset detector**: for each series, the sustained shift with the largest
    magnitude — deterministic: at each point, compare the mean of the window
    before vs after; a shift counts when the after-mean deviates by both a
    relative factor (default 1.5×) and the series' own spread. Returns onset
    time + before/after values. No external stats libraries.
  - **Mechanism rules** (v1, five):
    1. *Query slowed ← seq-scan surge on a referenced table*: query MeanMS
       onset preceded-or-met by SeqScans-rate onset on a table whose name the
       normalized query text references; antecedents attached when their onset
       precedes the mechanism's: table growth ≥ threshold (planner flip),
       `schema.index_dropped` event on that table, ModsSinceAnalyze high with
       stale LastAnalyze (stale stats).
    2. *Query slowed ← cache-hit collapse*: CacheHit onset downward on the
       query (or table growth as antecedent — working set outgrew memory).
    3. *Bloat chain*: DeadRatio onset upward ← autovacuum stalled (last
       autovacuum stale relative to mods) → attached as mechanism for a
       slowed query on that table when both align.
    4. *Config change antecedent*: a persisted `config.changed` event whose
       occurred-window precedes a symptom onset (work_mem,
       random_page_cost, shared_buffers, autovacuum_* named explicitly).
    5. *Replica lag ← WAL surge*: ReplayLagSec onset preceded by WAL-rate
       onset; the top WALBytes-rate query attached as antecedent when its
       onset aligns.
  - **Confidence**: temporal alignment (cause onset ≤ symptom onset, within
    the window) is a hard gate; score = f(magnitude of both shifts, tightness
    of alignment, mechanism specificity). Below 0.5 renders as "possibly" per
    house rules. Chains are ranked by symptom impact (share of DB time for
    queries, severity for server symptoms).
- `store.LoadRange(fingerprint string, since time.Time) ([]Snapshot, error)` —
  the only store addition (read-only). Events come from the existing
  `RecentEvents`.
- `cmd/pgbot/why.go` — `pgbot why [target] [connection-string]`:
  - No target: analyze every detected symptom, worst first (cap 3).
  - Target: a query id (`why 12345`) or a table name (`why orders`) focuses
    the analysis.
  - Flags: `--window` (default 168h), `--json`, `--timeout`, `--no-color`.
  - Connects read-only only to fingerprint the database and take a fresh
    snapshot-in-memory as the series' last point (and so `why` works right
    after `inspect` without double-saving; `--offline` skips the connection
    and uses stored snapshots only).
  - < 3 usable snapshots → exact message saying what to run and when to
    return (not an error exit).
- Render: terminal narrative — symptom line with numbers, then an indented
  chain (`because … after …`), each hop carrying its values and onset
  timestamps; confidence wording per house rules. `--json` emits the Report
  (own shape, versioned `why_schema_version: 1.0.0`, separate from the
  Context contract).
- MCP: one new tool `why` (optional `target`, `window_hours`) returning the
  JSON Report.

## Out of scope (YAGNI)

Host metrics, cross-database causation, new persistent state (reads what the
store already has), provider log/API access, AI anything.

## Testing

- Engine golden tests: synthetic snapshot series per rule — the seq-scan
  chain, the index-drop antecedent, the bloat chain, the config antecedent,
  the WAL/replica chain; negative cases (onsets misordered in time must NOT
  chain; flat series produce no symptom). Red first, per rule.
- Onset detector unit tests: step, ramp, flat, noisy-flat, counter reset.
- `LoadRange` store test.
- One integration test in the PG matrix: two synthetic snapshots + live third,
  command exits 0 and prints the "need more history" path where applicable.

## Risks

- Top-N query/table truncation: a query absent from early snapshots' top-20
  has a short series — the engine treats missing points as gaps (no
  interpolation) and requires ≥3 points for an onset.
- Stats resets: `diff.StatsResetBetween` logic informs the counter-reset split.
- Snapshot cadence is irregular (user-driven): all rates are per-second over
  actual intervals; onsets report wall-clock times.
