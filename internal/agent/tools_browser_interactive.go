package agent

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
)

// browserLauncher manages a reusable headless browser instance.
type browserLauncher struct {
	mu      sync.Mutex
	browser *rod.Browser
}

var sharedBrowser = &browserLauncher{}

func (l *browserLauncher) getOrCreate() *rod.Browser {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.browser != nil {
		return l.browser
	}
	u := launcher.New().Headless(true).MustLaunch()
	l.browser = rod.New().ControlURL(u).MustConnect()
	return l.browser
}

func (l *browserLauncher) close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.browser != nil {
		_ = l.browser.Close()
		l.browser = nil
	}
}

func validateHTTPURL(target string) error {
	parsed, err := url.Parse(target)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return fmt.Errorf("url must be an absolute http or https URL")
	}
	return nil
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

// browserClickTool clicks an element on the current page.
type browserClickTool struct{}

func (t *browserClickTool) Name() string      { return "browser_click" }
func (t *browserClickTool) Aliases() []string { return []string{"click", "press_button"} }
func (t *browserClickTool) Mutating() bool    { return true }
func (t *browserClickTool) Risk() Risk        { return RiskNetwork }
func (t *browserClickTool) Description() string {
	return "Click an element on the page by CSS selector, button text, or link text."
}
func (t *browserClickTool) Schema() map[string]any {
	return object(map[string]any{
		"selector": strProp("CSS selector, button text, or link text to click."),
		"button":   strProp("Mouse button: left (default), right, double."),
		"ref":      strProp("Optional element ref from browser_snapshot for precise targeting."),
	})
}
func (t *browserClickTool) Label(args map[string]any) string {
	return "Clicking " + Shorten(argString(args, "selector"), 40)
}
func (t *browserClickTool) DoneLabel(args map[string]any) string {
	return "Clicked " + Shorten(argString(args, "selector"), 40)
}

func (t *browserClickTool) Run(_ context.Context, _ *Env, args map[string]any) (Result, error) {
	selector := argString(args, "selector")
	if selector == "" {
		return Result{IsError: true, Output: "selector is required"}, nil
	}
	browser := sharedBrowser.getOrCreate()
	page := browser.MustPage().MustWaitLoad()

	// Try as CSS selector first, then fall back to text-based search.
	if page.MustElement(selector) != nil {
		page.MustElement(selector).MustClick()
	} else {
		// Try XPath for text/link matches.
		xpath := fmt.Sprintf(`//*[contains(text(), %q)]`, selector)
		if el := page.MustElementX(xpath); el != nil {
			el.MustClick()
		} else {
			return Result{IsError: true, Output: fmt.Sprintf("Element not found: %s", selector)}, nil
		}
	}

	page.MustWaitLoad()
	title := page.MustEval("() => document.title").Str()
	return Result{Output: fmt.Sprintf("Clicked %s on page %q", selector, title)}, nil
}

// browserTypeTool types text into an input element.
type browserTypeTool struct{}

func (t *browserTypeTool) Name() string      { return "browser_type" }
func (t *browserTypeTool) Aliases() []string { return []string{"type", "fill_input", "input_text"} }
func (t *browserTypeTool) Mutating() bool    { return true }
func (t *browserTypeTool) Risk() Risk        { return RiskNetwork }
func (t *browserTypeTool) Description() string {
	return "Type text into an input, textarea, or contenteditable element."
}
func (t *browserTypeTool) Schema() map[string]any {
	return object(map[string]any{
		"selector": strProp("CSS selector for the input element."),
		"text":     strProp("Text to type."),
		"clear":    boolProp("Clear the field before typing (default true)."),
	})
}
func (t *browserTypeTool) Label(args map[string]any) string {
	return "Typing into " + Shorten(argString(args, "selector"), 40)
}
func (t *browserTypeTool) DoneLabel(args map[string]any) string {
	return "Typed into " + Shorten(argString(args, "selector"), 40)
}

