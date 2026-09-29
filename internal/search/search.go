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

	_ "modernc.org/sqlite"
)

const (
	// defaultDBSuffix is the file name suffix for the search database.
	defaultDBSuffix = "search.db"
	// maxIndexBytes caps a single document so the FTS5 table stays fast.
	maxIndexBytes = 64 * 1024
)

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
	stop     chan struct{}
	indexing bool
}

// Open opens (or creates) the search database inside the given directory.
func Open(root string) (*Store, error) {
	if strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("search: root is required")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("search: cannot create dir: %w", err)
	}
	dbPath := filepath.Join(root, defaultDBSuffix)
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
	store := &Store{db: db, root: root, stop: make(chan struct{})}
	go store.indexLoop()
	return store, nil
}

// Close releases the database.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case <-s.stop:
		// already closed
	default:
		close(s.stop)
	}
	if s.db != nil {
		return s.db.Close()
	}
	return nil
}

// Index adds or replaces one document.
func (s *Store) Index(scope, path, title, body string) error {
	if strings.TrimSpace(body) == "" {
		return nil
	}
	if len(body) > maxIndexBytes {
		body = body[:maxIndexBytes]
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`INSERT INTO docs(scope, path, title, body) VALUES(?,?,?,?)`, scope, path, title, body)
	return err
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
	if strings.TrimSpace(query) == "" {
		return nil, nil
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT scope, path, title, snippet(docs, 3, '', '', ' ... ', 40), rank FROM docs WHERE docs MATCH ? ORDER BY rank LIMIT ?`, query, limit)
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

// indexLoop periodically refreshes the workspace file index.
func (s *Store) indexLoop() {
	ticker := time.NewTicker(2 * time.Minute)
	defer ticker.Stop()
	s.indexWorkspace()
	for {
		select {
		case <-ticker.C:
			s.indexWorkspace()
		case <-s.stop:
			return
		}
	}
}

func (s *Store) indexWorkspace() {
	s.mu.Lock()
	if s.indexing {
		s.mu.Unlock()
		return
	}
	s.indexing = true
	s.mu.Unlock()

	var files []string
	_ = filepath.WalkDir(s.root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if name == ".git" || name == "node_modules" || name == "dist" || name == ".next" {
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
		text := string(data)
		if len(text) > maxIndexBytes {
			text = text[:maxIndexBytes]
		}
		rel, _ := filepath.Rel(s.root, path)
		_ = s.Index("workspace", rel, filepath.Base(path), text)
	}

	s.mu.Lock()
	s.indexing = false
	s.mu.Unlock()
}
