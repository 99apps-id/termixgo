package telegram

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/99apps-id/termixgo/internal/channel"
)

// StreamEvent carries live turn updates from an agent to Telegram.
type StreamEvent struct {
	Kind     string // "thinking", "text", "tool-start", "tool-end", "notice", "error", "turn-start"
	Text     string
	ToolName string
	ToolOK   bool
}

// StreamingAgent is an optional interface that pushes token-level text deltas
// and structured tool events so Telegram can stream responses live like
// OpenClaw and Hermes.
type StreamingAgent interface {
	RunPromptStream(ctx context.Context, request channel.Request, prompt string, onEvent func(StreamEvent)) (string, error)
}

// ImageStreamingAgent is the vision counterpart to StreamingAgent.
type ImageStreamingAgent interface {
	RunPromptWithImageStream(ctx context.Context, request channel.Request, prompt, mediaType, data string, onEvent func(StreamEvent)) (string, error)
}

// VoiceStreamingAgent is the voice-fallback counterpart to StreamingAgent.
type VoiceStreamingAgent interface {
	RunVoicePromptStream(ctx context.Context, request channel.Request, prompt string, onEvent func(StreamEvent)) (string, error)
}

// streamEditInterval bounds how often Telegram messages are edited during live
// streaming. It is a variable so tests can drive streaming without waiting.
var streamEditInterval = 900 * time.Millisecond

const streamCursor = "▍"

// formatStreamDraft renders partial Markdown with a trailing live cursor
// placed outside any unclosed fenced code block so Telegram renders the code
// block cleanly while streaming.
func formatStreamDraft(raw string) string {
	if _, open := openedFence(raw); open {
		return raw + markdownFenceClose + "\n" + streamCursor
	}
	if strings.HasSuffix(raw, "\n") {
		return raw + streamCursor
	}
	return raw + " " + streamCursor
}

func needsStreamSplit(raw string) bool {
	if len(raw) > telegramChunkLimit-16 {
		return true
	}
	draft := formatStreamDraft(raw)
	return len(markdownToTelegramHTML(draft)) > telegramChunkLimit
}

