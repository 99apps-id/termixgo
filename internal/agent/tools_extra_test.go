package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHTTPRequestTool(t *testing.T) {
	// The test server listens on loopback, which the SSRF default refuses;
	// the opt-in below plays the operator allowing their own machine.
	t.Setenv("TERMIXGO_ALLOW_PRIVATE_FETCH", "1")
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && r.Header.Get("X-Custom") == "TermixgoTest" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"success": true, "id": 101}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("GET OK"))
	}))
	defer ts.Close()

	tool := &httpRequestTool{}

	// Blocked host validation
	res, _ := tool.Run(context.Background(), nil, map[string]any{"url": "http://169.254.169.254/api"})
	if !res.IsError {
		t.Fatalf("expected blocked host error")
	}

	// POST with headers and body
	res, err := tool.Run(context.Background(), nil, map[string]any{
		"url":    ts.URL,
		"method": "POST",
		"headers": map[string]any{
			"X-Custom": "TermixgoTest",
		},
		"body": `{"query": "test"}`,
	})
	if err != nil || res.IsError {
		t.Fatalf("unexpected error: %v, out: %s", err, res.Output)
	}
	if !strings.Contains(res.Output, "201 Created") {
		t.Errorf("expected 201 Created status, got: %s", res.Output)
	}
	if !strings.Contains(res.Output, `{"success": true, "id": 101}`) {
		t.Errorf("expected response body, got: %s", res.Output)
	}
}

func TestJWTInspectTool(t *testing.T) {
	tool := &jwtInspectTool{}

	// Invalid token
	res, _ := tool.Run(context.Background(), nil, map[string]any{"token": "not-a-token"})
	if !res.IsError {
		t.Fatalf("expected error for malformed token")
	}

	// Header: {"alg":"none","typ":"JWT"} -> eyJhbGciOiJub25lIiwidHlwIjoiSldUIn0
	// Payload: {"sub":"admin","name":"Iwan","exp":1700000000} -> eyJzdWIiOiJhZG1pbiIsIm5hbWUiOiJJd2FuIiwiZXhwIjoxNzAwMDAwMDAwfQ
	insecureToken := "eyJhbGciOiJub25lIiwidHlwIjoiSldUIn0.eyJzdWIiOiJhZG1pbiIsIm5hbWUiOiJJd2FuIiwiZXhwIjoxNzAwMDAwMDAwfQ."

	res, err := tool.Run(context.Background(), nil, map[string]any{"token": insecureToken})
	if err != nil || res.IsError {
		t.Fatalf("unexpected error: %v, out: %s", err, res.Output)
	}

	if !strings.Contains(res.Output, "'alg: none' detected") {
		t.Errorf("expected 'alg: none' critical warning, got: %s", res.Output)
	}
	if !strings.Contains(res.Output, "EXPIRED") {
		t.Errorf("expected expiration warning, got: %s", res.Output)
	}
	if !strings.Contains(res.Output, "admin") || !strings.Contains(res.Output, "Iwan") {
		t.Errorf("expected decoded payload, got: %s", res.Output)
	}
}

