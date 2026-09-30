// Package search provides full-text search over Termixgo memory, sessions and
// workspace files using SQLite FTS5.
package search

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	_ "modernc.org/sqlite"
)

const (
	// defaultDBSuffix is the file name suffix for the search database.
	defaultDBSuffix = "search.db"
	// maxIndexBytes caps a single document so the FTS5 table stays fast.
	maxIndexBytes = 64 * 1024
)

// skippedDirs are directory names no search enters: dependency trees, build
// output and caches. They hold thousands of generated files, so indexing them
// floods the results and bloats the database, and a walk that reads them makes a
// search take minutes on a large workspace.
//
// This is the one list: the agent's grep and glob walk reads it too, through
// IsSkippedDir, so a file the index knows is a file the tools will search.
var skippedDirs = map[string]bool{
	".git": true, "node_modules": true, "vendor": true, "dist": true, "build": true,
	".next": true, ".turbo": true, ".venv": true, "venv": true, "__pycache__": true,
	"target": true, ".pnpm-store": true, ".cache": true, "coverage": true,
}

// IsSkippedDir reports whether a directory name is one a search never enters.
func IsSkippedDir(name string) bool {
	return skippedDirs[strings.ToLower(strings.TrimSpace(name))]
}

// Result is one ranked search hit.
type Result struct {
	Scope   string
	Path    string
	Title   string
	Snippet string
	Rank    float64
}

// Store owns the SQLite database and its FTS5 virtual table.
type Store struct {
	mu       sync.Mutex
	db       *sql.DB
	root     string
	dataDir  string
	indexing bool
	// indexedAt is when the workspace walk last finished, which is what keeps a
	// burst of searches from re-walking the tree on every call.
	indexedAt time.Time
}

// Open opens (or creates) the search database inside the given directory and
// indexes files under that same directory.
//
// It is the shape the tests use. A caller that keeps its state elsewhere, such
// as <workspace>/.termixgo, wants OpenIn.
func Open(root string) (*Store, error) {
	return OpenIn(root, root)
}

// OpenIn opens the database in dataDir while indexing files under root.
//
// The two are separate on purpose: the index covers the operator's workspace,
// but writing search.db into that workspace would add a binary file to the tree
// the model reads. dataDir is where the state belongs, and it is skipped by the
// walk so the database, its WAL and the journal are never indexed as content.
func OpenIn(root, dataDir string) (*Store, error) {
	if strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("search: root is required")
	}
	if strings.TrimSpace(dataDir) == "" {
		dataDir = root
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("search: cannot create dir: %w", err)
	}
	dbPath := filepath.Join(dataDir, defaultDBSuffix)
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("search: open db: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA journal_mode=WAL;`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("search: wal: %w", err)
	}
	if _, err := db.Exec(`CREATE VIRTUAL TABLE IF NOT EXISTS docs USING fts5(scope, path, title, body, tokenize='porter unicode61');`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("search: create table: %w", err)
	}
	store := &Store{db: db, root: root, dataDir: dataDir}
	return store, nil
}

// Close releases the database.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db != nil {
		return s.db.Close()
	}
	return nil
}

// Index adds or replaces one document.
//
// A document is keyed by scope and path, so indexing the same file again
// replaces it. That is not cosmetic: the workspace walk re-indexes every file on
// each refresh and the memory tool re-indexes the memory files after every
// write, so a plain INSERT made one file return once per refresh, pushed other
// matches past the limit, and grew the database by a full copy of the workspace
// each time.
//
// An empty body removes the document instead of storing nothing, so a file that
// was emptied or truncated stops being reported as a match.
func (s *Store) Index(scope, path, title, body string) error {
	body = clampIndexBytes(body)
	s.mu.Lock()
	defer s.mu.Unlock()
	// The delete and the insert share one transaction: a reader must never see
	// the document missing.
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`DELETE FROM docs WHERE scope = ? AND path = ?`, scope, path); err != nil {
		return err
	}
	if strings.TrimSpace(body) == "" {
		return tx.Commit()
	}
	if _, err := tx.Exec(`INSERT INTO docs(scope, path, title, body) VALUES(?,?,?,?)`, scope, path, title, body); err != nil {
		return err
	}
	return tx.Commit()
}

// clampIndexBytes caps a document at maxIndexBytes.
//
// The cut is moved back to a rune boundary: slicing by byte can land inside a
// multi-byte character and store invalid UTF-8, which the tokenizer then reads
// as a replacement character.
func clampIndexBytes(text string) string {
	if len(text) <= maxIndexBytes {
		return text
	}
	cut := maxIndexBytes
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut]
}

// Remove deletes documents matching scope and path.
func (s *Store) Remove(scope, path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`DELETE FROM docs WHERE scope = ? AND path = ?`, scope, path)
	return err
}

// Search runs an FTS5 query and returns ranked results.
func (s *Store) Search(query string, limit int) ([]Result, error) {
	return s.SearchScope(query, "all", limit)
}

