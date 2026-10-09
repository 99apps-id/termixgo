package agent

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// tlsInspectTool inspects SSL/TLS certificates and TLS configurations.
type tlsInspectTool struct{}

func (t *tlsInspectTool) Name() string      { return "tls_inspect" }
func (t *tlsInspectTool) Aliases() []string { return []string{"ssl_inspect", "cert_info"} }
func (t *tlsInspectTool) Mutating() bool    { return false }
func (t *tlsInspectTool) Risk() Risk        { return RiskNetwork }
func (t *tlsInspectTool) Label(a map[string]any) string {
	return "Inspecting TLS " + Shorten(argString(a, "host"), 40)
}
func (t *tlsInspectTool) DoneLabel(a map[string]any) string {
	return "Inspected TLS " + Shorten(argString(a, "host"), 40)
}
func (t *tlsInspectTool) Description() string {
	return "Inspect SSL/TLS certificate chain, expiration, SANs, cipher suite, and TLS version for an in-scope target host."
}
func (t *tlsInspectTool) Schema() map[string]any {
	return object(map[string]any{
		"host":            strProp("Hostname or host:port to inspect (e.g. example.com or api.example.com:443)."),
		"port":            intProp("Port to connect to if not specified in host (defaults to 443)."),
		"timeout_seconds": intProp("Connection timeout in seconds, 1 to 30 (defaults to 10)."),
	}, "host")
}

func (t *tlsInspectTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	rawHost := strings.TrimSpace(argString(args, "host"))
	if rawHost == "" {
		return Result{Output: "host is required.", IsError: true}, nil
	}

	rawHost = strings.TrimPrefix(rawHost, "https://")
	rawHost = strings.TrimPrefix(rawHost, "http://")
	if slash := strings.IndexByte(rawHost, '/'); slash != -1 {
		rawHost = rawHost[:slash]
	}

	host, portStr, err := net.SplitHostPort(rawHost)
	if err != nil {
		host = rawHost
		port := argInt(args, "port", 443, 1, 65535)
		portStr = fmt.Sprintf("%d", port)
	}

	if isBlockedHost(host) {
		return Result{Output: fmt.Sprintf("host %q is a blocked address", host), IsError: true}, nil
	}

	timeoutSec := argInt(args, "timeout_seconds", 10, 1, 30)
	timeout := time.Duration(timeoutSec) * time.Second

	dialer := &net.Dialer{Timeout: timeout}
	target := net.JoinHostPort(host, portStr)

	// First attempt: strict verification
	conn, err := tls.DialWithDialer(dialer, "tcp", target, &tls.Config{
		ServerName: host,
	})

	var verifyErr error
	var cs tls.ConnectionState
	if err != nil {
		verifyErr = err
		// Retry without verification to inspect the certificate details
		insecureConn, dialErr := tls.DialWithDialer(dialer, "tcp", target, &tls.Config{
			ServerName:         host,
			InsecureSkipVerify: true,
		})
		if dialErr != nil {
			return Result{
				Output:  fmt.Sprintf("Failed to establish TLS connection to %s: %v", target, dialErr),
				IsError: true,
			}, nil
		}
		cs = insecureConn.ConnectionState()
		_ = insecureConn.Close()
	} else {
		cs = conn.ConnectionState()
		_ = conn.Close()
	}

	if len(cs.PeerCertificates) == 0 {
		return Result{Output: fmt.Sprintf("No peer certificates presented by %s", target), IsError: true}, nil
	}

	cert := cs.PeerCertificates[0]
	now := time.Now()
	daysRemaining := int(cert.NotAfter.Sub(now).Hours() / 24)

	tlsVersion := tlsVersionName(cs.Version)
	cipherSuite := tls.CipherSuiteName(cs.CipherSuite)

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("TLS Inspection Report: %s\n", target))
	sb.WriteString(strings.Repeat("-", 50) + "\n")
	sb.WriteString(fmt.Sprintf("TLS Version:       %s\n", tlsVersion))
	sb.WriteString(fmt.Sprintf("Cipher Suite:      %s\n", cipherSuite))
	if verifyErr != nil {
		sb.WriteString(fmt.Sprintf("Validation Status: FAILED (%v)\n", verifyErr))
	} else {
		sb.WriteString("Validation Status: VALID (Certificate chain verified)\n")
	}

	sb.WriteString("\n[Primary Certificate]\n")
	sb.WriteString(fmt.Sprintf("Subject Common:    %s\n", cert.Subject.CommonName))
	if len(cert.Subject.Organization) > 0 {
		sb.WriteString(fmt.Sprintf("Subject Org:       %s\n", strings.Join(cert.Subject.Organization, ", ")))
	}
	sb.WriteString(fmt.Sprintf("Issuer:            %s (%s)\n", cert.Issuer.CommonName, strings.Join(cert.Issuer.Organization, ", ")))
	sb.WriteString(fmt.Sprintf("Valid From:        %s\n", cert.NotBefore.Format("2006-01-02 15:04:05 MST")))
	sb.WriteString(fmt.Sprintf("Valid Until:       %s\n", cert.NotAfter.Format("2006-01-02 15:04:05 MST")))

	if now.After(cert.NotAfter) {
		sb.WriteString("Expiry Status:     EXPIRED!\n")
	} else if daysRemaining <= 14 {
		sb.WriteString(fmt.Sprintf("Expiry Status:     EXPIRING SOON (%d days left)\n", daysRemaining))
	} else {
		sb.WriteString(fmt.Sprintf("Expiry Status:     Valid (%d days remaining)\n", daysRemaining))
	}

	if len(cert.DNSNames) > 0 {
		sb.WriteString(fmt.Sprintf("SANs (DNS Names):  %s\n", strings.Join(cert.DNSNames, ", ")))
	}
	if len(cert.IPAddresses) > 0 {
		var ips []string
		for _, ip := range cert.IPAddresses {
			ips = append(ips, ip.String())
		}
		sb.WriteString(fmt.Sprintf("SANs (IPs):        %s\n", strings.Join(ips, ", ")))
	}
	sb.WriteString(fmt.Sprintf("Serial Number:     %s\n", cert.SerialNumber.String()))

	if len(cs.PeerCertificates) > 1 {
		sb.WriteString(fmt.Sprintf("\n[Certificate Chain (%d certificates)]\n", len(cs.PeerCertificates)))
		for i, c := range cs.PeerCertificates {
			sb.WriteString(fmt.Sprintf("  #%d CN: %s (Issuer: %s)\n", i+1, c.Subject.CommonName, c.Issuer.CommonName))
		}
	}

	return Result{Output: sb.String()}, nil
}

