package telegram

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	activityRows     = 5
	activityRowLimit = 120
)

type activityRow struct {
	tool    string
	text    string
	running bool
}

type activityCard struct {
	rows    []activityRow
	omitted int
}

func (c *activityCard) add(tool, line string, running, completed bool) {
	line = strings.Join(strings.Fields(line), " ")
	if len(line) > activityRowLimit {
		end := activityRowLimit - len("...")
		for !utf8.RuneStart(line[end]) {
			end--
		}
		line = line[:end] + "..."
	}
	row := activityRow{tool: tool, text: line, running: running}
	if completed {
		for i := len(c.rows) - 1; i >= 0; i-- {
			if c.rows[i].running && c.rows[i].tool == tool {
				c.rows[i] = row
				return
			}
		}
	}
	if len(c.rows) > 0 && c.rows[len(c.rows)-1] == row {
		return
	}
	if len(c.rows) == activityRows {
		copy(c.rows, c.rows[1:])
		c.rows = c.rows[:activityRows-1]
		c.omitted++
	}
	c.rows = append(c.rows, row)
}

func (c *activityCard) status(thinking bool) string {
	lines := make([]string, 0, len(c.rows)+2)
	if c.omitted > 0 {
		lines = append(lines, fmt.Sprintf("... (%d earlier lines omitted)", c.omitted))
	}
	for _, row := range c.rows {
		lines = append(lines, row.text)
	}
	if thinking {
		lines = append(lines, "Thinking...")
	}
	return strings.Join(lines, "\n")
}

func activityAnswerLimit(status string) int {
	if status == "" {
		return telegramChunkLimit
	}
	return telegramChunkLimit - len(escapeHTML(status)) - len("\n\n")
}

func renderActivity(status, answer string) (plain, rendered string) {
	if status == "" {
		return answer, markdownToTelegramHTML(answer)
	}
	if answer == "" {
		return status, escapeHTML(status)
	}
	return status + "\n\n" + answer, escapeHTML(status) + "\n\n" + markdownToTelegramHTML(answer)
}

func (b *Bot) editActivity(ctx context.Context, chatID, messageID int64, status, answer string) error {
	plain, rendered := renderActivity(status, answer)
	err := b.client.editMessage(ctx, chatID, messageID, rendered, nil, "HTML")
	if isParseError(err) {
		return b.client.EditMessageText(ctx, chatID, messageID, plain)
	}
	return err
}

func (b *Bot) finishActivity(ctx context.Context, chatID, messageID int64, status, answer string) {
	if messageID == 0 {
		b.replyMarkdown(ctx, chatID, answer)
		return
	}
	first, rest := nextChunk(answer, activityAnswerLimit(status))
	if err := b.editActivity(ctx, chatID, messageID, status, first); err != nil {
		b.replyMarkdown(ctx, chatID, answer)
		return
	}
	if rest != "" {
		b.replyMarkdown(ctx, chatID, rest)
	}
}
