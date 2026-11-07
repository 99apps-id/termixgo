package provider

import (
	"encoding/json"
	"fmt"
	"strings"
)

// toolCallAccumulator rebuilds streamed tool calls. Providers send a call in
// fragments keyed by index: the name arrives once, the arguments arrive in
// pieces that only form valid JSON when concatenated.
type toolCallAccumulator struct {
	order   []int
	byIndex map[int]*ToolCall
}

func newToolCallAccumulator() *toolCallAccumulator {
	return &toolCallAccumulator{byIndex: map[int]*ToolCall{}}
}

func (a *toolCallAccumulator) add(delta openAIToolDelta) {
	call, ok := a.byIndex[delta.Index]
	if !ok {
		call = &ToolCall{}
		a.byIndex[delta.Index] = call
		a.order = append(a.order, delta.Index)
	}
	if delta.ID != "" {
		call.ID = delta.ID
	}
	if delta.Function.Name != "" {
		call.Name = delta.Function.Name
	}
	call.Arguments += delta.Function.Arguments
}

// finish returns complete calls in arrival order, with missing ids and empty
// arguments normalised so a tool executor never sees a half-built call.
func (a *toolCallAccumulator) finish() []ToolCall {
	out := make([]ToolCall, 0, len(a.order))
	for _, index := range a.order {
		call := a.byIndex[index]
		if strings.TrimSpace(call.Name) == "" {
			continue
		}
		if strings.TrimSpace(call.ID) == "" {
			call.ID = fmt.Sprintf("call_%d", index)
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
		var sliceDecoded map[string]any
		if err := json.Unmarshal([]byte(trimmed[first:last+1]), &sliceDecoded); err == nil {
			if sliceDecoded == nil {
				sliceDecoded = map[string]any{}
			}
			return sliceDecoded, nil
		}
	}
	return nil, fmt.Errorf("arguments are not a JSON object")
}
