package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/99apps-id/termixgo/internal/browser"
)

// browserActionTimeout bounds one live-browser action. Navigation gets a longer
// budget than an evaluate, and a wait has its own deadline inside the session.
const browserActionTimeout = 60 * time.Second

// browserSession opens the shared live browser, starting it on first use.
func browserSession(ctx context.Context) (*browser.Session, error) {
	return browser.Open(ctx)
}

// browserNavigateTool opens a URL in the live browser.
type browserNavigateTool struct{}

func (t *browserNavigateTool) Name() string { return "browser_navigate" }
func (t *browserNavigateTool) Aliases() []string {
	return []string{"browser_goto", "navigate_browser", "open_page"}
}
func (t *browserNavigateTool) Mutating() bool { return false }
func (t *browserNavigateTool) Risk() Risk     { return RiskNetwork }
func (t *browserNavigateTool) Label(a map[string]any) string {
	return "Opening page " + Shorten(argString(a, "url"), 40)
}
func (t *browserNavigateTool) DoneLabel(a map[string]any) string {
	return "Opened page " + Shorten(argString(a, "url"), 40)
}
func (t *browserNavigateTool) Description() string {
	return "Open a URL in the live headless browser and wait for the page to load. The page stays open between calls, so follow it with browser_read, browser_click, browser_type or browser_screenshot. Use this for a JavaScript application or any flow with clicks and forms; web_fetch is enough for a static article."
}
func (t *browserNavigateTool) Schema() map[string]any {
	return object(map[string]any{
		"url": strProp("Absolute URL, or a bare host that defaults to https."),
	}, "url")
}

func (t *browserNavigateTool) Run(ctx context.Context, _ *Env, args map[string]any) (Result, error) {
	raw := strings.TrimSpace(argString(args, "url"))
	if raw == "" {
		return Result{Output: "url is required", IsError: true}, nil
	}
	session, err := browserSession(ctx)
	if err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	runCtx, cancel := context.WithTimeout(ctx, browserActionTimeout)
	defer cancel()
	summary, err := session.Navigate(runCtx, raw)
	if err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	return Result{Output: summary}, nil
}

// browserClickTool clicks a page element.
type browserClickTool struct{}

func (t *browserClickTool) Name() string { return "browser_click" }
func (t *browserClickTool) Aliases() []string {
	return []string{"browser_click_element", "click_page", "browser_fill_form"}
}
func (t *browserClickTool) Mutating() bool { return false }
func (t *browserClickTool) Risk() Risk     { return RiskNetwork }
func (t *browserClickTool) Label(a map[string]any) string {
	return "Clicking " + Shorten(firstNonEmpty(argString(a, "selector"), argString(a, "text")), 40)
}
func (t *browserClickTool) DoneLabel(a map[string]any) string {
	return "Clicked " + Shorten(firstNonEmpty(argString(a, "selector"), argString(a, "text")), 40)
}
func (t *browserClickTool) Description() string {
	return "Click an element in the live page by CSS selector, by its visible text, or by viewport coordinate. The element is scrolled into view and a real mouse press is dispatched, so hover, focus and pointer handlers run. Follow it with browser_read or browser_screenshot to see what changed."
}
func (t *browserClickTool) Schema() map[string]any {
	return object(map[string]any{
		"selector": strProp("CSS selector such as '#submit' or 'a[href$=docs]'."),
		"text":     strProp("Visible text of the button or link, used when no selector is given."),
		"x":        intProp("Viewport X in pixels, used when no selector or text is given."),
		"y":        intProp("Viewport Y in pixels, used when no selector or text is given."),
		"double":   boolProp("Whether to send a double click (default false)."),
	})
}

