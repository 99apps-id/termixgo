package agent

import (
	"context"
	"fmt"
	"strings"
)

// sshListConnectionsTool lists saved SSH connections.
type sshListConnectionsTool struct{}

func (t *sshListConnectionsTool) Name() string      { return "ssh_list_connections" }
func (t *sshListConnectionsTool) Aliases() []string { return []string{"list_ssh", "ssh_list"} }
func (t *sshListConnectionsTool) Mutating() bool    { return false }
func (t *sshListConnectionsTool) Risk() Risk        { return RiskNetwork }
func (t *sshListConnectionsTool) Description() string {
	return "List saved SSH connections."
}
func (t *sshListConnectionsTool) Schema() map[string]any {
	return object(map[string]any{})
}
func (t *sshListConnectionsTool) Label(_ map[string]any) string     { return "Listing SSH connections" }
func (t *sshListConnectionsTool) DoneLabel(_ map[string]any) string { return "Listed SSH connections" }

func (t *sshListConnectionsTool) Run(_ context.Context, _ *Env, _ map[string]any) (Result, error) {
	if err := LoadSshConnections(); err != nil {
		return Result{IsError: true, Output: fmt.Sprintf("load failed: %v", err)}, nil
	}
	conns := ListSshConnections()
	var b strings.Builder
	for _, c := range conns {
		b.WriteString(fmt.Sprintf("- %s: %s@%s:%d (key=%s)\n", c.Name, c.User, c.Host, c.Port, c.KeyPath))
	}
	if b.Len() == 0 {
		return Result{Output: "No saved SSH connections."}, nil
	}
	return Result{Output: b.String()}, nil
}

// sshConnectTool opens an SSH connection.
type sshConnectTool struct{}

func (t *sshConnectTool) Name() string      { return "ssh_connect" }
func (t *sshConnectTool) Aliases() []string { return []string{"ssh_open"} }
func (t *sshConnectTool) Mutating() bool    { return true }
func (t *sshConnectTool) Risk() Risk        { return RiskNetwork }
func (t *sshConnectTool) Description() string {
	return "Connect to a saved SSH connection by id or name."
}
func (t *sshConnectTool) Schema() map[string]any {
	return object(map[string]any{
		"connection_id": strProp("Saved connection id (preferred)."),
		"name":          strProp("Connection name to connect to."),
		"host":          strProp("Host if connecting ad-hoc."),
		"port":          intProp("Port (default 22)."),
		"user":          strProp("User if connecting ad-hoc."),
		"password":      strProp("Password for auth (optional if key/agent)."),
	})
}
func (t *sshConnectTool) Label(args map[string]any) string {
	return "Connecting SSH " + Shorten(argString(args, "connection_id")+argString(args, "name")+argString(args, "host"), 40)
}
func (t *sshConnectTool) DoneLabel(args map[string]any) string {
	return "Connected SSH " + Shorten(argString(args, "connection_id")+argString(args, "name")+argString(args, "host"), 40)
}

func (t *sshConnectTool) Run(_ context.Context, _ *Env, args map[string]any) (Result, error) {
	var conn SshConnection

	if id := argString(args, "connection_id"); id != "" {
		c := GetSshConnection(id)
		if c == nil {
			return Result{IsError: true, Output: "connection not found"}, nil
		}
		conn = *c
	} else if name := argString(args, "name"); name != "" {
		for _, c := range ListSshConnections() {
			if strings.EqualFold(c.Name, name) {
				conn = c
				break
			}
		}
		if conn.Host == "" {
			return Result{IsError: true, Output: "connection not found"}, nil
		}
	} else {
		conn.Host = argString(args, "host")
		conn.Port = argInt(args, "port", 22, 1, 65535)
		conn.User = argString(args, "user")
		conn.Password = argString(args, "password")
		if conn.Host == "" || conn.User == "" {
			return Result{IsError: true, Output: "host and user are required for ad-hoc connect"}, nil
		}
	}

	client, err := dialSSH(conn, argString(args, "password"))
	if err != nil {
		return Result{IsError: true, Output: fmt.Sprintf("ssh connect failed: %v", err)}, nil
	}

	sshActive = &SshActiveSession{
		ConnectionID: conn.ID,
		Client:       client,
	}
	return Result{Output: fmt.Sprintf("Connected to %s@%s:%d", conn.User, conn.Host, conn.Port)}, nil
}

// sshRunCommandTool runs a command on the active SSH session.
type sshRunCommandTool struct{}

func (t *sshRunCommandTool) Name() string      { return "ssh_run_command" }
func (t *sshRunCommandTool) Aliases() []string { return []string{"ssh_exec", "remote_exec"} }
func (t *sshRunCommandTool) Mutating() bool    { return true }
func (t *sshRunCommandTool) Risk() Risk        { return RiskNetwork }
func (t *sshRunCommandTool) Description() string {
	return "Run a command on the active SSH session."
}
func (t *sshRunCommandTool) Schema() map[string]any {
	return object(map[string]any{
		"command": strProp("Command to run on the remote host."),
	})
}
func (t *sshRunCommandTool) Label(args map[string]any) string {
	return "Running remote " + Shorten(argString(args, "command"), 40)
}
func (t *sshRunCommandTool) DoneLabel(args map[string]any) string {
	return "Ran remote " + Shorten(argString(args, "command"), 40)
}

func (t *sshRunCommandTool) Run(_ context.Context, _ *Env, args map[string]any) (Result, error) {
	if sshActive == nil || sshActive.Client == nil {
		return Result{IsError: true, Output: "no active SSH session; use ssh_connect first"}, nil
	}
	cmd := argString(args, "command")
	if cmd == "" {
		return Result{IsError: true, Output: "command is required"}, nil
	}
	out, err := sshRunCommand(sshActive.Client, cmd)
	if err != nil {
		return Result{IsError: true, Output: out + "\n" + err.Error()}, nil
	}
	return Result{Output: out}, nil
}

// sshActiveSessionTool returns the current active SSH session info.
type sshActiveSessionTool struct{}

func (t *sshActiveSessionTool) Name() string      { return "ssh_active_session" }
func (t *sshActiveSessionTool) Aliases() []string { return []string{"ssh_status"} }
func (t *sshActiveSessionTool) Mutating() bool    { return false }
func (t *sshActiveSessionTool) Risk() Risk        { return RiskNetwork }
func (t *sshActiveSessionTool) Description() string {
	return "Show the current active SSH session."
}
func (t *sshActiveSessionTool) Schema() map[string]any {
	return object(map[string]any{})
}
func (t *sshActiveSessionTool) Label(_ map[string]any) string     { return "Checking SSH session" }
func (t *sshActiveSessionTool) DoneLabel(_ map[string]any) string { return "Checked SSH session" }

func (t *sshActiveSessionTool) Run(_ context.Context, _ *Env, _ map[string]any) (Result, error) {
	if sshActive == nil || sshActive.Client == nil {
		return Result{Output: "No active SSH session."}, nil
	}
	// Verify connection is still alive.
	_, err := sshRunCommand(sshActive.Client, "echo ok")
	if err != nil {
		return Result{Output: fmt.Sprintf("Active session %s is dead: %v", sshActive.ConnectionID, err)}, nil
	}
	return Result{Output: fmt.Sprintf("Active SSH session: %s", sshActive.ConnectionID)}, nil
}