// SearchScope runs a query restricted to one scope: "memory", "journal",
// "workspace", or "all".
//
// The scope is part of the query rather than a filter over the results: the
// caller asks for the newest twenty rows, so discarding the wrong scope after
// the limit would report "no matches" whenever the other scopes were the
// better matches.
func (s *Store) SearchScope(query, scope string, limit int) ([]Result, error) {
	if strings.TrimSpace(query) == "" {
		return nil, nil
	}
	expression := ftsQuery(query)
	if expression == "" {
		return nil, nil
	}
	scope = strings.ToLower(strings.TrimSpace(scope))
	if scope == "" {
		scope = "all"
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	// The workspace documents are refreshed before the query, so a hit reflects
	// the files as they are now rather than as they were at startup. Memory and
	// journal documents are kept fresh by the writer, so they need no walk.
	s.refreshWorkspace()
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT scope, path, title, snippet(docs, 3, '', '', ' ... ', 40), rank FROM docs WHERE docs MATCH ? AND (? = 'all' OR scope = ?) ORDER BY rank LIMIT ?`, expression, scope, scope, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var results []Result
	for rows.Next() {
		var r Result
		if err := rows.Scan(&r.Scope, &r.Path, &r.Title, &r.Snippet, &r.Rank); err != nil {
			return nil, err
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

// ftsQuery turns free-form operator input into a valid FTS5 query.
//
// The agent passes whatever the operator typed, and FTS5 reads punctuation as
// syntax: "c++", "foo-bar", "status:1" and a lone "(" are all syntax errors
// there, so the search used to fail for input as ordinary as a version number.
// Every term is quoted as a phrase instead, which is the one form where the
// tokenizer treats the syntax characters as content. AND and OR keep their
// meaning because they are the operators an operator actually means.
func ftsQuery(raw string) string {
	items := make([]string, 0, 8)
	for _, field := range strings.Fields(raw) {
		switch strings.ToUpper(field) {
		case "AND", "OR":
			// A connector with no term on its left would be a syntax error of
			// its own, so it is dropped here.
			if len(items) > 0 {
				items = append(items, strings.ToUpper(field))
			}
			continue
		case "NOT", "NEAR":
			// A unary or proximity operator cannot be rebuilt from fields that
			// were split on whitespace, so it is dropped rather than turned
			// into a required word that nothing would match.
			continue
		}
		term := strings.ReplaceAll(field, `"`, "")
		if term == "" {
			continue
		}
		items = append(items, `"`+term+`"`)
	}

	var builder strings.Builder
	pending := ""
	for _, item := range items {
		if item == "AND" || item == "OR" {
			pending = " " + item + " "
			continue
		}
		if builder.Len() > 0 {
			if pending == "" {
				builder.WriteString(" AND ")
			} else {
				builder.WriteString(pending)
			}
		}
		builder.WriteString(item)
		pending = ""
	}
	return builder.String()
}

// indexLoop periodically refreshes the workspace file index.
//
// There is deliberately no timer. A walk of the whole workspace every couple of
// minutes reads every matching file on the operator's machine for a tool that is
// used occasionally, which is cost paid while the agent is idle. The index is
// refreshed before a search instead, so it is fresh exactly when it is read and
// costs nothing when nobody searches.
//
// workspaceIndexTTL is how stale the workspace documents may be before a search
// triggers a re-walk. The refresh then covers whatever changed since.
const workspaceIndexTTL = 5 * time.Minute

// refreshWorkspace re-walks the tree when the workspace documents are stale.
// A burst of searches therefore walks once, not once per call.
func (s *Store) refreshWorkspace() {
	s.mu.Lock()
	if s.indexing || time.Since(s.indexedAt) < workspaceIndexTTL {
		s.mu.Unlock()
		return
	}
	s.indexing = true
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		s.indexing = false
		s.indexedAt = time.Now()
		s.mu.Unlock()
	}()

	s.indexWorkspace()
}

// indexWorkspace walks the root and indexes the text files it finds. The caller
// owns the indexing flag, so this does not take the lock itself.
func (s *Store) indexWorkspace() {
	var files []string
	_ = filepath.WalkDir(s.root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			// The root is never skipped even if its own name matches, because
			// then a workspace called "build" or "dist" would index nothing.
			if path != s.root && IsSkippedDir(d.Name()) {
				return filepath.SkipDir
			}
			// The state directory holds the database, its WAL and the error
			// journal. None of it is content, and walking it would index the
			// database file itself.
			if s.dataDir != s.root && filepath.Clean(path) == filepath.Clean(s.dataDir) {
				return filepath.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		switch ext {
		case ".go", ".md", ".txt", ".json", ".yaml", ".yml", ".toml", ".sh", ".ps1", ".bat", ".py", ".js", ".ts":
			files = append(files, path)
		}
		return nil
	})

	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		text := clampIndexBytes(string(data))
		rel, _ := filepath.Rel(s.root, path)
		_ = s.Index("workspace", rel, filepath.Base(path), text)
	}
}
