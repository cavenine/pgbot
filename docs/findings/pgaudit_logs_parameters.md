---
id: pgaudit_logs_parameters
severity: warn
critical_when: ""
dimension: risk
object: setting
scope: infra
requires: [pgaudit]
thresholds: []
related: [pgaudit_silent]
---

# pgaudit_logs_parameters

**Severity:** warn · **Dimension:** risk · **Object identity:** `setting:pgaudit.log_parameter` · **Requires:** pgaudit installed

## What pgbot observed

The pgaudit extension is installed and `pgaudit.log_parameter` reads `on`:
every audited statement's **bind parameters are written verbatim** into the
server log.

## Why it matters

Bind parameters are where the sensitive values live — passwords in
`ALTER ROLE … PASSWORD`, tokens, card numbers, personal data in ordinary
`INSERT`/`UPDATE` statements. With parameter logging on, they land in plaintext
log files, which typically have **wider access** (ops tooling, log shippers,
aggregation services) and **longer retention** than the database itself. The
database's own access control does not protect data that has been copied into
its logs. Note the contrast with pgbot's own behavior: pgbot's reports carry
normalized query text with literals stripped, precisely to avoid this.

## How to verify it yourself

```sql
SHOW pgaudit.log_parameter;
```

Then read a few audit lines in the server log and confirm whether real
parameter values appear.

## How to fix it

```sql
ALTER SYSTEM SET pgaudit.log_parameter = off;
SELECT pg_reload_conf();
```

If a regulation explicitly requires parameter capture, treat the server log as
sensitive data in its own right: restrict access, encrypt at rest, and align
its retention with the data it now contains.

## When to ignore it

A regulation or investigation explicitly requires captured parameters **and**
the log pipeline is already handled as sensitive. The finding is a prompt to
confirm that second half, not a claim that the setting is always wrong.

```toml
[[ignore]]
finding = "pgaudit_logs_parameters"
object  = "setting:pgaudit.log_parameter"
reason  = "regulator requires parameter capture; log pipeline is access-controlled and encrypted"
expires = "2027-01-01"
```

## What pgbot cannot see

pgbot cannot read the log itself, so it cannot tell whether sensitive values
have already been written, where the logs are shipped, or who can read them.

## Related

- [pgaudit_silent](pgaudit_silent.md) — the opposite failure: auditing nothing.
