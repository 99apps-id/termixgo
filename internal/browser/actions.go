package browser

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"
	"unicode"
)

// WaitFor polls the page because a navigation can interrupt an event stream;
// polling the state the caller actually wants is simpler and survives a reload.
const waitPollInterval = 200 * time.Millisecond

// maxReadBytes bounds one extraction, so a huge DOM cannot become the prompt.
const maxReadBytes = 64 * 1024

// maxCaptureHeight bounds a full-page screenshot, which on a long feed can be
// tens of thousands of pixels tall and tens of megabytes encoded.
const maxCaptureHeight = 8000

// ElementPoint is the viewport point at the centre of a matched element.
type ElementPoint struct {
	X      float64
	Y      float64
	Width  float64
	Height float64
}

// Cookie is one browser cookie.
type Cookie struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Domain string `json:"domain"`
	Path   string `json:"path"`
}

// jsLiteral quotes a string as a JavaScript literal, so model text never
// becomes script text.
func jsLiteral(value string) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return `""`
	}
	return string(encoded)
}

// evaluateLocked runs JavaScript and returns its value. The caller holds
// actionMu.
func (s *Session) evaluateLocked(ctx context.Context, expression string) (any, error) {
	if s.conn == nil {
		return nil, errors.New("the browser is not connected")
	}
	result, err := s.conn.call(ctx, "Runtime.evaluate", map[string]any{
		"expression":    expression,
		"returnByValue": true,
		"awaitPromise":  true,
		"userGesture":   true,
	})
	if err != nil {
		return nil, err
	}
	var payload struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
		ExceptionDetails *struct {
			Text      string `json:"text"`
			Exception *struct {
				Description string `json:"description"`
			} `json:"exception"`
		} `json:"exceptionDetails"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		return nil, fmt.Errorf("cannot read the browser's answer: %v", err)
	}
	if payload.ExceptionDetails != nil {
		message := payload.ExceptionDetails.Text
		if payload.ExceptionDetails.Exception != nil && payload.ExceptionDetails.Exception.Description != "" {
			message = payload.ExceptionDetails.Exception.Description
		}
		return nil, fmt.Errorf("the page raised an error: %s", firstLine(message))
	}
	if len(payload.Result.Value) == 0 {
		return nil, nil
	}
	var value any
	if err := json.Unmarshal(payload.Result.Value, &value); err != nil {
		return nil, err
	}
	return value, nil
}

// Evaluate runs JavaScript in the page.
func (s *Session) Evaluate(ctx context.Context, expression string) (any, error) {
	if strings.TrimSpace(expression) == "" {
		return nil, errors.New("expression is required")
	}
	s.actionMu.Lock()
	defer s.actionMu.Unlock()
	s.touch()
	return s.evaluateLocked(ctx, expression)
}

// Navigate opens a URL and waits for the document to finish loading.
func (s *Session) Navigate(ctx context.Context, rawURL string) (string, error) {
	target, err := NormalizeURL(rawURL)
	if err != nil {
		return "", err
	}
	s.actionMu.Lock()
	defer s.actionMu.Unlock()
	s.touch()
	if _, err := s.conn.call(ctx, "Page.navigate", map[string]any{"url": target}); err != nil {
		return "", err
	}
	if err := s.waitReadyLocked(ctx); err != nil {
		return "", err
	}
	// One short settle: a client-rendered route often paints just after
	// load, and a click on the old DOM would land nowhere.
	select {
	case <-ctx.Done():
	case <-time.After(400 * time.Millisecond):
	}
	return s.pageSummaryLocked(ctx)
}

// waitReadyLocked polls readyState until the document is complete.
func (s *Session) waitReadyLocked(ctx context.Context) error {
	deadline := time.Now().Add(20 * time.Second)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		value, err := s.evaluateLocked(ctx, `document.readyState`)
		if err == nil {
			if state, ok := value.(string); ok && state == "complete" {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return errors.New("the page did not finish loading within 20 seconds")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(waitPollInterval):
		}
	}
}

// pageSummaryLocked reports the title, URL and a text head after a step.
func (s *Session) pageSummaryLocked(ctx context.Context) (string, error) {
	value, err := s.evaluateLocked(ctx, `(() => ({
  title: document.title || '',
  url: location.href,
  text: (document.body ? document.body.innerText : '').slice(0, 1200)
}))()`)
	if err != nil {
		return "", err
	}
	summary, ok := value.(map[string]any)
	if !ok {
		return "", errors.New("cannot read the page summary")
	}
	return fmt.Sprintf("Opened %v\nTitle: %v\nExcerpt:\n%v",
		summary["url"], summary["title"], strings.TrimSpace(toString(summary["text"]))), nil
}

func toString(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	return fmt.Sprint(value)
}

func firstLine(text string) string {
	if index := strings.IndexByte(text, '\n'); index >= 0 {
		return text[:index]
	}
	return text
}

// Click presses the left button at a selector, at visible text, or at a raw
// coordinate. The element is scrolled into view first, and the press is
// dispatched as a real mouse event so hover, focus and pointer handlers all
// run the way a person's click would trigger them.
func (s *Session) Click(ctx context.Context, selector, text string, x, y float64) error {
	if strings.TrimSpace(selector) == "" && strings.TrimSpace(text) == "" && x == 0 && y == 0 {
		return errors.New("a selector, a text label or a coordinate is required")
	}
	s.actionMu.Lock()
	defer s.actionMu.Unlock()
	s.touch()
	if selector != "" || text != "" {
		point, err := s.elementPointLocked(ctx, selector, text)
		if err != nil {
			return err
		}
		x, y = point.X, point.Y
	}
	return s.clickPointLocked(ctx, x, y)
}

func (s *Session) clickPointLocked(ctx context.Context, x, y float64) error {
	events := []map[string]any{
		{"type": "mouseMoved", "x": x, "y": y, "button": "none"},
		{"type": "mousePressed", "x": x, "y": y, "button": "left", "clickCount": 1},
		{"type": "mouseReleased", "x": x, "y": y, "button": "left", "clickCount": 1},
	}
	for _, event := range events {
		if _, err := s.conn.call(ctx, "Input.dispatchMouseEvent", event); err != nil {
			return err
		}
	}
	return nil
}

// DoubleClick sends two presses, which is what a canvas or a map needs.
func (s *Session) DoubleClick(ctx context.Context, selector, text string, x, y float64) error {
	s.actionMu.Lock()
	defer s.actionMu.Unlock()
	s.touch()
	if selector != "" || text != "" {
		point, err := s.elementPointLocked(ctx, selector, text)
		if err != nil {
			return err
		}
		x, y = point.X, point.Y
	}
	for _, event := range []map[string]any{
		{"type": "mouseMoved", "x": x, "y": y, "button": "none"},
		{"type": "mousePressed", "x": x, "y": y, "button": "left", "clickCount": 1},
		{"type": "mouseReleased", "x": x, "y": y, "button": "left", "clickCount": 1},
		{"type": "mousePressed", "x": x, "y": y, "button": "left", "clickCount": 2},
		{"type": "mouseReleased", "x": x, "y": y, "button": "left", "clickCount": 2},
	} {
		if _, err := s.conn.call(ctx, "Input.dispatchMouseEvent", event); err != nil {
			return err
		}
	}
	return nil
}

// elementPointLocked resolves a selector or a visible label to a point.
func (s *Session) elementPointLocked(ctx context.Context, selector, text string) (ElementPoint, error) {
	value, err := s.evaluateLocked(ctx, elementPointExpression(selector, text))
	if err != nil {
		return ElementPoint{}, err
	}
	result, ok := value.(map[string]any)
	if !ok {
		return ElementPoint{}, errors.New("no element matched")
	}
	if message, ok := result["error"].(string); ok {
		return ElementPoint{}, errors.New(message)
	}
	return ElementPoint{
		X:      toFloat(result["x"]),
		Y:      toFloat(result["y"]),
		Width:  toFloat(result["width"]),
		Height: toFloat(result["height"]),
	}, nil
}

func toFloat(value any) float64 {
	switch typed := value.(type) {
	case float64:
		return typed
	case int:
		return float64(typed)
	default:
		return 0
	}
}

func elementPointExpression(selector, text string) string {
	selectorLiteral := "null"
	if strings.TrimSpace(selector) != "" {
		selectorLiteral = jsLiteral(selector)
	}
	textLiteral := "null"
	if strings.TrimSpace(text) != "" {
		textLiteral = jsLiteral(text)
	}
	return `(() => {
  const q = ` + selectorLiteral + `;
  const t = ` + textLiteral + `;
  const visible = (e) => { const r = e.getBoundingClientRect(); return r.width > 0 && r.height > 0; };
  let el = null;
  if (q) {
    try { el = document.querySelector(q); } catch (err) { return {error: 'invalid selector: ' + err}; }
  }
  if (!el && t) {
    const wanted = t.trim().toLowerCase();
    const nodes = Array.from(document.querySelectorAll('a,button,input[type=submit],input[type=button],input[type=reset],[role=button],label,summary,option,li,span,div'));
    el = nodes.find(e => visible(e) && ((e.innerText || e.value || '').trim().toLowerCase() === wanted))
      || nodes.find(e => visible(e) && ((e.innerText || e.value || '').trim().toLowerCase().includes(wanted)));
  }
  if (!el) return {error: 'no matching element: ' + (q || t)};
  el.scrollIntoView({block: 'center', inline: 'center'});
  const r = el.getBoundingClientRect();
  if (r.width === 0 && r.height === 0) return {error: 'the element has no size'};
  return {x: r.left + r.width / 2, y: r.top + r.height / 2, width: r.width, height: r.height};
})()`
}

// Type focuses an element and inserts text as if it were typed. insertText
// fires the input events a framework listens to, so a controlled field updates
// rather than showing text its state does not know about.
func (s *Session) Type(ctx context.Context, selector, text string, clear, submit bool) error {
	s.actionMu.Lock()
	defer s.actionMu.Unlock()
	s.touch()

	if strings.TrimSpace(selector) != "" {
		focus := `(() => {
  let e = null;
  try { e = document.querySelector(` + jsLiteral(selector) + `); } catch (err) { return {error: 'invalid selector: ' + err}; }
  if (!e) return {error: 'no matching element: ' + ` + jsLiteral(selector) + `};
  e.focus();
  return {ok: true};
})()`
		value, err := s.evaluateLocked(ctx, focus)
		if err != nil {
			return err
		}
		if outcome, ok := value.(map[string]any); ok {
			if message, ok := outcome["error"].(string); ok {
				return errors.New(message)
			}
		}
	}
	if clear {
		// A real select-all and delete reaches the control's own value model,
		// where setting .value directly would leave React believing the old
		// text is still there.
		if err := s.pressLocked(ctx, "ctrl+a"); err != nil {
			return err
		}
		if err := s.pressLocked(ctx, "backspace"); err != nil {
			return err
		}
	}
	if text != "" {
		if _, err := s.conn.call(ctx, "Input.insertText", map[string]any{"text": text}); err != nil {
			return err
		}
	}
	if submit {
		return s.pressLocked(ctx, "enter")
	}
	return nil
}

// Press sends one key or modifier combination.
func (s *Session) Press(ctx context.Context, combo string) error {
	s.actionMu.Lock()
	defer s.actionMu.Unlock()
	s.touch()
	return s.pressLocked(ctx, combo)
}

// keySpec is the protocol description of one key press.
type keySpec struct {
	Key       string
	Code      string
	VK        int
	Text      string
	Modifiers int
}

// modifier bits as the protocol defines them.
const (
	modAlt   = 1
	modCtrl  = 2
	modMeta  = 4
	modShift = 8
)

var namedKeys = map[string]keySpec{
	"enter":     {Key: "Enter", Code: "Enter", VK: 13, Text: "\r"},
	"tab":       {Key: "Tab", Code: "Tab", VK: 9, Text: "\t"},
	"esc":       {Key: "Escape", Code: "Escape", VK: 27},
	"space":     {Key: " ", Code: "Space", VK: 32, Text: " "},
	"backspace": {Key: "Backspace", Code: "Backspace", VK: 8},
	"delete":    {Key: "Delete", Code: "Delete", VK: 46},
	"up":        {Key: "ArrowUp", Code: "ArrowUp", VK: 38},
	"down":      {Key: "ArrowDown", Code: "ArrowDown", VK: 40},
	"left":      {Key: "ArrowLeft", Code: "ArrowLeft", VK: 37},
	"right":     {Key: "ArrowRight", Code: "ArrowRight", VK: 39},
	"home":      {Key: "Home", Code: "Home", VK: 36},
	"end":       {Key: "End", Code: "End", VK: 35},
	"pageup":    {Key: "PageUp", Code: "PageUp", VK: 33},
	"pagedown":  {Key: "PageDown", Code: "PageDown", VK: 34},
}

// parseKey turns "ctrl+shift+s" or "enter" into a key spec.
func parseKey(combo string) (keySpec, error) {
	trimmed := strings.TrimSpace(combo)
	if trimmed == "" {
		return keySpec{}, errors.New("a key is required")
	}
	parts := strings.FieldsFunc(trimmed, func(r rune) bool { return r == '+' || r == ' ' })
	if len(parts) == 0 {
		return keySpec{}, fmt.Errorf("cannot read key %q", combo)
	}
	spec := keySpec{}
	key := ""
	for _, raw := range parts {
		part := strings.ToLower(strings.TrimSpace(raw))
		switch part {
		case "ctrl", "control":
			spec.Modifiers |= modCtrl
		case "alt", "option":
			spec.Modifiers |= modAlt
		case "shift":
			spec.Modifiers |= modShift
		case "cmd", "command", "meta", "super", "win":
			spec.Modifiers |= modMeta
		case "return":
			key = "enter"
		case "escape":
			key = "esc"
		case "del":
			key = "delete"
		default:
			key = part
		}
	}
	if key == "" {
		return keySpec{}, errors.New("a key is required after the modifiers")
	}
	if named, ok := namedKeys[key]; ok {
		named.Modifiers = spec.Modifiers
		return named, nil
	}
	runes := []rune(key)
	if len(runes) != 1 {
		return keySpec{}, fmt.Errorf("unknown key %q", key)
	}
	character := runes[0]
	text := string(character)
	if spec.Modifiers&modShift != 0 {
		text = string(unicode.ToUpper(character))
	}
	upper := unicode.ToUpper(character)
	spec.Key = text
	spec.Text = text
	spec.VK = int(upper)
	switch {
	case unicode.IsLetter(character):
		spec.Code = "Key" + string(upper)
	case unicode.IsDigit(character):
		spec.Code = "Digit" + string(character)
	default:
		spec.Code = ""
	}
	return spec, nil
}

// pressLocked dispatches keyDown and keyUp for one spec.
func (s *Session) pressLocked(ctx context.Context, combo string) error {
	spec, err := parseKey(combo)
	if err != nil {
		return err
	}
	base := map[string]any{
		"modifiers":             spec.Modifiers,
		"key":                   spec.Key,
		"code":                  spec.Code,
		"windowsVirtualKeyCode": spec.VK,
		"nativeVirtualKeyCode":  spec.VK,
	}
	down := cloneEvent(base)
	down["type"] = "keyDown"
	if spec.Text != "" {
		down["text"] = spec.Text
		down["unmodifiedText"] = spec.Text
	}
	if _, err := s.conn.call(ctx, "Input.dispatchKeyEvent", down); err != nil {
		return err
	}
	up := cloneEvent(base)
	up["type"] = "keyUp"
	_, err = s.conn.call(ctx, "Input.dispatchKeyEvent", up)
	return err
}

func cloneEvent(source map[string]any) map[string]any {
	cloned := make(map[string]any, len(source)+2)
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

// Scroll scrolls the page. A positive delta moves down.
func (s *Session) Scroll(ctx context.Context, direction string, amount int) error {
	if amount < 1 {
		amount = 1
	}
	if amount > 50 {
		amount = 50
	}
	s.actionMu.Lock()
	defer s.actionMu.Unlock()
	s.touch()
	x, y := 640.0, 400.0
	if value, err := s.evaluateLocked(ctx, `({x: window.innerWidth / 2, y: window.innerHeight / 2})`); err == nil {
		if point, ok := value.(map[string]any); ok {
			x, y = toFloat(point["x"]), toFloat(point["y"])
		}
	}
	delta := float64(120 * amount)
	if strings.EqualFold(strings.TrimSpace(direction), "up") {
		delta = -delta
	}
	_, err := s.conn.call(ctx, "Input.dispatchMouseEvent", map[string]any{
		"type": "mouseWheel", "x": x, "y": y, "deltaX": 0, "deltaY": delta,
	})
	return err
}

// WaitFor blocks until a selector, a text fragment or an expression becomes
// true. It reports what it waited for when the deadline passes.
func (s *Session) WaitFor(ctx context.Context, selector, text, expression string, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	if timeout > 60*time.Second {
		timeout = 60 * time.Second
	}
	condition := ""
	label := ""
	switch {
	case strings.TrimSpace(expression) != "":
		condition = expression
		label = "the expression"
	case strings.TrimSpace(selector) != "":
		condition = `!!document.querySelector(` + jsLiteral(selector) + `)`
		label = "selector " + selector
	case strings.TrimSpace(text) != "":
		condition = `document.body ? document.body.innerText.includes(` + jsLiteral(text) + `) : false`
		label = "text " + text
	default:
		return errors.New("a selector, text or expression is required")
	}
	s.actionMu.Lock()
	defer s.actionMu.Unlock()
	s.touch()
	deadline := time.Now().Add(timeout)
	for {
		value, err := s.evaluateLocked(ctx, condition)
		if err == nil && truthy(value) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out after %s waiting for %s", timeout.Round(time.Second), label)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(waitPollInterval):
		}
	}
}

func truthy(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case nil:
		return false
	case string:
		return typed != ""
	case float64:
		return typed != 0
	default:
		return true
	}
}

// Read returns page content: text, html, links or form controls.
func (s *Session) Read(ctx context.Context, mode, selector string) (string, error) {
	container := "document.body"
	if strings.TrimSpace(selector) != "" {
		container = "document.querySelector(" + jsLiteral(selector) + ")"
	}
	var expression string
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", "text":
		expression = `(() => { const e = ` + container + `; return e ? e.innerText : ''; })()`
	case "html":
		expression = `(() => { const e = ` + container + `; return e ? e.outerHTML : ''; })()`
	case "links":
		expression = `(() => Array.from(document.querySelectorAll('a[href]')).slice(0, 200).map(a => ((a.innerText || '').trim() + ' -> ' + a.href)).join('\n'))()`
	case "forms":
		expression = `(() => {
  const describe = (e, index) => {
    let selector = e.tagName.toLowerCase();
    if (e.id) selector = '#' + e.id;
    else if (e.getAttribute('name')) selector = e.tagName.toLowerCase() + '[name="' + e.getAttribute('name') + '"]';
    const label = (e.innerText || e.value || e.placeholder || e.getAttribute('aria-label') || '').trim().replace(/\s+/g, ' ');
    return '[' + (index + 1) + '] <' + e.tagName.toLowerCase() + ' type=' + (e.type || '') + ' name=' + (e.getAttribute('name') || '') + ' selector=' + selector + '> ' + label;
  };
  const nodes = Array.from(document.querySelectorAll('input:not([type=hidden]),textarea,select,button,[role=button]')).slice(0, 80);
  return nodes.map(describe).join('\n');
})()`
	default:
		return "", fmt.Errorf("mode must be text, html, links or forms, not %q", mode)
	}
	s.actionMu.Lock()
	defer s.actionMu.Unlock()
	s.touch()
	value, err := s.evaluateLocked(ctx, expression)
	if err != nil {
		return "", err
	}
	text := strings.TrimSpace(toString(value))
	if text == "" {
		return "(the page returned no content)", nil
	}
	if len(text) > maxReadBytes {
		text = text[:maxReadBytes] + "\n... [clipped]"
	}
	return text, nil
}

// Screenshot captures the viewport, the full page or one element.
func (s *Session) Screenshot(ctx context.Context, fullPage bool, selector string) ([]byte, error) {
	s.actionMu.Lock()
	defer s.actionMu.Unlock()
	s.touch()

	params := map[string]any{"format": "png", "fromSurface": true}
	switch {
	case strings.TrimSpace(selector) != "":
		rect, err := s.elementPointLocked(ctx, selector, "")
		if err != nil {
			return nil, err
		}
		left := rect.X - rect.Width/2
		top := rect.Y - rect.Height/2
		params["clip"] = map[string]any{"x": left, "y": top, "width": rect.Width, "height": rect.Height, "scale": 1}
	case fullPage:
		width, height, err := s.contentSizeLocked(ctx)
		if err != nil {
			return nil, err
		}
		if height > maxCaptureHeight {
			height = maxCaptureHeight
		}
		params["captureBeyondViewport"] = true
		params["clip"] = map[string]any{"x": 0.0, "y": 0.0, "width": width, "height": height, "scale": 1}
	}
	result, err := s.conn.call(ctx, "Page.captureScreenshot", params)
	if err != nil {
		return nil, err
	}
	var payload struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		return nil, fmt.Errorf("cannot read the screenshot: %v", err)
	}
	data, err := base64.StdEncoding.DecodeString(payload.Data)
	if err != nil {
		return nil, fmt.Errorf("cannot decode the screenshot: %v", err)
	}
	return data, nil
}

func (s *Session) contentSizeLocked(ctx context.Context) (float64, float64, error) {
	result, err := s.conn.call(ctx, "Page.getLayoutMetrics", nil)
	if err != nil {
		return 0, 0, err
	}
	var payload struct {
		CSSContentSize struct {
			Width  float64 `json:"width"`
			Height float64 `json:"height"`
		} `json:"cssContentSize"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		return 0, 0, err
	}
	width := math.Max(1, payload.CSSContentSize.Width)
	height := math.Max(1, payload.CSSContentSize.Height)
	return width, height, nil
}

