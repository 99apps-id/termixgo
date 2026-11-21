package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/99apps-id/termixgo/internal/browser"
)

// maxFlowSteps bounds one flow, so a runaway plan cannot hold the browser.
const maxFlowSteps = 50

// flowStep is one browser action in a flow.
type flowStep struct {
	Action   string `json:"action"`
	URL      string `json:"url,omitempty"`
	Selector string `json:"selector,omitempty"`
	Text     string `json:"text,omitempty"`
	Value    string `json:"value,omitempty"`
	Keys     string `json:"keys,omitempty"`
	Mode     string `json:"mode,omitempty"`
	Clear    bool   `json:"clear,omitempty"`
	Submit   bool   `json:"submit,omitempty"`
	Seconds  int    `json:"seconds,omitempty"`
	Amount   int    `json:"amount,omitempty"`
	FullPage bool   `json:"full_page,omitempty"`
}

// flowBrowser is the part of a session a flow drives; tests replace it.
type flowBrowser interface {
	Navigate(ctx context.Context, rawURL string) (string, error)
	Click(ctx context.Context, selector, text string, x, y float64) error
	Type(ctx context.Context, selector, text string, clear, submit bool) error
	Press(ctx context.Context, combo string) error
	Scroll(ctx context.Context, direction string, amount int) error
	WaitFor(ctx context.Context, selector, text, expression string, timeout time.Duration) error
	Read(ctx context.Context, mode, selector string) (string, error)
	Screenshot(ctx context.Context, fullPage bool, selector string) ([]byte, error)
}

var openFlowBrowser = func(ctx context.Context) (flowBrowser, error) {
	session, err := browser.Open(ctx)
	if err != nil {
		return nil, err
	}
	return session, nil
}

var flowNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

var flowVarPattern = regexp.MustCompile(`\{\{\s*([A-Za-z0-9_]+)\s*\}\}`)

func flowDir(env *Env) string { return filepath.Join(env.Workspace, ".termixgo", "flows") }

// substitute fills {{name}} placeholders. A missing variable is an error, so a
// replay never types a literal "{{password}}" into a form.
func substitute(text string, vars map[string]string) (string, error) {
	var missing []string
	out := flowVarPattern.ReplaceAllStringFunc(text, func(match string) string {
		name := flowVarPattern.FindStringSubmatch(match)[1]
		value, ok := vars[name]
		if !ok {
			missing = append(missing, name)
			return match
		}
		return value
	})
	if len(missing) > 0 {
		return "", fmt.Errorf("missing value for %s", strings.Join(missing, ", "))
	}
	return out, nil
}

func decodeSteps(raw any) ([]flowStep, error) {
	if raw == nil {
		return nil, nil
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var steps []flowStep
	if err := json.Unmarshal(data, &steps); err != nil {
		return nil, fmt.Errorf("steps must be a list of {action, ...} objects: %v", err)
	}
	return steps, nil
}

func decodeVars(raw any) map[string]string {
	vars := map[string]string{}
	if object, ok := raw.(map[string]any); ok {
		for key, value := range object {
			vars[key] = fmt.Sprint(value)
		}
	}
	return vars
}

type browserRunStepsTool struct{}

func (t *browserRunStepsTool) Name() string { return "browser_run_steps" }
func (t *browserRunStepsTool) Aliases() []string {
	return []string{"browser_fill_form", "browser_automation", "browser_flow", "run_browser_flow"}
}
func (t *browserRunStepsTool) Mutating() bool { return false }
func (t *browserRunStepsTool) Risk() Risk     { return RiskNetwork }
func (t *browserRunStepsTool) Label(a map[string]any) string {
	if name := argString(a, "flow"); name != "" {
		return "Running browser flow " + Shorten(name, 40)
	}
	return "Running browser steps"
}
func (t *browserRunStepsTool) DoneLabel(a map[string]any) string {
	if name := argString(a, "flow"); name != "" {
		return "Ran browser flow " + Shorten(name, 40)
	}
	return "Ran browser steps"
}
func (t *browserRunStepsTool) Description() string {
	return "Run several live-browser actions in one call: fill and submit a form, log in, walk a checkout, scrape a series of pages. Each step is {action, ...} with action navigate (url), click (selector or text), type (selector, value, clear, submit), press (keys such as Enter or Control+A), wait (selector, text or seconds), scroll (text up/down, amount), read (mode text/html/links, selector) or screenshot (full_page). Values may use {{name}} placeholders filled from vars. save_as stores the steps as a named flow; flow replays one with new vars; list=true shows saved flows. Stops at the first failing step and reports where."
}
func (t *browserRunStepsTool) Schema() map[string]any {
	return object(map[string]any{
		"steps": arrayProp("The actions to run in order.", map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action":    map[string]any{"type": "string", "enum": []string{"navigate", "click", "type", "press", "wait", "scroll", "read", "screenshot"}},
				"url":       map[string]any{"type": "string"},
				"selector":  map[string]any{"type": "string"},
				"text":      map[string]any{"type": "string"},
				"value":     map[string]any{"type": "string"},
				"keys":      map[string]any{"type": "string"},
				"mode":      map[string]any{"type": "string"},
				"clear":     map[string]any{"type": "boolean"},
				"submit":    map[string]any{"type": "boolean"},
				"seconds":   map[string]any{"type": "integer"},
				"amount":    map[string]any{"type": "integer"},
				"full_page": map[string]any{"type": "boolean"},
			},
			"required": []string{"action"},
		}),
		"vars":              map[string]any{"type": "object", "description": "Values for {{name}} placeholders in the steps.", "additionalProperties": map[string]any{"type": "string"}},
		"flow":              strProp("Name of a saved flow to replay instead of steps."),
		"save_as":           strProp("Save these steps as a named flow (lowercase letters, digits, dash, underscore). Vars are not saved."),
		"list":              boolProp("List the saved flows instead of running anything."),
		"continue_on_error": boolProp("Keep going after a failing step. Defaults to false."),
	})
}

