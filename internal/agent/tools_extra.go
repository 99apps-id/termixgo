package agent

import (
	"archive/zip"
	"context"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// httpRequestTool sends arbitrary HTTP requests for API testing and pentesting.
type httpRequestTool struct{}

func (t *httpRequestTool) Name() string      { return "http_request" }
func (t *httpRequestTool) Aliases() []string { return []string{"curl", "request", "fetch_api"} }
func (t *httpRequestTool) Mutating() bool    { return false }
func (t *httpRequestTool) Risk() Risk        { return RiskNetwork }
func (t *httpRequestTool) Label(a map[string]any) string {
	method := strings.ToUpper(argString(a, "method"))
	if method == "" {
		method = "GET"
	}
	return fmt.Sprintf("HTTP %s %s", method, Shorten(argString(a, "url"), 35))
}
func (t *httpRequestTool) DoneLabel(a map[string]any) string {
	method := strings.ToUpper(argString(a, "method"))
	if method == "" {
		method = "GET"
	}
	return fmt.Sprintf("HTTP %s %s completed", method, Shorten(argString(a, "url"), 35))
}
func (t *httpRequestTool) Description() string {
	return "Send an HTTP request with customizable method (GET, POST, PUT, DELETE, PATCH, OPTIONS, HEAD), headers, and body for API testing and security audits."
}
func (t *httpRequestTool) Schema() map[string]any {
	return object(map[string]any{
		"url":              strProp("Target URL (http:// or https://)."),
		"method":           strProp("HTTP method: GET, POST, PUT, DELETE, PATCH, OPTIONS, HEAD (defaults to GET)."),
		"headers":          object(map[string]any{}),
		"body":             strProp("Optional request body string."),
		"timeout_seconds":  intProp("Request timeout in seconds, 1 to 60 (defaults to 10)."),
		"follow_redirects": boolProp("Whether to follow HTTP redirects automatically (defaults to true)."),
	}, "url")
}

func (t *httpRequestTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	rawURL := strings.TrimSpace(argString(args, "url"))
	if rawURL == "" {
		return Result{Output: "url is required.", IsError: true}, nil
	}

	if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
		rawURL = "https://" + rawURL
	}

	parsed, err := url.Parse(rawURL)
	if err != nil {
		return Result{Output: fmt.Sprintf("invalid URL: %v", err), IsError: true}, nil
	}

	if isBlockedHost(parsed.Hostname()) {
		return Result{Output: fmt.Sprintf("host %q is a blocked address", parsed.Hostname()), IsError: true}, nil
	}

	method := strings.ToUpper(strings.TrimSpace(argString(args, "method")))
	if method == "" {
		method = "GET"
	}

	timeoutSec := argInt(args, "timeout_seconds", 10, 1, 60)
	followRedirects := true
	if v, ok := args["follow_redirects"].(bool); ok {
		followRedirects = v
	}

	client := &http.Client{
		Timeout: time.Duration(timeoutSec) * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if !followRedirects {
				return http.ErrUseLastResponse
			}
			if len(via) >= 10 {
				return fmt.Errorf("stopped after 10 redirects")
			}
			if isBlockedHost(req.URL.Hostname()) {
				return fmt.Errorf("redirect to blocked host %q", req.URL.Hostname())
			}
			return nil
		},
	}

	bodyStr := argString(args, "body")
	var bodyReader io.Reader
	if bodyStr != "" {
		bodyReader = strings.NewReader(bodyStr)
	}

	req, err := http.NewRequestWithContext(ctx, method, rawURL, bodyReader)
	if err != nil {
		return Result{Output: fmt.Sprintf("failed to build request: %v", err), IsError: true}, nil
	}

	// Apply headers
	req.Header.Set("User-Agent", "Termixgo/0.1.6 (SecurityAudit & API Test)")
	if rawHeaders, ok := args["headers"].(map[string]any); ok {
		for k, v := range rawHeaders {
			if vStr, ok := v.(string); ok {
				req.Header.Set(k, vStr)
			}
		}
	}

	start := time.Now()
	resp, err := client.Do(req)
	latency := time.Since(start)
	if err != nil {
		return Result{Output: fmt.Sprintf("HTTP request error: %v (latency: %s)", err, latency.Round(time.Millisecond)), IsError: true}, nil
	}
	defer resp.Body.Close()

	// Read body with 64KB cap
	const maxRead = 64 * 1024
	bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, maxRead))

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("HTTP %s %s -> %s\n", method, rawURL, resp.Status))
	sb.WriteString(fmt.Sprintf("Latency: %s | Protocol: %s\n", latency.Round(time.Millisecond), resp.Proto))
	sb.WriteString("\n[Response Headers]\n")
	for k, vv := range resp.Header {
		for _, v := range vv {
			sb.WriteString(fmt.Sprintf("  %s: %s\n", k, v))
		}
	}

	sb.WriteString("\n[Response Body]\n")
	if len(bodyBytes) == 0 {
		sb.WriteString("(empty body)\n")
	} else {
		sb.WriteString(string(bodyBytes))
		if len(bodyBytes) >= maxRead {
			sb.WriteString("\n... [Response body truncated at 64KB]")
		}
	}

	return Result{Output: sb.String()}, nil
}

