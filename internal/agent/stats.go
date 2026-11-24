package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/99apps-id/termixgo/internal/provider"
)

// StatsOptions narrows which saved sessions a report covers.
type StatsOptions struct {
	// Dir is the sessions directory; empty means the shared one.
	Dir string
	// Days limits the report to sessions updated within the last N days. Zero
	// means every session.
	Days int
	// Project keeps the sessions whose workspace path contains this text,
	// compared without regard to case or slash direction. Empty keeps all.
	Project string
	// Now is the clock the day window is measured from. Zero means time.Now.
	Now time.Time
}

// ToolUse is how often one tool was called across the sessions.
type ToolUse struct {
	Name  string `json:"name"`
	Calls int    `json:"calls"`
}

// ModelUse is the spend of one provider and model, summed from the sessions'
// usage segments.
type ModelUse struct {
	Provider string         `json:"provider,omitempty"`
	Model    string         `json:"model"`
	Records  int            `json:"records"`
	Usage    provider.Usage `json:"usage"`
	CostUSD  float64        `json:"costUsd"`
	// Unpriced counts the records that carried no dollar figure, so CostUSD is
	// a floor when it is nonzero.
	Unpriced int `json:"unpriced,omitempty"`
}

// StatsReport is the roll-up `termixgo stats` prints.
type StatsReport struct {
	Sessions int `json:"sessions"`
	Messages int `json:"messages"`
	// Days is the span from the first session's start to the last one's update,
	// counted in calendar days, at least one when there is any session.
	Days    int     `json:"days"`
	CostUSD float64 `json:"costUsd"`
	// UnpricedRecords counts usage records with no dollar figure; the total
	// cost is then a floor.
	UnpricedRecords int `json:"unpricedRecords,omitempty"`
	// Tokens are the sums over the sessions. Input is the fresh prompt: the
	// prompt count minus what the cache served or stored.
	InputTokens      int64 `json:"inputTokens"`
	OutputTokens     int64 `json:"outputTokens"`
	ReasoningTokens  int64 `json:"reasoningTokens"`
	CacheReadTokens  int64 `json:"cacheReadTokens"`
	CacheWriteTokens int64 `json:"cacheWriteTokens"`
	// AvgTokensPerSession and MedianTokensPerSession count every token a session
	// handled: fresh input, output, cache reads and cache writes. They are taken
	// over the sessions that recorded any tokens, because an empty session says
	// nothing about how much a working one costs.
	AvgTokensPerSession    int64      `json:"avgTokensPerSession"`
	MedianTokensPerSession int64      `json:"medianTokensPerSession"`
	Tools                  []ToolUse  `json:"tools"`
	Models                 []ModelUse `json:"models"`
}

