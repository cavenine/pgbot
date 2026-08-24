---
id: pgaudit_double_logging
severity: info
critical_when: ""
dimension: throughput
object: setting
scope: infra
requires: [pgaudit]
thresholds: []
related: [pgaudit_silent]
---

# pgaudit_double_logging

**Severity:** info · **Dimension:** throughput · **Object identity:** `setting:log_statement` · **Requires:** pgaudit installed

## What pgbot observed

pgaudit session logging is active (`pgaudit.log` selects at least one class)
**and** `log_statement` reads `all` — so every statement is recorded twice:
once by PostgreSQL's plain statement log and once by pgaudit.

## Why it matters

Both facilities write to the same server log. On a busy system that is double
the log volume and double the write I/O for no additional information — the
pgaudit record is a superset of the plain one (it names the objects actually
touched, survives `PREPARE`/dynamic SQL, and classifies the statement).
Log-volume cost is real: it competes for the same disk bandwidth as WAL and
data files.

## How to verify it yourself

```sql
SELECT name, setting FROM pg_settings
WHERE name IN ('pgaudit.log', 'log_statement');
```

Then look at the server log: each statement appearing as both a `LOG:
statement:` line and a `LOG: AUDIT:` line confirms the duplication.

## How to fix it

Keep pgaudit (it is the better record) and lower the plain statement log:

```sql
ALTER SYSTEM SET log_statement = 'ddl';   -- or 'none'
SELECT pg_reload_conf();
```

## When to ignore it

Another consumer parses the plain `log_statement` format specifically —
some log-analysis pipelines (e.g. pgBadger configurations) expect it — and
the duplicated volume is acceptable.

```toml
[[ignore]]
finding = "pgaudit_double_logging"
object  = "setting:log_statement"
reason  = "pgBadger parses log_statement output; duplicate volume accepted"
expires = "2027-01-01"
```

## What pgbot cannot see

Whether anything downstream depends on the plain statement-log format, and the
actual log volume being produced — pgbot reads configuration, not the log.

## Related

- [pgaudit_silent](pgaudit_silent.md) — the opposite failure: auditing nothing.
