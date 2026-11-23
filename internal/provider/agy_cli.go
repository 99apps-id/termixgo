package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

// agyCLIClient runs a turn through the installed Antigravity CLI instead of
// calling Cloud Code itself. The request to Google is then the CLI's own,
// byte for byte, which is the point of this route.
//
// It is a delegation, not a model transport: `agy -p` is a whole agent with
// its own tools and its own permission handling, and has no mode that returns
// text only. So for the length of a turn the work is done by agy's tools and
// Termixgo's own tools, approval gates and trust rules do not apply; what comes
// back is the agent's answer, its tool activity as reasoning lines, and its
// token usage. The model list marks these entries so the choice is deliberate.
type agyCLIClient struct {
	*httpClient

	mu           sync.Mutex
	conversation string
}

// agyBinaryName is the CLI's file name on this platform.
func agyBinaryName() string {
	if runtime.GOOS == "windows" {
		return "agy.exe"
	}
	return "agy"
}

// findAgy locates the Antigravity CLI: an explicit TERMIXGO_AGY_PATH, then
// PATH, then the places its installers put it (the standalone installer and
// the IDE's managed copy).
func findAgy() (string, error) {
	if explicit := strings.TrimSpace(os.Getenv("TERMIXGO_AGY_PATH")); explicit != "" {
		if info, err := os.Stat(explicit); err == nil && !info.IsDir() {
			return explicit, nil
		}
		return "", fmt.Errorf("TERMIXGO_AGY_PATH points at %q, which is not a file", explicit)
	}
	if found, err := exec.LookPath("agy"); err == nil {
		return found, nil
	}
	var candidates []string
	if local := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); local != "" {
		candidates = append(candidates, filepath.Join(local, "agy", "bin", agyBinaryName()))
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates,
			filepath.Join(home, ".gemini", "bin", agyBinaryName()),
			filepath.Join(home, ".local", "bin", agyBinaryName()),
			filepath.Join(home, ".agy", "bin", agyBinaryName()))
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	return "", errAgyMissing
}

// errAgyMissing names the next action: this route needs the CLI, and the
// antigravity-* models do not.
var errAgyMissing = errors.New("the Antigravity CLI (agy) was not found; install it, or set TERMIXGO_AGY_PATH to its path. The antigravity-* models reach the same models without the CLI")

// agyCLIInstalled reports whether the CLI can be found, for the key checks.
func agyCLIInstalled() bool {
	_, err := findAgy()
	return err == nil
}

// agyMode is the agent mode passed to the CLI. plan is the default because
// the CLI approves its own tools in headless mode, and plan keeps it to
// research; TERMIXGO_AGY_MODE=accept-edits or auto lets it change files.
func agyMode() string {
	switch mode := strings.ToLower(strings.TrimSpace(os.Getenv("TERMIXGO_AGY_MODE"))); mode {
	case "plan", "accept-edits", "auto":
		return mode
	}
	return "plan"
}

// agyExitGrace is how long the CLI gets to exit after it reports its result.
var agyExitGrace = 3 * time.Second

const (
	// agyPromptBudget keeps the whole command line under the 32,767-character
	// limit Windows puts on a process, with room for the other arguments.
	agyPromptBudget = 24000
	// agyHistoryBudget is the share of it that earlier turns may take when the
	// CLI has no conversation of its own to resume.
	agyHistoryBudget = 10000
)