// jwtInspectTool decodes and checks JWT token structures for vulnerabilities.
type jwtInspectTool struct{}

func (t *jwtInspectTool) Name() string      { return "jwt_inspect" }
func (t *jwtInspectTool) Aliases() []string { return []string{"jwt_decode", "decode_jwt"} }
func (t *jwtInspectTool) Mutating() bool    { return false }
func (t *jwtInspectTool) Risk() Risk        { return RiskEdit }
func (t *jwtInspectTool) Label(a map[string]any) string {
	return "Inspecting JWT token"
}
func (t *jwtInspectTool) DoneLabel(a map[string]any) string {
	return "Inspected JWT token"
}
func (t *jwtInspectTool) Description() string {
	return "Decode and audit a JSON Web Token (JWT). Parses header and payload, checks expiration, and flags security issues (alg: none, weak algorithm, sensitive claims)."
}
func (t *jwtInspectTool) Schema() map[string]any {
	return object(map[string]any{
		"token": strProp("JWT token string (header.payload.signature)."),
	}, "token")
}

func decodeBase64URLPart(part string) ([]byte, error) {
	// Add padding if needed
	switch len(part) % 4 {
	case 2:
		part += "=="
	case 3:
		part += "="
	}
	return base64.URLEncoding.DecodeString(part)
}

