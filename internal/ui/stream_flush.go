package ui

import "strings"

// Streaming answers in the inline layout
//
// A long answer used to stay in the live area until the turn ended, and the
// live area shows only the newest lines that fit, so the start of the answer
// could not be read while it streamed. Completed paragraphs are now printed to
// the scrollback as soon as they end; the live area keeps only the paragraph
// still being written.

// completedParagraphs returns how many leading bytes of text form whole
// paragraphs: everything up to the last blank line that is not inside a code
// fence. A fence left open keeps its whole body back, because a code block
// split across two prints would render as two broken blocks.
func completedParagraphs(text string) int {
	cut := 0
	inFence := false
	fence := ""
	offset := 0
	lines := strings.SplitAfter(text, "\n")
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		if marker := fenceMarker(trimmed); marker != "" && (!inFence || marker == fence) {
			inFence = !inFence
			fence = marker
		}
		offset += len(line)
		// A blank line ends a paragraph only once the line after it has
		// started, so a paragraph is never printed while its blank separator
		// could still turn out to be the middle of something else.
		if !inFence && trimmed == "" && strings.HasSuffix(line, "\n") && index > 0 && index+1 < len(lines) && lines[index+1] != "" {
			cut = offset
		}
	}
	return cut
}

// unprinted returns a block with the already-printed start of a streamed
// answer removed, ready to render the rest.
func unprinted(item block) block {
	if item.kind == blockAssistant && item.streamed > 0 {
		item.text = item.text[min(item.streamed, len(item.text)):]
		item.continuation = true
	}
	return item
}

// unprintedBlocks maps unprinted over a slice, copying it.
func unprintedBlocks(items []block) []block {
	out := make([]block, len(items))
	for index, item := range items {
		out[index] = unprinted(item)
	}
	return out
}

// flushStreamingAnswer prints the completed paragraphs of the answer that is
// still streaming, when it is the first block not yet printed.
func (m *Model) flushStreamingAnswer() {
	if !m.running || m.printed >= len(m.blocks) {
		return
	}
	item := &m.blocks[m.printed]
	if item.kind != blockAssistant || m.printed != len(m.blocks)-1 {
		return
	}
	rest := item.text[min(item.streamed, len(item.text)):]
	cut := completedParagraphs(rest)
	if cut <= 0 {
		return
	}
	chunk := block{kind: blockAssistant, text: rest[:cut], continuation: item.streamed > 0}
	item.streamed += cut
	rendered := transcript([]block{chunk}, m.styles, m.renderWidth(), m.showDetails)
	if strings.TrimSpace(rendered) != "" {
		m.pendingPrint = append(m.pendingPrint, rendered)
	}
}
