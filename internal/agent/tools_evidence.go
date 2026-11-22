package agent

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// evidenceFile is the working paper: an append-only log of findings, each
// chained to the one before it by a hash, so an entry that is changed or
// removed afterwards is detectable.
const evidenceFile = "evidence.jsonl"

var evidenceMu sync.Mutex

type evidenceEntry struct {
	Seq        int    `json:"seq"`
	Time       string `json:"time"`
	Finding    string `json:"finding"`
	Severity   string `json:"severity"`
	SourceFile string `json:"source_file,omitempty"`
	FileSHA256 string `json:"file_sha256,omitempty"`
	Location   string `json:"location,omitempty"`
	URL        string `json:"url,omitempty"`
	Amount     string `json:"amount,omitempty"`
	Basis      string `json:"basis,omitempty"`
	Note       string `json:"note,omitempty"`
	PrevHash   string `json:"prev_hash"`
	Hash       string `json:"hash"`
}

func (e evidenceEntry) digest() string {
	copied := e
	copied.Hash = ""
	data, _ := json.Marshal(copied)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func evidencePath(env *Env) string {
	root := ""
	if env != nil {
		root = env.Workspace
	}
	return filepath.Join(root, ".termixgo", evidenceFile)
}

func readEvidence(path string) ([]evidenceEntry, error) {
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var entries []evidenceEntry
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 4<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var entry evidenceEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			return entries, fmt.Errorf("line %d of the evidence log is not valid: %w", len(entries)+1, err)
		}
		entries = append(entries, entry)
	}
	return entries, scanner.Err()
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

type evidenceLogTool struct{}

func (t *evidenceLogTool) Name() string      { return "evidence_log" }
func (t *evidenceLogTool) Aliases() []string { return []string{"audit_trail", "working_paper"} }
func (t *evidenceLogTool) Mutating() bool    { return true }
func (t *evidenceLogTool) Risk() Risk        { return RiskEdit }
func (t *evidenceLogTool) Label(a map[string]any) string {
	return "Evidence log: " + evidenceAction(a)
}
func (t *evidenceLogTool) DoneLabel(a map[string]any) string {
	return "Evidence log: " + evidenceAction(a)
}
func evidenceAction(a map[string]any) string {
	if action := strings.ToLower(strings.TrimSpace(argString(a, "action"))); action != "" {
		return action
	}
	return "list"
}
func (t *evidenceLogTool) Description() string {
	return "The audit working paper. add records one finding with where it came from (file, sheet and cell or page, or a URL), the SHA-256 of the source file at that moment, the amount, a severity, and the regulation or standard it rests on (name, article, effective date). list shows entries, verify checks the hash chain and whether any source file has changed since it was logged, export writes a markdown working paper. Entries are appended to .termixgo/evidence.jsonl and cannot be edited without breaking the chain. Record a finding as you establish it, not afterwards."
}
func (t *evidenceLogTool) Schema() map[string]any {
	return object(map[string]any{
		"action":       strProp("add, list, verify or export. Default list."),
		"finding":      strProp("add: what was found, in one or two sentences."),
		"severity":     strProp("add: info, low, medium or high. Default info."),
		"source_file":  strProp("add: workspace file the finding comes from. Its SHA-256 is recorded."),
		"location":     strProp("add: sheet and cell, page, or row, for example Sheet2!C14 or p.7."),
		"url":          strProp("add: web source, when the evidence is online."),
		"amount":       strProp("add: the amount involved, as written in the source."),
		"basis":        strProp("add: the regulation, standard or policy the finding rests on, with its number, article and effective date. For tax and accounting rules this must be the current rule, confirmed from the source, not recalled."),
		"note":         strProp("add: anything the reviewer needs."),
		"severity_min": strProp("list and export: show only this severity and above."),
		"limit":        intProp("list: how many of the latest entries, 1 to 200. Default 20."),
		"path":         strProp("export: markdown file to write in the workspace. Without it the markdown is returned."),
	})
}

var evidenceSeverity = map[string]int{"info": 0, "low": 1, "medium": 2, "high": 3}

func (t *evidenceLogTool) Run(_ context.Context, env *Env, args map[string]any) (Result, error) {
	evidenceMu.Lock()
	defer evidenceMu.Unlock()
	switch evidenceAction(args) {
	case "add":
		return t.add(env, args)
	case "list":
		return t.list(env, args)
	case "verify":
		return t.verify(env)
	case "export":
		return t.export(env, args)
	}
	return Result{Output: "action must be add, list, verify or export", IsError: true}, nil
}

