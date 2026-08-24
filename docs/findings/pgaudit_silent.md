---
id: pgaudit_silent
severity: warn
critical_when: ""
dimension: risk
object: setting
scope: infra
requires: [pgaudit]
thresholds: []
related: [pgaudit_logs_parameters, pgaudit_double_logging]
---

# pgaudit_silent

**Severity:** warn · **Dimension:** risk · **Object identity:** `setting:pgaudit.log` · **Requires:** pgaudit installed

## What pgbot observed

The pgaudit extension is installed (it appears in `pg_extension`), but
`pgaudit.log` is unset or reads `none` — pgbot's literal test is that the
lower-cased, trimmed value is `""` or `"none"`. With no log classes selected,
pgaudit writes **no audit records at all**.

## Why it matters

pgaudit is installed for exactly one reason: compliance or forensics requires
an audit trail. In this state the trail does not exist. Whoever relies on it —
an auditor, an incident responder, a regulator — discovers that only when they
ask for records that were never written. This is the highest-cost audit
misconfiguration because it is invisible in day-to-day operation: everything
works, nothing is recorded.

## How to verify it yourself

```sql
SELECT extname FROM pg_extension WHERE extname = 'pgaudit';
SELECT setting FROM pg_settings WHERE name = 'pgaudit.log';
```

(`pg_settings` returns zero rows when the extension isn't loaded, where a
`SHOW` would error.)

If the extension row exists and `pgaudit.log` is empty or `none`, session
auditing is off. Also check per-database and per-role overrides:

```sql
SELECT * FROM pg_db_role_setting WHERE setconfig::text LIKE '%pgaudit%';
```

## How to fix it

Select the classes your requirement actually names — logging everything is
rarely the requirement and has real log-volume cost:

```sql
ALTER SYSTEM SET pgaudit.log = 'write, ddl, role';
SELECT pg_reload_conf();
```

Then confirm records appear in the server log. Object-level auditing (grants to
a `pgaudit.role` audit role) is the finer-grained alternative for
relation-scoped requirements.

## When to ignore it

The extension was installed for evaluation and auditing is intentionally off,
or auditing is configured per-database/per-role (`ALTER DATABASE … SET
pgaudit.log`) on databases pgbot did not inspect — the global GUC then reads
empty while the databases that matter are audited.

```toml
[[ignore]]
finding = "pgaudit_silent"
object  = "setting:pgaudit.log"
reason  = "auditing configured per-database via ALTER DATABASE, not globally"
expires = "2027-01-01"
```

## What pgbot cannot see

pgbot reads configuration over SQL; it cannot read the server log, so it
cannot confirm records are actually being written, rotated, or retained. It
also cannot see log shipping — a perfectly configured pgaudit whose logs are
deleted hourly still fails the compliance requirement.

## Related

- [pgaudit_logs_parameters](pgaudit_logs_parameters.md) — the opposite failure: auditing too much.
- [pgaudit_double_logging](pgaudit_double_logging.md) — paying twice for the same statements.
