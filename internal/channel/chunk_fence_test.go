package channel

import (
	"strings"
	"testing"
)

// A cut inside a fenced block closes the fence in its chunk and reopens it,
// with the language, in the next, and no chunk passes the limit.
func TestChunkBalancesCodeFences(t *testing.T) {
	var code []string
	for i := 0; i < 80; i++ {
		code = append(code, "fmt.Println(\"line number padding\")")
	}
	text := "intro\n\n```go\n" + strings.Join(code, "\n") + "\n```\n\noutro"
	const limit = 600
	chunks := Chunk(text, limit)
	if len(chunks) < 2 {
		t.Fatalf("expected several chunks, got %d", len(chunks))
	}
	for index, chunk := range chunks {
		if len(chunk) > limit {
			t.Errorf("chunk %d is %d bytes, over %d", index, len(chunk), limit)
		}
		if strings.Count(chunk, "```")%2 != 0 {
			t.Errorf("chunk %d has an unbalanced fence:\n%s", index, chunk)
		}
		if index > 0 && strings.Contains(chunk, "fmt.Println") && !strings.HasPrefix(chunk, "```go") && !strings.HasPrefix(chunk, "outro") {
			t.Errorf("chunk %d should reopen the go fence:\n%s", index, chunk)
		}
	}
}

// Text without a fence chunks exactly as before.
func TestChunkWithoutFencesIsUnchanged(t *testing.T) {
	text := strings.Repeat("word ", 300)
	if got, want := Chunk(text, 200), chunkPlain(strings.TrimSpace(text), 200); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("plain text chunking changed")
	}
}