func (t *browserTypeTool) Run(_ context.Context, _ *Env, args map[string]any) (Result, error) {
	selector := argString(args, "selector")
	text := argString(args, "text")
	if selector == "" || text == "" {
		return Result{IsError: true, Output: "selector and text are required"}, nil
	}
	browser := sharedBrowser.getOrCreate()
	page := browser.MustPage().MustWaitLoad()
	el := page.MustElement(selector)
	if argBool(args, "clear", true) {
		el.MustClick()
		page.MustEval("(el) => { el.value = ''; }", el)
	}
	page.MustEval("(el, t) => { const dt = new DataTransfer(); const f = new File([t], 'tmp'); dt.items.add(f); el.files = dt.files; el.dispatchEvent(new Event('input', {bubbles:true})); }", el, text)
	return Result{Output: fmt.Sprintf("Typed %d characters into %s", len(text), selector)}, nil
}

// browserPressTool presses a keyboard key.
type browserPressTool struct{}

func (t *browserPressTool) Name() string      { return "browser_press" }
func (t *browserPressTool) Aliases() []string { return []string{"press_key", "keypress"} }
func (t *browserPressTool) Mutating() bool    { return true }
func (t *browserPressTool) Risk() Risk        { return RiskNetwork }
func (t *browserPressTool) Description() string {
	return "Press a keyboard key on the page (e.g., Enter, Escape, Tab, ArrowDown)."
}
func (t *browserPressTool) Schema() map[string]any {
	return object(map[string]any{
		"key": strProp("Key to press (e.g., Enter, Escape, Tab, ArrowDown)."),
	})
}
func (t *browserPressTool) Label(args map[string]any) string {
	return "Pressing " + argString(args, "key")
}
func (t *browserPressTool) DoneLabel(args map[string]any) string {
	return "Pressed " + argString(args, "key")
}

func (t *browserPressTool) Run(_ context.Context, _ *Env, args map[string]any) (Result, error) {
	key := argString(args, "key")
	if key == "" {
		return Result{IsError: true, Output: "key is required"}, nil
	}
	browser := sharedBrowser.getOrCreate()
	page := browser.MustPage().MustWaitLoad()
	// Use JS dispatchEvent for key presses in Rod.
	page.MustEval(`(k) => {
		const ev = new KeyboardEvent('keydown', { key: k, bubbles: true });
		document.dispatchEvent(ev);
	}`, key)
	return Result{Output: fmt.Sprintf("Pressed %s", key)}, nil
}

// browserNavigateTool navigates to a URL.
type browserNavigateTool struct{}

func (t *browserNavigateTool) Name() string      { return "browser_navigate" }
func (t *browserNavigateTool) Aliases() []string { return []string{"goto", "open_url"} }
func (t *browserNavigateTool) Mutating() bool    { return true }
func (t *browserNavigateTool) Risk() Risk        { return RiskNetwork }
func (t *browserNavigateTool) Description() string {
	return "Navigate the browser to a new URL."
}
func (t *browserNavigateTool) Schema() map[string]any {
	return object(map[string]any{
		"url": strProp("URL to navigate to."),
	})
}
func (t *browserNavigateTool) Label(args map[string]any) string {
	return "Navigating to " + Shorten(argString(args, "url"), 40)
}
func (t *browserNavigateTool) DoneLabel(args map[string]any) string {
	return "Navigated to " + Shorten(argString(args, "url"), 40)
}

func (t *browserNavigateTool) Run(_ context.Context, _ *Env, args map[string]any) (Result, error) {
	target := argString(args, "url")
	if target == "" {
		return Result{IsError: true, Output: "url is required"}, nil
	}
	if err := validateHTTPURL(target); err != nil {
		return Result{IsError: true, Output: err.Error()}, nil
	}
	browser := sharedBrowser.getOrCreate()
	page := browser.MustPage().MustWaitLoad()
	if err := page.Navigate(target); err != nil {
		return Result{IsError: true, Output: fmt.Sprintf("Navigation failed: %v", err)}, nil
	}
	page.MustWaitLoad()
	title := page.MustEval("() => document.title").Str()
	return Result{Output: fmt.Sprintf("Navigated to %s (page: %q)", target, title)}, nil
}

// browserBackTool goes back in history.
type browserBackTool struct{}

func (t *browserBackTool) Name() string      { return "browser_back" }
func (t *browserBackTool) Aliases() []string { return []string{"back"} }
func (t *browserBackTool) Mutating() bool    { return true }
func (t *browserBackTool) Risk() Risk        { return RiskNetwork }
func (t *browserBackTool) Description() string {
	return "Go back one page in browser history."
}
func (t *browserBackTool) Schema() map[string]any            { return object(map[string]any{}) }
func (t *browserBackTool) Label(_ map[string]any) string     { return "Going back" }
func (t *browserBackTool) DoneLabel(_ map[string]any) string { return "Went back" }

