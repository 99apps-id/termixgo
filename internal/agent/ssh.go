package agent

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// SshConnection describes a saved SSH connection.
type SshConnection struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	User     string `json:"user"`
	KeyPath  string `json:"key_path,omitempty"`
	Password string `json:"-"` // never persisted
}

// SshActiveSession tracks the currently active SSH session.
type SshActiveSession struct {
	ConnectionID string
	Client       *ssh.Client
	Session      *ssh.Session
}

var (
	sshConnectionsMu sync.Mutex
	sshConnections   []SshConnection
	sshActive        *SshActiveSession
)

// SshConnectionsPath returns the path to the SSH connections file.
func SshConnectionsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".termixgo")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return filepath.Join(dir, "ssh_connections.json"), nil
}

// LoadSshConnections reads saved SSH connections from disk.
func LoadSshConnections() error {
	path, err := SshConnectionsPath()
	if err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return json.Unmarshal(data, &sshConnections)
}

// SaveSshConnections writes SSH connections to disk.
func SaveSshConnections() error {
	path, err := SshConnectionsPath()
	if err != nil {
		return err
	}
	data, err := json.Marshal(sshConnections)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// AddSshConnection appends a new connection and persists it.
func AddSshConnection(conn SshConnection) {
	sshConnectionsMu.Lock()
	defer sshConnectionsMu.Unlock()
	if conn.ID == "" {
		conn.ID = fmt.Sprintf("%d", time.Now().UnixNano())
	}
	sshConnections = append(sshConnections, conn)
	_ = SaveSshConnections()
}

// RemoveSshConnection removes a connection by id.
func RemoveSshConnection(id string) {
	sshConnectionsMu.Lock()
	defer sshConnectionsMu.Unlock()
	for i, c := range sshConnections {
		if c.ID == id {
			sshConnections = append(sshConnections[:i], sshConnections[i+1:]...)
			break
		}
	}
	_ = SaveSshConnections()
}

// ListSshConnections returns a copy of the connection list.
func ListSshConnections() []SshConnection {
	sshConnectionsMu.Lock()
	defer sshConnectionsMu.Unlock()
	out := make([]SshConnection, len(sshConnections))
	copy(out, sshConnections)
	return out
}

// GetSshConnection returns a connection by id or nil.
func GetSshConnection(id string) *SshConnection {
	for i := range ListSshConnections() {
		if sshConnections[i].ID == id {
			return &sshConnections[i]
		}
	}
	return nil
}

// CloseSshActiveSession closes the current active SSH session.
func CloseSshActiveSession() {
	if sshActive != nil {
		_ = sshActive.Session.Close()
		_ = sshActive.Client.Close()
		sshActive = nil
	}
}

// dialSSH opens an SSH connection using key or password auth.
func dialSSH(conn SshConnection, password string) (*ssh.Client, error) {
	cfg := &ssh.ClientConfig{
		User:            conn.User,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // TODO: known_hosts support
		Timeout:         10 * time.Second,
	}

	if password != "" {
		cfg.Auth = []ssh.AuthMethod{ssh.Password(password)}
	} else if conn.KeyPath != "" {
		key, err := os.ReadFile(conn.KeyPath)
		if err != nil {
			return nil, fmt.Errorf("read key: %w", err)
		}
		signer, err := ssh.ParsePrivateKey(key)
		if err != nil {
			return nil, fmt.Errorf("parse key: %w", err)
		}
		cfg.Auth = []ssh.AuthMethod{ssh.PublicKeys(signer)}
	} else {
		// Try ssh-agent.
		if agentConn, err := net.Dial("unix", os.Getenv("SSH_AUTH_SOCK")); err == nil {
			cfg.Auth = []ssh.AuthMethod{ssh.PublicKeysCallback(agent.NewClient(agentConn).Signers)}
		}
	}

	addr := fmt.Sprintf("%s:%d", conn.Host, conn.Port)
	return ssh.Dial("tcp", addr, cfg)
}

// sshRunCommand executes a command on a remote host and returns stdout+stderr.
func sshRunCommand(client *ssh.Client, command string) (string, error) {
	session, err := client.NewSession()
	if err != nil {
		return "", err
	}
	defer session.Close()

	var b strings.Builder
	session.Stdout = &b
	session.Stderr = &b

	if err := session.Run(command); err != nil {
		return b.String(), fmt.Errorf("remote command failed: %w: %s", err, b.String())
	}
	return b.String(), nil
}

// safeSummary returns a connection summary without secrets.
func safeSummary(conn SshConnection) map[string]any {
	return map[string]any{
		"id":       conn.ID,
		"name":     conn.Name,
		"host":     conn.Host,
		"port":     conn.Port,
		"user":     conn.User,
		"key_path": conn.KeyPath,
	}
}