func tlsVersionName(version uint16) string {
	switch version {
	case tls.VersionTLS13:
		return "TLS 1.3"
	case tls.VersionTLS12:
		return "TLS 1.2"
	case tls.VersionTLS11:
		return "TLS 1.1 (Deprecated/Insecure)"
	case tls.VersionTLS10:
		return "TLS 1.0 (Deprecated/Insecure)"
	default:
		return fmt.Sprintf("Unknown (0x%04x)", version)
	}
}

// httpHeadersAuditTool audits HTTP security headers for a target URL.
type httpHeadersAuditTool struct{}

func (t *httpHeadersAuditTool) Name() string { return "http_headers_audit" }
func (t *httpHeadersAuditTool) Aliases() []string {
	return []string{"security_headers", "audit_headers"}
}
func (t *httpHeadersAuditTool) Mutating() bool { return false }
func (t *httpHeadersAuditTool) Risk() Risk     { return RiskNetwork }
func (t *httpHeadersAuditTool) Label(a map[string]any) string {
	return "Auditing headers for " + Shorten(argString(a, "url"), 40)
}
func (t *httpHeadersAuditTool) DoneLabel(a map[string]any) string {
	return "Audited headers for " + Shorten(argString(a, "url"), 40)
}
func (t *httpHeadersAuditTool) Description() string {
	return "Audit HTTP response security headers (HSTS, CSP, X-Frame-Options, X-Content-Type-Options, Referrer-Policy, Permissions-Policy, CORS, and cookie flags) for a target URL."
}
func (t *httpHeadersAuditTool) Schema() map[string]any {
	return object(map[string]any{
		"url":              strProp("Target URL to audit (http:// or https://)."),
		"follow_redirects": boolProp("Follow HTTP redirects (default true)."),
		"timeout_seconds":  intProp("Request timeout in seconds, 1 to 30 (default 10)."),
	}, "url")
}

