package ui

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/99apps-id/termixgo/internal/agent"
	"github.com/99apps-id/termixgo/internal/provider"
)

// Attachment limits. The image cap mirrors the provider-facing cap in
// read_image, and the text budget keeps a pile of documents from crowding out
// the conversation they were attached to.
const (
	maxAttachments          = 8
	maxAttachmentImageBytes = 20 * 1024 * 1024
	maxProviderImageBytes   = 8 * 1024 * 1024
	attachmentTextBudget    = 400 * 1024
	// pasteSpoolThreshold is where a paste stops travelling inline. Above it
	// the text is saved under .termixgo/pastes and the turn receives the head
	// plus the path, so a hundred-page log does not become the prompt.
	pasteSpoolThreshold  = 100 * 1024
	pasteSpoolHead       = 8 * 1024
	attachmentDisplayCap = 16 * 1024
)

// attachment is one file the operator attached to the next turn. An image
// carries its bytes so it reaches a vision model the same way a photo from a
// chat channel does; a document carries the text ExtractDocument produced.
type attachment struct {
	Path      string
	Label     string
	Kind      string
	MediaType string
	Data      []byte
	Text      string
}

// splitAttachmentArgs splits a command line into paths, honouring a quoted
// path so a file name with a space stays one argument.
func splitAttachmentArgs(raw string) []string {
	var out []string
	var current strings.Builder
	quote := rune(0)
	flush := func() {
		if value := strings.TrimSpace(current.String()); value != "" {
			out = append(out, value)
		}
		current.Reset()
	}
	for _, char := range raw {
		switch {
		case quote != 0:
			if char == quote {
				quote = 0
			} else {
				current.WriteRune(char)
			}
		case char == '"' || char == '\'':
			quote = char
		case char == ' ' || char == '\t' || char == ',' || char == '\n':
			flush()
		default:
			current.WriteRune(char)
		}
	}
	flush()
	return out
}

// buildAttachment resolves one path and turns it into an attachment. An
// absolute path outside the workspace is allowed on purpose: the operator
// named the file, which is the same consent a clipboard paste carries, and
// buildAttachment is only reachable from an explicit command.
func buildAttachment(workspace, raw string) (attachment, error) {
	path := strings.TrimSpace(raw)
	if path == "" {
		return attachment{}, fmt.Errorf("empty path")
	}
	path = expandAttachmentHome(path)
	if !filepath.IsAbs(path) {
		if strings.TrimSpace(workspace) == "" {
			return attachment{}, fmt.Errorf("cannot resolve %s without a workspace", raw)
		}
		path = filepath.Join(workspace, filepath.FromSlash(path))
	}
	path = filepath.Clean(path)
	info, err := os.Stat(path)
	if err != nil {
		return attachment{}, fmt.Errorf("cannot attach %s: %v", raw, err)
	}
	if info.IsDir() {
		return attachment{}, fmt.Errorf("%s is a directory, not a file", raw)
	}

	if mediaType, ok := agent.ImageMediaType(path); ok {
		if info.Size() > maxAttachmentImageBytes {
			return attachment{}, fmt.Errorf("%s is %d bytes, over the %d byte attachment cap", raw, info.Size(), maxAttachmentImageBytes)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return attachment{}, fmt.Errorf("cannot read %s: %v", raw, err)
		}
		if shrunk, changed := agent.ShrinkImage(data); changed {
			data = shrunk
			mediaType = "image/png"
		}
		if len(data) > maxProviderImageBytes {
			return attachment{}, fmt.Errorf("%s is still %d bytes after scaling, over the %d byte provider cap", raw, len(data), maxProviderImageBytes)
		}
		return attachment{
			Path:      path,
			Label:     filepath.Base(path),
			Kind:      "image",
			MediaType: mediaType,
			Data:      data,
		}, nil
	}

	doc, err := agent.ExtractDocument(path)
	if err != nil {
		return attachment{}, err
	}
	return attachment{
		Path:  path,
		Label: filepath.Base(path),
		Kind:  doc.Kind,
		Text:  doc.Text,
	}, nil
}

