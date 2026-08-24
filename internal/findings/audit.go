package findings

import (
	"slices"
	"strings"

	"github.com/pgrundev/pgbot/internal/model"
)

// auditPosture grades the pgaudit configuration when the extension is
// installed. pgbot cannot read the audit trail itself (pgaudit writes to the
// server log, which is not reachable over SQL) — but every knob that decides
// whether the trail exists and what leaks into it IS visible in pg_settings,
// and the misconfigurations are the classic compliance foot-guns.
func auditPosture(c *model.Context, add func(model.Finding)) {
	if !slices.Contains(c.Server.Extensions, "pgaudit") {
		return
	}
	logClasses := strings.TrimSpace(strings.ToLower(settingParam(c, "pgaudit.log")))
	silent := logClasses == "" || logClasses == "none"

	if silent {
		add(model.Finding{
			ID: "pgaudit_silent", Object: "setting:pgaudit.log", Severity: model.SeverityWarn,
			Title:       "pgaudit is installed but auditing nothing",
			Detail:      "The pgaudit extension is installed, but pgaudit.log is unset (or 'none'), so no audit records are written. Anyone relying on this instance's audit trail — compliance, forensics — is relying on a trail that does not exist.",
			Remediation: "Set the classes you need, e.g. ALTER SYSTEM SET pgaudit.log = 'write, ddl, role'; SELECT pg_reload_conf(); — or per database/role with ALTER DATABASE/ROLE ... SET. Verify records appear in the server log.",
			Impact:      impact(model.DimRisk, 40, "audit trail silently absent", "pgaudit installed, pgaudit.log="+orNone(logClasses)),
			Confidence:  1.0,
		})
	}

	if settingParam(c, "pgaudit.log_parameter") == "on" {
		add(model.Finding{
			ID: "pgaudit_logs_parameters", Object: "setting:pgaudit.log_parameter", Severity: model.SeverityWarn,
			Title:       "pgaudit writes statement parameters into the server log",
			Detail:      "pgaudit.log_parameter=on records bind parameters verbatim — passwords, tokens, and personal data land in plaintext log files, which typically have wider access and longer retention than the database itself.",
			Remediation: "Set pgaudit.log_parameter = off unless a regulation explicitly requires parameter capture; if it does, treat the server log as sensitive data (access control, retention, encryption at rest).",
			Impact:      impact(model.DimRisk, 55, "secrets and PII in plaintext logs", "pgaudit.log_parameter=on"),
			Confidence:  1.0,
		})
	}

	if !silent && settingParam(c, "log_statement") == "all" {
		add(model.Finding{
			ID: "pgaudit_double_logging", Object: "setting:log_statement", Severity: model.SeverityInfo,
			Title:       "every statement is logged twice (pgaudit + log_statement=all)",
			Detail:      "pgaudit session logging and log_statement=all both record statements, so each one is written to the log twice — double the log volume and write I/O on a busy system, with no additional information.",
			Remediation: "Keep pgaudit (it records what actually executed, with object detail) and lower log_statement — 'ddl' or 'none' — unless another consumer depends on the plain statement log.",
			Impact:      impact(model.DimThroughput, 20, "duplicate log volume", "pgaudit.log="+logClasses+", log_statement=all"),
			Confidence:  1.0,
		})
	}
}
