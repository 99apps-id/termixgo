package agent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// convertTimeout bounds one conversion. A cold Office or LibreOffice start
// takes several seconds; a large workbook takes longer.
const convertTimeout = 3 * time.Minute

// convertFormats are the targets the tool accepts.
var convertFormats = map[string]bool{
	"pdf": true, "docx": true, "xlsx": true, "pptx": true, "odt": true,
	"ods": true, "odp": true, "html": true, "txt": true, "csv": true, "rtf": true,
}

// officeApp names the Office program that opens an input file on Windows.
func officeApp(ext string) string {
	switch ext {
	case ".doc", ".docx", ".rtf", ".odt", ".txt", ".html", ".htm":
		return "word"
	case ".xls", ".xlsx", ".xlsm", ".csv", ".ods":
		return "excel"
	case ".ppt", ".pptx", ".odp":
		return "powerpoint"
	}
	return ""
}

// findLibreOffice locates soffice on PATH or in its usual install folder.
func findLibreOffice() string {
	for _, name := range []string{"soffice", "libreoffice"} {
		if path, err := exec.LookPath(name); err == nil {
			return path
		}
	}
	var candidates []string
	switch runtime.GOOS {
	case "windows":
		candidates = []string{`C:\Program Files\LibreOffice\program\soffice.exe`, `C:\Program Files (x86)\LibreOffice\program\soffice.exe`}
	case "darwin":
		candidates = []string{"/Applications/LibreOffice.app/Contents/MacOS/soffice"}
	}
	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return ""
}

