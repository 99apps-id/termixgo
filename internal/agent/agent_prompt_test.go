package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/99apps-id/termixgo/internal/agenthome"
	"github.com/99apps-id/termixgo/internal/config"
)

func TestBuildSystemPartsIncludesSoulAndUser(t *testing.T) {
	workspace := t.TempDir()
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "SOUL.md"), []byte("# SOUL.md\n\n## Persona\nAsisten penjualan yang ramah."), 0o600); err != nil {
		t.Fatalf("write SOUL.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, "USER.md"), []byte("# USER.md\n\n- Nama: Ridwan\n- Kota:\n"), 0o600); err != nil {
		t.Fatalf("write USER.md: %v", err)
	}

	static, _ := BuildSystemParts(&Env{Workspace: workspace, AgentName: "sales", AgentHome: home}, "test-model")

	if !strings.Contains(static, "## SOUL - sales") {
		t.Errorf("the SOUL block is missing or not labelled with the agent name")
	}
	if !strings.Contains(static, "Asisten penjualan yang ramah.") {
		t.Errorf("the persona text did not reach the prompt")
	}
	if !strings.Contains(static, "## USER") || !strings.Contains(static, "Nama: Ridwan") {
		t.Errorf("the filled operator field did not reach the prompt")
	}
	if strings.Contains(static, "Kota:") {
		t.Errorf("an unfilled USER field reached the prompt")
	}
	if strings.Index(static, "## SOUL") > strings.Index(static, "## USER") {
		t.Errorf("SOUL must be placed before USER")
	}
}

func TestBuildSystemPartsSkipsBlankUserTemplate(t *testing.T) {
	workspace := t.TempDir()
	t.Setenv(config.EnvHome, t.TempDir())
	paths, err := agenthome.Ensure("sales", "", "sales")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	static, _ := BuildSystemParts(&Env{Workspace: workspace, AgentName: "sales", AgentHome: paths.Home}, "test-model")
	if strings.Contains(static, "## USER") {
		t.Errorf("an untouched USER.md template must contribute nothing")
	}
	if !strings.Contains(static, "## SOUL - sales") {
		t.Errorf("the shipped persona must still reach the prompt")
	}
}

func TestBuildSystemPartsWithoutAgentHomeIsUnchanged(t *testing.T) {
	workspace := t.TempDir()
	static, _ := BuildSystemParts(&Env{Workspace: workspace}, "test-model")
	if strings.Contains(static, "## SOUL") || strings.Contains(static, "## USER") {
		t.Errorf("a run with no agent home must not grow persona blocks")
	}
}