func (t *jwtInspectTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	token := strings.TrimSpace(argString(args, "token"))
	if token == "" {
		return Result{Output: "token is required.", IsError: true}, nil
	}

	parts := strings.Split(token, ".")
	if len(parts) < 2 || len(parts) > 3 {
		return Result{Output: fmt.Sprintf("invalid JWT format: expected 3 parts separated by dots, got %d", len(parts)), IsError: true}, nil
	}

	headerBytes, err := decodeBase64URLPart(parts[0])
	if err != nil {
		return Result{Output: fmt.Sprintf("failed to decode JWT header: %v", err), IsError: true}, nil
	}

	payloadBytes, err := decodeBase64URLPart(parts[1])
	if err != nil {
		return Result{Output: fmt.Sprintf("failed to decode JWT payload: %v", err), IsError: true}, nil
	}

	var headerMap map[string]any
	_ = json.Unmarshal(headerBytes, &headerMap)

	var payloadMap map[string]any
	_ = json.Unmarshal(payloadBytes, &payloadMap)

	var sb strings.Builder
	sb.WriteString("JWT Token Inspection & Security Audit\n")
	sb.WriteString(strings.Repeat("-", 50) + "\n")

	// Security Findings
	var findings []string
	alg, _ := headerMap["alg"].(string)
	if strings.EqualFold(alg, "none") {
		findings = append(findings, "CRITICAL: 'alg: none' detected! Token signature is NOT verified.")
	} else if alg == "" {
		findings = append(findings, "WARNING: Algorithm field 'alg' is missing in header.")
	}

	now := time.Now()
	if expNum, ok := payloadMap["exp"].(float64); ok {
		expTime := time.Unix(int64(expNum), 0)
		if now.After(expTime) {
			findings = append(findings, fmt.Sprintf("EXPIRED: Token expired at %s (%s ago).",
				expTime.Format("2006-01-02 15:04:05 MST"), now.Sub(expTime).Round(time.Second)))
		} else {
			sb.WriteString(fmt.Sprintf("Expires At:        %s (%s remaining)\n",
				expTime.Format("2006-01-02 15:04:05 MST"), expTime.Sub(now).Round(time.Minute)))
		}
	} else {
		findings = append(findings, "WARNING: No 'exp' expiration claim found. Token never expires.")
	}

	if iatNum, ok := payloadMap["iat"].(float64); ok {
		iatTime := time.Unix(int64(iatNum), 0)
		sb.WriteString(fmt.Sprintf("Issued At:         %s\n", iatTime.Format("2006-01-02 15:04:05 MST")))
	}

	// Sensitive claim check
	for key := range payloadMap {
		lower := strings.ToLower(key)
		if strings.Contains(lower, "password") || strings.Contains(lower, "secret") || strings.Contains(lower, "hash") {
			findings = append(findings, fmt.Sprintf("SENSITIVE: Potential sensitive credential claim %q found in payload.", key))
		}
	}

	if len(findings) > 0 {
		sb.WriteString("\n[SECURITY FINDINGS]\n")
		for _, f := range findings {
			sb.WriteString("  ! " + f + "\n")
		}
	} else {
		sb.WriteString("\n[SECURITY STATUS]\n  + No obvious configuration weaknesses detected in header/claims.\n")
	}

	prettyHeader, _ := json.MarshalIndent(headerMap, "", "  ")
	prettyPayload, _ := json.MarshalIndent(payloadMap, "", "  ")

	sb.WriteString("\n[Decoded Header]\n")
	sb.WriteString(string(prettyHeader) + "\n")

	sb.WriteString("\n[Decoded Payload]\n")
	sb.WriteString(string(prettyPayload) + "\n")

	if len(parts) == 3 && parts[2] != "" {
		sb.WriteString(fmt.Sprintf("\n[Signature]\nPresent (%d bytes)\n", len(parts[2])))
	} else {
		sb.WriteString("\n[Signature]\nMISSING (Unsigned token)\n")
	}

	return Result{Output: sb.String()}, nil
}

// secretScanTool scans workspace files for leaked credentials and hardcoded secrets.
type secretScanTool struct{}

func (t *secretScanTool) Name() string      { return "secret_scan" }
func (t *secretScanTool) Aliases() []string { return []string{"scan_secrets", "detect_secrets"} }
func (t *secretScanTool) Mutating() bool    { return false }
func (t *secretScanTool) Risk() Risk        { return RiskEdit }
func (t *secretScanTool) Label(a map[string]any) string {
	return "Scanning workspace for leaked secrets"
}
func (t *secretScanTool) DoneLabel(a map[string]any) string {
	return "Completed secret leak scan"
}
func (t *secretScanTool) Description() string {
	return "Scan workspace files to detect hardcoded secrets (API keys, private keys, database connection strings, JWT, tokens)."
}
func (t *secretScanTool) Schema() map[string]any {
	return object(map[string]any{
		"path":        strProp("Optional path or directory to scan (defaults to workspace root)."),
		"max_results": intProp("Maximum secret findings to report, 1 to 100 (defaults to 50)."),
	})
}