func (t *browserClickTool) Run(ctx context.Context, _ *Env, args map[string]any) (Result, error) {
	selector := strings.TrimSpace(argString(args, "selector"))
	text := strings.TrimSpace(argString(args, "text"))
	hasPoint := args["x"] != nil || args["y"] != nil
	if selector == "" && text == "" && !hasPoint {
		return Result{Output: "a selector, a text label or an x/y coordinate is required", IsError: true}, nil
	}
	session, err := browserSession(ctx)
	if err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	runCtx, cancel := context.WithTimeout(ctx, browserActionTimeout)
	defer cancel()
	x := float64(argInt(args, "x", 0, -20000, 20000))
	y := float64(argInt(args, "y", 0, -20000, 20000))
	if argBool(args, "double", false) {
		err = session.DoubleClick(runCtx, selector, text, x, y)
	} else {
		err = session.Click(runCtx, selector, text, x, y)
	}
	if err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	return Result{Output: fmt.Sprintf("Clicked %s. Read the page again to confirm the result.", describeTarget(selector, text, hasPoint))}, nil
}

// browserTypeTool fills a field or types into the focused element.
type browserTypeTool struct{}

func (t *browserTypeTool) Name() string { return "browser_type" }
func (t *browserTypeTool) Aliases() []string {
	return []string{"browser_fill", "fill_field", "write_input"}
}
func (t *browserTypeTool) Mutating() bool { return false }
func (t *browserTypeTool) Risk() Risk     { return RiskNetwork }
func (t *browserTypeTool) Label(a map[string]any) string {
	return "Typing into " + Shorten(firstNonEmpty(argString(a, "selector"), "the focused element"), 40)
}
func (t *browserTypeTool) DoneLabel(a map[string]any) string {
	return "Typed into " + Shorten(firstNonEmpty(argString(a, "selector"), "the focused element"), 40)
}
func (t *browserTypeTool) Description() string {
	return "Focus a field by CSS selector and insert text, optionally clearing it first and pressing Enter afterwards. Input events are real, so a React or Vue controlled field updates. Use browser_click first when the field is behind a menu."
}
func (t *browserTypeTool) Schema() map[string]any {
	return object(map[string]any{
		"selector": strProp("CSS selector of the input, textarea or editable element. Empty types into whatever is focused."),
		"text":     strProp("Text to insert. A newline is not special; use browser_press with enter instead."),
		"clear":    boolProp("Whether to select all and delete first (default true)."),
		"submit":   boolProp("Whether to press Enter after typing (default false)."),
	}, "text")
}

func (t *browserTypeTool) Run(ctx context.Context, _ *Env, args map[string]any) (Result, error) {
	text := argString(args, "text")
	if text == "" && !argBool(args, "clear", true) {
		return Result{Output: "text is required unless clear is true", IsError: true}, nil
	}
	session, err := browserSession(ctx)
	if err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	runCtx, cancel := context.WithTimeout(ctx, browserActionTimeout)
	defer cancel()
	if err := session.Type(runCtx,
		strings.TrimSpace(argString(args, "selector")),
		text,
		argBool(args, "clear", true),
		argBool(args, "submit", false),
	); err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	return Result{Output: fmt.Sprintf("Inserted %d characters. Read the page again to confirm the field holds them.", len(text))}, nil
}

// browserPressTool presses one key or combination.
type browserPressTool struct{}

func (t *browserPressTool) Name() string { return "browser_press" }
func (t *browserPressTool) Aliases() []string {
	return []string{"browser_key", "press_page_key"}
}
func (t *browserPressTool) Mutating() bool { return false }
func (t *browserPressTool) Risk() Risk     { return RiskNetwork }
func (t *browserPressTool) Label(a map[string]any) string {
	return "Pressing " + Shorten(argString(a, "key"), 20)
}
func (t *browserPressTool) DoneLabel(a map[string]any) string {
	return "Pressed " + Shorten(argString(a, "key"), 20)
}
func (t *browserPressTool) Description() string {
	return "Press a key or combination in the live page: enter, tab, esc, arrows, pageup, pagedown, or a combination such as ctrl+a or shift+tab."
}
func (t *browserPressTool) Schema() map[string]any {
	return object(map[string]any{
		"key": strProp("Key or combination, for example enter, tab, ctrl+a, shift+tab."),
	}, "key")
}