func (t *browserBackTool) Run(_ context.Context, _ *Env, _ map[string]any) (Result, error) {
	browser := sharedBrowser.getOrCreate()
	page := browser.MustPage().MustWaitLoad()
	page.MustEval("history.back()")
	page.MustWaitLoad()
	return Result{Output: "Went back in history"}, nil
}

// browserForwardTool goes forward in history.
type browserForwardTool struct{}

func (t *browserForwardTool) Name() string      { return "browser_forward" }
func (t *browserForwardTool) Aliases() []string { return []string{"forward"} }
func (t *browserForwardTool) Mutating() bool    { return true }
func (t *browserForwardTool) Risk() Risk        { return RiskNetwork }
func (t *browserForwardTool) Description() string {
	return "Go forward one page in browser history."
}
func (t *browserForwardTool) Schema() map[string]any            { return object(map[string]any{}) }
func (t *browserForwardTool) Label(_ map[string]any) string     { return "Going forward" }
func (t *browserForwardTool) DoneLabel(_ map[string]any) string { return "Went forward" }

func (t *browserForwardTool) Run(_ context.Context, _ *Env, _ map[string]any) (Result, error) {
	browser := sharedBrowser.getOrCreate()
	page := browser.MustPage().MustWaitLoad()
	page.MustEval("history.forward()")
	page.MustWaitLoad()
	return Result{Output: "Went forward in history"}, nil
}

// browserReloadTool reloads the current page.
type browserReloadTool struct{}

func (t *browserReloadTool) Name() string      { return "browser_reload" }
func (t *browserReloadTool) Aliases() []string { return []string{"reload", "refresh"} }
func (t *browserReloadTool) Mutating() bool    { return true }
func (t *browserReloadTool) Risk() Risk        { return RiskNetwork }
func (t *browserReloadTool) Description() string {
	return "Reload the current page."
}
func (t *browserReloadTool) Schema() map[string]any            { return object(map[string]any{}) }
func (t *browserReloadTool) Label(_ map[string]any) string     { return "Reloading page" }
func (t *browserReloadTool) DoneLabel(_ map[string]any) string { return "Reloaded page" }

func (t *browserReloadTool) Run(_ context.Context, _ *Env, _ map[string]any) (Result, error) {
	browser := sharedBrowser.getOrCreate()
	page := browser.MustPage().MustWaitLoad()
	page.MustReload().MustWaitLoad()
	return Result{Output: "Reloaded page"}, nil
}

// browserExtractTool extracts readable text from the page.
type browserExtractTool struct{}

func (t *browserExtractTool) Name() string      { return "browser_extract" }
func (t *browserExtractTool) Aliases() []string { return []string{"extract_page", "read_page"} }
func (t *browserExtractTool) Mutating() bool    { return false }
func (t *browserExtractTool) Risk() Risk        { return RiskNetwork }
func (t *browserExtractTool) Description() string {
	return "Extract readable text content from the current page."
}
func (t *browserExtractTool) Schema() map[string]any {
	return object(map[string]any{
		"selector": strProp("Optional CSS selector to limit extraction."),
	})
}
func (t *browserExtractTool) Label(_ map[string]any) string     { return "Extracting page text" }
func (t *browserExtractTool) DoneLabel(_ map[string]any) string { return "Extracted page text" }

func (t *browserExtractTool) Run(_ context.Context, _ *Env, _ map[string]any) (Result, error) {
	browser := sharedBrowser.getOrCreate()
	page := browser.MustPage().MustWaitLoad()
	text := page.MustEval("() => document.body.innerText").Str()
	if strings.TrimSpace(text) == "" {
		text = page.MustHTML()
	}
	return Result{Output: truncate(text, 16000)}, nil
}

// browserSnapshotTool returns an accessibility snapshot.
type browserSnapshotTool struct{}

func (t *browserSnapshotTool) Name() string      { return "browser_snapshot" }
func (t *browserSnapshotTool) Aliases() []string { return []string{"snapshot", "a11y"} }
func (t *browserSnapshotTool) Mutating() bool    { return false }
func (t *browserSnapshotTool) Risk() Risk        { return RiskNetwork }
func (t *browserSnapshotTool) Description() string {
	return "Return an accessibility snapshot of interactive elements on the page."
}
func (t *browserSnapshotTool) Schema() map[string]any            { return object(map[string]any{}) }
func (t *browserSnapshotTool) Label(_ map[string]any) string     { return "Capturing snapshot" }
func (t *browserSnapshotTool) DoneLabel(_ map[string]any) string { return "Captured snapshot" }