// ListTabs reports the open page targets.
func (s *Session) ListTabs(ctx context.Context) ([]Target, error) {
	s.actionMu.Lock()
	defer s.actionMu.Unlock()
	s.touch()
	targets, err := s.listTargets(ctx)
	if err != nil {
		return nil, err
	}
	var pages []Target
	for _, target := range targets {
		if target.Type == "page" {
			pages = append(pages, target)
		}
	}
	return pages, nil
}

// NewTab opens a tab and attaches to it.
func (s *Session) NewTab(ctx context.Context, rawURL string) (Target, error) {
	target, err := NormalizeURL(rawURL)
	if err != nil {
		return Target{}, err
	}
	s.actionMu.Lock()
	defer s.actionMu.Unlock()
	s.touch()
	created, err := s.newTarget(ctx, target)
	if err != nil {
		return Target{}, err
	}
	if err := s.attachLocked(ctx, created); err != nil {
		return Target{}, err
	}
	return created, nil
}

// SelectTab attaches to another open tab.
func (s *Session) SelectTab(ctx context.Context, id string) (Target, error) {
	trimmed := strings.TrimSpace(id)
	if trimmed == "" {
		return Target{}, errors.New("a tab id is required")
	}
	s.actionMu.Lock()
	defer s.actionMu.Unlock()
	s.touch()
	targets, err := s.listTargets(ctx)
	if err != nil {
		return Target{}, err
	}
	for _, target := range targets {
		if target.Type != "page" {
			continue
		}
		if target.ID == trimmed {
			if err := s.attachLocked(ctx, target); err != nil {
				return Target{}, err
			}
			return target, nil
		}
	}
	return Target{}, fmt.Errorf("no open tab has id %q", trimmed)
}