func (t *httpHeadersAuditTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
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

	timeoutSec := argInt(args, "timeout_seconds", 10, 1, 30)

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

	req, err := http.NewRequestWithContext(ctx, "GET", rawURL, nil)
	if err != nil {
		return Result{Output: fmt.Sprintf("failed to create request: %v", err), IsError: true}, nil
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (SecurityAudit; Termixgo/0.1.6)")

	resp, err := client.Do(req)
	if err != nil {
		return Result{Output: fmt.Sprintf("HTTP request failed: %v", err), IsError: true}, nil
	}
	defer resp.Body.Close()

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("HTTP Security Headers Audit: %s\n", rawURL))
	sb.WriteString(fmt.Sprintf("Status: %d %s | Protocol: %s\n", resp.StatusCode, resp.Status, resp.Proto))
	sb.WriteString(strings.Repeat("-", 60) + "\n")

	type checkResult struct {
		Name        string
		Status      string
		Value       string
		Explanation string
	}

	var passed []checkResult
	var warnings []checkResult
	var missing []checkResult

	// 1. Strict-Transport-Security (HSTS)
	if hsts := resp.Header.Get("Strict-Transport-Security"); hsts != "" {
		exp := "HSTS enabled"
		if !strings.Contains(hsts, "includeSubDomains") {
			exp += " (warning: includeSubDomains recommended)"
		}
		passed = append(passed, checkResult{"Strict-Transport-Security", "PRESENT", hsts, exp})
	} else if parsed.Scheme == "https" {
		missing = append(missing, checkResult{"Strict-Transport-Security", "MISSING", "", "Protects against SSL stripping and man-in-the-middle"})
	}

	// 2. Content-Security-Policy (CSP)
	if csp := resp.Header.Get("Content-Security-Policy"); csp != "" {
		exp := "CSP configured"
		if strings.Contains(csp, "'unsafe-inline'") || strings.Contains(csp, "'unsafe-eval'") {
			warnings = append(warnings, checkResult{"Content-Security-Policy", "WEAK", csp, "Contains 'unsafe-inline' or 'unsafe-eval'"})
		} else {
			passed = append(passed, checkResult{"Content-Security-Policy", "PRESENT", csp, exp})
		}
	} else {
		missing = append(missing, checkResult{"Content-Security-Policy", "MISSING", "", "Mitigates Cross-Site Scripting (XSS) and data injection"})
	}

	// 3. X-Frame-Options
	if xfo := resp.Header.Get("X-Frame-Options"); xfo != "" {
		passed = append(passed, checkResult{"X-Frame-Options", "PRESENT", xfo, "Protects against clickjacking attacks"})
	} else {
		missing = append(missing, checkResult{"X-Frame-Options", "MISSING", "", "Protects against clickjacking/framing"})
	}

	// 4. X-Content-Type-Options
	if xcto := resp.Header.Get("X-Content-Type-Options"); xcto != "" {
		if strings.EqualFold(xcto, "nosniff") {
			passed = append(passed, checkResult{"X-Content-Type-Options", "PRESENT", xcto, "Prevents MIME-sniffing"})
		} else {
			warnings = append(warnings, checkResult{"X-Content-Type-Options", "WEAK", xcto, "Value should be 'nosniff'"})
		}
	} else {
		missing = append(missing, checkResult{"X-Content-Type-Options", "MISSING", "", "Prevents browser MIME-type sniffing"})
	}

	// 5. Referrer-Policy
	if rp := resp.Header.Get("Referrer-Policy"); rp != "" {
		passed = append(passed, checkResult{"Referrer-Policy", "PRESENT", rp, "Controls referrer information sent in requests"})
	} else {
		missing = append(missing, checkResult{"Referrer-Policy", "MISSING", "", "Controls referrer information disclosure"})
	}

	// 6. Permissions-Policy
	if pp := resp.Header.Get("Permissions-Policy"); pp != "" {
		passed = append(passed, checkResult{"Permissions-Policy", "PRESENT", pp, "Restricts browser features & APIs"})
	} else {
		missing = append(missing, checkResult{"Permissions-Policy", "MISSING", "", "Restricts browser APIs (camera, microphone, geolocation)"})
	}

	// Information disclosure headers
	var leakHeaders []string
	for _, leak := range []string{"Server", "X-Powered-By", "X-AspNet-Version", "X-Runtime"} {
		if val := resp.Header.Get(leak); val != "" {
			leakHeaders = append(leakHeaders, fmt.Sprintf("%s: %s", leak, val))
		}
	}

	// Cookie analysis
	cookies := resp.Cookies()
	var insecureCookies []string
	for _, c := range cookies {
		var issues []string
		if !c.Secure && parsed.Scheme == "https" {
			issues = append(issues, "Missing Secure")
		}
		if !c.HttpOnly {
			issues = append(issues, "Missing HttpOnly")
		}
		if c.SameSite == http.SameSiteDefaultMode || c.SameSite == http.SameSiteNoneMode {
			issues = append(issues, "Lax/Strict SameSite recommended")
		}
		if len(issues) > 0 {
			insecureCookies = append(insecureCookies, fmt.Sprintf("%s (%s)", c.Name, strings.Join(issues, ", ")))
		}
	}

	sb.WriteString("[PASSED SECURITY HEADERS]\n")
	if len(passed) == 0 {
		sb.WriteString("  None\n")
	}
	for _, p := range passed {
		sb.WriteString(fmt.Sprintf("  + %-26s %s\n", p.Name+":", Shorten(p.Value, 60)))
	}

	if len(warnings) > 0 {
		sb.WriteString("\n[WARNINGS / PARTIAL PROTECTION]\n")
		for _, w := range warnings {
			sb.WriteString(fmt.Sprintf("  ! %-26s %s (%s)\n", w.Name+":", Shorten(w.Value, 50), w.Explanation))
		}
	}

	sb.WriteString("\n[MISSING CRITICAL HEADERS]\n")
	if len(missing) == 0 {
		sb.WriteString("  None (Great security header posture!)\n")
	}
	for _, m := range missing {
		sb.WriteString(fmt.Sprintf("  - %-26s %s\n", m.Name+":", m.Explanation))
	}

	if len(leakHeaders) > 0 {
		sb.WriteString("\n[INFO DISCLOSURE HEADERS DETECTED]\n")
		for _, l := range leakHeaders {
			sb.WriteString(fmt.Sprintf("  ! %s\n", l))
		}
	}

	if len(cookies) > 0 {
		sb.WriteString(fmt.Sprintf("\n[COOKIES AUDITED (%d set)]\n", len(cookies)))
		if len(insecureCookies) > 0 {
			for _, ic := range insecureCookies {
				sb.WriteString(fmt.Sprintf("  ! %s\n", ic))
			}
		} else {
			sb.WriteString("  + All cookies have secure flags (Secure, HttpOnly, SameSite).\n")
		}
	}

	return Result{Output: sb.String()}, nil
}