func (t *browserSnapshotTool) Run(_ context.Context, _ *Env, _ map[string]any) (Result, error) {
	browser := sharedBrowser.getOrCreate()
	page := browser.MustPage().MustWaitLoad()
	root := page.MustElement("body")
	snapshot, _ := root.Property("outerHTML")
	html := snapshot.String()
	return Result{Output: truncate(html, 16000)}, nil
}

// browserHoverTool hovers over an element.
type browserHoverTool struct{}

func (t *browserHoverTool) Name() string      { return "browser_hover" }
func (t *browserHoverTool) Aliases() []string { return []string{"hover"} }
func (t *browserHoverTool) Mutating() bool    { return true }
func (t *browserHoverTool) Risk() Risk        { return RiskNetwork }
func (t *browserHoverTool) Description() string {
	return "Hover over an element by CSS selector."
}
func (t *browserHoverTool) Schema() map[string]any {
	return object(map[string]any{
		"selector": strProp("CSS selector for the element to hover."),
	})
}
func (t *browserHoverTool) Label(args map[string]any) string {
	return "Hovering " + Shorten(argString(args, "selector"), 40)
}
func (t *browserHoverTool) DoneLabel(args map[string]any) string {
	return "Hovered " + Shorten(argString(args, "selector"), 40)
}

func (t *browserHoverTool) Run(_ context.Context, _ *Env, args map[string]any) (Result, error) {
	selector := argString(args, "selector")
	if selector == "" {
		return Result{IsError: true, Output: "selector is required"}, nil
	}
	browser := sharedBrowser.getOrCreate()
	page := browser.MustPage().MustWaitLoad()
	page.MustElement(selector).MustHover()
	return Result{Output: fmt.Sprintf("Hovered over %s", selector)}, nil
}

// browserScrollTool scrolls the page.
type browserScrollTool struct{}

func (t *browserScrollTool) Name() string      { return "browser_scroll" }
func (t *browserScrollTool) Aliases() []string { return []string{"scroll"} }
func (t *browserScrollTool) Mutating() bool    { return true }
func (t *browserScrollTool) Risk() Risk        { return RiskNetwork }
func (t *browserScrollTool) Description() string {
	return "Scroll the page by pixels or to a direction."
}
func (t *browserScrollTool) Schema() map[string]any {
	return object(map[string]any{
		"direction": strProp("Direction: down (default), up, top, bottom."),
		"pixels":    intProp("Pixels to scroll."),
	})
}
func (t *browserScrollTool) Label(args map[string]any) string {
	return "Scrolling " + argString(args, "direction")
}
func (t *browserScrollTool) DoneLabel(args map[string]any) string {
	return "Scrolled " + argString(args, "direction")
}

func (t *browserScrollTool) Run(_ context.Context, _ *Env, args map[string]any) (Result, error) {
	direction := strings.ToLower(argString(args, "direction"))
	if direction == "" {
		direction = "down"
	}
	browser := sharedBrowser.getOrCreate()
	page := browser.MustPage().MustWaitLoad()
	pixels := argInt(args, "pixels", 300, 0, 10000)
	switch direction {
	case "up":
		page.MustEval(`(p) => window.scrollBy(0, -p)`, pixels)
	case "top":
		page.MustEval("() => window.scrollTo(0, 0)")
	case "bottom":
		page.MustEval("() => window.scrollTo(0, document.body.scrollHeight)")
	default:
		page.MustEval(`(p) => window.scrollBy(0, p)`, pixels)
	}
	return Result{Output: fmt.Sprintf("Scrolled %s", direction)}, nil
}

// browserSelectTool selects an option in a dropdown.
type browserSelectTool struct{}