func (t *evidenceLogTool) add(env *Env, args map[string]any) (Result, error) {
	finding := strings.TrimSpace(argString(args, "finding"))
	if finding == "" {
		return Result{Output: "finding is required", IsError: true}, nil
	}
	severity := strings.ToLower(strings.TrimSpace(argString(args, "severity")))
	if severity == "" {
		severity = "info"
	}
	if _, ok := evidenceSeverity[severity]; !ok {
		return Result{Output: "severity must be info, low, medium or high", IsError: true}, nil
	}
	path := evidencePath(env)
	entries, err := readEvidence(path)
	if err != nil {
		return Result{Output: "the evidence log is damaged, run verify before adding: " + err.Error(), IsError: true}, nil
	}
	entry := evidenceEntry{
		Seq:      len(entries) + 1,
		Time:     time.Now().UTC().Format(time.RFC3339),
		Finding:  finding,
		Severity: severity,
		Location: strings.TrimSpace(argString(args, "location")),
		URL:      strings.TrimSpace(argString(args, "url")),
		Amount:   strings.TrimSpace(argString(args, "amount")),
		Basis:    strings.TrimSpace(argString(args, "basis")),
		Note:     strings.TrimSpace(argString(args, "note")),
	}
	if len(entries) > 0 {
		entry.PrevHash = entries[len(entries)-1].Hash
	}
	if source := strings.TrimSpace(argString(args, "source_file")); source != "" {
		resolved := resolvePath(env, source)
		if err := checkWorkspacePath(env, resolved); err != nil {
			return Result{Output: "source_file must be inside the workspace: " + err.Error(), IsError: true}, nil
		}
		sum, err := fileSHA256(resolved)
		if err != nil {
			return Result{Output: fmt.Sprintf("cannot read source_file %q: %v", source, err), IsError: true}, nil
		}
		entry.SourceFile = displayPath(env, resolved)
		entry.FileSHA256 = sum
	}
	entry.Hash = entry.digest()
	data, err := json.Marshal(entry)
	if err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	defer file.Close()
	if _, err := file.Write(append(data, '\n')); err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	note := ""
	if entry.Basis == "" {
		note = "\nNo basis was given. If this finding rests on a tax or accounting rule, add the regulation, article and effective date once confirmed from the current source."
	}
	return Result{Output: fmt.Sprintf("Logged finding #%d (%s)%s. Hash %s.%s", entry.Seq, entry.Severity, sourceNote(entry), entry.Hash[:12], note), Display: fmt.Sprintf("#%d", entry.Seq)}, nil
}

func sourceNote(e evidenceEntry) string {
	if e.SourceFile == "" {
		return ""
	}
	return fmt.Sprintf(" from %s (sha256 %s)", e.SourceFile, e.FileSHA256[:12])
}

func filterEvidence(entries []evidenceEntry, min string) []evidenceEntry {
	rank, ok := evidenceSeverity[strings.ToLower(strings.TrimSpace(min))]
	if !ok {
		return entries
	}
	var kept []evidenceEntry
	for _, entry := range entries {
		if evidenceSeverity[entry.Severity] >= rank {
			kept = append(kept, entry)
		}
	}
	return kept
}

func (t *evidenceLogTool) list(env *Env, args map[string]any) (Result, error) {
	entries, err := readEvidence(evidencePath(env))
	if err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	if len(entries) == 0 {
		return Result{Output: "The evidence log is empty."}, nil
	}
	total := len(entries)
	entries = filterEvidence(entries, argString(args, "severity_min"))
	limit := argInt(args, "limit", 20, 1, 200)
	skipped := 0
	if len(entries) > limit {
		skipped = len(entries) - limit
		entries = entries[len(entries)-limit:]
	}
	var out strings.Builder
	fmt.Fprintf(&out, "%d entries in the log", total)
	if skipped > 0 {
		fmt.Fprintf(&out, ", showing the latest %d", limit)
	}
	out.WriteString("\n")
	for _, entry := range entries {
		fmt.Fprintf(&out, "\n#%d [%s] %s\n  %s\n", entry.Seq, entry.Severity, entry.Time, entry.Finding)
		for _, line := range [][2]string{{"source", joinNonEmpty(" ", entry.SourceFile, entry.Location, entry.URL)}, {"amount", entry.Amount}, {"basis", entry.Basis}, {"note", entry.Note}} {
			if line[1] != "" {
				fmt.Fprintf(&out, "  %s: %s\n", line[0], line[1])
			}
		}
	}
	return Result{Output: strings.TrimRight(out.String(), "\n")}, nil
}

