package lsp

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Server describes how to start one language server.
type Server struct {
	// Name labels the server in messages ("gopls").
	Name string
	// Language is the languageId sent with didOpen.
	Language string
	// Candidates are tried in order; the first whose binary is on PATH runs.
	Candidates [][]string
}

// defaultServers maps a file extension to its server. Only widely used
// servers are listed; an override in the configuration replaces an entry.
var defaultServers = map[string]Server{
	".go":   {Name: "gopls", Language: "go", Candidates: [][]string{{"gopls"}}},
	".ts":   tsServer("typescript"),
	".tsx":  tsServer("typescriptreact"),
	".js":   tsServer("javascript"),
	".jsx":  tsServer("javascriptreact"),
	".mjs":  tsServer("javascript"),
	".cjs":  tsServer("javascript"),
	".py":   {Name: "python", Language: "python", Candidates: [][]string{{"pyright-langserver", "--stdio"}, {"basedpyright-langserver", "--stdio"}, {"pylsp"}}},
	".rs":   {Name: "rust-analyzer", Language: "rust", Candidates: [][]string{{"rust-analyzer"}}},
	".c":    clangd("c"),
	".h":    clangd("c"),
	".cc":   clangd("cpp"),
	".cpp":  clangd("cpp"),
	".hpp":  clangd("cpp"),
	".php":  {Name: "intelephense", Language: "php", Candidates: [][]string{{"intelephense", "--stdio"}}},
	".lua":  {Name: "lua-language-server", Language: "lua", Candidates: [][]string{{"lua-language-server"}}},
	".java": {Name: "jdtls", Language: "java", Candidates: [][]string{{"jdtls"}}},
}

func tsServer(language string) Server {
	return Server{Name: "typescript-language-server", Language: language, Candidates: [][]string{{"typescript-language-server", "--stdio"}}}
}

func clangd(language string) Server {
	return Server{Name: "clangd", Language: language, Candidates: [][]string{{"clangd"}}}
}

// ServerFor picks the server for a file. overrides maps an extension (with the
// dot) to a command line and wins over the default.
func ServerFor(path string, overrides map[string][]string) (Server, bool) {
	ext := strings.ToLower(filepath.Ext(path))
	server, ok := defaultServers[ext]
	if argv, has := overrides[ext]; has && len(argv) > 0 {
		language := server.Language
		if language == "" {
			language = strings.TrimPrefix(ext, ".")
		}
		return Server{Name: filepath.Base(argv[0]), Language: language, Candidates: [][]string{argv}}, true
	}
	return server, ok
}

// SupportedExtensions lists the extensions with a default server.
func SupportedExtensions() []string {
	out := make([]string, 0, len(defaultServers))
	for ext := range defaultServers {
		out = append(out, ext)
	}
	sort.Strings(out)
	return out
}

// Manager keeps one client per workspace and server, starting it on first
// use and again after a crash.
type Manager struct {
	mu      sync.Mutex
	clients map[string]*Client
	// starting serialises the start of one server, so two tool calls do not
	// launch it twice.
	starting map[string]*sync.Mutex
}

// NewManager builds an empty manager.
func NewManager() *Manager {
	return &Manager{clients: map[string]*Client{}, starting: map[string]*sync.Mutex{}}
}

var (
	sharedOnce sync.Once
	shared     *Manager
)

// Shared returns the process-wide manager.
func Shared() *Manager {
	sharedOnce.Do(func() { shared = NewManager() })
	return shared
}

// Client returns a running client for the file's language in root.
func (m *Manager) Client(ctx context.Context, root, path string, overrides map[string][]string) (*Client, Server, error) {
	server, ok := ServerFor(path, overrides)
	if !ok {
		return nil, Server{}, fmt.Errorf("no language server is known for %s files; supported: %s",
			filepath.Ext(path), strings.Join(SupportedExtensions(), " "))
	}
	key := strings.ToLower(filepath.Clean(root)) + "|" + server.Name

	m.mu.Lock()
	gate, ok := m.starting[key]
	if !ok {
		gate = &sync.Mutex{}
		m.starting[key] = gate
	}
	m.mu.Unlock()
	gate.Lock()
	defer gate.Unlock()

	m.mu.Lock()
	existing := m.clients[key]
	m.mu.Unlock()
	if existing != nil && !existing.Dead() {
		return existing, server, nil
	}
	if existing != nil {
		existing.Close()
	}

	var lastErr error
	for _, argv := range server.Candidates {
		client, err := Start(ctx, server.Name, argv, root)
		if err != nil {
			lastErr = err
			continue
		}
		m.mu.Lock()
		m.clients[key] = client
		m.mu.Unlock()
		return client, server, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no command configured for %s", server.Name)
	}
	return nil, server, lastErr
}

// Running lists the servers that are up, for /status.
func (m *Manager) Running() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for key, client := range m.clients {
		if !client.Dead() {
			out = append(out, key)
		}
	}
	sort.Strings(out)
	return out
}

// CloseAll stops every server.
func (m *Manager) CloseAll() {
	m.mu.Lock()
	clients := make([]*Client, 0, len(m.clients))
	for _, client := range m.clients {
		clients = append(clients, client)
	}
	m.clients = map[string]*Client{}
	m.mu.Unlock()
	var wg sync.WaitGroup
	for _, client := range clients {
		wg.Add(1)
		go func(c *Client) {
			defer wg.Done()
			c.Close()
		}(client)
	}
	wg.Wait()
}