// dnsReconTool queries DNS records for domain reconnaissance.
type dnsReconTool struct{}

func (t *dnsReconTool) Name() string      { return "dns_recon" }
func (t *dnsReconTool) Aliases() []string { return []string{"dns_lookup", "domain_recon"} }
func (t *dnsReconTool) Mutating() bool    { return false }
func (t *dnsReconTool) Risk() Risk        { return RiskNetwork }
func (t *dnsReconTool) Label(a map[string]any) string {
	return "DNS Recon " + Shorten(argString(a, "domain"), 40)
}
func (t *dnsReconTool) DoneLabel(a map[string]any) string {
	return "DNS Recon completed for " + Shorten(argString(a, "domain"), 40)
}
func (t *dnsReconTool) Description() string {
	return "Query DNS records (A, AAAA, CNAME, MX, TXT, SPF, DMARC, NS) for an in-scope target domain."
}
func (t *dnsReconTool) Schema() map[string]any {
	return object(map[string]any{
		"domain":          strProp("Target domain name (e.g. example.com)."),
		"timeout_seconds": intProp("DNS lookup timeout in seconds, 1 to 15 (default 5)."),
	}, "domain")
}

func (t *dnsReconTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	domain := strings.TrimSpace(argString(args, "domain"))
	if domain == "" {
		return Result{Output: "domain is required.", IsError: true}, nil
	}

	domain = strings.TrimPrefix(domain, "https://")
	domain = strings.TrimPrefix(domain, "http://")
	if slash := strings.IndexByte(domain, '/'); slash != -1 {
		domain = domain[:slash]
	}
	if host, _, err := net.SplitHostPort(domain); err == nil {
		domain = host
	}
	domain = strings.Trim(domain, ".")

	if isBlockedHost(domain) {
		return Result{Output: fmt.Sprintf("domain %q is a blocked address", domain), IsError: true}, nil
	}

	timeoutSec := argInt(args, "timeout_seconds", 5, 1, 15)

	lookupCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
	defer cancel()

	resolver := net.DefaultResolver

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("DNS Reconnaissance Report: %s\n", domain))
	sb.WriteString(strings.Repeat("-", 50) + "\n")

	// A and AAAA records
	ips, err := resolver.LookupIP(lookupCtx, "ip", domain)
	if err == nil && len(ips) > 0 {
		var v4s []string
		var v6s []string
		for _, ip := range ips {
			if ip.To4() != nil {
				v4s = append(v4s, ip.String())
			} else {
				v6s = append(v6s, ip.String())
			}
		}
		if len(v4s) > 0 {
			sb.WriteString(fmt.Sprintf("A Records:     %s\n", strings.Join(v4s, ", ")))
		}
		if len(v6s) > 0 {
			sb.WriteString(fmt.Sprintf("AAAA Records:  %s\n", strings.Join(v6s, ", ")))
		}
	} else if err != nil {
		sb.WriteString(fmt.Sprintf("IP Lookup:     %v\n", err))
	}

	// CNAME record
	cname, err := resolver.LookupCNAME(lookupCtx, domain)
	if err == nil && strings.Trim(cname, ".") != domain {
		sb.WriteString(fmt.Sprintf("CNAME:         %s\n", strings.Trim(cname, ".")))
	}

	// NS records
	nsList, err := resolver.LookupNS(lookupCtx, domain)
	if err == nil && len(nsList) > 0 {
		var nsNames []string
		for _, ns := range nsList {
			nsNames = append(nsNames, strings.Trim(ns.Host, "."))
		}
		sb.WriteString(fmt.Sprintf("NS Records:    %s\n", strings.Join(nsNames, ", ")))
	}

	// MX records
	mxList, err := resolver.LookupMX(lookupCtx, domain)
	if err == nil && len(mxList) > 0 {
		sort.Slice(mxList, func(i, j int) bool { return mxList[i].Pref < mxList[j].Pref })
		var mxEntries []string
		for _, mx := range mxList {
			mxEntries = append(mxEntries, fmt.Sprintf("%s (pri:%d)", strings.Trim(mx.Host, "."), mx.Pref))
		}
		sb.WriteString(fmt.Sprintf("MX Records:    %s\n", strings.Join(mxEntries, ", ")))
	}

	// TXT records
	txtList, err := resolver.LookupTXT(lookupCtx, domain)
	if err == nil && len(txtList) > 0 {
		sb.WriteString("\n[TXT Records]\n")
		for _, txt := range txtList {
			if strings.HasPrefix(txt, "v=spf1") {
				sb.WriteString(fmt.Sprintf("  SPF:   %s\n", txt))
			} else {
				sb.WriteString(fmt.Sprintf("  TXT:   %s\n", Shorten(txt, 80)))
			}
		}
	}

	// DMARC record (_dmarc.domain)
	dmarcDomain := "_dmarc." + domain
	dmarcList, err := resolver.LookupTXT(lookupCtx, dmarcDomain)
	if err == nil && len(dmarcList) > 0 {
		sb.WriteString("\n[DMARC Record]\n")
		for _, dmarc := range dmarcList {
			sb.WriteString(fmt.Sprintf("  %s\n", dmarc))
		}
	} else {
		sb.WriteString("\n[DMARC Record]\n  Not found (_dmarc." + domain + " has no TXT record)\n")
	}

	return Result{Output: sb.String()}, nil
}