func joinNonEmpty(sep string, parts ...string) string {
	var kept []string
	for _, part := range parts {
		if part != "" {
			kept = append(kept, part)
		}
	}
	return strings.Join(kept, sep)
}

func (t *evidenceLogTool) verify(env *Env) (Result, error) {
	entries, err := readEvidence(evidencePath(env))
	if err != nil {
		return Result{Output: "The log cannot be read: " + err.Error(), IsError: true}, nil
	}
	if len(entries) == 0 {
		return Result{Output: "The evidence log is empty."}, nil
	}
	var problems []string
	prev := ""
	for index, entry := range entries {
		switch {
		case entry.Seq != index+1:
			problems = append(problems, fmt.Sprintf("entry %d is numbered %d: an entry was removed or reordered", index+1, entry.Seq))
		case entry.PrevHash != prev:
			problems = append(problems, fmt.Sprintf("entry #%d does not follow the one before it: the chain is broken", entry.Seq))
		case entry.digest() != entry.Hash:
			problems = append(problems, fmt.Sprintf("entry #%d was changed after it was logged", entry.Seq))
		}
		prev = entry.Hash
	}
	changed := 0
	for _, entry := range entries {
		if entry.SourceFile == "" {
			continue
		}
		path := resolvePath(env, entry.SourceFile)
		sum, err := fileSHA256(path)
		switch {
		case err != nil:
			problems = append(problems, fmt.Sprintf("entry #%d: source %s can no longer be read", entry.Seq, entry.SourceFile))
		case sum != entry.FileSHA256:
			changed++
			problems = append(problems, fmt.Sprintf("entry #%d: source %s has changed since it was logged", entry.Seq, entry.SourceFile))
		}
	}
	if len(problems) == 0 {
		return Result{Output: fmt.Sprintf("The chain of %d entries is intact and every source file still matches its recorded hash.", len(entries))}, nil
	}
	return Result{Output: fmt.Sprintf("%d problem(s) in the evidence log:\n- %s", len(problems), strings.Join(problems, "\n- ")), IsError: true}, nil
}

func (t *evidenceLogTool) export(env *Env, args map[string]any) (Result, error) {
	entries, err := readEvidence(evidencePath(env))
	if err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	if len(entries) == 0 {
		return Result{Output: "The evidence log is empty."}, nil
	}
	entries = filterEvidence(entries, argString(args, "severity_min"))
	var out strings.Builder
	out.WriteString("# Working paper\n\n")
	fmt.Fprintf(&out, "Generated %s from %d logged entries. The log is hash-chained; run verify before relying on it.\n\n", time.Now().UTC().Format("2006-01-02 15:04 UTC"), len(entries))
	out.WriteString("| # | Severity | Finding | Source | Amount | Basis |\n|---|---|---|---|---|---|\n")
	cell := func(text string) string {
		return strings.ReplaceAll(strings.ReplaceAll(text, "|", "\\|"), "\n", " ")
	}
	for _, entry := range entries {
		source := joinNonEmpty(" ", entry.SourceFile, entry.Location, entry.URL)
		if entry.FileSHA256 != "" {
			source += " (sha256 " + entry.FileSHA256[:12] + ")"
		}
		fmt.Fprintf(&out, "| %d | %s | %s | %s | %s | %s |\n", entry.Seq, entry.Severity, cell(entry.Finding), cell(source), cell(entry.Amount), cell(entry.Basis))
	}
	markdown := out.String()
	if target := strings.TrimSpace(argString(args, "path")); target != "" {
		resolved := resolvePath(env, target)
		if err := checkWorkspacePath(env, resolved); err != nil {
			return Result{Output: "path must be inside the workspace: " + err.Error(), IsError: true}, nil
		}
		if err := os.MkdirAll(filepath.Dir(resolved), 0o755); err != nil {
			return Result{Output: err.Error(), IsError: true}, nil
		}
		if err := os.WriteFile(resolved, []byte(markdown), 0o644); err != nil {
			return Result{Output: err.Error(), IsError: true}, nil
		}
		return Result{Output: fmt.Sprintf("Wrote %d entries to %s.", len(entries), displayPath(env, resolved))}, nil
	}
	return Result{Output: markdown}, nil
}