func (t *browserPressTool) Run(ctx context.Context, _ *Env, args map[string]any) (Result, error) {
	key := strings.TrimSpace(argString(args, "key"))
	if key == "" {
		return Result{Output: "key is required", IsError: true}, nil
	}
	session, err := browserSession(ctx)
	if err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	runCtx, cancel := context.WithTimeout(ctx, browserActionTimeout)
	defer cancel()
	if err := session.Press(runCtx, key); err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	return Result{Output: fmt.Sprintf("Pressed %s.", key)}, nil
}

// browserScrollTool scrolls the live page.
type browserScrollTool struct{}

func (t *browserScrollTool) Name() string { return "browser_scroll" }
func (t *browserScrollTool) Aliases() []string {
	return []string{"scroll_page"}
}
func (t *browserScrollTool) Mutating() bool { return false }
func (t *browserScrollTool) Risk() Risk     { return RiskNetwork }
func (t *browserScrollTool) Label(a map[string]any) string {
	return "Scrolling the page"
}
func (t *browserScrollTool) DoneLabel(a map[string]any) string {
	return "Scrolled the page"
}
func (t *browserScrollTool) Description() string {
	return "Scroll the live page up or down by a number of wheel notches. Use it to reach content lazy-loaded below the fold before reading or clicking it."
}
func (t *browserScrollTool) Schema() map[string]any {
	return object(map[string]any{
		"direction": strProp("up or down (default down)."),
		"amount":    intProp("Wheel notches, 1 to 50 (default 3)."),
	}, "direction")
}

func (t *browserScrollTool) Run(ctx context.Context, _ *Env, args map[string]any) (Result, error) {
	session, err := browserSession(ctx)
	if err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	runCtx, cancel := context.WithTimeout(ctx, browserActionTimeout)
	defer cancel()
	direction := strings.TrimSpace(argString(args, "direction"))
	if err := session.Scroll(runCtx, direction, argInt(args, "amount", 3, 1, 50)); err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	return Result{Output: fmt.Sprintf("Scrolled %s.", firstNonEmpty(direction, "down"))}, nil
}

// browserWaitForTool waits until the page reaches a state.
type browserWaitForTool struct{}

func (t *browserWaitForTool) Name() string { return "browser_wait_for" }
func (t *browserWaitForTool) Aliases() []string {
	return []string{"browser_wait"}
}
func (t *browserWaitForTool) Mutating() bool { return false }
func (t *browserWaitForTool) Risk() Risk     { return RiskNetwork }
func (t *browserWaitForTool) Label(a map[string]any) string {
	return "Waiting for the page"
}
func (t *browserWaitForTool) DoneLabel(a map[string]any) string {
	return "Page reached the expected state"
}
func (t *browserWaitForTool) Description() string {
	return "Wait until a selector exists, a text fragment appears, or a JavaScript expression is true. Use it after a click that loads a route or a result, instead of guessing with a sleep."
}
func (t *browserWaitForTool) Schema() map[string]any {
	return object(map[string]any{
		"selector":        strProp("CSS selector to wait for."),
		"text":            strProp("Visible text to wait for."),
		"expression":      strProp("JavaScript expression that must become true."),
		"timeout_seconds": intProp("Deadline in seconds, 1 to 60 (default 10)."),
	})
}

func (t *browserWaitForTool) Run(ctx context.Context, _ *Env, args map[string]any) (Result, error) {
	selector := strings.TrimSpace(argString(args, "selector"))
	text := strings.TrimSpace(argString(args, "text"))
	expression := strings.TrimSpace(argString(args, "expression"))
	if selector == "" && text == "" && expression == "" {
		return Result{Output: "a selector, text or expression is required", IsError: true}, nil
	}
	session, err := browserSession(ctx)
	if err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	timeout := time.Duration(argInt(args, "timeout_seconds", 10, 1, 60)) * time.Second
	runCtx, cancel := context.WithTimeout(ctx, timeout+10*time.Second)
	defer cancel()
	if err := session.WaitFor(runCtx, selector, text, expression, timeout); err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	return Result{Output: "The page reached the expected state."}, nil
}