// portProbeTool tests TCP port connectivity with bounds and timeouts.
type portProbeTool struct{}

func (t *portProbeTool) Name() string      { return "port_probe" }
func (t *portProbeTool) Aliases() []string { return []string{"tcp_probe", "check_port"} }
func (t *portProbeTool) Mutating() bool    { return false }
func (t *portProbeTool) Risk() Risk        { return RiskNetwork }
func (t *portProbeTool) Label(a map[string]any) string {
	return "Probing ports on " + Shorten(argString(a, "host"), 40)
}
func (t *portProbeTool) DoneLabel(a map[string]any) string {
	return "Probed ports on " + Shorten(argString(a, "host"), 40)
}
func (t *portProbeTool) Description() string {
	return "Probe specific TCP ports on a host to verify reachability and open services. Safe, non-invasive, bounded probe (max 25 ports)."
}
func (t *portProbeTool) Schema() map[string]any {
	return object(map[string]any{
		"host":       strProp("Hostname or IP address to probe."),
		"ports":      arrayProp("List of TCP ports to test (e.g. [80, 443, 22, 3306], max 25 ports).", intProp("TCP port number")),
		"timeout_ms": intProp("Per-port connection timeout in milliseconds, 200 to 5000 (default 1500)."),
	}, "host", "ports")
}