func TestSecretScanTool(t *testing.T) {
	env := testEnv(t)
	dummyFile := filepath.Join(env.Workspace, "config.py")
	dummySecret := "OPENAI_API_KEY = \"sk-1234567890abcdef1234567890\"\n"
	if err := os.WriteFile(dummyFile, []byte(dummySecret), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := &secretScanTool{}
	res, err := tool.Run(context.Background(), env, map[string]any{"path": "config.py"})
	if err != nil || res.IsError {
		t.Fatalf("unexpected error: %v, out: %s", err, res.Output)
	}

	if !strings.Contains(res.Output, "OpenAI API Key") {
		t.Errorf("expected OpenAI key detected, got: %s", res.Output)
	}
	if strings.Contains(res.Output, "sk-1234567890abcdef1234567890") {
		t.Errorf("secret must be masked in report, got: %s", res.Output)
	}
	if !strings.Contains(res.Output, "sk-1****7890") {
		t.Errorf("expected masked secret snippet, got: %s", res.Output)
	}
}

func TestHashCalcTool(t *testing.T) {
	tool := &hashCalcTool{}

	// SHA256 of "hello" is 2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824
	res, err := tool.Run(context.Background(), nil, map[string]any{
		"algorithm": "sha256",
		"content":   "hello",
	})
	if err != nil || res.IsError {
		t.Fatalf("unexpected error: %v, out: %s", err, res.Output)
	}
	expectedHash := "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
	if !strings.Contains(res.Output, expectedHash) {
		t.Errorf("expected hash %q, got: %s", expectedHash, res.Output)
	}

	// MD5 of "hello" is 5d41402abc4b2a76b9719d911017c592
	res, err = tool.Run(context.Background(), nil, map[string]any{
		"algorithm": "md5",
		"content":   "hello",
	})
	if err != nil || res.IsError {
		t.Fatalf("unexpected error: %v, out: %s", err, res.Output)
	}
	if !strings.Contains(res.Output, "5d41402abc4b2a76b9719d911017c592") {
		t.Errorf("expected md5 hash, got: %s", res.Output)
	}
}

func TestArchiveToolZipAndUnzip(t *testing.T) {
	env := testEnv(t)
	srcDir := filepath.Join(env.Workspace, "source")
	_ = os.MkdirAll(srcDir, 0o755)
	file1 := filepath.Join(srcDir, "doc.txt")
	_ = os.WriteFile(file1, []byte("Hello Termixgo Archive!"), 0o644)

	archivePath := filepath.Join(env.Workspace, "test.zip")
	extractDir := filepath.Join(env.Workspace, "extracted")

	tool := &archiveTool{}

	// 1. Create Zip
	zipRes, err := tool.Run(context.Background(), env, map[string]any{
		"action":       "zip",
		"archive_path": "test.zip",
		"source_paths": []any{"source"},
	})
	if err != nil || zipRes.IsError {
		t.Fatalf("zip failed: %v, out: %s", err, zipRes.Output)
	}
	if _, err := os.Stat(archivePath); err != nil {
		t.Fatalf("zip file was not created: %v", err)
	}

	// 2. Extract Zip
	unzipRes, err := tool.Run(context.Background(), env, map[string]any{
		"action":           "unzip",
		"archive_path":     "test.zip",
		"destination_path": "extracted",
	})
	if err != nil || unzipRes.IsError {
		t.Fatalf("unzip failed: %v, out: %s", err, unzipRes.Output)
	}

	// Check extracted file exists
	extractedFile := filepath.Join(extractDir, "source", "doc.txt")
	data, err := os.ReadFile(extractedFile)
	if err != nil {
		t.Fatalf("extracted file missing: %v", err)
	}
	if string(data) != "Hello Termixgo Archive!" {
		t.Errorf("extracted content mismatch: %q", string(data))
	}
}

func TestEncodingTool(t *testing.T) {
	tool := &encodingTool{}

	// Base64
	encRes, err := tool.Run(context.Background(), nil, map[string]any{
		"action": "encode",
		"format": "base64",
		"input":  "Termixgo Pro",
	})
	if err != nil || encRes.IsError {
		t.Fatalf("encode base64 failed: %v, out: %s", err, encRes.Output)
	}
	if !strings.Contains(encRes.Output, "VGVybWl4Z28gUHJv") {
		t.Errorf("expected base64 encoded text, got: %s", encRes.Output)
	}

	decRes, err := tool.Run(context.Background(), nil, map[string]any{
		"action": "decode",
		"format": "base64",
		"input":  "VGVybWl4Z28gUHJv",
	})
	if err != nil || decRes.IsError {
		t.Fatalf("decode base64 failed: %v, out: %s", err, decRes.Output)
	}
	if !strings.Contains(decRes.Output, "Termixgo Pro") {
		t.Errorf("expected decoded text, got: %s", decRes.Output)
	}

	// Hex
	hexRes, _ := tool.Run(context.Background(), nil, map[string]any{
		"action": "encode",
		"format": "hex",
		"input":  "ABC",
	})
	if !strings.Contains(hexRes.Output, "414243") {
		t.Errorf("expected hex 414243, got: %s", hexRes.Output)
	}

	// URL
	urlRes, _ := tool.Run(context.Background(), nil, map[string]any{
		"action": "encode",
		"format": "url",
		"input":  "hello world & secure=true",
	})
	if !strings.Contains(urlRes.Output, "hello+world+%26+secure%3Dtrue") && !strings.Contains(urlRes.Output, "hello%20world") {
		t.Errorf("expected URL escaped string, got: %s", urlRes.Output)
	}
}

func TestArchiveToolWorkspaceEscapeRefused(t *testing.T) {
	env := testEnv(t)
	tool := &archiveTool{}

	// Refuse archive creation outside workspace
	res, _ := tool.Run(context.Background(), env, map[string]any{
		"action":       "zip",
		"archive_path": "../outside.zip",
		"source_paths": []any{"source"},
	})
	if !res.IsError {
		t.Fatalf("expected error for archive_path outside workspace")
	}

	// Refuse extracting outside workspace
	res, _ = tool.Run(context.Background(), env, map[string]any{
		"action":           "unzip",
		"archive_path":     "test.zip",
		"destination_path": "../outside_dir",
	})
	if !res.IsError {
		t.Fatalf("expected error for destination_path outside workspace")
	}
}

func TestSecretScanAndHashCalcWorkspaceEscapeRefused(t *testing.T) {
	env := testEnv(t)

	secTool := &secretScanTool{}
	res, _ := secTool.Run(context.Background(), env, map[string]any{"path": "../outside_dir"})
	if !res.IsError {
		t.Fatalf("expected secret_scan to refuse path outside workspace")
	}

	hashTool := &hashCalcTool{}
	res, _ = hashTool.Run(context.Background(), env, map[string]any{"path": "../outside.txt"})
	if !res.IsError {
		t.Fatalf("expected hash_calc to refuse file outside workspace")
	}
}

func TestIsBlockedHostCloudMetadata(t *testing.T) {
	cases := []string{
		"169.254.169.254",
		"metadata.google.internal",
		"metadata.goog",
		"fd00:ec2::254",
		"100.100.100.200",
		"::ffff:169.254.169.254",
	}
	for _, host := range cases {
		if !isBlockedHost(host) {
			t.Errorf("host %q should be blocked as cloud metadata/link-local", host)
		}
	}
	if isBlockedHost("example.com") || isBlockedHost("127.0.0.1") || isBlockedHost("localhost") {
		t.Errorf("legitimate host should not be blocked")
	}
}

// TestPrivateHostBoundary pins the SSRF default: private and loopback ranges
// are refused unless the operator opted in, so a model-authored URL cannot
// reach the operator's LAN on its own.
func TestPrivateHostBoundary(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "localhost", "10.0.0.5", "192.168.1.1", "172.16.0.2", "::1"} {
		if !isPrivateHost(host) {
			t.Errorf("host %q should be private", host)
		}
	}
	for _, host := range []string{"example.com", "8.8.8.8", "1.1.1.1"} {
		if isPrivateHost(host) {
			t.Errorf("host %q should not be private", host)
		}
	}
	t.Setenv("TERMIXGO_ALLOW_PRIVATE_FETCH", "1")
	if !allowPrivateFetch() {
		t.Errorf("the operator opt-in should be honoured")
	}
}