// agyPrompt builds the prompt for one run. With a conversation to resume only
// the newest user message is sent, because the CLI already holds the rest.
// Without one, the tail of the earlier turns goes in front so a resumed
// Termixgo session does not start the CLI blind.
func agyPrompt(messages []Message, resume bool) string {
	last := -1
	for index := len(messages) - 1; index >= 0; index-- {
		if messages[index].Role == RoleUser && strings.TrimSpace(messages[index].Content) != "" {
			last = index
			break
		}
	}
	if last < 0 {
		return ""
	}
	task := clipRunes(strings.TrimSpace(messages[last].Content), agyPromptBudget-agyHistoryBudget)
	if resume || last == 0 {
		return strings.TrimLeft(task, "-")
	}
	var history []string
	used := 0
	for index := last - 1; index >= 0; index-- {
		message := messages[index]
		var line string
		switch message.Role {
		case RoleUser:
			line = "User: " + strings.TrimSpace(message.Content)
		case RoleAssistant:
			if strings.TrimSpace(message.Content) == "" {
				continue
			}
			line = "Assistant: " + strings.TrimSpace(message.Content)
		default:
			continue
		}
		line = clipRunes(line, 2000)
		if used+len(line) > agyHistoryBudget {
			break
		}
		used += len(line)
		history = append([]string{line}, history...)
	}
	if len(history) == 0 {
		return strings.TrimLeft(task, "-")
	}
	return "Earlier in this conversation:\n" + strings.Join(history, "\n") + "\n\nCurrent request:\n" + task
}

// agyEvent is the part of the CLI's stream-json output the client reads.
type agyEvent struct {
	Event          string `json:"event"`
	ConversationID string `json:"conversation_id"`
	StepUpdate     *struct {
		ConversationID string `json:"conversation_id"`
		StepIndex      int    `json:"step_index"`
		State          string `json:"state"`
		StepType       string `json:"step_type"`
		TextDelta      string `json:"text_delta"`
		ToolName       string `json:"tool_name"`
		ToolInfo       *struct {
			Parameters map[string]any `json:"parameters"`
		} `json:"tool_info"`
		Usage *agyUsage `json:"usage"`
	} `json:"step_update"`
	Result *struct {
		ConversationID string `json:"conversation_id"`
		Status         string `json:"status"`
		Response       string `json:"response"`
		Error          string `json:"error"`
		Usage          *agyUsage
	} `json:"result"`
}

type agyUsage struct {
	InputTokens     int `json:"input_tokens"`
	OutputTokens    int `json:"output_tokens"`
	ThinkingTokens  int `json:"thinking_tokens"`
	CacheReadTokens int `json:"cache_read_tokens"`
}

func (u agyUsage) add(other agyUsage) agyUsage {
	return agyUsage{
		InputTokens:     u.InputTokens + other.InputTokens,
		OutputTokens:    u.OutputTokens + other.OutputTokens,
		ThinkingTokens:  u.ThinkingTokens + other.ThinkingTokens,
		CacheReadTokens: u.CacheReadTokens + other.CacheReadTokens,
	}
}

// toUsage reports the CLI's counters in the shared shape. Its input count
// leaves out what the cache served, so the prompt is the two together.
func (u agyUsage) toUsage() Usage {
	prompt := u.InputTokens + u.CacheReadTokens
	return Usage{
		PromptTokens:     prompt,
		CompletionTokens: u.OutputTokens,
		TotalTokens:      prompt + u.OutputTokens,
		CacheReadTokens:  u.CacheReadTokens,
		ReasoningTokens:  u.ThinkingTokens,
	}
}

func (c *agyCLIClient) resumeID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conversation
}

func (c *agyCLIClient) setConversation(id string) {
	if strings.TrimSpace(id) == "" {
		return
	}
	c.mu.Lock()
	c.conversation = id
	c.mu.Unlock()
}

func (c *agyCLIClient) Stream(ctx context.Context, req ChatRequest, emit func(StreamEvent) error) error {
	binary, err := findAgy()
	if err != nil {
		return err
	}
	resume := c.resumeID()
	emitted, err := c.run(ctx, binary, req, resume, emit)
	if err != nil && resume != "" && !emitted {
		// A conversation the CLI no longer holds (cleared, or another data
		// directory) is not worth failing the turn over: start a fresh one,
		// with the history carried in the prompt.
		c.mu.Lock()
		c.conversation = ""
		c.mu.Unlock()
		_, err = c.run(ctx, binary, req, "", emit)
	}
	return err
}