var commonPortServices = map[int]string{
	21:    "FTP",
	22:    "SSH",
	23:    "Telnet",
	25:    "SMTP",
	53:    "DNS",
	80:    "HTTP",
	110:   "POP3",
	143:   "IMAP",
	443:   "HTTPS",
	465:   "SMTPS",
	587:   "SMTP Submission",
	993:   "IMAPS",
	995:   "POP3S",
	1433:  "MSSQL",
	1521:  "Oracle",
	3000:  "Dev Web (Node/React)",
	3306:  "MySQL",
	5432:  "PostgreSQL",
	6379:  "Redis",
	8000:  "HTTP Alt",
	8080:  "HTTP Proxy/Dev",
	8443:  "HTTPS Alt",
	8888:  "HTTP Alt",
	9200:  "Elasticsearch",
	27017: "MongoDB",
}

func (t *portProbeTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	rawHost := strings.TrimSpace(argString(args, "host"))
	if rawHost == "" {
		return Result{Output: "host is required.", IsError: true}, nil
	}
	rawHost = strings.TrimPrefix(rawHost, "https://")
	rawHost = strings.TrimPrefix(rawHost, "http://")
	if slash := strings.IndexByte(rawHost, '/'); slash != -1 {
		rawHost = rawHost[:slash]
	}
	if host, _, err := net.SplitHostPort(rawHost); err == nil {
		rawHost = host
	}

	if isBlockedHost(rawHost) {
		return Result{Output: fmt.Sprintf("host %q is a blocked address", rawHost), IsError: true}, nil
	}

	// Extract ports
	var ports []int
	if rawPorts, ok := args["ports"].([]any); ok {
		for _, p := range rawPorts {
			switch v := p.(type) {
			case float64:
				if int(v) > 0 && int(v) <= 65535 {
					ports = append(ports, int(v))
				}
			case int:
				if v > 0 && v <= 65535 {
					ports = append(ports, v)
				}
			}
		}
	} else if rawPortsStr, ok := args["ports"].(string); ok {
		// Tolerant fallback for comma-separated ports
		for _, pStr := range strings.Split(rawPortsStr, ",") {
			var p int
			if _, err := fmt.Sscanf(strings.TrimSpace(pStr), "%d", &p); err == nil && p > 0 && p <= 65535 {
				ports = append(ports, p)
			}
		}
	}

	if len(ports) == 0 {
		return Result{Output: "ports must contain at least one valid TCP port number (1-65535).", IsError: true}, nil
	}

	if len(ports) > 25 {
		ports = ports[:25]
	}

	timeoutMs := argInt(args, "timeout_ms", 1500, 200, 5000)
	timeout := time.Duration(timeoutMs) * time.Millisecond

	type probeResult struct {
		Port    int
		Service string
		Status  string
		Latency time.Duration
		Err     string
	}

	results := make([]probeResult, len(ports))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 5) // max 5 concurrent dials

	for i, port := range ports {
		wg.Add(1)
		go func(idx int, p int) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				results[idx] = probeResult{Port: p, Status: "cancelled"}
				return
			}

			svc := commonPortServices[p]
			if svc == "" {
				svc = "Unknown"
			}

			start := time.Now()
			target := net.JoinHostPort(rawHost, fmt.Sprintf("%d", p))
			conn, err := (&net.Dialer{Timeout: timeout}).DialContext(ctx, "tcp", target)
			latency := time.Since(start)

			if err != nil {
				status := "closed"
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					status = "filtered/timeout"
				}
				results[idx] = probeResult{
					Port:    p,
					Service: svc,
					Status:  status,
					Latency: latency,
					Err:     err.Error(),
				}
			} else {
				_ = conn.Close()
				results[idx] = probeResult{
					Port:    p,
					Service: svc,
					Status:  "open",
					Latency: latency,
				}
			}
		}(i, port)
	}

	wg.Wait()

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Port Probe Report: %s (Timeout: %s)\n", rawHost, timeout))
	sb.WriteString(strings.Repeat("-", 50) + "\n")
	sb.WriteString(fmt.Sprintf("%-7s %-16s %-16s %s\n", "PORT", "SERVICE", "STATE", "LATENCY"))
	sb.WriteString(strings.Repeat("-", 50) + "\n")

	for _, res := range results {
		sb.WriteString(fmt.Sprintf("%-7d %-16s %-16s %s\n",
			res.Port, res.Service, res.Status, res.Latency.Round(time.Millisecond)))
	}

	return Result{Output: sb.String()}, nil
}
