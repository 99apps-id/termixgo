package agent

import (
	"context"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/99apps-id/termixgo/internal/config"
	"github.com/99apps-id/termixgo/internal/provider"
)

func pooledEnv(t *testing.T, client provider.Client) (*Env, *[]Event, *sync.Mutex) {
	t.Helper()
	env := testEnv(t)
	env.Config = config.Default()
	env.Subagents = NewSubagentPool()
	var mu sync.Mutex
	var wakes []Event
	env.Subagents.SetEmitter(func(event Event) {
		mu.Lock()
		wakes = append(wakes, event)
		mu.Unlock()
	})
	env.RunSubagent = func(ctx context.Context, subType, prompt string) (string, SubagentSpend, error) {
		return RunSubagent(ctx, env, client, provider.Model{ID: "test-model"}, subType, prompt, 4)
	}
	return env, &wakes, &mu
}

// A follow-up sent to a finished subagent runs in the same session: the
// second request carries the first pass's exchange.
func TestSendSubagentContinuesTheSameSession(t *testing.T) {
	client := &fakeClient{steps: [][]provider.StreamEvent{
		{textChunk("The routes live in routes.go.")},
		{textChunk("routes.go has three handlers.")},
	}}
	env, _, _ := pooledEnv(t, client)

	first, _ := (&subagentTool{}).Run(context.Background(), env, map[string]any{"prompt": "where are the routes?", "description": "find routes"})
	if first.IsError || !strings.Contains(first.Output, "agent-1") || !first.SubagentSpent {
		t.Fatalf("first = %+v", first)
	}
	second, _ := (&sendSubagentTool{}).Run(context.Background(), env, map[string]any{"id": "agent-1", "message": "how many handlers?"})
	if second.IsError || !strings.Contains(second.Output, "three handlers") {
		t.Fatalf("second = %+v", second)
	}
	last := client.requests[len(client.requests)-1]
	var history strings.Builder
	for _, message := range last.Messages {
		history.WriteString(message.Content)
	}
	if !strings.Contains(history.String(), "where are the routes?") || !strings.Contains(history.String(), "routes.go.") {
		t.Fatalf("the follow-up should carry the first pass, got %q", history.String())
	}
}

// A background subagent returns at once, wakes the model when it finishes,
// and its spend is handed to the turn that reads the report, once.
func TestBackgroundSubagentWakesAndHandsOverItsSpend(t *testing.T) {
	client := &fakeClient{steps: [][]provider.StreamEvent{
		{textChunk("Background answer."), usageChunk(100, 20)},
	}}
	env, wakes, mu := pooledEnv(t, client)

	started, _ := (&subagentTool{}).Run(context.Background(), env, map[string]any{"prompt": "scan", "background": true})
	if started.IsError || started.SubagentSpent || !strings.Contains(started.Output, "background") {
		t.Fatalf("start = %+v", started)
	}
	status, _ := (&subagentStatusTool{}).Run(context.Background(), env, map[string]any{"id": "agent-1", "wait_seconds": 10})
	if status.IsError || !strings.Contains(status.Output, "Background answer.") || !status.SubagentSpent || status.SubagentUsage.PromptTokens != 100 {
		t.Fatalf("status = %+v", status)
	}
	again, _ := (&subagentStatusTool{}).Run(context.Background(), env, map[string]any{"id": "agent-1"})
	if again.SubagentSpent {
		t.Fatalf("the spend must be folded only once")
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		count := len(*wakes)
		mu.Unlock()
		if count == 1 || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(*wakes) != 1 || (*wakes)[0].Kind != EventWake || !strings.Contains((*wakes)[0].Text, "agent-1") {
		t.Fatalf("wakes = %+v", *wakes)
	}
}

func TestSubagentStatusListsAndRejectsUnknownIDs(t *testing.T) {
	env, _, _ := pooledEnv(t, &fakeClient{})
	list, _ := (&subagentStatusTool{}).Run(context.Background(), env, map[string]any{})
	if !strings.Contains(list.Output, "No subagents") {
		t.Fatalf("list = %q", list.Output)
	}
	missing, _ := (&sendSubagentTool{}).Run(context.Background(), env, map[string]any{"id": "agent-9", "message": "hi"})
	if !missing.IsError {
		t.Fatalf("an unknown id must fail")
	}
}

// A grandchild spawned inside a pooled pass must not reuse the pass's session.
func TestSubagentSlotDoesNotLeakToGrandchildren(t *testing.T) {
	slot := &subagentSlot{}
	ctx := withSubagentSlot(context.Background(), slot)
	if subagentSlotFrom(withSubagentSlot(ctx, nil)) != nil {
		t.Fatalf("clearing the slot should hide it from nested runs")
	}
}

func TestMonitorWakesOnAMatchAndOnExit(t *testing.T) {
	prevPoll, prevCool := monitorPoll, monitorCooldown
	monitorPoll, monitorCooldown = 20*time.Millisecond, 0
	t.Cleanup(func() { monitorPoll, monitorCooldown = prevPoll, prevCool })

	manager := NewProcessManager()
	var mu sync.Mutex
	var wakes []Event
	manager.SetEmitter(func(event Event) {
		if event.Kind == EventWake {
			mu.Lock()
			wakes = append(wakes, event)
			mu.Unlock()
		}
	})
	t.Cleanup(manager.Shutdown)
	command := "echo booting; echo server ready"
	if runtime.GOOS == "windows" {
		command = "echo booting & echo server ready"
	}
	env := testEnv(t)
	env.Processes = manager
	process, err := manager.Start(context.Background(), command, env.Workspace)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	result, _ := (&monitorTool{}).Run(context.Background(), env, map[string]any{"handle": process.ID, "pattern": "ready", "from_start": true})
	if result.IsError {
		t.Fatalf("monitor = %+v", result)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		count := len(wakes)
		mu.Unlock()
		if count >= 2 || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(wakes) != 2 {
		t.Fatalf("want a match wake and an exit wake, got %+v", wakes)
	}
	if !strings.Contains(wakes[0].Text, "server ready") || strings.Contains(wakes[0].Text, "booting") {
		t.Fatalf("the match wake should carry only the matching line: %q", wakes[0].Text)
	}
	if !strings.Contains(wakes[1].Text, "exited with code 0") {
		t.Fatalf("exit wake = %q", wakes[1].Text)
	}
}

func TestMonitorRejectsABadPattern(t *testing.T) {
	manager := NewProcessManager()
	manager.SetEmitter(func(Event) {})
	env := testEnv(t)
	t.Cleanup(manager.Shutdown)
	env.Processes = manager
	process, err := manager.Start(context.Background(), longRunningCommand(), env.Workspace)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	result, _ := (&monitorTool{}).Run(context.Background(), env, map[string]any{"handle": process.ID, "pattern": "("})
	if !result.IsError || !strings.Contains(result.Output, "invalid pattern") {
		t.Fatalf("result = %+v", result)
	}
}