// browserReadTool reads the live page as text, HTML, links or form controls.
type browserReadTool struct{}

func (t *browserReadTool) Name() string { return "browser_read" }
func (t *browserReadTool) Aliases() []string {
	return []string{"browser_text", "read_page", "browser_extract"}
}
func (t *browserReadTool) Mutating() bool { return false }
func (t *browserReadTool) Risk() Risk     { return RiskNetwork }
func (t *browserReadTool) Label(a map[string]any) string {
	return "Reading the page"
}
func (t *browserReadTool) DoneLabel(a map[string]any) string {
	return "Read the page"
}
func (t *browserReadTool) Description() string {
	return "Read the live page: visible text (default), raw HTML, the links with their targets, or the form controls with usable selectors. This is how you see a client-rendered application after browser_navigate or a click."
}
func (t *browserReadTool) Schema() map[string]any {
	return object(map[string]any{
		"mode":     strProp("What to read: text (default), html, links or forms."),
		"selector": strProp("Optional CSS selector to read one element instead of the whole page."),
	})
}

func (t *browserReadTool) Run(ctx context.Context, _ *Env, args map[string]any) (Result, error) {
	session, err := browserSession(ctx)
	if err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	runCtx, cancel := context.WithTimeout(ctx, browserActionTimeout)
	defer cancel()
	text, err := session.Read(runCtx, argString(args, "mode"), strings.TrimSpace(argString(args, "selector")))
	if err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	return Result{Output: text}, nil
}

// browserEvalTool runs JavaScript in the page.
type browserEvalTool struct{}

func (t *browserEvalTool) Name() string { return "browser_eval" }
func (t *browserEvalTool) Aliases() []string {
	return []string{"browser_js", "run_js", "evaluate_js"}
}
func (t *browserEvalTool) Mutating() bool { return false }
func (t *browserEvalTool) Risk() Risk     { return RiskNetwork }
func (t *browserEvalTool) Label(a map[string]any) string {
	return "Running JavaScript in the page"
}
func (t *browserEvalTool) DoneLabel(a map[string]any) string {
	return "Ran JavaScript in the page"
}
func (t *browserEvalTool) Description() string {
	return "Evaluate a JavaScript expression in the live page and return its value. Use it for a detail the read modes do not expose, such as reading localStorage, counting rows, or waiting on a promise. Prefer browser_read and browser_click for ordinary work."
}
func (t *browserEvalTool) Schema() map[string]any {
	return object(map[string]any{
		"expression": strProp("JavaScript expression, for example document.title or document.querySelectorAll('tr').length."),
	}, "expression")
}

func (t *browserEvalTool) Run(ctx context.Context, _ *Env, args map[string]any) (Result, error) {
	expression := strings.TrimSpace(argString(args, "expression"))
	if expression == "" {
		return Result{Output: "expression is required", IsError: true}, nil
	}
	session, err := browserSession(ctx)
	if err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	runCtx, cancel := context.WithTimeout(ctx, browserActionTimeout)
	defer cancel()
	value, err := session.Evaluate(runCtx, expression)
	if err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	encoded, marshalErr := json.MarshalIndent(value, "", "  ")
	if marshalErr != nil {
		return Result{Output: fmt.Sprint(value)}, nil
	}
	text := string(encoded)
	if len(text) > 16*1024 {
		text = text[:16*1024] + "\n... [clipped]"
	}
	return Result{Output: text}, nil
}

// browserTabsTool manages the browser's tabs.
type browserTabsTool struct{}

