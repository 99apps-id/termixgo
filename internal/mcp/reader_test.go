package mcp

import (
	"bufio"
	"errors"
	"io"
	"strings"
	"testing"
)

// TestReadLineWithLimitReadsALongFrame is the regression for a real server
// response that is one line far bigger than the reader's buffer. Windows-MCP's
// tools/list is about 20 KB, so a client that stops at the 4 KB bufio boundary
// can never list a normal server's tools.
func TestReadLineWithLimitReadsALongFrame(t *testing.T) {
	payload := strings.Repeat("x", 20000)
	client := &Client{name: "fake"}
	reader := bufio.NewReaderSize(strings.NewReader(payload+"\n"), 4096)

	line, err := client.readLineWithLimit(reader)
	if err != nil {
		t.Fatalf("readLineWithLimit: %v", err)
	}
	if got := strings.TrimRight(string(line), "\n"); got != payload {
		t.Fatalf("line = %d bytes, want %d", len(got), len(payload))
	}
}

// TestReadLineWithLimitReturnsPartialOnEOF keeps the last fragment when the
// server closes stdout without a trailing newline.
func TestReadLineWithLimitReturnsPartialOnEOF(t *testing.T) {
	client := &Client{name: "fake"}
	reader := bufio.NewReaderSize(strings.NewReader("hello"), 4096)

	line, err := client.readLineWithLimit(reader)
	if !errors.Is(err, io.EOF) {
		t.Fatalf("err = %v, want EOF", err)
	}
	if string(line) != "hello" {
		t.Fatalf("line = %q, want %q", line, "hello")
	}
}

// TestReadLineWithLimitRefusesAnOverlongFrame is the bound: a server dumping
// without a newline must not grow memory without limit.
func TestReadLineWithLimitRefusesAnOverlongFrame(t *testing.T) {
	client := &Client{name: "fake"}
	reader := bufio.NewReaderSize(strings.NewReader(strings.Repeat("x", maxMessageBytes+10)), 4096)

	if _, err := client.readLineWithLimit(reader); err == nil {
		t.Fatalf("a frame over the cap must be refused")
	}
}