// runWithStream runs one turn with OpenClaw/Hermes-style live streaming:
// thinking/tool status updates first, followed by live token-level text
// streaming into the active message with trailing-edge throttling, 429
// Retry-After backoff, and seamless multi-message overflow.
func (b *Bot) runWithStream(ctx context.Context, chatID int64, run func(context.Context, func(StreamEvent)) (string, error)) {
	if !b.tryStartRunChat(chatID) {
		b.reply(ctx, chatID, "A run is already in progress in this chat. Send /stop to stop it, or send a new instruction to steer it.")
		return
	}
	defer b.endRunChat(chatID)

	statusMessage, err := b.client.SendMessage(ctx, chatID, "Working...", nil)
	if err != nil {
		b.logf("could not send the progress message: %v", err)
	}
	_ = b.client.SendChatAction(ctx, chatID, "typing")
	typingDone := make(chan struct{})
	go b.keepTyping(ctx, chatID, typingDone)
	defer close(typingDone)

	var (
		mu              sync.Mutex
		activeMsgID     = statusMessage.MessageID
		toolLines       []string
		thinkingActive  bool
		rawSegment      strings.Builder
		tailBuf         strings.Builder
		resetSeq        uint64
		committedChunks int
		preambleSpilled bool
		lastRendered    string
		lastEdit        time.Time
		retryUntil      time.Time
	)

	signalCh := make(chan struct{}, 1)
	stopCh := make(chan struct{})
	flusherDone := make(chan struct{})

	notifyFlusher := func() {
		select {
		case signalCh <- struct{}{}:
		default:
		}
	}

	editCtxFor := func() (context.Context, context.CancelFunc) {
		if ctx != nil && ctx.Err() == nil {
			return ctx, func() {}
		}
		return context.WithTimeout(context.Background(), 15*time.Second)
	}

	flushStep := func() {
		mu.Lock()
		msgID := activeMsgID
		if msgID == 0 {
			mu.Unlock()
			return
		}
		now := time.Now()
		if now.Before(retryUntil) {
			mu.Unlock()
			return
		}

		// Snapshot either live streamed text or tool/thinking status.
		if tailBuf.Len() > 0 {
			snapSeq := resetSeq
			snapRawTail := tailBuf.String()
			snapLen := len(snapRawTail)
			redactedTail := b.redact(snapRawTail)

			if needsStreamSplit(redactedTail) {
				part, rest := nextChunk(redactedTail, telegramChunkLimit)
				if rest != "" {
					mu.Unlock()
					editCtx, cancel := editCtxFor()
					editErr := b.client.EditMarkdown(editCtx, chatID, msgID, part, nil)
					var nextMsg Message
					var sendErr error
					if editErr == nil {
						nextMsg, sendErr = b.client.SendMarkdown(editCtx, chatID, formatStreamDraft(rest), nil)
					}
					cancel()

					mu.Lock()
					if editErr != nil {
						if retry := RetryAfterSeconds(editErr); retry > 0 {
							retryUntil = time.Now().Add(time.Duration(retry) * time.Second)
						}
						b.logf("could not finalize stream chunk: %v", editErr)
						mu.Unlock()
						return
					}
					lastEdit = time.Now()
					committedChunks++
					if resetSeq == snapSeq {
						appended := ""
						if tailBuf.Len() >= snapLen {
							appended = tailBuf.String()[snapLen:]
						}
						tailBuf.Reset()
						tailBuf.WriteString(rest)
						tailBuf.WriteString(appended)
					}
					if sendErr != nil {
						if retry := RetryAfterSeconds(sendErr); retry > 0 {
							retryUntil = time.Now().Add(time.Duration(retry) * time.Second)
						}
						b.logf("could not open continuation message: %v", sendErr)
						activeMsgID = 0
					} else if nextMsg.MessageID != 0 {
						activeMsgID = nextMsg.MessageID
						lastRendered = formatStreamDraft(rest)
					}
					mu.Unlock()
					return
				}
			}

			draft := formatStreamDraft(redactedTail)
			if draft == lastRendered {
				mu.Unlock()
				return
			}
			mu.Unlock()

			editCtx, cancel := editCtxFor()
			editErr := b.client.EditMarkdown(editCtx, chatID, msgID, draft, nil)
			cancel()

			mu.Lock()
			if editErr != nil {
				if retry := RetryAfterSeconds(editErr); retry > 0 {
					retryUntil = time.Now().Add(time.Duration(retry) * time.Second)
				}
				b.logf("could not edit stream message: %v", editErr)
			} else {
				lastEdit = time.Now()
				lastRendered = draft
			}
			mu.Unlock()
			return
		}

		// No answer text yet: render compact tool activity or thinking state.
		var statusBody string
		if len(toolLines) > 0 {
			statusBody = strings.Join(toolLines, "\n")
		} else if thinkingActive {
			statusBody = "Thinking..."
		} else {
			mu.Unlock()
			return
		}
		if statusBody == lastRendered {
			mu.Unlock()
			return
		}
		mu.Unlock()

		editCtx, cancel := editCtxFor()
		editErr := b.client.EditMarkdown(editCtx, chatID, msgID, statusBody, nil)
		cancel()

		mu.Lock()
		if editErr != nil {
			if retry := RetryAfterSeconds(editErr); retry > 0 {
				retryUntil = time.Now().Add(time.Duration(retry) * time.Second)
			}
			b.logf("could not edit status message: %v", editErr)
		} else {
			lastEdit = time.Now()
			lastRendered = statusBody
		}
		mu.Unlock()
	}

	go func() {
		defer close(flusherDone)
		for {
			select {
			case <-stopCh:
				return
			case <-signalCh:
			}

			// Wait until both the edit throttle window and any 429 retry window pass.
			for {
				mu.Lock()
				nextAt := lastEdit.Add(streamEditInterval)
				if retryUntil.After(nextAt) {
					nextAt = retryUntil
				}
				mu.Unlock()
				wait := time.Until(nextAt)
				if wait <= 0 {
					break
				}
				timer := time.NewTimer(wait)
				select {
				case <-stopCh:
					timer.Stop()
					return
				case <-timer.C:
				}
			}

			flushStep()
		}
	}()

	onEvent := func(ev StreamEvent) {
		mu.Lock()
		switch ev.Kind {
		case "turn-start":
			thinkingActive = false
		case "thinking":
			if tailBuf.Len() == 0 && len(toolLines) == 0 && !thinkingActive {
				thinkingActive = true
				notifyFlusher()
			}
		case "text":
			if ev.Text == "" {
				mu.Unlock()
				return
			}
			rawSegment.WriteString(ev.Text)
			tailBuf.WriteString(ev.Text)
			notifyFlusher()
		case "tool-start":
			label := strings.TrimSpace(ev.Text)
			if label == "" && ev.ToolName != "" {
				label = "Running " + ev.ToolName + "..."
			}
			if label != "" {
				label = b.redact(label)
				// Clear pre-tool text buffer so the final card won't jump back when
				// a subsequent assistant step produces the real final answer.
				if tailBuf.Len() > 0 {
					if committedChunks > 0 {
						preambleSpilled = true
					}
					rawSegment.Reset()
					tailBuf.Reset()
					resetSeq++
				}
				line := "⏳ " + label
				if len(toolLines) == 0 || toolLines[len(toolLines)-1] != line {
					toolLines = append(toolLines, line)
					toolLines = capCardLines(toolLines, telegramChunkLimit)
				}
				notifyFlusher()
			}
		case "tool-end":
			label := strings.TrimSpace(ev.Text)
			if label == "" && ev.ToolName != "" {
				label = "Ran " + ev.ToolName
			}
			if label != "" {
				label = b.redact(label)
				prefix := "✓ "
				if !ev.ToolOK {
					prefix = "✗ "
					label += " (failed)"
				}
				line := prefix + label
				// Replace matching in-progress line if it's the most recent entry.
				if len(toolLines) > 0 && strings.HasPrefix(toolLines[len(toolLines)-1], "⏳ ") {
					toolLines[len(toolLines)-1] = line
				} else {
					toolLines = append(toolLines, line)
				}
				toolLines = capCardLines(toolLines, telegramChunkLimit)
				notifyFlusher()
			}
		case "notice":
			if line := strings.TrimSpace(ev.Text); line != "" {
				line = b.redact(line)
				toolLines = append(toolLines, line)
				toolLines = capCardLines(toolLines, telegramChunkLimit)
				notifyFlusher()
			}
		case "error":
			if line := strings.TrimSpace(ev.Text); line != "" {
				line = b.redact(line)
				toolLines = append(toolLines, line)
				toolLines = capCardLines(toolLines, telegramChunkLimit)
				notifyFlusher()
			}
		}
		mu.Unlock()
	}

	answer, runErr := run(ctx, onEvent)
	close(stopCh)
	<-flusherDone

	mu.Lock()
	msgID := activeMsgID
	commits := committedChunks
	spilled := preambleSpilled
	rawSegStr := rawSegment.String()
	tailStr := tailBuf.String()
	mu.Unlock()

	finalCtx, cancelFinal := editCtxFor()
	defer cancelFinal()

	if runErr != nil {
		failure := b.redact(formatRunFailure(runErr))
		if msgID == 0 {
			b.replyMarkdown(finalCtx, chatID, failure)
			return
		}
		if err := b.client.EditMarkdown(finalCtx, chatID, msgID, failure, nil); err != nil {
			b.replyMarkdown(finalCtx, chatID, failure)
		}
		return
	}

	if strings.TrimSpace(answer) != "" && channel.Silent(answer) {
		if msgID != 0 && commits == 0 {
			if err := b.client.DeleteMessage(finalCtx, chatID, msgID); err != nil {
				b.logf("could not delete the silent progress card: %v", err)
			}
		}
		return
	}
	if strings.TrimSpace(answer) == "" {
		answer = "(no output)"
	}

	// Choose what remains to deliver based on whether earlier overflow chunks
	// were already committed to chat messages during streaming.
	finalBody := answer
	if commits > 0 && !spilled {
		trimmedRaw := strings.TrimSpace(rawSegStr)
		redactedTrimmed := b.redact(trimmedRaw)
		if answer == redactedTrimmed || answer == trimmedRaw {
			finalBody = strings.TrimSpace(tailStr)
		} else if strings.HasPrefix(answer, redactedTrimmed) {
			finalBody = strings.TrimSpace(tailStr) + answer[len(redactedTrimmed):]
		} else if strings.HasPrefix(answer, trimmedRaw) {
			finalBody = strings.TrimSpace(tailStr) + answer[len(trimmedRaw):]
		} else {
			// Answer was rewritten (e.g. by a retry); send as fresh message(s)
			// rather than overwriting the last overflow chunk's continuation.
			if msgID != 0 {
				_ = b.client.EditMarkdown(finalCtx, chatID, msgID, strings.TrimSpace(tailStr), nil)
			}
			b.replyMarkdown(finalCtx, chatID, answer)
			return
		}
	}

	finalBody = b.redact(finalBody)
	if strings.TrimSpace(finalBody) == "" {
		return
	}
	if msgID == 0 {
		b.replyMarkdown(finalCtx, chatID, finalBody)
		return
	}

	first, rest := nextChunk(finalBody, telegramChunkLimit)
	if err := b.client.EditMarkdown(finalCtx, chatID, msgID, first, nil); err != nil {
		b.replyMarkdown(finalCtx, chatID, finalBody)
		return
	}
	if rest != "" {
		b.replyMarkdown(finalCtx, chatID, rest)
	}
}