func (t *browserTabsTool) Name() string { return "browser_tabs" }
func (t *browserTabsTool) Aliases() []string {
	return []string{"browser_tab"}
}
func (t *browserTabsTool) Mutating() bool { return false }
func (t *browserTabsTool) Risk() Risk     { return RiskNetwork }
func (t *browserTabsTool) Label(a map[string]any) string {
	return "Browser tabs " + Shorten(argString(a, "action"), 20)
}
func (t *browserTabsTool) DoneLabel(a map[string]any) string {
	return "Browser tabs " + Shorten(argString(a, "action"), 20)
}
func (t *browserTabsTool) Description() string {
	return "List, open, switch or close tabs in the live browser. Switching attaches the session to that tab, so the next read or click acts on it."
}
func (t *browserTabsTool) Schema() map[string]any {
	return object(map[string]any{
		"action": strProp("list (default), new, select or close."),
		"id":     strProp("Target id, for select and close."),
		"url":    strProp("URL to open when action=new; defaults to about:blank."),
	}, "action")
}

func (t *browserTabsTool) Run(ctx context.Context, _ *Env, args map[string]any) (Result, error) {
	action := strings.ToLower(strings.TrimSpace(argString(args, "action")))
	if action == "" {
		action = "list"
	}
	switch action {
	case "list", "new", "select", "close":
	default:
		return Result{Output: fmt.Sprintf("unknown action %q: use list, new, select or close", action), IsError: true}, nil
	}
	session, err := browserSession(ctx)
	if err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	runCtx, cancel := context.WithTimeout(ctx, browserActionTimeout)
	defer cancel()
	switch action {
	case "list":
		targets, err := session.ListTabs(runCtx)
		if err != nil {
			return Result{Output: err.Error(), IsError: true}, nil
		}
		var out strings.Builder
		for _, target := range targets {
			marker := " "
			if target.ID == session.TargetID() {
				marker = "*"
			}
			fmt.Fprintf(&out, "%s [%s] %s (%s)\n", marker, target.ID, firstNonEmpty(target.Title, "(untitled)"), target.URL)
		}
		if out.Len() == 0 {
			return Result{Output: "No page is open."}, nil
		}
		return Result{Output: out.String()}, nil
	case "new":
		target, err := session.NewTab(runCtx, firstNonEmpty(strings.TrimSpace(argString(args, "url")), "about:blank"))
		if err != nil {
			return Result{Output: err.Error(), IsError: true}, nil
		}
		return Result{Output: fmt.Sprintf("Opened tab [%s] %s.", target.ID, target.URL)}, nil
	case "select":
		target, err := session.SelectTab(runCtx, argString(args, "id"))
		if err != nil {
			return Result{Output: err.Error(), IsError: true}, nil
		}
		return Result{Output: fmt.Sprintf("Attached to tab [%s] %s.", target.ID, target.URL)}, nil
	case "close":
		if err := session.CloseTab(runCtx, argString(args, "id")); err != nil {
			return Result{Output: err.Error(), IsError: true}, nil
		}
		return Result{Output: "Closed the tab and attached to another one."}, nil
	default:
		return Result{Output: fmt.Sprintf("unknown action %q: use list, new, select or close", action), IsError: true}, nil
	}
}

// browserCookiesTool reads and writes the browser's cookies.
type browserCookiesTool struct{}

func (t *browserCookiesTool) Name() string { return "browser_cookies" }
func (t *browserCookiesTool) Aliases() []string {
	return []string{"browser_cookie"}
}
func (t *browserCookiesTool) Mutating() bool { return false }
func (t *browserCookiesTool) Risk() Risk     { return RiskNetwork }
func (t *browserCookiesTool) Label(a map[string]any) string {
	return "Browser cookies " + Shorten(argString(a, "action"), 20)
}
func (t *browserCookiesTool) DoneLabel(a map[string]any) string {
	return "Browser cookies " + Shorten(argString(a, "action"), 20)
}
func (t *browserCookiesTool) Description() string {
	return "List the cookies the page can see, set one, or clear them all. Use it when a login or a consent banner must survive a navigation."
}
func (t *browserCookiesTool) Schema() map[string]any {
	return object(map[string]any{
		"action": strProp("list (default), set or clear."),
		"name":   strProp("Cookie name, for action=set."),
		"value":  strProp("Cookie value, for action=set."),
		"url":    strProp("URL the cookie belongs to. Defaults to the current page."),
	}, "action")
}

