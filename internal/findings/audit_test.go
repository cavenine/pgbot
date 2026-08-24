package findings

import (
	"testing"

	"github.com/pgrundev/pgbot/internal/model"
)

func auditCtx(extensions []string, params map[string]string) *model.Context {
	return &model.Context{
		Server:   model.ServerInfo{Extensions: extensions},
		Settings: &model.Settings{Params: params},
	}
}

// pgaudit installed with pgaudit.log unset or 'none' is the compliance
// foot-gun: the operator believes auditing is on, and nothing is recorded.
func TestAuditPosture_pgauditSilent(t *testing.T) {
	for _, val := range []string{"", "none"} {
		f := has(Compute(auditCtx([]string{"pgaudit"}, map[string]string{"pgaudit.log": val})), "pgaudit_silent")
		if f == nil || f.Severity != model.SeverityWarn {
			t.Errorf("pgaudit.log=%q: expected warn pgaudit_silent, got %+v", val, f)
		}
	}
	// Actually logging → not silent.
	if has(Compute(auditCtx([]string{"pgaudit"}, map[string]string{"pgaudit.log": "ddl, role"})), "pgaudit_silent") != nil {
		t.Error("pgaudit.log='ddl, role' is auditing — pgaudit_silent must not fire")
	}
	// Extension absent → no pgaudit findings at all, whatever the params say.
	if has(Compute(auditCtx(nil, map[string]string{"pgaudit.log": ""})), "pgaudit_silent") != nil {
		t.Error("no pgaudit extension — pgaudit_silent must not fire")
	}
}

// pgaudit.log_parameter=on writes bind parameters into the server log —
// passwords and PII land in plaintext log files, often with wider access and
// longer retention than the database itself.
func TestAuditPosture_logsParameters(t *testing.T) {
	f := has(Compute(auditCtx([]string{"pgaudit"}, map[string]string{
		"pgaudit.log": "all", "pgaudit.log_parameter": "on",
	})), "pgaudit_logs_parameters")
	if f == nil || f.Severity != model.SeverityWarn {
		t.Fatalf("expected warn pgaudit_logs_parameters, got %+v", f)
	}
	if f.Impact.Dimension != model.DimRisk {
		t.Errorf("parameter logging is a risk finding, got dimension %q", f.Impact.Dimension)
	}
	if has(Compute(auditCtx([]string{"pgaudit"}, map[string]string{
		"pgaudit.log": "all", "pgaudit.log_parameter": "off",
	})), "pgaudit_logs_parameters") != nil {
		t.Error("log_parameter=off must not fire")
	}
}

// pgaudit doing session logging alongside log_statement=all logs every
// statement twice — real I/O and log-volume cost on a busy system.
func TestAuditPosture_doubleLogging(t *testing.T) {
	f := has(Compute(auditCtx([]string{"pgaudit"}, map[string]string{
		"pgaudit.log": "all", "log_statement": "all",
	})), "pgaudit_double_logging")
	if f == nil || f.Severity != model.SeverityInfo {
		t.Fatalf("expected info pgaudit_double_logging, got %+v", f)
	}
	if has(Compute(auditCtx([]string{"pgaudit"}, map[string]string{
		"pgaudit.log": "all", "log_statement": "ddl",
	})), "pgaudit_double_logging") != nil {
		t.Error("log_statement=ddl is not double logging — must not fire")
	}
	// pgaudit silent → nothing is logged twice.
	if has(Compute(auditCtx([]string{"pgaudit"}, map[string]string{
		"pgaudit.log": "none", "log_statement": "all",
	})), "pgaudit_double_logging") != nil {
		t.Error("pgaudit.log=none logs nothing — must not fire")
	}
}