// CloseTab closes a tab and attaches to another one.
func (s *Session) CloseTab(ctx context.Context, id string) error {
	trimmed := strings.TrimSpace(id)
	if trimmed == "" {
		return errors.New("a tab id is required")
	}
	s.actionMu.Lock()
	defer s.actionMu.Unlock()
	s.touch()
	if err := s.requestJSON(ctx, http.MethodGet, "/json/close/"+trimmed, nil); err != nil {
		return err
	}
	targets, err := s.listTargets(ctx)
	if err != nil {
		return nil
	}
	for _, target := range targets {
		if target.Type == "page" && target.ID != trimmed {
			return s.attachLocked(ctx, target)
		}
	}
	created, err := s.newTarget(ctx, "about:blank")
	if err != nil {
		return nil
	}
	return s.attachLocked(ctx, created)
}

// attachLocked points the session at another tab.
func (s *Session) attachLocked(ctx context.Context, target Target) error {
	if target.WebSocketDebuggerURL == "" {
		return errors.New("the tab published no debugger URL")
	}
	if s.conn != nil {
		s.conn.Close()
	}
	conn, err := dialCDP(ctx, target.WebSocketDebuggerURL)
	if err != nil {
		return err
	}
	s.conn = conn
	_, _ = s.conn.call(ctx, "Emulation.setDeviceMetricsOverride", map[string]any{
		"width": 1280, "height": 800, "deviceScaleFactor": 1, "mobile": false,
	})
	return nil
}

