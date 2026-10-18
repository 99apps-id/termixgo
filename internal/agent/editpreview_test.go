package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/99apps-id/termixgo/internal/provider"
)

// TestToolStartCarriesEditPreviewTheTranscriptNeeds is the whole feature in
// one assertion path: the diff shown in the transcript must be computed
// BEFORE the run, because after a successful edit the old text is gone and a
// late preview would find nothing. The approval dialog already worked this
// way; the transcript is the second consumer of the same computation.
func TestToolStartCarriesEditPreviewTheTranscriptNeeds(t *testing.T) {
	client := &fakeClient{steps: [][]provider.StreamEvent{
		{callChunk("c1", "edit", `{"path":"note.txt","old_string":"hello from disk","new_string":"hello changed"}`)},
		{textChunk("edited")},
	}}
	runner, env, rec := newTestRunner(t, client, &ApprovalPolicy{Mode: ApprovalAll}, nil)
	if err := os.WriteFile(filepath.Join(env.Workspace, "note.txt"), []byte("before the line\nhello from disk\nafter the line\n"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	session := NewSession(env.Workspace, "test-model")
	if err := runner.Run(context.Background(), session, "change the greeting"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	var start *Event
	for index := range rec.events {
		event := &rec.events[index]
		if event.Kind == EventToolStart && event.ToolName == "edit" {
			start = event
		}
	}
	if start == nil {
		t.Fatal("no ToolStart event for the edit call")
	}
	if !strings.Contains(start.Preview, "-hello from disk") {
		t.Errorf("preview must show the removed line: %q", start.Preview)
	}
	if !strings.Contains(start.Preview, "+hello changed") {
		t.Errorf("preview must show the added line: %q", start.Preview)
	}
	if !strings.Contains(start.Preview, " before the line") {
		t.Errorf("preview must keep context lines: %q", start.Preview)
	}
	// And the edit itself still ran: the preview must not replace execution.
	data, err := os.ReadFile(filepath.Join(env.Workspace, "note.txt"))
	if err != nil || !strings.Contains(string(data), "hello changed") {
		t.Errorf("the edit did not apply: %q err=%v", data, err)
	}
}

// TestToolStartPreviewEmptyForNonEditTools pins the cost: reading tools carry
// no preview, and nothing is computed for them beyond the switch lookup.
func TestToolStartPreviewEmptyForNonEditTools(t *testing.T) {
	client := &fakeClient{steps: [][]provider.StreamEvent{
		{callChunk("c1", "read_file", `{"path":"note.txt"}`)},
		{textChunk("read it")},
	}}
	runner, env, rec := newTestRunner(t, client, &ApprovalPolicy{Mode: ApprovalAll}, nil)
	if err := os.WriteFile(filepath.Join(env.Workspace, "note.txt"), []byte("content\n"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	session := NewSession(env.Workspace, "test-model")
	if err := runner.Run(context.Background(), session, "read the file"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, event := range rec.events {
		if event.Kind == EventToolStart && event.ToolName == "read_file" && event.Preview != "" {
			t.Errorf("a read tool must carry no preview, got %q", event.Preview)
		}
	}
}
