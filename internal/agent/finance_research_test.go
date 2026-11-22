package agent

import (
	"context"
	"fmt"
	"math"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runTool(t *testing.T, tool Tool, env *Env, args map[string]any) Result {
	t.Helper()
	result, err := tool.Run(context.Background(), env, args)
	if err != nil {
		t.Fatalf("%s returned an error: %v", tool.Name(), err)
	}
	return result
}

func workspaceEnv(t *testing.T, files map[string]string) *Env {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return &Env{Workspace: root}
}

func TestParseAmountReadsBothNumberStyles(t *testing.T) {
	cases := []struct {
		in        string
		want      string
		ambiguous bool
	}{
		{"1.234.567,89", "1234567.89", false},
		{"1,234,567.89", "1234567.89", false},
		{"Rp 1.500.000", "1500000", false},
		{"(1.000,50)", "-1000.5", false},
		{"2.500-", "-2500", true},
		{"12.5", "12.5", false},
		{"12,5", "12.5", false},
		{"1.234", "1234", true},
		{"-0,75", "-0.75", false},
		{"USD 99.90", "99.9", false},
	}
	for _, c := range cases {
		got, ok := parseAmount(c.in, "auto")
		if !ok {
			t.Errorf("%q should parse", c.in)
			continue
		}
		want, _ := new(big.Rat).SetString(c.want)
		if got.value.Cmp(want) != 0 || got.ambiguous != c.ambiguous {
			t.Errorf("%q = %s ambiguous=%v, want %s ambiguous=%v", c.in, got.value.FloatString(2), got.ambiguous, c.want, c.ambiguous)
		}
	}
	for _, bad := range []string{"", "INV-001", "12.34.56", "abc", "1.2.3,4,5"} {
		if _, ok := parseAmount(bad, "auto"); ok {
			t.Errorf("%q must not parse as an amount", bad)
		}
	}
	if got, _ := parseAmount("1.234", "dot"); got.value.Cmp(big.NewRat(1234, 1000)) != 0 {
		t.Error("decimal=dot settles 1.234 as a decimal")
	}
}

func TestRoundingModes(t *testing.T) {
	cases := []struct {
		value, mode string
		scale       int
		want        string
	}{
		{"2.5", "half_up", 0, "3"},
		{"2.5", "half_even", 0, "2"},
		{"3.5", "half_even", 0, "4"},
		{"0.125", "half_up", 2, "0.13"},
		{"0.125", "half_even", 2, "0.12"},
		{"-2.5", "half_up", 0, "-3"},
		{"-2.5", "down", 0, "-2"},
		{"2.01", "up", 1, "2.1"},
	}
	for _, c := range cases {
		value, _ := new(big.Rat).SetString(c.value)
		if got := roundRat(value, c.scale, c.mode).FloatString(c.scale); got != c.want {
			t.Errorf("%s %s to %d = %s, want %s", c.value, c.mode, c.scale, got, c.want)
		}
	}
}

func TestCalcIsExactAndNamesItsRounding(t *testing.T) {
	calc := &calcTool{}
	cases := map[string]string{
		"(1250000 * 11%) + 5000": "142,500.00",
		"0.1 + 0.2":              "0.30",
		"round(100 / 3, 2)":      "33.33",
		"2 ^ 10":                 "1,024.00",
		"-(3 + 4) * 2":           "-14.00",
		"sum(1.1, 2.2, 3.3)":     "6.60",
		"avg(1, 2)":              "1.50",
		"1000 * 15% * 2":         "300.00",
	}
	for expression, want := range cases {
		result := runTool(t, calc, nil, map[string]any{"expression": expression})
		if result.IsError || !strings.Contains(result.Output, "= "+want+"\n") {
			t.Errorf("%s: want %s, got %q", expression, want, result.Output)
		}
	}
	if r := runTool(t, calc, nil, map[string]any{"expression": "1 / 0"}); !r.IsError {
		t.Error("division by zero is an error")
	}
	if r := runTool(t, calc, nil, map[string]any{"expression": "pph21 * 2"}); !r.IsError || !strings.Contains(r.Output, "unknown name") {
		t.Errorf("an unknown name is refused, not guessed: %q", r.Output)
	}
	if r := runTool(t, calc, nil, map[string]any{"expression": "10 / 3", "scale": 4}); !strings.Contains(r.Output, "3.3333") {
		t.Errorf("scale applies: %q", r.Output)
	}
	if r := runTool(t, calc, nil, map[string]any{"expression": "2.5", "scale": 0, "rounding": "half_even"}); !strings.Contains(r.Output, "= 2\n") {
		t.Errorf("scale 0 and half_even: %q", r.Output)
	}
}

func TestCalcOverAColumnWarnsAboutATotalRow(t *testing.T) {
	env := workspaceEnv(t, map[string]string{"ledger.csv": "Date,Amount\n1,100.50\n2,200.25\n3,\"1.000,00\"\n4,n/a\n5,1300.75\nTotal,2.601,50\n"})
	// The last two rows differ from the header width on purpose: a short row
	// must not break the read.
	result := runTool(t, &calcTool{}, env, map[string]any{"file": "ledger.csv", "column": "Amount", "decimal": "dot"})
	if result.IsError {
		t.Fatal(result.Output)
	}
	if !strings.Contains(result.Output, "sum of column") || !strings.Contains(result.Output, "1 non-empty cell(s) were not numbers") {
		t.Errorf("output = %q", result.Output)
	}

	totalRow := workspaceEnv(t, map[string]string{"t.csv": "Item,Amount\na,10\nb,20\nc,30\nTotal,60\n"})
	got := runTool(t, &calcTool{}, totalRow, map[string]any{"file": "t.csv", "column": "Amount"})
	if !strings.Contains(got.Output, "= 120.00") || !strings.Contains(got.Output, "counted twice") {
		t.Errorf("a total row counted twice must be flagged: %q", got.Output)
	}
}

func TestCalcRefusesFilesOutsideTheWorkspace(t *testing.T) {
	env := workspaceEnv(t, nil)
	outside := filepath.Join(t.TempDir(), "x.csv")
	if err := os.WriteFile(outside, []byte("A\n1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if r := runTool(t, &calcTool{}, env, map[string]any{"file": outside, "column": "A"}); !r.IsError {
		t.Fatal("a file outside the workspace must be refused")
	}
}

func TestReconcileByKey(t *testing.T) {
	env := workspaceEnv(t, map[string]string{
		"faktur.csv": "No,Amount\nINV-001,1000\nINV-002,2000\nINV-003,3000\nINV-004,500\nINV-004,500\nINV-006,60\n",
		"gl.csv":     "Ref,Value\ninv001,1000\nINV002,2050\nINV-003,3000.005\nINV-004,1000\nINV-005,777\n",
	})
	result := runTool(t, &reconcileTool{}, env, map[string]any{
		"left_file": "faktur.csv", "left_key": "No", "left_amount": "Amount",
		"right_file": "gl.csv", "right_key": "Ref", "right_amount": "Value",
		"tolerance": "0.01", "decimal": "dot",
	})
	if result.IsError {
		t.Fatal(result.Output)
	}
	out := result.Output
	for _, want := range []string{
		"Matched: 3",
		"Amount differs: 1",
		"INV-002: left 2,000.00 (row 3) vs right 2,050.00 (row 3), difference -50.00",
		"Only on the left: 1",
		"INV-006: 60.00 (row 7)",
		"Only on the right: 1",
		"INV-005: 777.00",
		"Duplicate keys on the left",
		"INV-004: 1,000.00 (row 5,6)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestReconcileByAmountPairsOneToOne(t *testing.T) {
	env := workspaceEnv(t, map[string]string{
		"bank.csv": "Amount\n100\n100\n250\n999\n",
		"book.csv": "Amount\n100\n250.4\n7\n",
	})
	result := runTool(t, &reconcileTool{}, env, map[string]any{
		"left_file": "bank.csv", "left_amount": "Amount",
		"right_file": "book.csv", "right_amount": "Amount",
		"tolerance": "0.5", "decimal": "dot",
	})
	if !strings.Contains(result.Output, "Matched: 2") || !strings.Contains(result.Output, "Only on the left: 2") || !strings.Contains(result.Output, "Only on the right: 1") {
		t.Errorf("one bank 100 must stay unmatched, not match twice:\n%s", result.Output)
	}
	if r := runTool(t, &reconcileTool{}, env, map[string]any{"left_file": "bank.csv", "left_amount": "Amount", "left_key": "Amount", "right_file": "book.csv", "right_amount": "Amount"}); !r.IsError {
		t.Error("a key on one side only is refused")
	}
}

func TestBenfordSeparatesNaturalFromUniformData(t *testing.T) {
	var natural, uniform []string
	// Exactly three decades in 1500 steps, so every leading digit gets the
	// share Benford predicts.
	value := 1000.0
	step := math.Pow(10, 1.0/500)
	for i := 0; i < 1500; i++ {
		natural = append(natural, fmt.Sprintf("%.2f", value))
		value *= step
	}
	for i := 100; i < 1000; i++ {
		uniform = append(uniform, fmt.Sprintf("%d", i))
	}
	good := runTool(t, &benfordTool{}, nil, map[string]any{"values": natural, "decimal": "dot"})
	if !strings.Contains(good.Output, "close conformity") {
		t.Errorf("a growth series follows Benford:\n%s", good.Output)
	}
	bad := runTool(t, &benfordTool{}, nil, map[string]any{"values": uniform, "decimal": "dot"})
	if !strings.Contains(bad.Output, "nonconformity") || !strings.Contains(bad.Output, "significant at 1%") {
		t.Errorf("uniform 100..999 does not:\n%s", bad.Output)
	}
	small := runTool(t, &benfordTool{}, nil, map[string]any{"values": []string{"10", "20", "30", "1000", "2000"}, "threshold": "1000", "decimal": "dot"})
	if !strings.Contains(small.Output, "fewer than 30") || !strings.Contains(small.Output, "Round amounts") {
		t.Errorf("a small sample is called weak:\n%s", small.Output)
	}
	near := runTool(t, &benfordTool{}, nil, map[string]any{"values": []string{"960", "990", "999", "1000", "400", "960"}, "threshold": "1000", "decimal": "dot"})
	if !strings.Contains(near.Output, "within 5% below the threshold 1,000.00: 4") {
		t.Errorf("threshold hugging is counted:\n%s", near.Output)
	}
}

func TestEvidenceLogChainsAndDetectsChange(t *testing.T) {
	env := workspaceEnv(t, map[string]string{"gl.csv": "A\n1\n"})
	log := &evidenceLogTool{}
	first := runTool(t, log, env, map[string]any{"action": "add", "finding": "INV-002 is 50 over the ledger", "severity": "high", "source_file": "gl.csv", "location": "row 3", "amount": "50.00", "basis": "PMK 000/2099 Pasal 1, effective 2099-01-01"})
	if first.IsError || !strings.Contains(first.Output, "#1") || !strings.Contains(first.Output, "from gl.csv (sha256 ") {
		t.Fatalf("add: %q", first.Output)
	}
	second := runTool(t, log, env, map[string]any{"action": "add", "finding": "Round amounts", "severity": "low"})
	if !strings.Contains(second.Output, "No basis was given") {
		t.Errorf("a missing basis is pointed out: %q", second.Output)
	}
	if r := runTool(t, log, env, map[string]any{"action": "add", "finding": "x", "severity": "urgent"}); !r.IsError {
		t.Error("an unknown severity is refused")
	}
	if r := runTool(t, log, env, map[string]any{"action": "verify"}); r.IsError || !strings.Contains(r.Output, "intact") {
		t.Fatalf("verify: %q", r.Output)
	}
	if r := runTool(t, log, env, map[string]any{"action": "list", "severity_min": "high"}); !strings.Contains(r.Output, "INV-002") || strings.Contains(r.Output, "Round amounts") {
		t.Errorf("list filters by severity: %q", r.Output)
	}
	if r := runTool(t, log, env, map[string]any{"action": "export"}); !strings.Contains(r.Output, "| 1 | high | INV-002 is 50 over the ledger |") {
		t.Errorf("export: %q", r.Output)
	}

	if err := os.WriteFile(filepath.Join(env.Workspace, "gl.csv"), []byte("A\n2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if r := runTool(t, log, env, map[string]any{"action": "verify"}); !r.IsError || !strings.Contains(r.Output, "has changed since it was logged") {
		t.Errorf("a changed source is caught: %q", r.Output)
	}

	path := evidencePath(env)
	data, _ := os.ReadFile(path)
	tampered := strings.Replace(string(data), "50 over", "5 over", 1)
	if err := os.WriteFile(path, []byte(tampered), 0o644); err != nil {
		t.Fatal(err)
	}
	if r := runTool(t, log, env, map[string]any{"action": "verify"}); !r.IsError || !strings.Contains(r.Output, "was changed after it was logged") {
		t.Errorf("an edited entry is caught: %q", r.Output)
	}

	lines := strings.Split(strings.TrimSpace(tampered), "\n")
	if err := os.WriteFile(path, []byte(lines[1]+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if r := runTool(t, log, env, map[string]any{"action": "verify"}); !r.IsError {
		t.Errorf("a removed entry is caught: %q", r.Output)
	}
}

const articleHTML = `<!doctype html><html lang="id"><head>
<title>Fallback title</title>
<meta property="og:title" content="Aturan Baru &amp; Dampaknya">
<meta name="author" content="Rina Putri">
<meta property="article:published_time" content="2026-03-04T08:00:00+07:00">
<meta property="og:site_name" content="Kontan Contoh">
<link rel="canonical" href="/kanonik/aturan-baru">
<script type="application/ld+json">{"@type":"NewsArticle","dateModified":"2026-03-05T09:00:00+07:00"}</script>
<script>var tracking = "do not read me";</script>
</head><body>
<nav>Home | Menu | Login</nav>
<article><h1>Aturan Baru</h1>
<p>` + "Paragraf pertama menjelaskan isi aturan secara rinci dan panjang. " + `</p>
<p>` + "Paragraf kedua memuat contoh perhitungan dan batas waktu yang berlaku. " + `</p>
<p>` + "Paragraf ketiga menutup dengan rujukan ke <a href=\"/aturan/pmk-1\">PMK terkait</a> dan <a href=\"#top\">atas</a>. " + `</p>
<p>` + "Tambahan kalimat supaya isi artikel melewati batas empat ratus karakter agar dipilih sebagai bagian utama halaman ini. " + `</p>
<p>` + "Sekali lagi kalimat tambahan untuk memastikan panjangnya cukup dan konsisten dalam pengujian otomatis ini. " + `</p>
</article>
<aside>Berita lain yang tidak relevan</aside>
<footer>Hak cipta, kebijakan privasi</footer>
</body></html>`

func articleServer(t *testing.T) *httptest.Server {
	t.Helper()
	t.Setenv("TERMIXGO_ALLOW_PRIVATE_FETCH", "1")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/data.json" {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"a":1}`)
			return
		}
		if r.URL.Path == "/blocked" {
			http.Error(w, "no", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, articleHTML)
	}))
	t.Cleanup(server.Close)
	return server
}

func TestWebExtractReturnsMetadataAndOnlyTheArticle(t *testing.T) {
	server := articleServer(t)
	result := runTool(t, &webExtractTool{}, nil, map[string]any{"url": server.URL + "/berita/aturan-baru", "links": true})
	if result.IsError {
		t.Fatal(result.Output)
	}
	out := result.Output
	for _, want := range []string{
		"Title: Aturan Baru & Dampaknya",
		"Author: Rina Putri",
		"Published: 2026-03-04T08:00:00+07:00",
		"Updated: 2026-03-05T09:00:00+07:00",
		"Site: Kontan Contoh",
		"Language: id",
		"Canonical: " + server.URL + "/kanonik/aturan-baru",
		"Paragraf kedua memuat contoh",
		"PMK terkait: " + server.URL + "/aturan/pmk-1",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	for _, unwanted := range []string{"Home | Menu", "do not read me", "Hak cipta", "Berita lain", "#top"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("%q is page furniture and must not appear:\n%s", unwanted, out)
		}
	}
	if r := runTool(t, &webExtractTool{}, nil, map[string]any{"url": server.URL + "/data.json"}); !r.IsError || !strings.Contains(r.Output, "not an HTML page") {
		t.Errorf("json is not an article: %q", r.Output)
	}
	if r := runTool(t, &webExtractTool{}, nil, map[string]any{"url": server.URL + "/blocked"}); !r.IsError {
		t.Error("an error status is reported")
	}
	if r := runTool(t, &webExtractTool{}, nil, map[string]any{"url": "http://169.254.169.254/latest"}); !r.IsError {
		t.Error("the metadata address is never fetched")
	}
}

func TestWebExtractRefusesPrivateHostsByDefault(t *testing.T) {
	t.Setenv("TERMIXGO_ALLOW_PRIVATE_FETCH", "")
	if r := runTool(t, &webExtractTool{}, nil, map[string]any{"url": "http://127.0.0.1:9/x"}); !r.IsError || !strings.Contains(r.Output, "private or loopback") {
		t.Errorf("got %q", r.Output)
	}
}

func TestCiteStoresFormatsAndDedupes(t *testing.T) {
	server := articleServer(t)
	env := workspaceEnv(t, nil)
	cite := &citeTool{}
	added := runTool(t, cite, env, map[string]any{"action": "add", "url": server.URL + "/berita/aturan-baru", "quote": "batas waktu yang berlaku"})
	if added.IsError || !strings.Contains(added.Output, "Added source [1]: Aturan Baru & Dampaknya") {
		t.Fatalf("add: %q", added.Output)
	}
	if again := runTool(t, cite, env, map[string]any{"action": "add", "url": server.URL + "/berita/aturan-baru"}); !strings.Contains(again.Output, "already holds") {
		t.Errorf("the same URL is not added twice: %q", again.Output)
	}
	runTool(t, cite, env, map[string]any{"action": "add", "title": "Laporan Tahunan 2025", "site": "Contoh Tbk", "published": "2026-01", "fetch": false})
	second := runTool(t, cite, env, map[string]any{"action": "add", "title": "Undated note", "fetch": false})
	if !strings.Contains(second.Output, "Not found in the source: author, publication date") {
		t.Errorf("missing fields are named, not invented: %q", second.Output)
	}

	apa := runTool(t, cite, env, map[string]any{"action": "export"}).Output
	if !strings.Contains(apa, "Rina Putri. (2026, March 4). Aturan Baru & Dampaknya. Kontan Contoh. "+server.URL+"/berita/aturan-baru (accessed ") {
		t.Errorf("apa:\n%s", apa)
	}
	if !strings.Contains(apa, "Laporan Tahunan 2025. (2026, January). Contoh Tbk.") || !strings.Contains(apa, "Undated note. (n.d.).") {
		t.Errorf("apa without an author or day:\n%s", apa)
	}
	bib := runTool(t, cite, env, map[string]any{"action": "export", "format": "bibtex"}).Output
	if !strings.Contains(bib, "@misc{putri2026_1,") || !strings.Contains(bib, `title = {Aturan Baru \& Dampaknya}`) {
		t.Errorf("bibtex:\n%s", bib)
	}
	md := runTool(t, cite, env, map[string]any{"action": "export", "format": "markdown"}).Output
	if !strings.Contains(md, "> batas waktu yang berlaku") {
		t.Errorf("markdown keeps the quote:\n%s", md)
	}
	if r := runTool(t, cite, env, map[string]any{"action": "remove", "id": 3}); r.IsError {
		t.Fatal(r.Output)
	}
	listed := runTool(t, cite, env, map[string]any{"action": "list"}).Output
	if strings.Contains(listed, "Undated note") || !strings.Contains(listed, "[2] Laporan Tahunan 2025") {
		t.Errorf("remove keeps the other numbers:\n%s", listed)
	}
	if r := runTool(t, cite, env, map[string]any{"action": "remove", "id": 99}); !r.IsError {
		t.Error("removing an unknown source is an error")
	}
}

func TestWaybackCheckListSave(t *testing.T) {
	var saved string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/wayback/available":
			if r.URL.Query().Get("url") == "https://93.184.216.34/none" {
				fmt.Fprint(w, `{"archived_snapshots":{}}`)
				return
			}
			fmt.Fprint(w, `{"archived_snapshots":{"closest":{"available":true,"url":"http://web.archive.org/web/20260101120000/https://93.184.216.34/page","timestamp":"20260101120000","status":"200"}}}`)
		case r.URL.Path == "/cdx/search/cdx":
			fmt.Fprint(w, `[["timestamp","statuscode","original"],["20250101000000","200","https://93.184.216.34/page"],["20260101120000","200","https://93.184.216.34/page"]]`)
		case strings.HasPrefix(r.URL.Path, "/save/"):
			saved = strings.TrimPrefix(r.URL.Path, "/save/")
			w.Header().Set("Content-Location", "/web/20261122101010/https://93.184.216.34/page")
			fmt.Fprint(w, "ok")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	oldAPI, oldWeb := waybackAPI, waybackWeb
	waybackAPI, waybackWeb = server.URL, server.URL
	defer func() { waybackAPI, waybackWeb = oldAPI, oldWeb }()

	tool := &waybackTool{}
	check := runTool(t, tool, nil, map[string]any{"url": "https://93.184.216.34/page"})
	if !strings.Contains(check.Output, "https://web.archive.org/web/20260101120000/") || !strings.Contains(check.Output, "2026-01-01 12:00 UTC") {
		t.Errorf("check: %q", check.Output)
	}
	if none := runTool(t, tool, nil, map[string]any{"url": "https://93.184.216.34/none"}); !strings.Contains(none.Output, "No snapshot") {
		t.Errorf("none: %q", none.Output)
	}
	list := runTool(t, tool, nil, map[string]any{"action": "list", "url": "https://93.184.216.34/page"})
	if !strings.Contains(list.Output, "2 most recent") || !strings.Contains(list.Output, "/web/20250101000000/https://93.184.216.34/page") {
		t.Errorf("list: %q", list.Output)
	}
	save := runTool(t, tool, nil, map[string]any{"action": "save", "url": "https://93.184.216.34/page"})
	if save.IsError || !strings.Contains(save.Output, "2026-11-22 10:10 UTC") || !strings.HasSuffix(save.Output, "/web/20261122101010/https://93.184.216.34/page") {
		t.Errorf("save: %q", save.Output)
	}
	if saved != "https://93.184.216.34/page" {
		t.Errorf("the archive was asked to save %q", saved)
	}
	if r := runTool(t, tool, nil, map[string]any{"action": "save", "url": "http://10.0.0.5/internal"}); !r.IsError {
		t.Error("a private address is never sent to a third party")
	}
}