// statsSession is the part of a saved session a report reads. Decoding only
// these fields keeps a long transcript from costing memory it is not used for.
type statsSession struct {
	Workspace string    `json:"workspace"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	Messages  []struct {
		Role      string
		ToolCalls []struct{ Name string }
	} `json:"messages"`
	Usage    provider.Usage `json:"usage"`
	CostUSD  float64        `json:"costUsd"`
	Segments []UsageSegment `json:"segments"`
}

// SummarizeSessions reads every saved session in a directory and rolls them up.
// A file that cannot be read or parsed is skipped: one damaged session must not
// hide the rest of the history.
func SummarizeSessions(options StatsOptions) (StatsReport, error) {
	dir, err := sessionsDir(options.Dir)
	if err != nil {
		return StatsReport{}, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return StatsReport{}, nil
		}
		return StatsReport{}, err
	}
	now := options.Now
	if now.IsZero() {
		now = time.Now()
	}
	var cutoff time.Time
	if options.Days > 0 {
		cutoff = now.AddDate(0, 0, -options.Days)
	}
	project := normalizeStatsPath(options.Project)

	var report StatsReport
	var first, last time.Time
	var perSession []int64
	tools := map[string]int{}
	models := map[string]*ModelUse{}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		var session statsSession
		if json.Unmarshal(data, &session) != nil {
			continue
		}
		if !cutoff.IsZero() && session.UpdatedAt.Before(cutoff) {
			continue
		}
		if project != "" && !strings.Contains(normalizeStatsPath(session.Workspace), project) {
			continue
		}
		report.Sessions++
		report.Messages += len(session.Messages)
		report.CostUSD += session.CostUSD
		if first.IsZero() || (!session.CreatedAt.IsZero() && session.CreatedAt.Before(first)) {
			first = session.CreatedAt
		}
		if session.UpdatedAt.After(last) {
			last = session.UpdatedAt
		}
		for _, message := range session.Messages {
			for _, call := range message.ToolCalls {
				if name := strings.TrimSpace(call.Name); name != "" {
					tools[name]++
				}
			}
		}

		usage := session.Usage
		// A session written before segments existed has only the display total.
		if len(session.Segments) == 0 && (usage.TotalTokens > 0 || usage.PromptTokens > 0) {
			session.Segments = []UsageSegment{{Model: "(older sessions, model not recorded)", Usage: usage, CostUSD: session.CostUSD, Records: 1}}
		}
		fresh := usage.PromptTokens - usage.CacheReadTokens - usage.CacheWriteTokens
		if fresh < 0 {
			fresh = 0
		}
		report.InputTokens += int64(fresh)
		report.OutputTokens += int64(usage.CompletionTokens)
		report.ReasoningTokens += int64(usage.ReasoningTokens)
		report.CacheReadTokens += int64(usage.CacheReadTokens)
		report.CacheWriteTokens += int64(usage.CacheWriteTokens)
		if handled := int64(fresh + usage.CompletionTokens + usage.CacheReadTokens + usage.CacheWriteTokens); handled > 0 {
			perSession = append(perSession, handled)
		}

		for _, segment := range session.Segments {
			key := segment.Provider + "\x00" + segment.Model
			item := models[key]
			if item == nil {
				item = &ModelUse{Provider: segment.Provider, Model: segment.Model}
				models[key] = item
			}
			item.Records += segment.Records
			item.Usage = item.Usage.Add(segment.Usage)
			item.CostUSD += segment.CostUSD
			item.Unpriced += segment.Unpriced
			report.UnpricedRecords += segment.Unpriced
		}
	}

	if report.Sessions == 0 {
		return report, nil
	}
	if !first.IsZero() && !last.IsZero() && last.After(first) {
		report.Days = int(last.Sub(first).Hours()/24) + 1
	}
	if report.Days < 1 {
		report.Days = 1
	}
	var total int64
	for _, tokens := range perSession {
		total += tokens
	}
	if len(perSession) > 0 {
		report.AvgTokensPerSession = total / int64(len(perSession))
		sort.Slice(perSession, func(i, j int) bool { return perSession[i] < perSession[j] })
		middle := len(perSession) / 2
		if len(perSession)%2 == 1 {
			report.MedianTokensPerSession = perSession[middle]
		} else {
			report.MedianTokensPerSession = (perSession[middle-1] + perSession[middle]) / 2
		}
	}
	for name, calls := range tools {
		report.Tools = append(report.Tools, ToolUse{Name: name, Calls: calls})
	}
	sort.Slice(report.Tools, func(i, j int) bool {
		if report.Tools[i].Calls != report.Tools[j].Calls {
			return report.Tools[i].Calls > report.Tools[j].Calls
		}
		return report.Tools[i].Name < report.Tools[j].Name
	})
	for _, item := range models {
		report.Models = append(report.Models, *item)
	}
	sort.Slice(report.Models, func(i, j int) bool {
		if report.Models[i].CostUSD != report.Models[j].CostUSD {
			return report.Models[i].CostUSD > report.Models[j].CostUSD
		}
		if report.Models[i].Usage.TotalTokens != report.Models[j].Usage.TotalTokens {
			return report.Models[i].Usage.TotalTokens > report.Models[j].Usage.TotalTokens
		}
		return report.Models[i].Model < report.Models[j].Model
	})
	return report, nil
}

// normalizeStatsPath makes a path comparable across case and slash direction.
func normalizeStatsPath(path string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(path), "\\", "/"))
}
