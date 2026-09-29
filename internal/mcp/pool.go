package mcp

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// maxToolNameLength is the longest tool name every provider accepts. OpenAI and
// Anthropic both document 64 characters, so a longer qualified name has to be
// shortened rather than sent and rejected.
const maxToolNameLength = 64

// separator joins a server name to a tool name. A double underscore is what the
// other MCP clients use, and it is unlikely to appear inside either half.
const separator = "__"

// Bound is one tool a server contributes, with the name the model will see.
type Bound struct {
	// Server is the configured label of the server that owns the tool.
	Server string
	// Tool is the name the server itself uses.
	Tool Tool
	// Name is the qualified, provider-safe name offered to the model.
	Name string
}

// Failure records a server that could not be started or listed. It is kept
// rather than returned because one broken server must not stop a session.
type Failure struct {
	Name string
	Err  error
}

// ServerStatus reports one configured server for the operator.
type ServerStatus struct {
	Name       string
	Command    string
	Connected  bool
	Disabled   bool
	ServerName string
	Version    string
	ToolCount  int
	Err        error
	Stderr     string
}

// Pool owns the connected servers and the tools they contribute.
//
// It is deliberately best effort: a server that fails to start is recorded and
// skipped, because an operator with three servers configured should not lose
// the two that work to the one that does not.
type Pool struct {
	mu       sync.Mutex
	clients  map[string]*Client
	bound    []Bound
	failures []Failure
	status   []ServerStatus
	// disabled is kept so the operator can see a server they turned off,
	// rather than having it look like a typo.
	disabled []ServerStatus
}

// NewPool builds an empty pool.
func NewPool() *Pool {
	return &Pool{clients: map[string]*Client{}}
}

// Connect starts every server. The context bounds only the handshake and the
// tool listing; the servers themselves outlive it.
func (p *Pool) Connect(ctx context.Context, disabled []Options, enabled []Options) {
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, options := range disabled {
		p.disabled = append(p.disabled, ServerStatus{
			Name:     options.Name,
			Command:  describe(options),
			Disabled: true,
		})
	}
	for _, options := range enabled {
		client, err := Start(ctx, options)
		if err != nil {
			p.failures = append(p.failures, Failure{Name: options.Name, Err: err})
			p.status = append(p.status, ServerStatus{
				Name:    options.Name,
				Command: describe(options),
				Err:     err,
			})
			continue
		}
		p.clients[options.Name] = client

		tools, err := client.Tools(ctx)
		status := ServerStatus{
			Name:       options.Name,
			Command:    describe(options),
			Connected:  true,
			ServerName: client.ServerName(),
			Version:    client.ProtocolVersion(),
		}
		if err != nil {
			p.failures = append(p.failures, Failure{Name: options.Name, Err: err})
			status.Err = err
			status.Stderr = client.Stderr()
			p.status = append(p.status, status)
			continue
		}
		for _, tool := range tools {
			// An invalid tool name from the server is reported rather than
			// silently renamed, because a renamed tool is one the model cannot
			// call the way the server documented it.
			if strings.TrimSpace(tool.Name) == "" {
				continue
			}
			bound := Bound{
				Server: options.Name,
				Tool:   tool,
				Name:   p.claim(options.Name, tool.Name),
			}
			p.bound = append(p.bound, bound)
		}
		status.ToolCount = len(tools)
		p.status = append(p.status, status)
	}
}

// claim builds a unique, provider-safe tool name for a server tool.
//
// The uniqueness pass matters: two servers often publish a tool with the same
// name, and the registry indexes by name, so a collision would silently make
// one of them unreachable.
func (p *Pool) claim(server, tool string) string {
	base := qualify(server, tool)
	name := base
	for suffix := 2; p.taken(name); suffix++ {
		tail := fmt.Sprintf("_%d", suffix)
		trimmed := base
		if len(trimmed)+len(tail) > maxToolNameLength {
			trimmed = trimmed[:maxToolNameLength-len(tail)]
		}
		name = trimmed + tail
	}
	return name
}

// taken reports whether a name is already claimed by another tool in this
// pool. The reserved prefix in qualify is what keeps a server from naming its
// tool after a built-in one, so there is nothing else to check here.
func (p *Pool) taken(name string) bool {
	for _, bound := range p.bound {
		if bound.Name == name {
			return true
		}
	}
	return false
}