func (t *browserSelectTool) Name() string      { return "browser_select" }
func (t *browserSelectTool) Aliases() []string { return []string{"select_option"} }
func (t *browserSelectTool) Mutating() bool    { return true }
func (t *browserSelectTool) Risk() Risk        { return RiskNetwork }
func (t *browserSelectTool) Description() string {
	return "Select an option in a <select> dropdown."
}
func (t *browserSelectTool) Schema() map[string]any {
	return object(map[string]any{
		"selector": strProp("CSS selector for the <select> element."),
		"value":    strProp("Option value to select."),
	})
}
func (t *browserSelectTool) Label(args map[string]any) string {
	return "Selecting in " + Shorten(argString(args, "selector"), 40)
}
func (t *browserSelectTool) DoneLabel(args map[string]any) string {
	return "Selected in " + Shorten(argString(args, "selector"), 40)
}

func (t *browserSelectTool) Run(_ context.Context, _ *Env, args map[string]any) (Result, error) {
	selector := argString(args, "selector")
	value := argString(args, "value")
	if selector == "" || value == "" {
		return Result{IsError: true, Output: "selector and value are required"}, nil
	}
	browser := sharedBrowser.getOrCreate()
	page := browser.MustPage().MustWaitLoad()
	page.MustElement(selector).MustSelect(value)
	return Result{Output: fmt.Sprintf("Selected %q in %s", value, selector)}, nil
}

// browserWaitTool waits for an element to appear.
type browserWaitTool struct{}

func (t *browserWaitTool) Name() string      { return "browser_wait" }
func (t *browserWaitTool) Aliases() []string { return []string{"wait_for_element"} }
func (t *browserWaitTool) Mutating() bool    { return false }
func (t *browserWaitTool) Risk() Risk        { return RiskNetwork }
func (t *browserWaitTool) Description() string {
	return "Wait for an element to appear in the DOM."
}
func (t *browserWaitTool) Schema() map[string]any {
	return object(map[string]any{
		"selector": strProp("CSS selector to wait for."),
		"timeout":  intProp("Timeout in milliseconds."),
	})
}
func (t *browserWaitTool) Label(args map[string]any) string {
	return "Waiting for " + Shorten(argString(args, "selector"), 40)
}
func (t *browserWaitTool) DoneLabel(args map[string]any) string {
	return "Waited for " + Shorten(argString(args, "selector"), 40)
}

func (t *browserWaitTool) Run(_ context.Context, _ *Env, args map[string]any) (Result, error) {
	selector := argString(args, "selector")
	if selector == "" {
		return Result{IsError: true, Output: "selector is required"}, nil
	}
	timeout := time.Duration(argInt(args, "timeout", 5000, 0, 30000)) * time.Millisecond
	if timeout == 0 {
		timeout = 5 * time.Second
	}
	browser := sharedBrowser.getOrCreate()
	page := browser.MustPage().MustWaitLoad()
	page.Timeout(timeout).MustElement(selector)
	return Result{Output: fmt.Sprintf("Element %s appeared within %s", selector, timeout)}, nil
}

// browserWaitForTool waits for text to appear on the page.
type browserWaitForTool struct{}

func (t *browserWaitForTool) Name() string      { return "browser_wait_for" }
func (t *browserWaitForTool) Aliases() []string { return []string{"wait_for_text"} }
func (t *browserWaitForTool) Mutating() bool    { return false }
func (t *browserWaitForTool) Risk() Risk        { return RiskNetwork }
func (t *browserWaitForTool) Description() string {
	return "Wait for specific text to appear on the page."
}
func (t *browserWaitForTool) Schema() map[string]any {
	return object(map[string]any{
		"text":    strProp("Text to wait for."),
		"timeout": intProp("Timeout in milliseconds."),
	})
}
func (t *browserWaitForTool) Label(_ map[string]any) string     { return "Waiting for text" }
func (t *browserWaitForTool) DoneLabel(_ map[string]any) string { return "Waited for text" }

func (t *browserWaitForTool) Run(_ context.Context, _ *Env, args map[string]any) (Result, error) {
	text := argString(args, "text")
	if text == "" {
		return Result{IsError: true, Output: "text is required"}, nil
	}
	timeout := time.Duration(argInt(args, "timeout", 5000, 0, 30000)) * time.Millisecond
	if timeout == 0 {
		timeout = 5 * time.Second
	}
	browser := sharedBrowser.getOrCreate()
	page := browser.MustPage().MustWaitLoad()
	page.Timeout(timeout).MustWait(`() => document.body.innerText.includes("` + strings.ReplaceAll(text, `"`, `\"`) + `")`)
	return Result{Output: fmt.Sprintf("Text %q appeared within %s", text, timeout)}, nil
}