var secretPatterns = []struct {
	Name    string
	Pattern *regexp.Regexp
}{
	{"AWS Access Key", regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`)},
	{"GitHub Token", regexp.MustCompile(`\bgh[pousr]_[0-9A-Za-z]{36,}\b`)},
	{"OpenAI API Key", regexp.MustCompile(`\bsk-[a-zA-Z0-9_-]{20,}\b`)},
	{"Anthropic API Key", regexp.MustCompile(`\bsk-ant-[a-zA-Z0-9_-]{20,}\b`)},
	{"Google API Key", regexp.MustCompile(`\bAIza[0-9A-Za-z\-_]{35}\b`)},
	{"Telegram Bot Token", regexp.MustCompile(`\b[0-9]{9,10}:[a-zA-Z0-9_-]{35}\b`)},
	{"Private Key Header", regexp.MustCompile(`-----BEGIN (RSA|OPENSSH|EC|DSA|PGP|PRIVATE KEY)`)},
	{"Database Connection URI", regexp.MustCompile(`\b(postgres|postgresql|mysql|mongodb|redis)://[^:\s]+:[^@\s]+@`)},
	{"Slack Token", regexp.MustCompile(`\bxox[baprs]-[0-9A-Za-z-]{20,}\b`)},
}

func maskSecret(s string) string {
	if len(s) <= 8 {
		return "****"
	}
	return s[:4] + "****" + s[len(s)-4:]
}

func (t *secretScanTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	scanPathRaw := strings.TrimSpace(argString(args, "path"))
	targetPath := env.Workspace
	if scanPathRaw != "" {
		targetPath = resolvePath(env, scanPathRaw)
	}

	maxResults := argInt(args, "max_results", 50, 1, 100)

	type matchItem struct {
		File    string
		Line    int
		Type    string
		Snippet string
	}

	var matches []matchItem
	skipDirs := map[string]bool{
		".git": true, "node_modules": true, "vendor": true,
		".termixgo": true, ".idea": true, ".vscode": true, "dist": true, "build": true,
	}

	err := filepath.Walk(targetPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if info.IsDir() {
			if skipDirs[info.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if info.Size() > 1024*1024 || !isTextFile(path) {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}

		relPath := displayPath(env, path)
		lines := strings.Split(string(data), "\n")
		for lineIdx, line := range lines {
			if len(matches) >= maxResults {
				return io.EOF // Stop walking
			}
			for _, pat := range secretPatterns {
				if loc := pat.Pattern.FindString(line); loc != "" {
					matches = append(matches, matchItem{
						File:    relPath,
						Line:    lineIdx + 1,
						Type:    pat.Name,
						Snippet: maskSecret(loc),
					})
					break
				}
			}
		}
		return nil
	})

	if err != nil && err != io.EOF && err != context.Canceled {
		return Result{Output: fmt.Sprintf("Error walking directory: %v", err), IsError: true}, nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Secret Scan Report: %s\n", displayPath(env, targetPath)))
	sb.WriteString(strings.Repeat("-", 60) + "\n")

	if len(matches) == 0 {
		sb.WriteString("No leaked credentials or hardcoded secrets detected!\n")
		return Result{Output: sb.String()}, nil
	}

	sb.WriteString(fmt.Sprintf("WARNING: Detected %d potential secret leak(s):\n\n", len(matches)))
	for idx, m := range matches {
		sb.WriteString(fmt.Sprintf("[%d] %s (Line %d)\n", idx+1, m.File, m.Line))
		sb.WriteString(fmt.Sprintf("    Type:    %s\n", m.Type))
		sb.WriteString(fmt.Sprintf("    Finding: %s\n\n", m.Snippet))
	}

	return Result{Output: strings.TrimRight(sb.String(), "\n")}, nil
}

// hashCalcTool calculates cryptographic checksums (MD5, SHA1, SHA256, SHA512).
type hashCalcTool struct{}

func (t *hashCalcTool) Name() string      { return "hash_calc" }
func (t *hashCalcTool) Aliases() []string { return []string{"calculate_hash", "checksum"} }
func (t *hashCalcTool) Mutating() bool    { return false }
func (t *hashCalcTool) Risk() Risk        { return RiskEdit }
func (t *hashCalcTool) Label(a map[string]any) string {
	algo := strings.ToUpper(argString(a, "algorithm"))
	if algo == "" {
		algo = "SHA256"
	}
	return fmt.Sprintf("Calculating %s hash", algo)
}
func (t *hashCalcTool) DoneLabel(a map[string]any) string {
	algo := strings.ToUpper(argString(a, "algorithm"))
	if algo == "" {
		algo = "SHA256"
	}
	return fmt.Sprintf("Calculated %s hash", algo)
}
func (t *hashCalcTool) Description() string {
	return "Calculate cryptographic hash checksums (MD5, SHA1, SHA256, SHA512) for a string or file."
}
func (t *hashCalcTool) Schema() map[string]any {
	return object(map[string]any{
		"algorithm": strProp("Hash algorithm: md5, sha1, sha256, sha512 (defaults to sha256)."),
		"content":   strProp("Direct text content to hash."),
		"path":      strProp("File path to hash (if content is omitted)."),
	})
}

func (t *hashCalcTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	algo := strings.ToLower(strings.TrimSpace(argString(args, "algorithm")))
	if algo == "" {
		algo = "sha256"
	}

	var h hash.Hash
	switch algo {
	case "md5":
		h = md5.New()
	case "sha1":
		h = sha1.New()
	case "sha256":
		h = sha256.New()
	case "sha512":
		h = sha512.New()
	default:
		return Result{Output: fmt.Sprintf("unsupported algorithm %q; use md5, sha1, sha256, or sha512", algo), IsError: true}, nil
	}

	content := argString(args, "content")
	filePath := strings.TrimSpace(argString(args, "path"))

	var targetLabel string
	if filePath != "" {
		resolved := resolvePath(env, filePath)
		f, err := os.Open(resolved)
		if err != nil {
			return Result{Output: fmt.Sprintf("failed to open file %q: %v", filePath, err), IsError: true}, nil
		}
		defer f.Close()
		if _, err := io.Copy(h, f); err != nil {
			return Result{Output: fmt.Sprintf("failed to read file: %v", err), IsError: true}, nil
		}
		targetLabel = fmt.Sprintf("File: %s", displayPath(env, resolved))
	} else if content != "" {
		h.Write([]byte(content))
		targetLabel = fmt.Sprintf("Content: %q", Shorten(content, 30))
	} else {
		return Result{Output: "either 'content' or 'path' must be provided.", IsError: true}, nil
	}

	digest := hex.EncodeToString(h.Sum(nil))
	return Result{
		Output: fmt.Sprintf("Algorithm: %s\n%s\nChecksum:  %s", strings.ToUpper(algo), targetLabel, digest),
	}, nil
}

// archiveTool creates or extracts .zip archives safely.
type archiveTool struct{}

func (t *archiveTool) Name() string      { return "archive_tool" }
func (t *archiveTool) Aliases() []string { return []string{"zip", "unzip", "zip_extract"} }
func (t *archiveTool) Mutating() bool    { return true }
func (t *archiveTool) Risk() Risk        { return RiskEdit }
func (t *archiveTool) Label(a map[string]any) string {
	act := strings.ToLower(argString(a, "action"))
	return fmt.Sprintf("Archive %s %s", act, Shorten(argString(a, "archive_path"), 30))
}
func (t *archiveTool) DoneLabel(a map[string]any) string {
	act := strings.ToLower(argString(a, "action"))
	return fmt.Sprintf("Archive %s completed", act)
}
func (t *archiveTool) Description() string {
	return "Compress files into a .zip archive or extract an existing .zip archive safely without external utilities."
}
func (t *archiveTool) Schema() map[string]any {
	return object(map[string]any{
		"action":           strProp("Action: 'zip' to create archive, 'unzip' to extract."),
		"archive_path":     strProp("Path to the .zip archive file."),
		"source_paths":     arrayProp("Files or directories to include in zip (for 'zip' action).", strProp("File or directory path")),
		"destination_path": strProp("Destination directory for extracted files (for 'unzip' action, defaults to archive directory)."),
	}, "action", "archive_path")
}

func (t *archiveTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	action := strings.ToLower(strings.TrimSpace(argString(args, "action")))
	archivePathRaw := strings.TrimSpace(argString(args, "archive_path"))
	if archivePathRaw == "" {
		return Result{Output: "archive_path is required.", IsError: true}, nil
	}
	archivePath := resolvePath(env, archivePathRaw)

	switch action {
	case "zip":
		var sources []string
		if rawSources, ok := args["source_paths"].([]any); ok {
			for _, s := range rawSources {
				if sStr, ok := s.(string); ok && strings.TrimSpace(sStr) != "" {
					sources = append(sources, strings.TrimSpace(sStr))
				}
			}
		}
		if len(sources) == 0 {
			return Result{Output: "source_paths must contain at least one file or folder to compress.", IsError: true}, nil
		}

		if err := os.MkdirAll(filepath.Dir(archivePath), 0o755); err != nil {
			return Result{Output: fmt.Sprintf("failed to create destination dir: %v", err), IsError: true}, nil
		}

		zipFile, err := os.Create(archivePath)
		if err != nil {
			return Result{Output: fmt.Sprintf("failed to create zip file: %v", err), IsError: true}, nil
		}
		defer zipFile.Close()

		zipWriter := zip.NewWriter(zipFile)
		count := 0

		for _, src := range sources {
			resolvedSrc := resolvePath(env, src)
			err := filepath.Walk(resolvedSrc, func(path string, info os.FileInfo, err error) error {
				if err != nil {
					return err
				}
				if info.IsDir() {
					return nil
				}
				relToEnv, _ := filepath.Rel(env.Workspace, path)
				header, err := zip.FileInfoHeader(info)
				if err != nil {
					return err
				}
				header.Name = filepath.ToSlash(relToEnv)
				header.Method = zip.Deflate

				writer, err := zipWriter.CreateHeader(header)
				if err != nil {
					return err
				}
				fileData, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				_, err = writer.Write(fileData)
				if err == nil {
					count++
				}
				return err
			})
			if err != nil {
				_ = zipWriter.Close()
				return Result{Output: fmt.Sprintf("error archiving %s: %v", src, err), IsError: true}, nil
			}
		}

		if err := zipWriter.Close(); err != nil {
			return Result{Output: fmt.Sprintf("failed to close zip: %v", err), IsError: true}, nil
		}

		return Result{
			Output: fmt.Sprintf("Created archive %s containing %d file(s).", displayPath(env, archivePath), count),
		}, nil

	case "unzip":
		destPathRaw := strings.TrimSpace(argString(args, "destination_path"))
		destPath := filepath.Dir(archivePath)
		if destPathRaw != "" {
			destPath = resolvePath(env, destPathRaw)
		}

		r, err := zip.OpenReader(archivePath)
		if err != nil {
			return Result{Output: fmt.Sprintf("failed to open zip file: %v", err), IsError: true}, nil
		}
		defer r.Close()

		extractedCount := 0
		cleanDest := filepath.Clean(destPath)

		for _, f := range r.File {
			fPath := filepath.Join(cleanDest, f.Name)
			// Guard against ZipSlip path traversal
			if !strings.HasPrefix(filepath.Clean(fPath), cleanDest) {
				return Result{Output: fmt.Sprintf("security error: zip path traversal detected in %s", f.Name), IsError: true}, nil
			}

			if f.FileInfo().IsDir() {
				_ = os.MkdirAll(fPath, 0o755)
				continue
			}

			if err := os.MkdirAll(filepath.Dir(fPath), 0o755); err != nil {
				return Result{Output: fmt.Sprintf("failed to create dir for %s: %v", fPath, err), IsError: true}, nil
			}

			outFile, err := os.OpenFile(fPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
			if err != nil {
				return Result{Output: fmt.Sprintf("failed to create file %s: %v", fPath, err), IsError: true}, nil
			}

			rc, err := f.Open()
			if err != nil {
				outFile.Close()
				return Result{Output: fmt.Sprintf("failed to read zip member %s: %v", f.Name, err), IsError: true}, nil
			}

			_, err = io.Copy(outFile, rc)
			rc.Close()
			outFile.Close()
			if err != nil {
				return Result{Output: fmt.Sprintf("failed to extract %s: %v", f.Name, err), IsError: true}, nil
			}
			extractedCount++
		}

		return Result{
			Output: fmt.Sprintf("Extracted %d file(s) from %s into %s", extractedCount, displayPath(env, archivePath), displayPath(env, cleanDest)),
		}, nil

	default:
		return Result{Output: fmt.Sprintf("invalid action %q; use 'zip' or 'unzip'", action), IsError: true}, nil
	}
}

// encodingTool handles common developer conversions (base64, base64url, hex, url encode/decode).
type encodingTool struct{}

func (t *encodingTool) Name() string      { return "encoding_tool" }
func (t *encodingTool) Aliases() []string { return []string{"encode_decode", "codec"} }
func (t *encodingTool) Mutating() bool    { return false }
func (t *encodingTool) Risk() Risk        { return RiskEdit }
func (t *encodingTool) Label(a map[string]any) string {
	return fmt.Sprintf("%s %s", strings.Title(argString(a, "action")), argString(a, "format"))
}
func (t *encodingTool) DoneLabel(a map[string]any) string {
	return fmt.Sprintf("%s %s completed", strings.Title(argString(a, "action")), argString(a, "format"))
}
func (t *encodingTool) Description() string {
	return "Encode or decode strings between plaintext, base64, base64url, hex, and url encoding."
}
func (t *encodingTool) Schema() map[string]any {
	return object(map[string]any{
		"action": strProp("Operation: 'encode' or 'decode'."),
		"format": strProp("Format: 'base64', 'base64url', 'hex', or 'url'."),
		"input":  strProp("Input text to process."),
	}, "action", "format", "input")
}

func (t *encodingTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	action := strings.ToLower(strings.TrimSpace(argString(args, "action")))
	format := strings.ToLower(strings.TrimSpace(argString(args, "format")))
	input := argString(args, "input")

	if action != "encode" && action != "decode" {
		return Result{Output: fmt.Sprintf("invalid action %q; must be 'encode' or 'decode'", action), IsError: true}, nil
	}

	var output string
	switch format {
	case "base64":
		if action == "encode" {
			output = base64.StdEncoding.EncodeToString([]byte(input))
		} else {
			dec, err := base64.StdEncoding.DecodeString(input)
			if err != nil {
				return Result{Output: fmt.Sprintf("invalid base64 input: %v", err), IsError: true}, nil
			}
			output = string(dec)
		}

	case "base64url":
		if action == "encode" {
			output = base64.RawURLEncoding.EncodeToString([]byte(input))
		} else {
			dec, err := decodeBase64URLPart(input)
			if err != nil {
				return Result{Output: fmt.Sprintf("invalid base64url input: %v", err), IsError: true}, nil
			}
			output = string(dec)
		}

	case "hex":
		if action == "encode" {
			output = hex.EncodeToString([]byte(input))
		} else {
			dec, err := hex.DecodeString(strings.TrimPrefix(input, "0x"))
			if err != nil {
				return Result{Output: fmt.Sprintf("invalid hex input: %v", err), IsError: true}, nil
			}
			output = string(dec)
		}

	case "url":
		if action == "encode" {
			output = url.QueryEscape(input)
		} else {
			dec, err := url.QueryUnescape(input)
			if err != nil {
				return Result{Output: fmt.Sprintf("invalid url-encoded input: %v", err), IsError: true}, nil
			}
			output = dec
		}

	default:
		return Result{Output: fmt.Sprintf("unsupported format %q; use base64, base64url, hex, or url", format), IsError: true}, nil
	}

	return Result{
		Output: fmt.Sprintf("Action: %s (%s)\nOutput:\n%s", strings.ToUpper(action), format, output),
	}, nil
}
