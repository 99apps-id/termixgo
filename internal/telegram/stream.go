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

// streamDraftReserve is the room a streamed chunk keeps for the live cursor and
// a closing fence, in raw and in converted HTML bytes.
const streamDraftReserve = 64

// Card rows for a tool in flight and a finished one. Plain symbols, no emoji.
const (
	toolRunningMark = "… "
	toolDoneMark    = "✓ "
	toolFailedMark  = "✗ "
)

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

func needsStreamSplit(raw string, limit int) bool {
	draft := formatStreamDraft(raw)
	return len(draft) > limit || len(markdownToTelegramHTML(draft)) > limit
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
		activity        activityCard
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

		status := ""
		if committedChunks == 0 {
			status = activity.status(thinkingActive)
		}
		limit := activityAnswerLimit(status)
		if tailBuf.Len() > 0 {
			snapSeq := resetSeq
			snapRawTail := tailBuf.String()
			snapLen := len(snapRawTail)
			redactedTail := b.redact(snapRawTail)

			if needsStreamSplit(redactedTail, limit) {
				// The split check measures the draft (cursor and a closing fence
				// included), so the cut leaves room for them; cutting at the full
				// limit could return no rest and edit an over-long draft.
				part, rest := nextChunk(redactedTail, limit-streamDraftReserve)
				if rest != "" {
					mu.Unlock()
					editCtx, cancel := editCtxFor()
					editErr := b.editActivity(editCtx, chatID, msgID, status, part)
					var nextMsg Message
					var sendErr error
					nextDraft := "Working..."
					if !needsStreamSplit(rest, telegramChunkLimit) {
						nextDraft = formatStreamDraft(rest)
					}
					if editErr == nil {
						nextMsg, sendErr = b.client.SendMarkdown(editCtx, chatID, nextDraft, nil)
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
						lastRendered = markdownToTelegramHTML(nextDraft)
						notifyFlusher()
					}
					mu.Unlock()
					return
				}
			}

			draft := formatStreamDraft(redactedTail)
			_, rendered := renderActivity(status, draft)
			if rendered == lastRendered {
				mu.Unlock()
				return
			}
			mu.Unlock()

			editCtx, cancel := editCtxFor()
			editErr := b.editActivity(editCtx, chatID, msgID, status, draft)
			cancel()

			mu.Lock()
			if editErr != nil {
				if retry := RetryAfterSeconds(editErr); retry > 0 {
					retryUntil = time.Now().Add(time.Duration(retry) * time.Second)
				}
				b.logf("could not edit stream message: %v", editErr)
			} else {
				lastEdit = time.Now()
				lastRendered = rendered
			}
			mu.Unlock()
			return
		}

		// No answer text yet: render compact tool activity or thinking state.
		_, rendered := renderActivity(status, "")
		if status == "" || rendered == lastRendered {
			mu.Unlock()
			return
		}
		mu.Unlock()

		editCtx, cancel := editCtxFor()
		editErr := b.editActivity(editCtx, chatID, msgID, status, "")
		cancel()

		mu.Lock()
		if editErr != nil {
			if retry := RetryAfterSeconds(editErr); retry > 0 {
				retryUntil = time.Now().Add(time.Duration(retry) * time.Second)
			}
			b.logf("could not edit status message: %v", editErr)
		} else {
			lastEdit = time.Now()
			lastRendered = rendered
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
			if tailBuf.Len() == 0 && !thinkingActive {
				thinkingActive = true
				notifyFlusher()
			}
		case "text":
			if ev.Text == "" {
				mu.Unlock()
				return
			}
			thinkingActive = false
			rawSegment.WriteString(ev.Text)
			tailBuf.WriteString(ev.Text)
			notifyFlusher()
		case "tool-start":
			thinkingActive = false
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
				activity.add(ev.ToolName, toolRunningMark+label, true, false)
				notifyFlusher()
			}
		case "tool-end":
			label := strings.TrimSpace(ev.Text)
			if label == "" && ev.ToolName != "" {
				label = "Ran " + ev.ToolName
			}
			if label != "" {
				label = b.redact(label)
				prefix := toolDoneMark
				if !ev.ToolOK {
					prefix = toolFailedMark
					label += " (failed)"
				}
				activity.add(ev.ToolName, prefix+label, false, true)
				notifyFlusher()
			}
		case "notice", "error":
			if line := strings.TrimSpace(ev.Text); line != "" {
				activity.add("", b.redact(line), false, false)
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
	status := ""
	if commits == 0 {
		status = activity.status(false)
	}
	spilled := preambleSpilled
	rawSegStr := rawSegment.String()
	tailStr := tailBuf.String()
	mu.Unlock()

	finalCtx, cancelFinal := editCtxFor()
	defer cancelFinal()

	if runErr != nil {
		failure := b.redact(formatRunFailure(runErr))
		b.finishActivity(finalCtx, chatID, msgID, status, failure)
		return
	}

	if strings.TrimSpace(answer) != "" && channel.Silent(answer) {
		if msgID != 0 {
			// Streamed chunks already in the chat stay; the live draft does not,
			// or the chat would keep a message ending in the cursor.
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
	b.finishActivity(finalCtx, chatID, msgID, status, finalBody)
}
