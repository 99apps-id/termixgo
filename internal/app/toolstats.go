package app

import (
	"sort"
	"strings"
)

// ToolStat counts finished calls and failures for one tool across the live
// session. It answers "where did the turn go" in /cost without attributing
// provider money no provider reports.
type ToolStat struct {
	Name   string
	Calls  int
	Errors int
}

// recordToolResult folds one finished tool call into the live ledger. It runs
// on the emit path, so it must stay cheap and never block.
func (a *App) recordToolResult(name string, ok bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.toolCalls == nil {
		a.toolCalls = map[string]*ToolStat{}
	}
	stat, exists := a.toolCalls[name]
	if !exists {
		stat = &ToolStat{Name: name}
		a.toolCalls[name] = stat
	}
	stat.Calls++
	if !ok {
		stat.Errors++
	}
}

// ToolStats returns the live ledger ordered by calls, then name. The slice
// and every element are copies so callers cannot observe a partially updated
// value while a run is appending to the backing map.
func (a *App) ToolStats() []ToolStat {
	a.mu.Lock()
	defer a.mu.Unlock()
	stats := make([]ToolStat, 0, len(a.toolCalls))
	for _, stat := range a.toolCalls {
		item := *stat
		stats = append(stats, item)
	}
	sort.Slice(stats, func(i, j int) bool {
		if stats[i].Calls != stats[j].Calls {
			return stats[i].Calls > stats[j].Calls
		}
		return stats[i].Name < stats[j].Name
	})
	return stats
}