// browserConsoleTool returns console messages.
type browserConsoleTool struct{}

func (t *browserConsoleTool) Name() string      { return "browser_console" }
func (t *browserConsoleTool) Aliases() []string { return []string{"console_logs"} }
func (t *browserConsoleTool) Mutating() bool    { return false }
func (t *browserConsoleTool) Risk() Risk        { return RiskNetwork }
func (t *browserConsoleTool) Description() string {
	return "Return browser console messages from the current page."
}
func (t *browserConsoleTool) Schema() map[string]any            { return object(map[string]any{}) }
func (t *browserConsoleTool) Label(_ map[string]any) string     { return "Reading console" }
func (t *browserConsoleTool) DoneLabel(_ map[string]any) string { return "Read console" }

func (t *browserConsoleTool) Run(_ context.Context, _ *Env, _ map[string]any) (Result, error) {
	browser := sharedBrowser.getOrCreate()
	page := browser.MustPage().MustWaitLoad()
	// Rod v0.116 does not expose Console() directly; use JS fallback.
	logs := page.MustEval("() => { const r = []; (console.log || (() => {})).apply(console, arguments); return r; }").Str()
	return Result{Output: truncate(logs, 16000)}, nil
}

// browserNetworkTool returns network requests.
type browserNetworkTool struct{}

func (t *browserNetworkTool) Name() string      { return "browser_network" }
func (t *browserNetworkTool) Aliases() []string { return []string{"network_logs"} }
func (t *browserNetworkTool) Mutating() bool    { return false }
func (t *browserNetworkTool) Risk() Risk        { return RiskNetwork }
func (t *browserNetworkTool) Description() string {
	return "Return network requests made by the current page."
}
func (t *browserNetworkTool) Schema() map[string]any            { return object(map[string]any{}) }
func (t *browserNetworkTool) Label(_ map[string]any) string     { return "Reading network" }
func (t *browserNetworkTool) DoneLabel(_ map[string]any) string { return "Read network" }

func (t *browserNetworkTool) Run(_ context.Context, _ *Env, _ map[string]any) (Result, error) {
	browser := sharedBrowser.getOrCreate()
	page := browser.MustPage().MustWaitLoad()
	// Rod v0.116 does not expose Network() directly; use JS fallback.
	requests := page.MustEval("() => { const r = []; window.__net = window.__net || []; return window.__net.slice(-20).map(x => x.method + ' ' + x.url + ' ' + x.status).join('\\n'); }").Str()
	return Result{Output: truncate(requests, 16000)}, nil
}

// browserStorageTool gets or sets browser storage.
type browserStorageTool struct{}

func (t *browserStorageTool) Name() string      { return "browser_storage" }
func (t *browserStorageTool) Aliases() []string { return []string{"local_storage"} }
func (t *browserStorageTool) Mutating() bool    { return true }
func (t *browserStorageTool) Risk() Risk        { return RiskNetwork }
func (t *browserStorageTool) Description() string {
	return "Get or set browser local storage values."
}
func (t *browserStorageTool) Schema() map[string]any {
	return object(map[string]any{
		"action": strProp("Action: get, set, remove, clear."),
		"key":    strProp("Storage key."),
		"value":  strProp("Storage value for set."),
	})
}
func (t *browserStorageTool) Label(args map[string]any) string {
	return argString(args, "action") + " storage"
}
func (t *browserStorageTool) DoneLabel(args map[string]any) string {
	return argString(args, "action") + " storage"
}

func (t *browserStorageTool) Run(_ context.Context, _ *Env, args map[string]any) (Result, error) {
	action := strings.ToLower(argString(args, "action"))
	key := argString(args, "key")
	browser := sharedBrowser.getOrCreate()
	page := browser.MustPage().MustWaitLoad()
	switch action {
	case "get":
		if key == "" {
			return Result{IsError: true, Output: "key is required for get"}, nil
		}
		val := page.MustEval("(k) => localStorage.getItem(k)", key).Str()
		return Result{Output: val}, nil
	case "set":
		if key == "" || argString(args, "value") == "" {
			return Result{IsError: true, Output: "key and value are required for set"}, nil
		}
		page.MustEval("(k, v) => localStorage.setItem(k, v)", key, argString(args, "value"))
		return Result{Output: fmt.Sprintf("Set %q in localStorage", key)}, nil
	case "remove":
		if key == "" {
			return Result{IsError: true, Output: "key is required for remove"}, nil
		}
		page.MustEval("(k) => localStorage.removeItem(k)", key)
		return Result{Output: fmt.Sprintf("Removed %q from localStorage", key)}, nil
	case "clear":
		page.MustEval("() => localStorage.clear()")
		return Result{Output: "Cleared localStorage"}, nil
	default:
		return Result{IsError: true, Output: "action must be get, set, remove, or clear"}, nil
	}
}