// Cookies reads every cookie visible to the page.
func (s *Session) Cookies(ctx context.Context) ([]Cookie, error) {
	s.actionMu.Lock()
	defer s.actionMu.Unlock()
	s.touch()
	result, err := s.conn.call(ctx, "Network.getCookies", map[string]any{})
	if err != nil {
		return nil, err
	}
	var payload struct {
		Cookies []Cookie `json:"cookies"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		return nil, err
	}
	return payload.Cookies, nil
}

// SetCookie sets one cookie for a URL.
func (s *Session) SetCookie(ctx context.Context, name, value, rawURL string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("a cookie name is required")
	}
	var target string
	if strings.TrimSpace(rawURL) != "" {
		normalized, err := NormalizeURL(rawURL)
		if err != nil {
			return err
		}
		target = normalized
	}
	s.actionMu.Lock()
	defer s.actionMu.Unlock()
	s.touch()
	if target == "" {
		current, err := s.evaluateLocked(ctx, `location.href`)
		if err != nil {
			return errors.New("a url is required when no page is open")
		}
		target = toString(current)
	}
	result, err := s.conn.call(ctx, "Network.setCookie", map[string]any{
		"name": name, "value": value, "url": target,
	})
	if err != nil {
		return err
	}
	var payload struct {
		Success bool `json:"success"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		return err
	}
	if !payload.Success {
		return fmt.Errorf("the browser refused cookie %q", name)
	}
	return nil
}

// ClearCookies drops every cookie in the profile.
func (s *Session) ClearCookies(ctx context.Context) error {
	s.actionMu.Lock()
	defer s.actionMu.Unlock()
	s.touch()
	_, err := s.conn.call(ctx, "Network.clearBrowserCookies", nil)
	return err
}