// officeAvailable reports whether Microsoft Office answers COM on Windows.
var officeAvailable = func(app string) bool {
	if runtime.GOOS != "windows" {
		return false
	}
	progID := map[string]string{"word": "Word.Application", "excel": "Excel.Application", "powerpoint": "PowerPoint.Application"}[app]
	if progID == "" {
		return false
	}
	return exec.Command("reg", "query", `HKCR\`+progID).Run() == nil
}

type convertDocumentTool struct{}

func (t *convertDocumentTool) Name() string { return "convert_document" }
func (t *convertDocumentTool) Aliases() []string {
	return []string{"docx_to_pdf", "office_to_pdf", "export_pdf", "convert_office"}
}
func (t *convertDocumentTool) Mutating() bool { return true }
func (t *convertDocumentTool) Risk() Risk     { return RiskEdit }
func (t *convertDocumentTool) Label(a map[string]any) string {
	return fmt.Sprintf("Converting %s to %s", Shorten(argString(a, "path"), 40), strings.ToUpper(argString(a, "format")))
}
func (t *convertDocumentTool) DoneLabel(a map[string]any) string {
	return fmt.Sprintf("Converted %s to %s", Shorten(argString(a, "path"), 40), strings.ToUpper(argString(a, "format")))
}
func (t *convertDocumentTool) Description() string {
	return "Convert an office document to another format with full layout fidelity: Word, Excel or PowerPoint to PDF, legacy .doc/.xls/.ppt to the modern format, OpenDocument to Office, a spreadsheet to CSV. Uses LibreOffice when installed, otherwise Microsoft Office on Windows. Never overwrites an existing file."
}
func (t *convertDocumentTool) Schema() map[string]any {
	return object(map[string]any{
		"path":   strProp("The document to convert (.docx, .doc, .xlsx, .xls, .csv, .pptx, .ppt, .odt, .ods, .odp, .rtf, .html)."),
		"format": strProp("Target format: pdf, docx, xlsx, pptx, odt, ods, odp, html, txt, csv or rtf."),
		"output": strProp("Optional output path in the workspace. Defaults to the input name with the new extension."),
	}, "path", "format")
}

func (t *convertDocumentTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	input := resolvePath(env, argString(args, "path", "file"))
	if input == "" {
		return Result{Output: "path is required", IsError: true}, nil
	}
	if err := checkWorkspacePath(env, input); err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	if info, err := os.Stat(input); err != nil || info.IsDir() {
		return Result{Output: fmt.Sprintf("%s is not a file", displayPath(env, input)), IsError: true}, nil
	}
	format := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(argString(args, "format", "to")), "."))
	if !convertFormats[format] {
		return Result{Output: "format must be one of pdf, docx, xlsx, pptx, odt, ods, odp, html, txt, csv, rtf", IsError: true}, nil
	}
	output := strings.TrimSpace(argString(args, "output"))
	if output == "" {
		output = strings.TrimSuffix(input, filepath.Ext(input)) + "." + format
	} else {
		output = resolvePath(env, output)
	}
	if err := checkWorkspacePath(env, output); err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	if strings.EqualFold(filepath.Clean(output), filepath.Clean(input)) {
		return Result{Output: "the output would replace the input; choose another format or output path", IsError: true}, nil
	}
	if _, err := os.Stat(output); err == nil {
		return Result{Output: fmt.Sprintf("%s already exists; pass another output path", displayPath(env, output)), IsError: true}, nil
	}
	if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}

	ctx, cancel := context.WithTimeout(ctx, convertTimeout)
	defer cancel()
	var used string
	var err error
	if soffice := findLibreOffice(); soffice != "" {
		used = "LibreOffice"
		err = convertWithLibreOffice(ctx, soffice, input, output, format)
	} else if app := officeApp(strings.ToLower(filepath.Ext(input))); app != "" && officeAvailable(app) {
		used = "Microsoft " + strings.ToUpper(app[:1]) + app[1:]
		err = convertWithOffice(ctx, app, input, output, format)
	} else {
		return Result{Output: "no converter found: install LibreOffice (soffice) or, on Windows, Microsoft Office. read_document and create_office_document still work without one.", IsError: true}, nil
	}
	if err != nil {
		return Result{Output: fmt.Sprintf("%s could not convert %s: %v", used, displayPath(env, input), err), IsError: true}, nil
	}
	info, statErr := os.Stat(output)
	if statErr != nil || info.Size() == 0 {
		return Result{Output: fmt.Sprintf("%s reported success but wrote no %s file", used, format), IsError: true}, nil
	}
	return Result{Output: fmt.Sprintf("Converted with %s: %s (%d KB)", used, displayPath(env, output), (info.Size()+1023)/1024)}, nil
}

// convertWithLibreOffice runs a headless conversion with a private profile,
// so it neither collides with an open LibreOffice window nor leaves state.
func convertWithLibreOffice(ctx context.Context, soffice, input, output, format string) error {
	dir, err := os.MkdirTemp("", "termixgo-convert-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	profile := "file:///" + strings.TrimPrefix(filepath.ToSlash(filepath.Join(dir, "profile")), "/")
	target := format
	if format == "txt" {
		target = "txt:Text"
	}
	cmd := exec.CommandContext(ctx, soffice, "--headless", "--norestore", "--nolockcheck",
		"-env:UserInstallation="+profile, "--convert-to", target, "--outdir", dir, input)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	produced := filepath.Join(dir, strings.TrimSuffix(filepath.Base(input), filepath.Ext(input))+"."+format)
	data, err := os.ReadFile(produced)
	if err != nil {
		return fmt.Errorf("no output file was produced")
	}
	return os.WriteFile(output, data, 0o644)
}

// officeScript drives Word, Excel or PowerPoint over COM. Paths arrive as
// parameters, never spliced into the script, and every program is closed in
// a finally block so a failed conversion leaves no Office process behind.
const officeScript = `param([string]$App, [string]$In, [string]$Out, [string]$Format)
$ErrorActionPreference = 'Stop'
$office = $null
$doc = $null
$book = $null
$deck = $null
$excelPid = 0
try {
  switch ($App) {
    'word' {
      $codes = @{ pdf = 17; docx = 16; html = 8; txt = 2; rtf = 6; odt = 23 }
      if (-not $codes.ContainsKey($Format)) { throw "Word cannot save $Format" }
      $office = New-Object -ComObject Word.Application
      $office.Visible = $false
      $office.DisplayAlerts = 0
      $doc = $office.Documents.Open($In, $false, $true)
      $doc.SaveAs2($Out, $codes[$Format])
      $doc.Close($false)
    }
    'excel' {
      $office = New-Object -ComObject Excel.Application
      $office.Visible = $false
      $office.DisplayAlerts = $false
      # Excel can outlive Quit while PowerShell still holds an implicit
      # collection reference. The process id of this instance, and only this
      # one, is read from its window so it can be ended afterwards.
      Add-Type -Namespace TermixgoOffice -Name Win -MemberDefinition '[DllImport("user32.dll")] public static extern int GetWindowThreadProcessId(System.IntPtr hWnd, out int processId);'
      $excelPid = 0
      [void][TermixgoOffice.Win]::GetWindowThreadProcessId([System.IntPtr]$office.Hwnd, [ref]$excelPid)
      $book = $office.Workbooks.Open($In, 0, $true)
      if ($Format -eq 'pdf') { $book.ExportAsFixedFormat(0, $Out) }
      else {
        $codes = @{ xlsx = 51; csv = 6; ods = 60; html = 44; txt = 42 }
        if (-not $codes.ContainsKey($Format)) { throw "Excel cannot save $Format" }
        $book.SaveAs($Out, $codes[$Format])
      }
      $book.Close($false)
    }
    'powerpoint' {
      $codes = @{ pdf = 32; pptx = 24; odp = 35 }
      if (-not $codes.ContainsKey($Format)) { throw "PowerPoint cannot save $Format" }
      $office = New-Object -ComObject PowerPoint.Application
      $deck = $office.Presentations.Open($In, -1, 0, 0)
      $deck.SaveAs($Out, $codes[$Format])
      $deck.Close()
    }
  }
} finally {
  # Every COM reference has to be released before Quit takes effect; a held
  # workbook keeps a hidden Excel alive after the script ends.
  foreach ($item in @($doc, $book, $deck)) {
    if ($item -ne $null) {
      try { [void][System.Runtime.InteropServices.Marshal]::FinalReleaseComObject($item) } catch {}
    }
  }
  if ($office -ne $null) {
    try { $office.Quit() } catch {}
    try { [void][System.Runtime.InteropServices.Marshal]::FinalReleaseComObject($office) } catch {}
  }
  $doc = $null; $book = $null; $deck = $null; $office = $null
  [GC]::Collect()
  [GC]::WaitForPendingFinalizers()
  if ($excelPid -ne 0) {
    $proc = Get-Process -Id $excelPid -ErrorAction SilentlyContinue
    if ($proc -ne $null -and -not $proc.WaitForExit(2000)) {
      Stop-Process -Id $excelPid -Force -ErrorAction SilentlyContinue
    }
  }
}
`

func convertWithOffice(ctx context.Context, app, input, output, format string) error {
	dir, err := os.MkdirTemp("", "termixgo-office-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	script := filepath.Join(dir, "convert.ps1")
	if err := os.WriteFile(script, []byte(officeScript), 0o600); err != nil {
		return err
	}
	absIn, _ := filepath.Abs(input)
	absOut, _ := filepath.Abs(output)
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
		"-File", script, "-App", app, "-In", absIn, "-Out", absOut, "-Format", format)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