// expandAttachmentHome resolves a leading ~ the way the file tools do.
func expandAttachmentHome(path string) string {
	if path != "~" && !strings.HasPrefix(path, "~/") && !strings.HasPrefix(path, `~\`) {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	if path == "~" {
		return home
	}
	rest := strings.TrimLeft(path[1:], `/\`)
	return filepath.Join(home, filepath.FromSlash(strings.ReplaceAll(rest, `\`, "/")))
}

// attachmentImages renders the image attachments as provider attachments.
func attachmentImages(items []attachment) []provider.Image {
	var images []provider.Image
	for _, item := range items {
		if item.Kind != "image" || len(item.Data) == 0 {
			continue
		}
		images = append(images, provider.Image{
			MediaType: item.MediaType,
			Data:      base64.StdEncoding.EncodeToString(item.Data),
		})
	}
	return images
}

// attachmentDocumentBlock renders the document attachments as prompt text.
func attachmentDocumentBlock(items []attachment) string {
	var out strings.Builder
	for _, item := range items {
		if item.Kind == "image" || strings.TrimSpace(item.Text) == "" {
			continue
		}
		fmt.Fprintf(&out, "\n\n--- attached document: %s (%s) ---\n%s", item.Label, item.Kind, item.Text)
	}
	return out.String()
}

// attachmentSummary is the one-line state shown above the composer.
func attachmentSummary(items []attachment) string {
	if len(items) == 0 {
		return ""
	}
	parts := make([]string, 0, len(items))
	for _, item := range items {
		label := item.Label
		if item.Kind != "" {
			label += " (" + item.Kind + ")"
		}
		parts = append(parts, label)
	}
	return "attached: " + strings.Join(parts, ", ") + " | /detach clears"
}

// consumeAttachments folds the pending attachments into one turn and clears
// them. The prompt text carries documents; images ride on the first user
// message through RunTurnWithImages.
func (m *Model) consumeAttachments(input string) (string, []provider.Image) {
	items := m.attachments
	m.attachments = nil
	if len(items) == 0 {
		return input, nil
	}
	return input + attachmentDocumentBlock(items), attachmentImages(items)
}

// spoolLongInput saves a very large paste under .termixgo/pastes and returns
// the head plus a pointer, so the turn can read the rest on demand instead of
// carrying every byte in one message.
func spoolLongInput(workspace, input string) string {
	if len(input) <= pasteSpoolThreshold || strings.TrimSpace(workspace) == "" {
		return input
	}
	dir := filepath.Join(workspace, ".termixgo", "pastes")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return input
	}
	name := fmt.Sprintf("paste-%d.txt", time.Now().UnixNano())
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(input), 0o600); err != nil {
		return input
	}
	head := clipMentionContent(input, pasteSpoolHead)
	relative := filepath.ToSlash(filepath.Join(".termixgo", "pastes", name))
	return fmt.Sprintf("%s\n\n[The full paste is %d bytes and was saved to %s. Read the rest with read_file on that path when you need it.]",
		head, len(input), relative)
}

// displayInput is what the transcript shows for one turn. A huge paste is
// clipped so the rendered block cannot distort the frame.
func displayInput(input string) string {
	if len(input) <= attachmentDisplayCap {
		return input
	}
	return clipMentionContent(input, attachmentDisplayCap)
}

// slashAttach adds one or more files to the next turn.
func (m *Model) slashAttach(args string) (tea.Model, tea.Cmd) {
	trimmed := strings.TrimSpace(args)
	if trimmed == "" || trimmed == "list" {
		text := attachmentSummary(m.attachments)
		if text == "" {
			text = "No attachment. Use /attach <path> for an image or a document, or /paste for the clipboard."
		}
		m.blocks = append(m.blocks, block{kind: blockNotice, text: text})
		m.refresh()
		return m, nil
	}
	if trimmed == "clear" || trimmed == "none" || trimmed == "off" {
		return m.slashDetach()
	}

	workspace := m.app.Workspace()
	added := 0
	var problems []string
	textBudget := 0
	for _, item := range m.attachments {
		textBudget += len(item.Text)
	}
	for _, raw := range splitAttachmentArgs(trimmed) {
		if len(m.attachments) >= maxAttachments {
			problems = append(problems, fmt.Sprintf("attachment limit is %d", maxAttachments))
			break
		}
		att, err := buildAttachment(workspace, raw)
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		if len(att.Text) > 0 {
			if textBudget+len(att.Text) > attachmentTextBudget {
				problems = append(problems, fmt.Sprintf("%s exceeds the %d byte attachment text budget", att.Label, attachmentTextBudget))
				continue
			}
			textBudget += len(att.Text)
		}
		m.attachments = append(m.attachments, att)
		added++
	}

	lines := []string{}
	if added > 0 {
		lines = append(lines, fmt.Sprintf("Attached %d file(s) to the next turn.", added))
		lines = append(lines, attachmentSummary(m.attachments))
	}
	if len(problems) > 0 {
		lines = append(lines, problems...)
	}
	m.blocks = append(m.blocks, block{kind: blockNotice, text: strings.Join(lines, "\n")})
	m.refresh()
	return m, nil
}

// slashDetach drops every pending attachment.
func (m *Model) slashDetach() (tea.Model, tea.Cmd) {
	count := len(m.attachments)
	m.attachments = nil
	text := "No attachment to drop."
	if count > 0 {
		text = fmt.Sprintf("Dropped %d attachment(s).", count)
	}
	m.blocks = append(m.blocks, block{kind: blockNotice, text: text})
	m.refresh()
	return m, nil
}

// slashPaste reads the clipboard. Text goes into the composer, where the
// operator can still edit it; an image is saved under .termixgo/pastes and
// attached, because a terminal paste never carries pixels.
func (m *Model) slashPaste(args string) (tea.Model, tea.Cmd) {
	mode := strings.ToLower(strings.TrimSpace(args))
	switch mode {
	case "", "text", "image":
	default:
		m.blocks = append(m.blocks, block{kind: blockNotice, text: "Use /paste, /paste text or /paste image."})
		m.refresh()
		return m, nil
	}

	var problems []string
	if mode == "" || mode == "text" {
		if text, err := readClipboardText(); err == nil && strings.TrimSpace(text) != "" {
			// A huge paste is attached as a file instead of entering the
			// composer: the textarea cap would cut it first, and the turn can
			// read the whole file on demand.
			if len(text) > pasteSpoolThreshold {
				if att, attachErr := attachPastedText(m.app.Workspace(), text); attachErr != nil {
					problems = append(problems, attachErr.Error())
				} else {
					m.attachments = append(m.attachments, att)
					m.blocks = append(m.blocks, block{kind: blockNotice, text: fmt.Sprintf("Pasted %d characters and attached them as a file. %s", len(text), attachmentSummary(m.attachments))})
					m.refresh()
					return m, nil
				}
			} else {
				current := m.composer.Value()
				if current != "" && !strings.HasSuffix(current, "\n") {
					current += "\n"
				}
				m.composer.SetValue(current + text)
				m.blocks = append(m.blocks, block{kind: blockNotice, text: fmt.Sprintf("Pasted %d characters from the clipboard. Press Enter to send.", len(text))})
				m.refresh()
				return m, nil
			}
		} else if err != nil {
			problems = append(problems, err.Error())
		}
	}

	if mode == "" || mode == "image" {
		if len(m.attachments) >= maxAttachments {
			problems = append(problems, fmt.Sprintf("attachment limit is %d", maxAttachments))
		} else if data, err := readClipboardImage(); err == nil && len(data) > 0 {
			path, saveErr := savePastedImage(m.app.Workspace(), data)
			if saveErr != nil {
				problems = append(problems, saveErr.Error())
			} else if att, buildErr := buildAttachment(m.app.Workspace(), path); buildErr != nil {
				problems = append(problems, buildErr.Error())
			} else {
				m.attachments = append(m.attachments, att)
				m.blocks = append(m.blocks, block{kind: blockNotice, text: "Attached the clipboard image to the next turn. " + attachmentSummary(m.attachments)})
				m.refresh()
				return m, nil
			}
		} else if err != nil {
			problems = append(problems, err.Error())
		}
	}

	if len(problems) == 0 {
		problems = append(problems, "The clipboard is empty.")
	}
	m.blocks = append(m.blocks, block{kind: blockNotice, text: strings.Join(problems, "\n")})
	m.refresh()
	return m, nil
}

// savePastedImage writes clipboard pixels into the workspace so the agent can
// also read them with read_image on a later turn.
func savePastedImage(workspace string, data []byte) (string, error) {
	path, err := pastePath(workspace, "clip", "png")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", fmt.Errorf("cannot save the pasted image: %v", err)
	}
	return path, nil
}

// attachPastedText saves a large clipboard paste as a document attachment, so
// it reaches the turn whole instead of being cut by the composer limit.
func attachPastedText(workspace, text string) (attachment, error) {
	path, err := pastePath(workspace, "paste", "txt")
	if err != nil {
		return attachment{}, err
	}
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		return attachment{}, fmt.Errorf("cannot save the pasted text: %v", err)
	}
	return buildAttachment(workspace, path)
}

// pastePath builds one path under .termixgo/pastes and creates the directory.
func pastePath(workspace, prefix, extension string) (string, error) {
	if strings.TrimSpace(workspace) == "" {
		return "", fmt.Errorf("cannot save a paste without a workspace")
	}
	dir := filepath.Join(workspace, ".termixgo", "pastes")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("cannot create the paste directory: %v", err)
	}
	return filepath.Join(dir, fmt.Sprintf("%s-%d.%s", prefix, time.Now().UnixNano(), extension)), nil
}

// attachmentHint is the composer-adjacent line naming what is pending.
func (m *Model) attachmentHint() string { return attachmentSummary(m.attachments) }
