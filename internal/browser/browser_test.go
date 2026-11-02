package browser

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNormalizeURL(t *testing.T) {
	for _, want := range []string{
		"https://example.com",
		"http://localhost:3000/app",
		"https://example.com/a?b=c",
		"about:blank",
	} {
		got, err := NormalizeURL(want)
		if err != nil {
			t.Fatalf("%q: %v", want, err)
		}
		if got != want {
			t.Fatalf("%q normalized to %q", want, got)
		}
	}
	got, err := NormalizeURL("example.com")
	if err != nil || got != "https://example.com" {
		t.Fatalf("bare host => %q, %v", got, err)
	}
	for _, bad := range []string{"", "ftp://example.com/x", "http://169.254.169.254/latest", "http://metadata.google.internal/"} {
		if _, err := NormalizeURL(bad); err == nil {
			t.Fatalf("%q should be refused", bad)
		}
	}
	if _, err := NormalizeURL("file:///etc/passwd"); err != nil {
		t.Fatalf("a file URL should be allowed: %v", err)
	}
}

func TestIsMetadataHost(t *testing.T) {
	for _, host := range []string{"169.254.169.254", "fd00:ec2::254", "metadata.google.internal", "169.254.1.1"} {
		if !isMetadataHost(host) {
			t.Fatalf("%s should be metadata", host)
		}
	}
	for _, host := range []string{"example.com", "127.0.0.1", "localhost", "10.0.0.5"} {
		if isMetadataHost(host) {
			t.Fatalf("%s should be reachable", host)
		}
	}
}

func TestParseKey(t *testing.T) {
	spec, err := parseKey("ctrl+shift+s")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if spec.Modifiers != modCtrl|modShift {
		t.Fatalf("modifiers = %d", spec.Modifiers)
	}
	if spec.Key != "S" || spec.Text != "S" || spec.VK != int('S') {
		t.Fatalf("spec = %+v", spec)
	}
	enter, err := parseKey("enter")
	if err != nil {
		t.Fatalf("enter: %v", err)
	}
	if enter.Key != "Enter" || enter.VK != 13 || enter.Text != "\r" {
		t.Fatalf("enter spec = %+v", enter)
	}
	if _, err := parseKey("hyper"); err == nil {
		t.Fatal("an unknown key should fail")
	}
}

func TestTargetIDFromURL(t *testing.T) {
	got := targetIDFromURL("ws://127.0.0.1:9222/devtools/page/ABC123")
	if got != "ABC123" {
		t.Fatalf("got %q", got)
	}
	if targetIDFromURL("ws://127.0.0.1:9222/devtools/browser") == "" {
		t.Fatal("a browser path should still yield a segment")
	}
}

func TestReadDevToolsPort(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "DevToolsActivePort")
	if err := os.WriteFile(path, []byte("4222\n/devtools/browser/xyz\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	port, browserPath, err := readDevToolsPort(dir, time.Now().Add(time.Second))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if port != 4222 || browserPath != "/devtools/browser/xyz" {
		t.Fatalf("port=%d path=%q", port, browserPath)
	}
}

func TestJSLiteralQuote(t *testing.T) {
	if got := jsLiteral(`a"b`); got != `"a\"b"` {
		t.Fatalf("got %s", got)
	}
	if got := jsLiteral("plain"); got != `"plain"` {
		t.Fatalf("got %s", got)
	}
}