func (t *browserRunStepsTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	if env == nil || strings.TrimSpace(env.Workspace) == "" {
		return Result{Output: "no workspace", IsError: true}, nil
	}
	if argBool(args, "list", false) {
		return listFlows(env), nil
	}
	steps, err := decodeSteps(args["steps"])
	if err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	if name := strings.TrimSpace(argString(args, "flow")); name != "" && len(steps) == 0 {
		if !flowNamePattern.MatchString(name) {
			return Result{Output: "flow names use lowercase letters, digits, dash and underscore", IsError: true}, nil
		}
		data, err := os.ReadFile(filepath.Join(flowDir(env), name+".json"))
		if err != nil {
			return Result{Output: fmt.Sprintf("no saved flow %q; list=true shows the saved ones", name), IsError: true}, nil
		}
		if err := json.Unmarshal(data, &steps); err != nil {
			return Result{Output: fmt.Sprintf("flow %q is unreadable: %v", name, err), IsError: true}, nil
		}
	}
	if len(steps) == 0 {
		return Result{Output: "pass steps, or flow to replay a saved one", IsError: true}, nil
	}
	if len(steps) > maxFlowSteps {
		return Result{Output: fmt.Sprintf("a flow holds at most %d steps", maxFlowSteps), IsError: true}, nil
	}
	for index, step := range steps {
		if _, ok := flowActions[strings.ToLower(step.Action)]; !ok {
			return Result{Output: fmt.Sprintf("step %d: unknown action %q", index+1, step.Action), IsError: true}, nil
		}
	}

	var saved string
	if name := strings.TrimSpace(argString(args, "save_as")); name != "" {
		if !flowNamePattern.MatchString(name) {
			return Result{Output: "save_as uses lowercase letters, digits, dash and underscore", IsError: true}, nil
		}
		data, _ := json.MarshalIndent(steps, "", "  ")
		if err := os.MkdirAll(flowDir(env), 0o755); err != nil {
			return Result{Output: err.Error(), IsError: true}, nil
		}
		path := filepath.Join(flowDir(env), name+".json")
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return Result{Output: err.Error(), IsError: true}, nil
		}
		saved = displayPath(env, path)
	}

	session, err := openFlowBrowser(ctx)
	if err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	vars := decodeVars(args["vars"])
	keepGoing := argBool(args, "continue_on_error", false)
	var log []string
	failed := 0
	for index, step := range steps {
		line, err := runFlowStep(ctx, env, session, step, vars, index)
		if err != nil {
			failed++
			log = append(log, fmt.Sprintf("%d. %s FAILED: %v", index+1, step.Action, err))
			if !keepGoing {
				log = append(log, fmt.Sprintf("Stopped at step %d of %d.", index+1, len(steps)))
				break
			}
			continue
		}
		log = append(log, fmt.Sprintf("%d. %s", index+1, line))
	}
	if saved != "" {
		log = append(log, "Saved the flow to "+saved+".")
	}
	output := clipBytes(strings.Join(log, "\n"), 24*1024)
	return Result{Output: output, IsError: failed > 0 && !keepGoing}, nil
}

var flowActions = map[string]bool{
	"navigate": true, "click": true, "type": true, "press": true,
	"wait": true, "scroll": true, "read": true, "screenshot": true,
}

