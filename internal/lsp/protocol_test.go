package lsp

import (
	"bufio"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadMessageFraming(t *testing.T) {
	stream := "\r\nContent-Length: 7\r\nContent-Type: application/vscode-jsonrpc\r\n\r\n{\"a\":1}Content-Length: 2\r\n\r\n{}"
	reader := bufio.NewReader(strings.NewReader(stream))
	first, err := readMessage(reader)
	if err != nil || string(first) != `{"a":1}` {
		t.Fatalf("first = %q, %v", first, err)
	}
	second, err := readMessage(reader)
	if err != nil || string(second) != `{}` {
		t.Fatalf("second = %q, %v", second, err)
	}
}

func TestURIRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dir with space", "main.go")
	uri := PathToURI(path)
	if !strings.HasPrefix(uri, "file:///") || strings.Contains(uri, " ") {
		t.Fatalf("uri = %q", uri)
	}
	if got := URIToPath(uri); normalizeURI(PathToURI(got)) != normalizeURI(uri) {
		t.Fatalf("round trip %q -> %q", path, got)
	}
}

// LSP counts columns in UTF-16 units, so a character outside the BMP is two.
func TestUTF16Columns(t *testing.T) {
	line := "a\U0001F600b"
	byteB := strings.Index(line, "b")
	if got := UTF16Column(line, byteB); got != 3 {
		t.Fatalf("UTF16Column = %d, want 3", got)
	}
	if got := ByteColumn(line, 3); got != byteB {
		t.Fatalf("ByteColumn = %d, want %d", got, byteB)
	}
	if got := RuneColumn(line, 3); got != 3 {
		t.Fatalf("RuneColumn = %d, want 3", got)
	}
}

func TestApplyEditsFromTheEnd(t *testing.T) {
	text := "func old() {}\nvar x = old()\r\nold()\n"
	edits := []TextEdit{
		{Range: Range{Start: Position{0, 5}, End: Position{0, 8}}, NewText: "fresh"},
		{Range: Range{Start: Position{1, 8}, End: Position{1, 11}}, NewText: "fresh"},
		{Range: Range{Start: Position{2, 0}, End: Position{2, 3}}, NewText: "fresh"},
	}
	want := "func fresh() {}\nvar x = fresh()\r\nfresh()\n"
	if got := ApplyEdits(text, edits); got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

func TestServerForHonoursOverrides(t *testing.T) {
	server, ok := ServerFor("x.py", map[string][]string{".py": {"pylsp"}})
	if !ok || server.Candidates[0][0] != "pylsp" || server.Language != "python" {
		t.Fatalf("override not applied: %+v", server)
	}
	if _, ok := ServerFor("x.unknownext", nil); ok {
		t.Fatalf("an unknown extension has no server")
	}
}
