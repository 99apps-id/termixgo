package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/99apps-id/termixgo/internal/provider"
)

// This file holds the internal, non-tool tasks: naming a session and
// condensing an old part of the conversation before it is trimmed.
//
// Both run as one plain completion rather than through the loop. They are
// mechanical jobs, so they can run on a cheaper model than the conversation
// itself, which is the point of the separate model settings: summarising a long
// history and naming it should not cost frontier-model rates.

// Task kinds. They name the two internal jobs that can run on their own model.
const (
	// TaskCompaction condenses an older part of the conversation before it is
	// trimmed, so the brief stands in for turns that are being dropped.
	TaskCompaction = "compaction"
	// TaskTitle names a session from its opening turns.
	TaskTitle = "title"
)

// taskMaxTokens bounds the answer of a task call. Both jobs answer with a short
// piece of prose, and a runaway task call is charged to the session like any
// other request.
const taskMaxTokens = 1024

// transcriptByteLimit bounds how much of the history one task call reads. A
// summary is a compression step, so handing it the whole conversation defeats
// the purpose: the request would be as large as the history it is meant to
// shrink.
const transcriptByteLimit = 48000

// taskCall runs one non-streaming completion and returns its text.
func taskCall(ctx context.Context, client provider.Client, model, effort, system, prompt string) (string, error) {
	if client == nil {
		return "", errors.New("no client is available for the internal task")
	}
	var builder strings.Builder
	request := provider.ChatRequest{
		Model:     model,
		System:    system,
		Messages:  []provider.Message{{Role: provider.RoleUser, Content: prompt}},
		MaxTokens: taskMaxTokens,
		Effort:    effort,
	}
	if err := client.Stream(ctx, request, func(event provider.StreamEvent) error {
		if event.Type == provider.EventTextDelta {
			builder.WriteString(event.Text)
		}
		return nil
	}); err != nil {
		return "", err
	}
	return strings.TrimSpace(builder.String()), nil
}

// RenderTranscript flattens messages into a readable transcript for an internal
// task. Tool arguments and reasoning are left out: the brief is about what was
// asked and decided, and the raw payloads are what made the history too large
// to keep in the first place.
func RenderTranscript(messages []provider.Message) string {
	var builder strings.Builder
	for _, message := range messages {
		text := strings.TrimSpace(message.Content)
		if text == "" && len(message.ToolCalls) == 0 {
			continue
		}
		switch message.Role {
		case provider.RoleUser:
			fmt.Fprintf(&builder, "USER: %s\n", text)
		case provider.RoleAssistant:
			if text != "" {
				fmt.Fprintf(&builder, "ASSISTANT: %s\n", text)
			}
			for _, call := range message.ToolCalls {
				fmt.Fprintf(&builder, "ASSISTANT called %s\n", call.Name)
			}
		case provider.RoleTool:
			if text != "" {
				fmt.Fprintf(&builder, "TOOL %s: %s\n", message.Name, Shorten(text, 400))
			}
		}
		if builder.Len() > transcriptByteLimit {
			break
		}
	}
	return clipTailBytes(builder.String(), transcriptByteLimit)
}

// SummarySystemPrompt fixes what the brief is and, more importantly, what it is
// not: dropped detail must be named rather than invented, because the model
// reads this brief instead of the turns it replaced.
const SummarySystemPrompt = "You condense the earlier part of a coding session into a brief for the assistant that continues it. Keep every decision made, every file touched with its path, every command that mattered and its result, and anything still unfinished. Name what you are unsure about instead of guessing. Write plain prose or short bullets, no headings, no preamble. Never invent a fact that is not in the transcript."

// SummarizeHistory condenses an older part of the conversation into a brief.
func SummarizeHistory(ctx context.Context, client provider.Client, model, effort, transcript string) (string, error) {
	prompt := "Condense this earlier part of the session. The turns themselves are being dropped, so the brief is the only thing the assistant will have from them.\n\n" + transcript
	return taskCall(ctx, client, model, effort, SummarySystemPrompt, prompt)
}

// TitleSystemPrompt keeps a session name short and literal. The name is read in
// a list of sessions, so a sentence is less useful than a phrase.
const TitleSystemPrompt = "You name coding sessions. Answer with a title of at most six words that says what the session is about. Use the words the user used. No quotes, no trailing punctuation, no preamble."

// SuggestTitle names a session from its opening. It returns an empty string
// rather than a placeholder when the model answers nothing usable, so a caller
// never replaces a derived title with a blank one.
func SuggestTitle(ctx context.Context, client provider.Client, model, effort, transcript string) (string, error) {
	title, err := taskCall(ctx, client, model, effort, TitleSystemPrompt, "Name this session:\n\n"+transcript)
	if err != nil {
		return "", err
	}
	return cleanTitle(title), nil
}

// cleanTitle turns a model answer into something safe to store and show: one
// line, no surrounding quotes, and within the cap the session already enforces.
func cleanTitle(raw string) string {
	title := strings.TrimSpace(raw)
	if index := strings.IndexAny(title, "\r\n"); index >= 0 {
		title = strings.TrimSpace(title[:index])
	}
	title = strings.Trim(title, "\"'`")
	title = strings.TrimSpace(title)
	if title == "" {
		return ""
	}
	return clipBytes(title, 60)
}