// run starts one CLI process and turns its stream into events. emitted
// reports whether any text or usage reached the caller, which is what decides
// whether a failed run may be repeated.
func (c *agyCLIClient) run(ctx context.Context, binary string, req ChatRequest, resume string, emit func(StreamEvent) error) (emitted bool, err error) {
	prompt := agyPrompt(req.Messages, resume != "")
	if strings.TrimSpace(prompt) == "" {
		return false, errors.New("there is no user message to send to the Antigravity CLI")
	}
	wire := gemini4WireModel(req.Model, req.Effort)
	args := []string{"-p", prompt, "--model", wire, "--output-format", "stream-json", "--mode", agyMode()}
	if resume != "" {
		args = append(args, "--conversation", resume)
	}
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.WaitDelay = 3 * time.Second
	var stderr limitedBuffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return false, err
	}
	if err := cmd.Start(); err != nil {
		return false, fmt.Errorf("start the Antigravity CLI: %w", err)
	}

	var total agyUsage
	var sawUsage bool
	var finalStatus, finalResponse, finalError string
	announced := map[int]bool{}
	reader := bufio.NewReaderSize(stdout, 1<<20)
	var streamErr error
	for {
		line, readErr := reader.ReadBytes('\n')
		if trimmed := bytes.TrimSpace(line); len(trimmed) > 0 {
			var event agyEvent
			if json.Unmarshal(trimmed, &event) == nil {
				done, emitErr := c.handle(event, announced, &total, &sawUsage, &finalStatus, &finalResponse, &finalError, &emitted, emit)
				if emitErr != nil {
					streamErr = emitErr
					break
				}
				if done {
					// The result event closes the run. The CLI can stay alive well after
					// it (a Neon run was seen holding the process for minutes), so the
					// turn does not wait for it to exit.
					break
				}
			}
		}
		if readErr != nil {
			if readErr != io.EOF {
				streamErr = readErr
			}
			break
		}
	}
	if streamErr != nil {
		// The caller stopped the turn or a read failed: end the process so it
		// does not run on unattended.
		_ = cmd.Process.Kill()
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	var waitErr error
	select {
	case waitErr = <-exited:
	case <-time.After(agyExitGrace):
		_ = cmd.Process.Kill()
		waitErr = <-exited
	}
	if streamErr != nil {
		return emitted, streamErr
	}
	if ctx.Err() != nil {
		return emitted, ctx.Err()
	}
	if sawUsage {
		step := total.toUsage()
		if emitErr := emit(StreamEvent{Type: EventUsage, Usage: &step}); emitErr != nil {
			return true, emitErr
		}
		emitted = true
	}
	if finalStatus != "" && !strings.EqualFold(finalStatus, "SUCCESS") {
		message := strings.TrimSpace(finalError)
		if message == "" {
			message = strings.TrimSpace(finalResponse)
		}
		if message == "" {
			message = "status " + strings.ToLower(finalStatus)
		}
		hint := ""
		if lower := strings.ToLower(message); strings.Contains(lower, "authentic") || strings.Contains(lower, "sign in") || strings.Contains(lower, "login") {
			// The CLI keeps its own sign-in, apart from 'termixgo login antigravity'.
			hint = ". The agy CLI has its own sign-in: run agy once and sign in there (with the Google account that has the model), or use the antigravity-* models, which use the Termixgo login"
		}
		return emitted, fmt.Errorf("the Antigravity CLI ended the run unsuccessfully: %s%s", clipRunes(message, 400), hint)
	}
	if finalStatus == "" {
		detail := strings.TrimSpace(stderr.String())
		if waitErr != nil && detail == "" {
			detail = waitErr.Error()
		}
		if detail == "" {
			detail = "it produced no result"
		}
		return emitted, fmt.Errorf("the Antigravity CLI stopped without finishing: %s", clipRunes(detail, 400))
	}
	return emitted, nil
}