// ReservedPrefix marks the tools that come from outside this process.
//
// Every contributed name carries it, which is what makes a hijack of a built-in
// tool impossible: the registry indexes by name, so without the prefix a server
// could publish a tool called write_file and take that call over.
const ReservedPrefix = "mcp_"

// qualify builds the provider-safe name for a server tool, shortening the tool
// half first because the server half is what the operator recognises in the
// transcript.
func qualify(server, tool string) string {
	host := sanitize(server)
	if host == "" {
		host = "server"
	}
	name := sanitize(tool)
	if name == "" {
		name = "tool"
	}

	prefix := ReservedPrefix + host + separator
	if len(prefix)+len(name) <= maxToolNameLength {
		return prefix + name
	}
	room := maxToolNameLength - len(prefix)
	if room < 1 {
		// The server name alone is over the limit, so trim it and what is left
		// of the budget goes to the tool.
		host = truncate(host, maxToolNameLength-len(ReservedPrefix)-len(separator)-1)
		prefix = ReservedPrefix + host + separator
		room = maxToolNameLength - len(prefix)
	}
	return prefix + truncate(name, room)
}

// truncate clips a name to fit, keeping the head.
func truncate(value string, limit int) string {
	if limit < 1 {
		return ""
	}
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}

// sanitize maps a name to the character set every provider accepts, which is
// letters, digits, underscore and hyphen.
func sanitize(raw string) string {
	var builder strings.Builder
	lastUnderscore := false
	for _, symbol := range strings.TrimSpace(raw) {
		switch {
		case symbol >= 'a' && symbol <= 'z',
			symbol >= 'A' && symbol <= 'Z',
			symbol >= '0' && symbol <= '9':
			builder.WriteRune(symbol)
			lastUnderscore = false
		case symbol == '_' || symbol == '-':
			builder.WriteRune(symbol)
			lastUnderscore = false
		default:
			// A run of invalid characters becomes one underscore, so "a b"
			// and "a  b" do not produce different names.
			if !lastUnderscore && builder.Len() > 0 {
				builder.WriteRune('_')
				lastUnderscore = true
			}
		}
	}
	return strings.Trim(builder.String(), "_-")
}

// describe renders the command line for the operator, which is what they need
// to reproduce a failure by hand.
func describe(options Options) string {
	if len(options.Args) == 0 {
		return options.Command
	}
	return options.Command + " " + strings.Join(options.Args, " ")
}

// Tools returns the contributed tools, ordered by server then tool so the
// listing and the registry are stable across runs.
func (p *Pool) Tools() []Bound {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]Bound, len(p.bound))
	copy(out, p.bound)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Server != out[j].Server {
			return out[i].Server < out[j].Server
		}
		return out[i].Tool.Name < out[j].Tool.Name
	})
	return out
}

// Lookup finds a contributed tool by its qualified name.
func (p *Pool) Lookup(name string) (Bound, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, bound := range p.bound {
		if bound.Name == name {
			return bound, true
		}
	}
	return Bound{}, false
}

// Call runs a contributed tool on the server that owns it.
func (p *Pool) Call(ctx context.Context, name string, args map[string]any) (CallResult, error) {
	bound, ok := p.Lookup(name)
	if !ok {
		return CallResult{}, fmt.Errorf("no MCP tool is named %q", name)
	}
	p.mu.Lock()
	client := p.clients[bound.Server]
	p.mu.Unlock()
	if client == nil {
		return CallResult{}, fmt.Errorf("the MCP server %q is not connected", bound.Server)
	}
	return client.Call(ctx, bound.Tool.Name, args)
}

// Failures returns the servers that could not be used.
func (p *Pool) Failures() []Failure {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]Failure, len(p.failures))
	copy(out, p.failures)
	return out
}

// Status lists every configured server, connected or not, so a typo in the
// config is visible rather than silent.
func (p *Pool) Status() []ServerStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]ServerStatus, 0, len(p.status)+len(p.disabled))
	out = append(out, p.status...)
	out = append(out, p.disabled...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Close stops every connected server.
func (p *Pool) Close() {
	p.mu.Lock()
	clients := make([]*Client, 0, len(p.clients))
	for _, client := range p.clients {
		clients = append(clients, client)
	}
	p.clients = map[string]*Client{}
	p.bound = nil
	p.mu.Unlock()
	for _, client := range clients {
		client.Close()
	}
}