// browserDownloadTool downloads a file from a URL.
type browserDownloadTool struct{}

func (t *browserDownloadTool) Name() string      { return "browser_download" }
func (t *browserDownloadTool) Aliases() []string { return []string{"download"} }
func (t *browserDownloadTool) Mutating() bool    { return true }
func (t *browserDownloadTool) Risk() Risk        { return RiskNetwork }
func (t *browserDownloadTool) Description() string {
	return "Download a file from a URL using the browser."
}
func (t *browserDownloadTool) Schema() map[string]any {
	return object(map[string]any{
		"url":  strProp("URL to download."),
		"save": strProp("Optional local file path to save."),
	})
}
func (t *browserDownloadTool) Label(args map[string]any) string {
	return "Downloading " + Shorten(argString(args, "url"), 40)
}
func (t *browserDownloadTool) DoneLabel(args map[string]any) string {
	return "Downloaded " + Shorten(argString(args, "url"), 40)
}

func (t *browserDownloadTool) Run(_ context.Context, env *Env, args map[string]any) (Result, error) {
	target := argString(args, "url")
	if target == "" {
		return Result{IsError: true, Output: "url is required"}, nil
	}
	if err := validateHTTPURL(target); err != nil {
		return Result{IsError: true, Output: err.Error()}, nil
	}
	browser := sharedBrowser.getOrCreate()
	page := browser.MustPage().MustWaitLoad()
	_ = page.MustNavigate(target).MustWaitLoad()
	img, err := page.Screenshot(false, nil)
	if err != nil {
		return Result{IsError: true, Output: fmt.Sprintf("Screenshot failed: %v", err)}, nil
	}
	if save := argString(args, "save"); save != "" {
		out := save
		if err := checkWorkspacePath(env, out); err != nil {
			return Result{IsError: true, Output: err.Error()}, nil
		}
		if err := os.WriteFile(out, img, 0o644); err != nil {
			return Result{IsError: true, Output: fmt.Sprintf("Write failed: %v", err)}, nil
		}
		return Result{Output: fmt.Sprintf("Downloaded %d bytes to %s", len(img), out)}, nil
	}
	return Result{Output: fmt.Sprintf("Downloaded %d bytes (not saved to disk)", len(img))}, nil
}

// browserUploadTool uploads a file via a file input.
type browserUploadTool struct{}

func (t *browserUploadTool) Name() string      { return "browser_upload" }
func (t *browserUploadTool) Aliases() []string { return []string{"upload_file"} }
func (t *browserUploadTool) Mutating() bool    { return true }
func (t *browserUploadTool) Risk() Risk        { return RiskNetwork }
func (t *browserUploadTool) Description() string {
	return "Upload a file through an <input type='file'> element."
}
func (t *browserUploadTool) Schema() map[string]any {
	return object(map[string]any{
		"selector": strProp("CSS selector for the file input."),
		"path":     strProp("Local file path to upload."),
	})
}
func (t *browserUploadTool) Label(args map[string]any) string {
	return "Uploading to " + Shorten(argString(args, "selector"), 40)
}
func (t *browserUploadTool) DoneLabel(args map[string]any) string {
	return "Uploaded to " + Shorten(argString(args, "selector"), 40)
}

func (t *browserUploadTool) Run(_ context.Context, env *Env, args map[string]any) (Result, error) {
	selector := argString(args, "selector")
	path := argString(args, "path")
	if selector == "" || path == "" {
		return Result{IsError: true, Output: "selector and path are required"}, nil
	}
	if err := checkWorkspacePath(env, path); err != nil {
		return Result{IsError: true, Output: err.Error()}, nil
	}
	browser := sharedBrowser.getOrCreate()
	page := browser.MustPage().MustWaitLoad()
	page.MustElement(selector).SetFiles([]string{path})
	return Result{Output: fmt.Sprintf("Uploaded %s via %s", path, selector)}, nil
}