func runFlowStep(ctx context.Context, env *Env, session flowBrowser, step flowStep, vars map[string]string, index int) (string, error) {
	fill := func(text string) (string, error) { return substitute(text, vars) }
	selector, err := fill(step.Selector)
	if err != nil {
		return "", err
	}
	text, err := fill(step.Text)
	if err != nil {
		return "", err
	}
	runCtx, cancel := context.WithTimeout(ctx, browserActionTimeout)
	defer cancel()
	switch strings.ToLower(step.Action) {
	case "navigate":
		url, err := fill(step.URL)
		if err != nil {
			return "", err
		}
		summary, err := session.Navigate(runCtx, url)
		if err != nil {
			return "", err
		}
		return "navigate " + Shorten(summary, 200), nil
	case "click":
		if selector == "" && text == "" {
			return "", fmt.Errorf("click needs selector or text")
		}
		if err := session.Click(runCtx, selector, text, 0, 0); err != nil {
			return "", err
		}
		return "click " + describeTarget(selector, text, false), nil
	case "type":
		value, err := fill(step.Value)
		if err != nil {
			return "", err
		}
		if value == "" {
			value = text
		}
		if err := session.Type(runCtx, selector, value, step.Clear, step.Submit); err != nil {
			return "", err
		}
		// The value is not echoed: a flow often types a password.
		return fmt.Sprintf("type %d character(s) into %s", len([]rune(value)), firstNonEmpty(selector, "the focused element")), nil
	case "press":
		keys := firstNonEmpty(step.Keys, text)
		if keys == "" {
			return "", fmt.Errorf("press needs keys")
		}
		if err := session.Press(runCtx, keys); err != nil {
			return "", err
		}
		return "press " + keys, nil
	case "wait":
		if selector == "" && text == "" {
			seconds := step.Seconds
			if seconds <= 0 || seconds > 30 {
				seconds = 1
			}
			select {
			case <-time.After(time.Duration(seconds) * time.Second):
			case <-ctx.Done():
				return "", ctx.Err()
			}
			return fmt.Sprintf("wait %ds", seconds), nil
		}
		timeout := 15 * time.Second
		if step.Seconds > 0 && step.Seconds <= 60 {
			timeout = time.Duration(step.Seconds) * time.Second
		}
		if err := session.WaitFor(runCtx, selector, text, "", timeout); err != nil {
			return "", err
		}
		return "wait for " + describeTarget(selector, text, false), nil
	case "scroll":
		direction := firstNonEmpty(strings.ToLower(text), "down")
		if err := session.Scroll(runCtx, direction, step.Amount); err != nil {
			return "", err
		}
		return "scroll " + direction, nil
	case "read":
		mode := firstNonEmpty(step.Mode, "text")
		content, err := session.Read(runCtx, mode, selector)
		if err != nil {
			return "", err
		}
		return "read " + mode + ":\n" + clipBytes(strings.TrimSpace(content), 6000), nil
	case "screenshot":
		data, err := session.Screenshot(runCtx, step.FullPage, selector)
		if err != nil {
			return "", err
		}
		path := filepath.Join(env.Workspace, ".termixgo", "media", fmt.Sprintf("flow-%s-%d.png", time.Now().Format("20060102-150405"), index+1))
		if err := writeMedia(path, data); err != nil {
			return "", err
		}
		return "screenshot " + displayPath(env, path) + " (read_image views it)", nil
	}
	return "", fmt.Errorf("unknown action %q", step.Action)
}

func listFlows(env *Env) Result {
	entries, err := os.ReadDir(flowDir(env))
	if err != nil {
		return Result{Output: "No saved browser flows. Save one with save_as."}
	}
	var lines []string
	for _, entry := range entries {
		name, ok := strings.CutSuffix(entry.Name(), ".json")
		if !ok || entry.IsDir() {
			continue
		}
		data, _ := os.ReadFile(filepath.Join(flowDir(env), entry.Name()))
		var steps []flowStep
		_ = json.Unmarshal(data, &steps)
		vars := map[string]bool{}
		for _, match := range flowVarPattern.FindAllStringSubmatch(string(data), -1) {
			vars[match[1]] = true
		}
		names := make([]string, 0, len(vars))
		for name := range vars {
			names = append(names, name)
		}
		sort.Strings(names)
		line := fmt.Sprintf("%s (%d steps)", name, len(steps))
		if len(names) > 0 {
			line += " vars: " + strings.Join(names, ", ")
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return Result{Output: "No saved browser flows. Save one with save_as."}
	}
	sort.Strings(lines)
	return Result{Output: strings.Join(lines, "\n")}
}