func (t *browserCookiesTool) Run(ctx context.Context, _ *Env, args map[string]any) (Result, error) {
	action := strings.ToLower(strings.TrimSpace(argString(args, "action")))
	if action == "" {
		action = "list"
	}
	switch action {
	case "list", "set", "clear":
	default:
		return Result{Output: fmt.Sprintf("unknown action %q: use list, set or clear", action), IsError: true}, nil
	}
	if action == "set" && strings.TrimSpace(argString(args, "name")) == "" {
		return Result{Output: "name is required for action=set", IsError: true}, nil
	}
	session, err := browserSession(ctx)
	if err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	runCtx, cancel := context.WithTimeout(ctx, browserActionTimeout)
	defer cancel()
	switch action {
	case "list":
		cookies, err := session.Cookies(runCtx)
		if err != nil {
			return Result{Output: err.Error(), IsError: true}, nil
		}
		if len(cookies) == 0 {
			return Result{Output: "The browser holds no cookie for this page."}, nil
		}
		var out strings.Builder
		for index, cookie := range cookies {
			if index >= 100 {
				fmt.Fprintf(&out, "... (%d more cookies elided)\n", len(cookies)-100)
				break
			}
			value := cookie.Value
			if len(value) > 80 {
				value = value[:80] + "..."
			}
			fmt.Fprintf(&out, "%s=%s (domain=%s path=%s)\n", cookie.Name, value, cookie.Domain, cookie.Path)
		}
		return Result{Output: out.String()}, nil
	case "set":
		name := strings.TrimSpace(argString(args, "name"))
		if name == "" {
			return Result{Output: "name is required for action=set", IsError: true}, nil
		}
		if err := session.SetCookie(runCtx, name, argString(args, "value"), argString(args, "url")); err != nil {
			return Result{Output: err.Error(), IsError: true}, nil
		}
		return Result{Output: fmt.Sprintf("Set cookie %q.", name)}, nil
	case "clear":
		if err := session.ClearCookies(runCtx); err != nil {
			return Result{Output: err.Error(), IsError: true}, nil
		}
		return Result{Output: "Cleared every browser cookie."}, nil
	default:
		return Result{Output: fmt.Sprintf("unknown action %q: use list, set or clear", action), IsError: true}, nil
	}
}

// browserCloseTool shuts the live browser down.
type browserCloseTool struct{}

func (t *browserCloseTool) Name() string      { return "browser_close" }
func (t *browserCloseTool) Aliases() []string { return []string{"close_browser"} }
func (t *browserCloseTool) Mutating() bool    { return false }
func (t *browserCloseTool) Risk() Risk        { return RiskNetwork }
func (t *browserCloseTool) Label(a map[string]any) string {
	return "Closing the browser"
}
func (t *browserCloseTool) DoneLabel(a map[string]any) string {
	return "Closed the browser"
}
func (t *browserCloseTool) Description() string {
	return "Close the live browser and free its memory. It also closes itself after five idle minutes."
}
func (t *browserCloseTool) Schema() map[string]any { return object(map[string]any{}) }

func (t *browserCloseTool) Run(_ context.Context, _ *Env, _ map[string]any) (Result, error) {
	if !browser.IsOpen() {
		return Result{Output: "No live browser is open."}, nil
	}
	if err := browser.Close(); err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	return Result{Output: "Closed the live browser."}, nil
}

func describeTarget(selector, text string, hasPoint bool) string {
	switch {
	case selector != "":
		return selector
	case text != "":
		return fmt.Sprintf("%q", text)
	default:
		return "the given coordinate"
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
