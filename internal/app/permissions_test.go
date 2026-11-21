package app

import (
	"strings"
	"testing"
)

func TestRevokeToolDropsEveryAllowance(t *testing.T) {
	application := newTestApp(t)
	application.AllowTool("run_command")
	application.AllowTool("write_file")
	if perms := application.Permissions(); strings.Join(perms.Always, ",") != "run_command,write_file" {
		t.Fatalf("always = %v", perms.Always)
	}
	if err := application.RevokeTool("run_command"); err != nil {
		t.Fatal(err)
	}
	perms := application.Permissions()
	if strings.Join(perms.Always, ",") != "write_file" || strings.Contains(strings.Join(perms.Session, ","), "run_command") {
		t.Fatalf("after revoke: %+v", perms)
	}
	if err := application.RevokeAllTools(); err != nil {
		t.Fatal(err)
	}
	if perms := application.Permissions(); len(perms.Always)+len(perms.Session)+len(perms.Folder) != 0 {
		t.Fatalf("clear should drop everything: %+v", perms)
	}
}

func TestContextReportNamesEveryPart(t *testing.T) {
	application := newTestApp(t)
	application.Session().AddUser("explain the parser")
	application.Session().AddToolResult("c1", "read_file", strings.Repeat("x", 8000))
	report := application.ContextReport()
	for _, want := range []string{"system prompt", "learned memory", "tool schemas", "your messages", "tool results", "estimated total", "Largest tool results:", "read_file"} {
		if !strings.Contains(report, want) {
			t.Fatalf("report lacks %q:\n%s", want, report)
		}
	}
	if thousands(1234567) != "1,234,567" || thousands(12) != "12" {
		t.Fatalf("thousands formatting is wrong")
	}
}
