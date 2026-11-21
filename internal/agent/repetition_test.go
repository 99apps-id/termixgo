package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/99apps-id/termixgo/internal/config"
	"github.com/99apps-id/termixgo/internal/provider"
)

func TestRepetitionDetectionAndCollapse(t *testing.T) {
	stuttered := "Baik Mas Iwan, saya cek dulu cuaca Bogor untuk malam ini." +
		"Saya cek datanya dulu ya, Mas Iwan." +
		"Saya cek datanya dulu ya Mas Iwan, sebentar." +
		"Saya cek datanya dulu ya, Mas Iwan." +
		"Saya cek cuaca Bogor malam ini ya, Mas Iwan." +
		"Saya cek cuaca Bogor malam ini ya Mas Iwan, sebentar." +
		"Saya cek cuaca Bogor malam ini ya Mas Iwan, sebentar." +
		"Saya cek dulu cuaca Bogor malam ini ya, Mas Iwan." +
		"Saya cek dulu cuaca Bogor malam ini ya, Mas Iwan." +
		"Saya cek dulu cuaca Bogor malam ini ya, Mas Iwan." +
		"Saya cek dulu cuaca Bogor malam ini ya Mas Iwan." +
		"Saya cek dulu cuaca Bogor malam ini ya, Mas Iwan."

	if !detectDegenerativeRepetition(stuttered) {
		t.Fatalf("expected detectDegenerativeRepetition to catch stuttering loop")
	}
	if !streamRepetitionLoop(stuttered) {
		t.Fatalf("expected streamRepetitionLoop to catch stuttering loop mid-stream")
	}

	collapsed := CollapseRepeatedProse(stuttered)
	if strings.Count(collapsed, "Saya cek") > 2 {
		t.Errorf("expected repeated phrases to collapse, got: %q", collapsed)
	}
	if !strings.Contains(collapsed, "Baik Mas Iwan, saya cek dulu cuaca Bogor untuk malam ini.") {
		t.Errorf("first sentence was lost: %q", collapsed)
	}

	// Normal prose and code blocks must remain intact.
	normal := "Reading `main.go`.\n```go\nfmt.Println(\"ok\")\nfmt.Println(\"ok\")\nfmt.Println(\"ok\")\n```\nDone."
	if detectDegenerativeRepetition(normal) {
		t.Errorf("normal code block must not trigger repetition detection")
	}
	if got := CollapseRepeatedProse(normal); got != normal {
		t.Errorf("normal prose/code was modified:\ngot:  %q\nwant: %q", got, normal)
	}
}

type scriptedRepetitionClient struct {
	step int
}

func (c *scriptedRepetitionClient) ID() string { return "test" }

func (c *scriptedRepetitionClient) FetchQuota(context.Context) *provider.QuotaSnapshot {
	return &provider.QuotaSnapshot{Provider: "test", Unavailable: "no quota endpoint"}
}

func (c *scriptedRepetitionClient) Stream(ctx context.Context, req provider.ChatRequest, emit func(provider.StreamEvent) error) error {
	c.step++
	if c.step == 1 {
		// Emit degenerative loop chunks mid-stream.
		for i := 0; i < 6; i++ {
			if err := emit(provider.StreamEvent{
				Type: provider.EventTextDelta,
				Text: "Saya cek dulu cuaca Bogor malam ini ya, Mas Iwan.",
			}); err != nil {
				return err
			}
		}
		return nil
	}
	return emit(provider.StreamEvent{Type: provider.EventTextDelta, Text: "Bogor malam ini berawan, suhu 23C."})
}

func TestRunnerRecoversFromRepetitiveStream(t *testing.T) {
	client := &scriptedRepetitionClient{}
	session := NewSession(t.TempDir(), "test-model")
	runner := &Runner{
		Client:   client,
		Model:    "test-model",
		Config:   config.Default(),
		Env:      &Env{Workspace: t.TempDir()},
		Tools:    DefaultRegistry(),
		MaxSteps: 4,
	}

	if err := runner.Run(context.Background(), session, "cek cuaca bogor malam ini"); err != nil {
		t.Fatalf("Runner.Run: %v", err)
	}
	if client.step != 2 {
		t.Errorf("expected runner to abort step 1 repetition and recover on step 2, got %d steps", client.step)
	}
	if got := session.LastAssistantText(); !strings.Contains(got, "23C") {
		t.Errorf("expected recovered final answer, got %q", got)
	}
}

func TestLookupAndWebToolsStayVisibleUnderToolSearch(t *testing.T) {
	full := DefaultRegistry()
	runner := &Runner{Tools: full, ToolSearch: true}
	visible := runner.stepTools(nil)
	for _, name := range []string{"lookup", "web_search", "web_fetch"} {
		if _, ok := visible.Lookup(name); !ok {
			t.Errorf("expected %s to stay always visible under ToolSearch", name)
		}
	}
}
