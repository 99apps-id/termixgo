package provider

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// TestMain lets this test binary stand in for the agy CLI: when the helper
// variable is set it prints a canned stream-json run and exits before the
// test framework parses the CLI's flags.
func TestMain(m *testing.M) {
	// The identity lookup reads the agy installed on the machine, which a test
	// must not depend on.
	agyIdentityLookup = func() (agyIdentity, bool) { return agyIdentity{}, false }
	if script := os.Getenv("TERMIXGO_TEST_AGY_SCRIPT"); script != "" {
		fmt.Print(script)
		if args := os.Getenv("TERMIXGO_TEST_AGY_ARGS_FILE"); args != "" {
			_ = os.WriteFile(args, []byte(strings.Join(os.Args[1:], "\x00")), 0o600)
		}
		if os.Getenv("TERMIXGO_TEST_AGY_HANG") != "" {
			// A CLI that reports its result and then keeps running.
			time.Sleep(time.Minute)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

const agySuccessRun = `{"event":"init","conversation_id":"c-1","init":{"model":"gemini-3.8-flash-low","permission_mode":"always-proceed"}}
{"event":"step_update","step_update":{"conversation_id":"c-1","step_index":1,"state":"ACTIVE","step_type":"tool","tool_name":"read_file","tool_info":{"name":"read_file","parameters":{"Path":"a.txt"}}}}
{"event":"step_update","step_update":{"conversation_id":"c-1","step_index":1,"state":"DONE","step_type":"tool","tool_name":"read_file"}}
{"event":"step_update","step_update":{"conversation_id":"c-1","step_index":2,"state":"ACTIVE","step_type":"agent_response","text_delta":"it says "}}
{"event":"step_update","step_update":{"conversation_id":"c-1","step_index":2,"state":"DONE","step_type":"agent_response","text_delta":"hello","usage":{"input_tokens":100,"output_tokens":7,"thinking_tokens":3,"cache_read_tokens":50}}}
{"event":"result","result":{"conversation_id":"c-1","status":"SUCCESS","response":"it says hello"}}
`

func useFakeAgy(t *testing.T, script string) string {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("executable: %v", err)
	}
	argsFile := t.TempDir() + "/args"
	t.Setenv("TERMIXGO_AGY_PATH", executable)
	t.Setenv("TERMIXGO_TEST_AGY_SCRIPT", script)
	t.Setenv("TERMIXGO_TEST_AGY_ARGS_FILE", argsFile)
	return argsFile
}

func agyClient(t *testing.T) Client {
	t.Helper()
	client, err := newHTTPClient(Provider{ID: "agy-cli", Label: "Antigravity CLI (agy)", Kind: KindAgyCLI}, "agy://local", "agy-cli")
	if err != nil {
		t.Fatalf("newHTTPClient: %v", err)
	}
	return client
}

func TestAgyCLIStreamsTextToolActivityAndUsage(t *testing.T) {
	argsFile := useFakeAgy(t, agySuccessRun)
	var text, reasoning strings.Builder
	var usage Usage
	err := agyClient(t).Stream(context.Background(), ChatRequest{
		Model: "gemini-3.8-flash-medium", Effort: EffortLow,
		Messages: []Message{{Role: RoleUser, Content: "read a.txt"}},
	}, func(event StreamEvent) error {
		switch event.Type {
		case EventTextDelta:
			text.WriteString(event.Text)
		case EventReasoningDelta:
			reasoning.WriteString(event.Text)
		case EventUsage:
			usage = *event.Usage
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if text.String() != "it says hello" {
		t.Errorf("text = %q", text.String())
	}
	if !strings.Contains(reasoning.String(), "agy: read_file a.txt") {
		t.Errorf("reasoning = %q, want the tool line", reasoning.String())
	}
	// The CLI's input count leaves out cached tokens, so the prompt is both.
	if usage.PromptTokens != 150 || usage.CompletionTokens != 7 || usage.CacheReadTokens != 50 || usage.ReasoningTokens != 3 || usage.TotalTokens != 157 {
		t.Errorf("usage = %+v", usage)
	}
	raw, _ := os.ReadFile(argsFile)
	args := strings.Split(string(raw), "\x00")
	joined := strings.Join(args, " ")
	// The effort picks the model-name suffix, and --effort is never passed:
	// the CLI rejects it for a suffixed id.
	if !strings.Contains(joined, "--model gemini-3.8-flash-low") || strings.Contains(joined, "--effort") {
		t.Errorf("args = %q", joined)
	}
	if !strings.Contains(joined, "--output-format stream-json") || !strings.Contains(joined, "--mode plan") || args[0] != "-p" || args[1] != "read a.txt" {
		t.Errorf("args = %q", joined)
	}
}

func TestAgyCLIResumesItsConversation(t *testing.T) {
	argsFile := useFakeAgy(t, agySuccessRun)
	client := agyClient(t)
	history := []Message{{Role: RoleUser, Content: "first"}}
	if err := client.Stream(context.Background(), ChatRequest{Model: "gemini-4-argon-low", Messages: history}, func(StreamEvent) error { return nil }); err != nil {
		t.Fatalf("first run: %v", err)
	}
	history = append(history, Message{Role: RoleAssistant, Content: "done"}, Message{Role: RoleUser, Content: "second"})
	if err := client.Stream(context.Background(), ChatRequest{Model: "gemini-4-argon-low", Messages: history}, func(StreamEvent) error { return nil }); err != nil {
		t.Fatalf("second run: %v", err)
	}
	raw, _ := os.ReadFile(argsFile)
	args := strings.Split(string(raw), "\x00")
	if !strings.Contains(strings.Join(args, " "), "--conversation c-1") || args[1] != "second" {
		t.Errorf("args = %q, want the new message only and the conversation id", args)
	}
}

func TestAgyCLIReportsAFailedRun(t *testing.T) {
	useFakeAgy(t, `{"event":"result","result":{"status":"ERROR","response":"quota exhausted for this model"}}`+"\n")
	err := agyClient(t).Stream(context.Background(), ChatRequest{Model: "gemini-4-argon-low", Messages: []Message{{Role: RoleUser, Content: "hi"}}}, func(StreamEvent) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "quota exhausted") {
		t.Fatalf("err = %v, want the CLI's own reason", err)
	}
}

func TestAgyCLIWithoutTheBinaryNamesTheNextAction(t *testing.T) {
	t.Setenv("TERMIXGO_AGY_PATH", "")
	t.Setenv("PATH", t.TempDir())
	t.Setenv("LOCALAPPDATA", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	if agyCLIInstalled() {
		t.Skip("an agy binary exists in a fixed location on this machine")
	}
	_, err := NewClient("agy-cli", "", func(string) string { return ResolveKey(nil, "agy-cli") })
	if err == nil || !strings.Contains(err.Error(), "TERMIXGO_AGY_PATH") || !strings.Contains(err.Error(), "antigravity-*") {
		t.Fatalf("err = %v, want install hint and the HTTP alternative", err)
	}
	if HasKey(nil, "agy-cli") {
		t.Errorf("agy-cli must not read as ready without the CLI")
	}
}

func TestAgyPromptCarriesHistoryOnlyWithoutAConversation(t *testing.T) {
	messages := []Message{
		{Role: RoleUser, Content: "what is 2+2"},
		{Role: RoleAssistant, Content: "4"},
		{Role: RoleTool, Content: "ignored tool output"},
		{Role: RoleUser, Content: "and times 3"},
	}
	fresh := agyPrompt(messages, false)
	if !strings.Contains(fresh, "User: what is 2+2") || !strings.Contains(fresh, "Assistant: 4") || !strings.HasSuffix(fresh, "and times 3") || strings.Contains(fresh, "ignored tool output") {
		t.Errorf("fresh prompt = %q", fresh)
	}
	if resumed := agyPrompt(messages, true); resumed != "and times 3" {
		t.Errorf("resumed prompt = %q, want only the newest message", resumed)
	}
	if got := agyPrompt([]Message{{Role: RoleUser, Content: "--model x"}}, true); strings.HasPrefix(got, "-") {
		t.Errorf("a prompt must not start with a dash: %q", got)
	}
}

func TestAgyModeDefaultsToPlan(t *testing.T) {
	t.Setenv("TERMIXGO_AGY_MODE", "")
	if agyMode() != "plan" {
		t.Errorf("agyMode() = %q, want plan", agyMode())
	}
	t.Setenv("TERMIXGO_AGY_MODE", "auto")
	if agyMode() != "auto" {
		t.Errorf("agyMode() = %q, want auto", agyMode())
	}
	t.Setenv("TERMIXGO_AGY_MODE", "yolo")
	if agyMode() != "plan" {
		t.Errorf("an unknown mode must fall back to plan, got %q", agyMode())
	}
}

// TestAgyCLIDoesNotWaitForAProcessThatStaysAlive covers the CLI holding on
// after its result: the turn ends when the result arrives.
func TestAgyCLIDoesNotWaitForAProcessThatStaysAlive(t *testing.T) {
	useFakeAgy(t, agySuccessRun)
	t.Setenv("TERMIXGO_TEST_AGY_HANG", "1")
	previous := agyExitGrace
	agyExitGrace = 300 * time.Millisecond
	t.Cleanup(func() { agyExitGrace = previous })
	started := time.Now()
	var text strings.Builder
	err := agyClient(t).Stream(context.Background(), ChatRequest{Model: "gemini-3.8-flash-low", Messages: []Message{{Role: RoleUser, Content: "hi"}}}, func(event StreamEvent) error {
		if event.Type == EventTextDelta {
			text.WriteString(event.Text)
		}
		return nil
	})
	if err != nil || text.String() != "it says hello" {
		t.Fatalf("err=%v text=%q", err, text.String())
	}
	if elapsed := time.Since(started); elapsed > 20*time.Second {
		t.Errorf("the turn took %v: it waited for the process", elapsed)
	}
}

func TestAgyCLIAuthFailureNamesItsOwnSignIn(t *testing.T) {
	useFakeAgy(t, `{"event":"result","result":{"status":"ERROR","response":"authentication failed or timed out"}}`+"\n")
	err := agyClient(t).Stream(context.Background(), ChatRequest{Model: "gemini-4-neon-high", Messages: []Message{{Role: RoleUser, Content: "hi"}}}, func(StreamEvent) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "own sign-in") || !strings.Contains(err.Error(), "antigravity-*") {
		t.Fatalf("err = %v, want the hint that agy has its own sign-in", err)
	}
}

func TestAgyCLIQuotaNeedsTheTermixgoLogin(t *testing.T) {
	snap := agyClient(t).FetchQuota(context.Background())
	if snap == nil || snap.Unavailable == "" || len(snap.Windows) != 0 || snap.Provider != "agy-cli" {
		t.Fatalf("snapshot = %+v, want unavailable with a reason", snap)
	}
}

// TestAgyCLIUsageIsTheLastCallNotASum covers a run of several model calls: the
// CLI re-sends its own prompt on each, so the figure that matters is the last
// call's, which is the context the model held.
func TestAgyCLIUsageIsTheLastCallNotASum(t *testing.T) {
	useFakeAgy(t, `{"event":"init","conversation_id":"c-9"}
{"event":"step_update","step_update":{"conversation_id":"c-9","step_index":1,"state":"DONE","step_type":"agent_response","usage":{"input_tokens":24000,"output_tokens":100,"thinking_tokens":10,"cache_read_tokens":0}}}
{"event":"step_update","step_update":{"conversation_id":"c-9","step_index":3,"state":"DONE","step_type":"agent_response","usage":{"input_tokens":13000,"output_tokens":50,"thinking_tokens":5,"cache_read_tokens":24000}}}
{"event":"step_update","step_update":{"conversation_id":"c-9","step_index":5,"state":"DONE","step_type":"agent_response","text_delta":"done","usage":{"input_tokens":2000,"output_tokens":30,"thinking_tokens":0,"cache_read_tokens":37000}}}
{"event":"result","result":{"conversation_id":"c-9","status":"SUCCESS","response":"done"}}
`)
	var usage Usage
	err := agyClient(t).Stream(context.Background(), ChatRequest{Model: "gemini-4-neon-high", Messages: []Message{{Role: RoleUser, Content: "hi"}}}, func(event StreamEvent) error {
		if event.Type == EventUsage {
			usage = *event.Usage
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if usage.PromptTokens != 39000 || usage.CompletionTokens != 30 || usage.CacheReadTokens != 37000 {
		t.Errorf("usage = %+v, want the last call's (39000 prompt), not the sum (about 100k)", usage)
	}
}