// TestArchiveSkipsSymlinks pins the zip boundary: a link to a file outside
// the workspace must not pull outside bytes into the archive.
func TestArchiveSkipsSymlinks(t *testing.T) {
	env := testEnv(t)
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(env.Workspace, "link.txt")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks are not available here: %v", err)
	}
	tool := &archiveTool{}
	// A top-level link argument is refused outright: it names a file outside
	// the workspace, so there is nothing to archive.
	res, _ := tool.Run(context.Background(), env, map[string]any{
		"action":       "zip",
		"archive_path": "sym.zip",
		"source_paths": []any{"link.txt"},
	})
	if !res.IsError {
		t.Fatalf("zip must refuse a link that points outside the workspace, got %q", res.Output)
	}
	// A link nested inside an archived directory is skipped instead: the
	// directory itself is legitimate, only the link must not pull outside
	// bytes into the archive.
	real := filepath.Join(env.Workspace, "real.txt")
	if err := os.WriteFile(real, []byte("inside"), 0o644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(env.Workspace, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(sub, "link.txt")); err != nil {
		t.Skipf("symlinks are not available here: %v", err)
	}
	res, _ = tool.Run(context.Background(), env, map[string]any{
		"action":       "zip",
		"archive_path": "sub.zip",
		"source_paths": []any{"sub", "real.txt"},
	})
	if res.IsError {
		t.Fatalf("zip should skip the nested link, not fail: %s", res.Output)
	}
	if !strings.Contains(res.Output, "2 file(s)") {
		t.Errorf("only the two real files should be archived, got %q", res.Output)
	}
}

// TestFetchToolsRefusePrivateByDefault keeps the SSRF default wired through
// the fetch tools, while probe_url stays loopback-only by design.
func TestFetchToolsRefusePrivateByDefault(t *testing.T) {
	env := testEnv(t)
	fetch := &webFetchTool{}
	if res, _ := fetch.Run(context.Background(), env, map[string]any{"url": "http://127.0.0.1:9/nope"}); !res.IsError {
		t.Fatalf("web_fetch must refuse a loopback URL by default, got %q", res.Output)
	}
	httpTool := &httpRequestTool{}
	if res, _ := httpTool.Run(context.Background(), env, map[string]any{"url": "http://192.168.1.1/api"}); !res.IsError {
		t.Fatalf("http_request must refuse a private URL by default, got %q", res.Output)
	}
	t.Setenv("TERMIXGO_ALLOW_PRIVATE_FETCH", "1")
	// The opt-in only lifts the guard; the connection itself still fails on
	// an unroutable TEST-NET address, so the refused message must be gone
	// even though the attempt cannot succeed here.
	if res, _ := httpTool.Run(context.Background(), env, map[string]any{"url": "http://192.0.2.1:9/api", "timeout_seconds": 1}); strings.Contains(res.Output, "private or loopback") {
		t.Fatalf("with the opt-in a private fetch should attempt the connection, got %q", res.Output)
	}
}