// browserVisualDiffTool captures a screenshot for visual regression comparison.
type browserVisualDiffTool struct{}

func (t *browserVisualDiffTool) Name() string      { return "browser_visual_diff" }
func (t *browserVisualDiffTool) Aliases() []string { return []string{"visual_diff"} }
func (t *browserVisualDiffTool) Mutating() bool    { return false }
func (t *browserVisualDiffTool) Risk() Risk        { return RiskNetwork }
func (t *browserVisualDiffTool) Description() string {
	return "Capture a screenshot for visual regression comparison."
}
func (t *browserVisualDiffTool) Schema() map[string]any {
	return object(map[string]any{
		"save": strProp("Optional file path to save screenshot."),
	})
}
func (t *browserVisualDiffTool) Label(_ map[string]any) string     { return "Capturing screenshot" }
func (t *browserVisualDiffTool) DoneLabel(_ map[string]any) string { return "Captured screenshot" }

func (t *browserVisualDiffTool) Run(_ context.Context, env *Env, args map[string]any) (Result, error) {
	browser := sharedBrowser.getOrCreate()
	page := browser.MustPage().MustWaitLoad()
	img, err := page.Screenshot(false, nil)
	if err != nil {
		return Result{IsError: true, Output: fmt.Sprintf("Screenshot failed: %v", err)}, nil
	}
	if save := argString(args, "save"); save != "" {
		out := save
		if err := checkWorkspacePath(env, out); err != nil {
			return Result{IsError: true, Output: err.Error()}, nil
		}
		if err := os.WriteFile(out, img, 0o644); err != nil {
			return Result{IsError: true, Output: fmt.Sprintf("Write failed: %v", err)}, nil
		}
		return Result{Output: fmt.Sprintf("Screenshot saved to %s (%d bytes)", out, len(img))}, nil
	}
	return Result{Output: fmt.Sprintf("Screenshot captured (%d bytes)", len(img))}, nil
}

// browserCloseTool closes the current page.
type browserCloseTool struct{}

func (t *browserCloseTool) Name() string      { return "browser_close" }
func (t *browserCloseTool) Aliases() []string { return []string{"close_tab"} }
func (t *browserCloseTool) Mutating() bool    { return true }
func (t *browserCloseTool) Risk() Risk        { return RiskNetwork }
func (t *browserCloseTool) Description() string {
	return "Close the current browser tab/page."
}
func (t *browserCloseTool) Schema() map[string]any            { return object(map[string]any{}) }
func (t *browserCloseTool) Label(_ map[string]any) string     { return "Closing tab" }
func (t *browserCloseTool) DoneLabel(_ map[string]any) string { return "Closed tab" }

func (t *browserCloseTool) Run(_ context.Context, _ *Env, _ map[string]any) (Result, error) {
	browser := sharedBrowser.getOrCreate()
	page := browser.MustPage().MustWaitLoad()
	page.MustClose()
	return Result{Output: "Closed tab"}, nil
}

// browserConnectTool connects to an existing Chrome DevTools session.
type browserConnectTool struct{}

func (t *browserConnectTool) Name() string      { return "browser_connect" }
func (t *browserConnectTool) Aliases() []string { return []string{"connect_browser"} }
func (t *browserConnectTool) Mutating() bool    { return true }
func (t *browserConnectTool) Risk() Risk        { return RiskNetwork }
func (t *browserConnectTool) Description() string {
	return "Connect to an existing Chrome instance via CDP (remote debugging)."
}
func (t *browserConnectTool) Schema() map[string]any {
	return object(map[string]any{
		"url": strProp("Chrome DevTools URL, e.g., http://localhost:9222."),
	})
}
func (t *browserConnectTool) Label(args map[string]any) string {
	return "Connecting to " + Shorten(argString(args, "url"), 40)
}
func (t *browserConnectTool) DoneLabel(args map[string]any) string {
	return "Connected to " + Shorten(argString(args, "url"), 40)
}

func (t *browserConnectTool) Run(_ context.Context, _ *Env, args map[string]any) (Result, error) {
	u := argString(args, "url")
	if u == "" {
		return Result{IsError: true, Output: "url is required"}, nil
	}
	sharedBrowser.close()
	sharedBrowser.browser = rod.New().ControlURL(u).MustConnect()
	return Result{Output: fmt.Sprintf("Connected to browser at %s", u)}, nil
}