// handle applies one CLI event. Text becomes answer text, tool activity
// becomes a reasoning line so the operator can see what the agent is doing,
// and usage is summed across the run's model calls.
func (c *agyCLIClient) handle(event agyEvent, announced map[int]bool, total *agyUsage, sawUsage *bool, status, response, failure *string, emitted *bool, emit func(StreamEvent) error) (bool, error) {
	switch event.Event {
	case "init":
		c.setConversation(event.ConversationID)
	case "result":
		if event.Result == nil {
			return false, nil
		}
		c.setConversation(event.Result.ConversationID)
		*status = event.Result.Status
		*response = event.Result.Response
		*failure = event.Result.Error
		if !*emitted && strings.TrimSpace(event.Result.Response) != "" && strings.EqualFold(event.Result.Status, "SUCCESS") {
			// A run that streamed no deltas still has its answer here.
			*emitted = true
			return true, emit(StreamEvent{Type: EventTextDelta, Text: event.Result.Response})
		}
		return true, nil
	case "step_update":
		step := event.StepUpdate
		if step == nil {
			return false, nil
		}
		c.setConversation(step.ConversationID)
		switch {
		case step.StepType == "agent_response":
			if step.TextDelta != "" {
				*emitted = true
				if err := emit(StreamEvent{Type: EventTextDelta, Text: step.TextDelta}); err != nil {
					return false, err
				}
			}
			if step.State == "DONE" && step.Usage != nil {
				*total = total.add(*step.Usage)
				*sawUsage = true
			}
		case step.StepType == "tool" && step.State == "ACTIVE" && !announced[step.StepIndex]:
			announced[step.StepIndex] = true
			return false, emit(StreamEvent{Type: EventReasoningDelta, Text: agyToolLine(step.ToolName, step.ToolInfo)})
		case strings.Contains(step.StepType, "think") && step.TextDelta != "":
			return false, emit(StreamEvent{Type: EventReasoningDelta, Text: step.TextDelta})
		}
	}
	return false, nil
}

// agyToolLine renders one tool call as a short reasoning line.
func agyToolLine(name string, info *struct {
	Parameters map[string]any `json:"parameters"`
}) string {
	line := "agy: " + name
	if info != nil && len(info.Parameters) > 0 {
		keys := make([]string, 0, len(info.Parameters))
		for key := range info.Parameters {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if value, ok := info.Parameters[key].(string); ok && strings.TrimSpace(value) != "" {
				line += " " + clipRunes(value, 120)
				break
			}
		}
	}
	return line + "\n"
}

// limitedBuffer keeps the first 8 KiB written to it: enough for the CLI's
// own error text, bounded so a chatty process cannot grow it.
type limitedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if room := 8192 - b.buf.Len(); room > 0 {
		if len(p) > room {
			b.buf.Write(p[:room])
		} else {
			b.buf.Write(p)
		}
	}
	return len(p), nil
}

func (b *limitedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// FetchQuota reports the Antigravity allowance of the Termixgo login. The CLI
// exposes no usage of its own on a headless run, so the figure comes from the
// same quota call the antigravity-* route makes, and it is the allowance of the
// account signed in to Termixgo, which is the CLI's account only when the same
// Google account is used for both.
func (c *agyCLIClient) FetchQuota(ctx context.Context) *QuotaSnapshot {
	unavailable := func(reason string) *QuotaSnapshot {
		return &QuotaSnapshot{Provider: c.info.ID, FetchedAt: time.Now(), Unavailable: reason}
	}
	if c.resolveKey == nil {
		return unavailable("the agy CLI keeps its own login and reports no usage; run 'termixgo login antigravity' to see the Antigravity allowance")
	}
	key := strings.TrimSpace(c.resolveKey("antigravity"))
	if key == "" {
		return unavailable("the agy CLI keeps its own login and reports no usage; run 'termixgo login antigravity' to see the Antigravity allowance")
	}
	info, _ := ByID("antigravity")
	base := &httpClient{info: info, baseURL: strings.TrimRight(DefaultBaseURL("antigravity"), "/"), apiKey: key, http: c.http, resolveKey: c.resolveKey, forceResolveKey: c.forceResolveKey}
	snap := (&antigravityClient{httpClient: base}).FetchQuota(ctx)
	if snap == nil {
		return unavailable("no usage answer from Antigravity")
	}
	copied := *snap
	copied.Provider = c.info.ID
	return &copied
}
