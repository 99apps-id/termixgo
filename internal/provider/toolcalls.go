package provider

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

var (
	reTrailingCommas = regexp.MustCompile(`,\s*([}\]])`)
	rePythonTrue     = regexp.MustCompile(`:\s*True\b`)
	rePythonFalse    = regexp.MustCompile(`:\s*False\b`)
	rePythonNone     = regexp.MustCompile(`:\s*None\b`)
	reSingleQuoted   = regexp.MustCompile(`'([^'\\]*(?:\\.[^'\\]*)*)'`)
)

// toolCallAccumulator rebuilds streamed tool calls. Providers send a call in
// fragments keyed by index: the name arrives once, the arguments arrive in
// pieces that only form valid JSON when concatenated.
//
// Some OpenAI-compatible gateways omit index on continuation chunks or reuse
// index 0 across distinct tool calls in the same response. Tracking the last
// active slot and splitting when a new ID or function name arrives on an
// occupied slot keeps arguments attached to the call that owns them.
type toolCallAccumulator struct {
	order   []int
	byIndex map[int]*ToolCall
	lastKey int
	hasLast bool
	nextSeq int
}

func newToolCallAccumulator() *toolCallAccumulator {
	return &toolCallAccumulator{byIndex: map[int]*ToolCall{}, nextSeq: 1000}
}

func (a *toolCallAccumulator) add(delta openAIToolDelta) {
	name := delta.resolvedName()
	args := delta.resolvedArguments()

	// Resolve which slot this delta belongs to.
	var key int
	if delta.HasIndex || delta.Index != 0 {
		key = delta.Index
	} else if delta.ID != "" {
		found := false
		for _, k := range a.order {
			if a.byIndex[k].ID == delta.ID {
				key = k
				found = true
				break
			}
		}
		if !found {
			if a.hasLast && name == "" && a.byIndex[a.lastKey].ID == "" {
				key = a.lastKey
			} else {
				key = a.nextSeq
				a.nextSeq++
			}
		}
	} else if a.hasLast && name == "" {
		key = a.lastKey
	} else if a.hasLast && a.byIndex[a.lastKey].Name == "" {
		key = a.lastKey
	} else {
		key = delta.Index
	}

	call, ok := a.byIndex[key]
	if ok && name != "" && call.Name != "" {
		// A new tool call arrived on an already-populated index (common on
		// gateways that hardcode index:0 for every tool call in a batch).
		newCallOnSameIndex := (delta.ID != "" && call.ID != "" && delta.ID != call.ID) ||
			(name != call.Name) ||
			(strings.TrimSpace(call.Arguments) != "" && json.Valid([]byte(strings.TrimSpace(call.Arguments))))
		if newCallOnSameIndex {
			key = a.nextSeq
			a.nextSeq++
			ok = false
		}
	}
	if !ok {
		call = &ToolCall{}
		a.byIndex[key] = call
		a.order = append(a.order, key)
	}
	a.lastKey = key
	a.hasLast = true

	if delta.ID != "" {
		call.ID = delta.ID
	}
	if name != "" && call.Name == "" {
		call.Name = name
	} else if name != "" && call.Name != name && strings.HasPrefix(name, call.Name) {
		// Handle gateways that stream the function name incrementally.
		call.Name = name
	}
	call.Arguments += args
}

// finish returns complete calls in arrival order, with missing ids and empty
// arguments normalised so a tool executor never sees a half-built call.
func (a *toolCallAccumulator) finish() []ToolCall {
	out := make([]ToolCall, 0, len(a.order))
	for _, key := range a.order {
		call := a.byIndex[key]
		if strings.TrimSpace(call.Name) == "" {
			continue
		}
		if strings.TrimSpace(call.ID) == "" {
			call.ID = fmt.Sprintf("call_%d", key)
		}
		if strings.TrimSpace(call.Arguments) == "" {
			call.Arguments = "{}"
		}
		out = append(out, *call)
	}
	return out
}

// decodeArguments parses a tool call's argument object, tolerating an empty
// payload and rejecting a body that is not a JSON object.
func decodeArguments(raw string) (map[string]any, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return map[string]any{}, nil
	}
	if strings.HasPrefix(trimmed, "```") {
		trimmed = strings.TrimPrefix(trimmed, "```")
		if idx := strings.Index(trimmed, "\n"); idx != -1 {
			trimmed = trimmed[idx+1:]
		}
		trimmed = strings.TrimSuffix(trimmed, "```")
		trimmed = strings.TrimSpace(trimmed)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(trimmed), &decoded); err == nil {
		if decoded == nil {
			decoded = map[string]any{}
		}
		return decoded, nil
	}
	if strings.HasPrefix(trimmed, "\"") && strings.HasSuffix(trimmed, "\"") {
		var unquoted string
		if err := json.Unmarshal([]byte(trimmed), &unquoted); err == nil {
			var inner map[string]any
			if err := json.Unmarshal([]byte(strings.TrimSpace(unquoted)), &inner); err == nil {
				if inner == nil {
					inner = map[string]any{}
				}
				return inner, nil
			}
		}
	}
	first := strings.Index(trimmed, "{")
	last := strings.LastIndex(trimmed, "}")
	if first != -1 && last > first {
		slice := trimmed[first : last+1]
		var sliceDecoded map[string]any
		if err := json.Unmarshal([]byte(slice), &sliceDecoded); err == nil {
			if sliceDecoded == nil {
				sliceDecoded = map[string]any{}
			}
			return sliceDecoded, nil
		}
		repaired := reTrailingCommas.ReplaceAllString(slice, "$1")
		repaired = rePythonTrue.ReplaceAllString(repaired, ": true")
		repaired = rePythonFalse.ReplaceAllString(repaired, ": false")
		repaired = rePythonNone.ReplaceAllString(repaired, ": null")
		if err := json.Unmarshal([]byte(repaired), &sliceDecoded); err == nil {
			if sliceDecoded == nil {
				sliceDecoded = map[string]any{}
			}
			return sliceDecoded, nil
		}
		singleQuoted := reSingleQuoted.ReplaceAllString(repaired, `"$1"`)
		if err := json.Unmarshal([]byte(singleQuoted), &sliceDecoded); err == nil {
			if sliceDecoded == nil {
				sliceDecoded = map[string]any{}
			}
			return sliceDecoded, nil
		}
	}
	return nil, fmt.Errorf("arguments are not a JSON object")
}
