package agent

import (
	"testing"

	"github.com/99apps-id/termixgo/internal/provider"
)

// TestLoadRepairsImpossibleUsage covers a session saved while the old per-chunk
// accounting was live: a total no real run could produce is rebuilt from the
// messages on load, and the cost derived from it is scaled down with it.
func TestLoadRepairsImpossibleUsage(t *testing.T) {
	withState(t)
	session := NewSession("/work", "stepfun-plan/step-3.7-flash")
	session.AddUser("do the thing")
	session.AddAssistant("done.", "", nil, nil)
	session.AddUsage(provider.Usage{PromptTokens: 2_000_000_000, CompletionTokens: 1_000, TotalTokens: 2_000_001_000})
	session.SetCost(123.45)
	if err := session.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := LoadSession(session.ID())
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if usage := loaded.Usage(); usage.PromptTokens >= 1_000_000 {
		t.Errorf("usage = %+v, want the impossible total repaired", usage)
	}
	if cost := loaded.Cost(); cost >= 123.45 {
		t.Errorf("cost = %v, want it scaled down with the usage", cost)
	}
}

// TestLoadKeepsAPlausibleUsage guards the repair from firing on a real session:
// a total within what the steps and the window can account for is left alone.
func TestLoadKeepsAPlausibleUsage(t *testing.T) {
	withState(t)
	session := NewSession("/work", "stepfun-plan/step-3.7-flash")
	session.AddUser("do the thing")
	session.AddAssistant("done.", "", nil, nil)
	session.AddUsage(provider.Usage{PromptTokens: 1200, CompletionTokens: 300, TotalTokens: 1500})
	session.SetCost(0.0125)
	if err := session.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := LoadSession(session.ID())
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if usage := loaded.Usage(); usage.TotalTokens != 1500 {
		t.Errorf("usage = %+v, want the stored 1500 kept", usage)
	}
	if cost := loaded.Cost(); cost != 0.0125 {
		t.Errorf("cost = %v, want the stored 0.0125 kept", cost)
	}
}

func TestLoadSessionRepairsLegacyEmptyToolArguments(t *testing.T) {
	withState(t)
	session := NewSession("/work", "stepfun-plan/step-3.7-flash")
	session.AddUser("first turn")
	session.AddAssistant("", "", []provider.ToolCall{
		{ID: "c1", Name: "read_file", Arguments: "{}"},
		{ID: "c2", Name: "grep", Arguments: "{}"},
		{ID: "c3", Name: "code_outline", Arguments: "{}"},
	}, nil)
	session.AddToolResult("c1", "read_file", elidedToolBody)
	session.AddToolResult("c2", "grep", elidedToolBody)
	session.AddToolResult("c3", "code_outline", elidedToolBody)
	session.AddAssistant("done", "", nil, nil)
	session.AddUser("second turn")
	if err := session.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := LoadSession(session.ID())
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	calls := loaded.Messages()[1].ToolCalls
	if calls[0].Arguments != `{"path":"[elided]"}` {
		t.Errorf("read_file arguments = %q, want placeholder", calls[0].Arguments)
	}
	if calls[1].Arguments != `{"pattern":"[elided]"}` {
		t.Errorf("grep arguments = %q, want placeholder", calls[1].Arguments)
	}
	if calls[2].Arguments != `{"path":"[elided]"}` {
		t.Errorf("code_outline arguments = %q, want placeholder", calls[2].Arguments)
	}
}
